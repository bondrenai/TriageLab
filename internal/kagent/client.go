// Package kagent implements the pinned public KAgent MCP contract. It does not
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

// Client uses KAgent's stateless MCP 2026-07-28 API, including metadata on every
// request. Plain HTTP is restricted to loopback port forwards. user is only an
// identity hint for development authentication, never an authentication token.
type Client struct {
	endpoint string
	user     string
	token    string
	http     *http.Client
}

// New creates a bounded, TLS-verifying client. endpoint may be a controller base
// URL or its complete /mcp URL. Redirects and ambient HTTP proxies are disabled.
func New(endpoint, user, token string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("KAgent endpoint must be HTTP(S) without credentials, query, or fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, fmt.Errorf("remote KAgent endpoints require HTTPS; use a loopback port-forward")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("HTTP timeout must be positive")
	}
	if user == "" || !printable(user, true) {
		return nil, fmt.Errorf("KAgent user ID must be nonempty printable ASCII")
	}
	if token != "" && !printable(token, false) {
		return nil, fmt.Errorf("KAgent token must not contain whitespace or controls")
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

func (c *Client) rpc(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
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
		return nil, fmt.Errorf("encode KAgent request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, protocolError("could not construct KAgent request")
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
	resp, err := c.http.Do(req)
	if err != nil {
		kind := "transport"
		if timeout, ok := err.(interface{ Timeout() bool }); (ok && timeout.Timeout()) || ctx.Err() != nil {
			kind = "timeout"
		}
		return nil, &Error{Kind: kind, Message: "KAgent " + method + " request failed; outcome may be uncertain; check controller and port-forward"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Kind: "http", Code: resp.StatusCode, Message: fmt.Sprintf("KAgent HTTP %d during %s; check endpoint and access", resp.StatusCode, method)}
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &Error{Kind: "transport", Message: "KAgent response was interrupted; outcome may be uncertain"}
	}
	if len(body) > maxResponseBytes {
		return nil, protocolError("KAgent response exceeds transport byte limit")
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return sseResult(body, id)
	}
	if mediaType != "application/json" {
		return nil, protocolError("KAgent returned an unsupported HTTP content type")
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
				return nil, protocolError("KAgent SSE contains invalid JSON")
			}
			if _, present := envelope["id"]; present {
				return unwrap(event, id)
			}
		}
	}
	if scanner.Err() != nil {
		return nil, protocolError("KAgent SSE event exceeds transport limit")
	}
	return nil, protocolError("KAgent SSE ended without a JSON-RPC response")
}

func unwrap(body []byte, id string) (map[string]any, error) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, protocolError("KAgent returned invalid JSON")
	}
	if envelope["jsonrpc"] != "2.0" || envelope["id"] != id {
		return nil, protocolError("KAgent response has an invalid envelope or request ID")
	}
	if value, present := envelope["error"]; present {
		code := 0
		if remote, ok := value.(map[string]any); ok {
			if number, ok := remote["code"].(float64); ok {
				code = int(number)
			}
		}
		// Remote error messages can echo case content or credentials; don't log them.
		return nil, &Error{Kind: "rpc", Code: code, Message: fmt.Sprintf("KAgent JSON-RPC error %d", code)}
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		return nil, protocolError("KAgent response is missing an object result")
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
		return nil, &Error{Kind: "tool", Message: "KAgent tool " + name + " failed; inspect controller diagnostics"}
	}
	if output, ok := result["structuredContent"].(map[string]any); ok {
		return output, nil
	}
	if name == "read_sandbox_file" {
		if content, ok := result["content"].([]any); ok {
			return map[string]any{"content": content}, nil
		}
	}
	return nil, protocolError("KAgent tool " + name + " omitted structured output; check pinned version")
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
			return nil, protocolError("KAgent transcript does not match requested task")
		}
		entries, ok := page["items"].([]any)
		if !ok && page["items"] != nil {
			return nil, protocolError("KAgent transcript items are invalid")
		}
		for _, value := range entries {
			entry, ok := value.(map[string]any)
			if !ok || nonzero(entry["omitted_bytes"]) {
				return nil, protocolError("KAgent omitted transcript evidence")
			}
			index, ok := intValue(entry["index"])
			if !ok || index != len(items) {
				return nil, protocolError("KAgent transcript item indexes are not contiguous")
			}
			encoded, _ := json.Marshal(entry)
			totalBytes += len(encoded)
			items = append(items, entry)
			if len(items) > 4096 || totalBytes > maxTranscriptBytes {
				return nil, protocolError("KAgent transcript exceeds evidence limit")
			}
		}
		next := 0
		if raw, present := page["next_item"]; present {
			next, ok = intValue(raw)
			if !ok {
				return nil, protocolError("KAgent transcript cursor is invalid")
			}
		}
		if next == 0 {
			total, valid := intValue(page["total_items"])
			if page["truncated"] == true || !valid || total != len(items) {
				return nil, protocolError("KAgent returned an incomplete transcript")
			}
			return items, nil
		}
		if next <= cursor || next != len(items) {
			return nil, protocolError("KAgent transcript pagination did not advance correctly")
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
