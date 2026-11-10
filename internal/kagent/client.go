// Package kagent implements the pinned public kagent MCP contract. It does not
// execute commands locally or implement an agent or sandbox backend.
package kagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	Version            = "v1.0.0-rc3"
	MCPVersion         = "2026-07-28"
	tasksExtension     = "io.modelcontextprotocol/tasks"
	maxResponseBytes   = 12 << 20
	maxTranscriptBytes = 16 << 20
)

// Error is an upstream failure. Mutations are never automatically retried.
type Error struct {
	Kind    string
	Code    int
	Message string
}

func (e *Error) Error() string { return e.Message }

func protocolError(message string) error { return &Error{Kind: "protocol", Message: message} }

// Client uses kagent's stateless MCP 2026-07-28 API, including metadata on every
// request. Plain HTTP is restricted to loopback port forwards unless the
// explicit AllowClusterHTTP option covers an in-cluster service endpoint. user
// is only an identity hint for development authentication, never an
// authentication token.
type Client struct {
	endpoint string
	user     string
	token    string
	http     *http.Client
}

// Option adjusts a development-deployment policy check of New.
type Option func(*policy)

type policy struct{ allowClusterHTTP bool }

// AllowClusterHTTP permits plain HTTP to in-cluster Kubernetes service
// endpoints (*.svc, *.svc.cluster.local). It exists for a dedicated
// single-user deployment whose pod network is the trust boundary.
func AllowClusterHTTP() Option {
	return func(p *policy) { p.allowClusterHTTP = true }
}

// New creates a bounded, TLS-verifying client. endpoint may be a controller base
// URL or its complete /mcp URL. Redirects and ambient HTTP proxies are disabled.
func New(endpoint, user, token string, timeout time.Duration, opts ...Option) (*Client, error) {
	p := policy{}
	for _, opt := range opts {
		opt(&p)
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("kagent endpoint must be HTTP(S) without credentials, query, or fragment")
	}
	if u.Scheme == "http" {
		host := strings.ToLower(u.Hostname())
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			if !p.allowClusterHTTP {
				return nil, fmt.Errorf("remote kagent endpoints require HTTPS; use a loopback port-forward")
			}
			if !strings.HasSuffix(host, ".svc") && !strings.HasSuffix(host, ".svc.cluster.local") {
				return nil, fmt.Errorf("cluster HTTP is limited to *.svc and *.svc.cluster.local service endpoints")
			}
		}
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("HTTP timeout must be positive")
	}
	if user == "" || !printable(user, true) {
		return nil, fmt.Errorf("kagent user ID must be nonempty printable ASCII")
	}
	if token != "" && !printable(token, false) {
		return nil, fmt.Errorf("kagent token must not contain whitespace or controls")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/mcp") {
		endpoint += "/mcp"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{endpoint: endpoint, user: user, token: token, http: &http.Client{
		Timeout: timeout, Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func printable(value string, allowSpace bool) bool {
	minimum := rune(33)
	if allowSpace {
		minimum = 32
	}
	for _, char := range value {
		if char < minimum || char > 126 {
			return false
		}
	}
	return true
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

// rpc wraps one MCP round trip with telemetry. The span and metrics carry
// only the fixed method/tool vocabulary and error kind; params and results,
// which contain case content and credentials, are never recorded.
func (c *Client) rpc(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	call := method
	if method == "tools/call" {
		if name, ok := params["name"].(string); ok {
			call = name
		}
	}
	start := time.Now()
	ctx, span := tracer().Start(ctx, "kagent.rpc", trace.WithAttributes(attribute.String("mcp.call", call)))
	result, err := c.rpcCall(ctx, method, params)
	if err != nil {
		kind := errorKind(err)
		span.SetStatus(codes.Error, "kagent "+call+" failed")
		span.SetAttributes(attribute.String("kagent.error.kind", kind))
		loadInstruments().rpcEnded(call, time.Since(start).Seconds(), true)
	} else {
		loadInstruments().rpcEnded(call, time.Since(start).Seconds(), false)
	}
	span.End()
	return result, err
}

func (c *Client) rpcCall(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(random)
	requestParams := make(map[string]any, len(params)+1)
	for key, value := range params {
		requestParams[key] = value
	}
	requestParams["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    MCPVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "triagelab", "version": "0.1.0"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{tasksExtension: map[string]any{}}},
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": requestParams})
	if err != nil {
		return nil, fmt.Errorf("encode kagent request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, protocolError("could not construct kagent request")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", c.user)
	req.Header.Set("Mcp-Protocol-Version", MCPVersion)
	req.Header.Set("Mcp-Method", method)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if method == "tools/call" {
		req.Header.Set("Mcp-Name", fmt.Sprint(params["name"]))
	}
	if strings.HasPrefix(method, "tasks/") {
		req.Header.Set("Mcp-Name", fmt.Sprint(params["taskId"]))
	}
	// Propagate the active trace so an instrumented controller can correlate.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := c.http.Do(req)
	if err != nil {
		kind := "transport"
		if timeout, ok := err.(interface{ Timeout() bool }); (ok && timeout.Timeout()) || ctx.Err() != nil {
			kind = "timeout"
		}
		return nil, &Error{Kind: kind, Message: "kagent " + method + " request failed; outcome may be uncertain; check controller and port-forward"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Kind: "http", Code: resp.StatusCode, Message: fmt.Sprintf("kagent HTTP %d during %s; check endpoint and access", resp.StatusCode, method)}
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &Error{Kind: "transport", Message: "kagent response was interrupted; outcome may be uncertain"}
	}
	if len(body) > maxResponseBytes {
		return nil, protocolError("kagent response exceeds transport byte limit")
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return sseResult(body, id)
	}
	if mediaType != "application/json" {
		return nil, protocolError("kagent returned an unsupported HTTP content type")
	}
	return unwrap(body, id)
}

func sseResult(body []byte, id string) (map[string]any, error) {
	scanner := bufio.NewScanner(bytes.NewReader(append(body, '\n', '\n')))
	scanner.Buffer(make([]byte, 4096), maxResponseBytes+2)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		} else if line == "" && len(data) > 0 {
			event := []byte(strings.Join(data, "\n"))
			data = nil
			var envelope map[string]any
			if err := json.Unmarshal(event, &envelope); err != nil {
				return nil, protocolError("kagent SSE contains invalid JSON")
			}
			if _, present := envelope["id"]; present {
				return unwrap(event, id)
			}
		}
	}
	if scanner.Err() != nil {
		return nil, protocolError("kagent SSE event exceeds transport limit")
	}
	return nil, protocolError("kagent SSE ended without a JSON-RPC response")
}

func unwrap(body []byte, id string) (map[string]any, error) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, protocolError("kagent returned invalid JSON")
	}
	if envelope["jsonrpc"] != "2.0" || envelope["id"] != id {
		return nil, protocolError("kagent response has an invalid envelope or request ID")
	}
	if value, present := envelope["error"]; present {
		code := 0
		if remote, ok := value.(map[string]any); ok {
			if number, ok := remote["code"].(float64); ok {
				code = int(number)
			}
		}
		// Remote error messages can echo case content or credentials; don't log them.
		return nil, &Error{Kind: "rpc", Code: code, Message: fmt.Sprintf("kagent JSON-RPC error %d", code)}
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		return nil, protocolError("kagent response is missing an object result")
	}
	return result, nil
}

// CallTool returns declared structured output; text is never promoted to trusted
// evidence. read_sandbox_file is the upstream's content-only exception.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	result, err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	if failed, _ := result["isError"].(bool); failed {
		return nil, &Error{Kind: "tool", Message: "kagent tool " + name + " failed; inspect controller diagnostics"}
	}
	if output, ok := result["structuredContent"].(map[string]any); ok {
		return output, nil
	}
	if name == "read_sandbox_file" {
		if content, ok := result["content"].([]any); ok {
			return map[string]any{"content": content}, nil
		}
	}
	return nil, protocolError("kagent tool " + name + " omitted structured output; check pinned version")
}

// TaskItems reads complete durable A2A messages/artifacts. Any omitted evidence
// or non-advancing pagination fails closed. It does not interpret model prose.
func (c *Client) TaskItems(ctx context.Context, session, task string) ([]map[string]any, error) {
	var items []map[string]any
	cursor, totalBytes := 0, 0
	for {
		page, err := c.CallTool(ctx, "get_task", map[string]any{"session_id": session, "task_id": task, "from_item": cursor, "max_bytes": 4 << 20})
		if err != nil {
			return nil, err
		}
		if page["session_id"] != session || page["task_id"] != task {
			return nil, protocolError("kagent transcript does not match requested task")
		}
		entries, ok := page["items"].([]any)
		if !ok && page["items"] != nil {
			return nil, protocolError("kagent transcript items are invalid")
		}
		for _, value := range entries {
			entry, ok := value.(map[string]any)
			if !ok || nonzero(entry["omitted_bytes"]) {
				return nil, protocolError("kagent omitted transcript evidence")
			}
			index, ok := intValue(entry["index"])
			if !ok || index != len(items) {
				return nil, protocolError("kagent transcript item indexes are not contiguous")
			}
			encoded, _ := json.Marshal(entry)
			totalBytes += len(encoded)
			items = append(items, entry)
			if len(items) > 4096 || totalBytes > maxTranscriptBytes {
				return nil, protocolError("kagent transcript exceeds evidence limit")
			}
		}
		next := 0
		if raw, present := page["next_item"]; present {
			next, ok = intValue(raw)
			if !ok {
				return nil, protocolError("kagent transcript cursor is invalid")
			}
		}
		if next == 0 {
			total, valid := intValue(page["total_items"])
			if page["truncated"] == true || !valid || total != len(items) {
				return nil, protocolError("kagent returned an incomplete transcript")
			}
			return items, nil
		}
		if next <= cursor || next != len(items) {
			return nil, protocolError("kagent transcript pagination did not advance correctly")
		}
		cursor = next
	}
}

func intValue(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		if number >= 0 && number < 1<<31 && float64(int(number)) == number {
			return int(number), true
		}
	}
	return 0, false
}

func nonzero(value any) bool {
	if value == nil {
		return false
	}
	number, ok := intValue(value)
	return !ok || number != 0
}

// CancelTask uses the rc3 MCP task extension reference. The caller must observe
// cancellation before declaring the task stopped; acknowledgement alone is not exit.
func (c *Client) CancelTask(ctx context.Context, session, task string) error {
	if session == "" || task == "" {
		return fmt.Errorf("session and task IDs are required")
	}
	reference, err := json.Marshal(struct {
		Session string `json:"sessionId"`
		Task    string `json:"taskId"`
	}{session, task})
	if err != nil {
		return err
	}
	_, err = c.rpc(ctx, "tasks/cancel", map[string]any{"taskId": "v1." + base64.RawURLEncoding.EncodeToString(reference)})
	return err
}
