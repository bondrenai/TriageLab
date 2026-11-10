package kagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These are protocol fixtures, never a fake implementation for the live demo.
func testRequest(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func testResponse(w http.ResponseWriter, request map[string]any, result map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": result})
}

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL+"/mcp", "investigator", "test-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestStatelessContractJSONAndSSE(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprint(sse), func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				request := testRequest(t, r)
				if r.URL.Path != "/mcp" || r.Header.Get("Mcp-Method") != "tools/call" || r.Header.Get("Mcp-Name") != "create_sandbox" || r.Header.Get("Mcp-Protocol-Version") != MCPVersion {
					t.Errorf("wrong path/headers: %s %v", r.URL, r.Header)
				}
				if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-User-Id") != "investigator" {
					t.Error("missing identity headers")
				}
				params := request["params"].(map[string]any)
				meta := params["_meta"].(map[string]any)
				if meta["io.modelcontextprotocol/protocolVersion"] != MCPVersion {
					t.Error("missing stateless metadata")
				}
				if params["arguments"].(map[string]any)["request_id"] != "stable" {
					t.Error("lost idempotency key")
				}
				result := map[string]any{"structuredContent": map[string]any{"id": "sandbox"}}
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
					body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": result})
					_, _ = fmt.Fprintf(w, ": heartbeat\r\nevent: message\r\ndata: %s\r\n\r\n", body)
				} else {
					testResponse(w, request, result)
				}
			})
			got, err := client.CallTool(context.Background(), "create_sandbox", map[string]any{"request_id": "stable"})
			if err != nil || got["id"] != "sandbox" || calls.Load() != 1 {
				t.Fatalf("result=%v err=%v calls=%d", got, err, calls.Load())
			}
		})
	}
}

func TestToolFailureDoesNotEchoSecret(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		testResponse(w, testRequest(t, r), map[string]any{"isError": true, "content": []any{map[string]any{"text": "secret-token"}}})
	})
	_, err := client.CallTool(context.Background(), "create_sandbox", nil)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestHTTPFailuresNeverRedirectOrRetry(t *testing.T) {
	for _, status := range []int{302, 401, 404, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "http://127.0.0.1:1/forbidden")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("secret-token"))
			})
			_, err := client.CallTool(context.Background(), "start_sandbox_process", nil)
			if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestUnsafeEndpointsRejected(t *testing.T) {
	for _, endpoint := range []string{"http://remote.example", "https://user:password@example.com", "https://host/?secret=1", "file:///tmp/controller", "https://host/#fragment"} {
		if _, err := New(endpoint, "user", "", time.Second); err == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
	for _, endpoint := range []string{"http://remote.example", "http://triagelab-tools.kagent.svc.example.net", "http://10.0.0.7:8083"} {
		if _, err := New(endpoint, "user", "", time.Second, AllowClusterHTTP()); err == nil {
			t.Errorf("cluster opt-in accepted %s", endpoint)
		}
	}
	if _, err := New("http://triagelab-tools.kagent.svc.cluster.local:8083/mcp", "user", "", time.Second); err == nil {
		t.Error("cluster service endpoint accepted without the opt-in")
	}
	if _, err := New("http://triagelab-tools.kagent.svc.cluster.local:8083/mcp", "user", "", time.Second, AllowClusterHTTP()); err != nil {
		t.Errorf("cluster service endpoint rejected with the opt-in: %v", err)
	}
	if _, err := New("http://triagelab-tools.kagent.svc:8083/mcp", "user", "", time.Second, AllowClusterHTTP()); err != nil {
		t.Errorf("short cluster service name rejected with the opt-in: %v", err)
	}
}

// Configuration validation and the client must apply one endpoint policy.
func TestCheckEndpointMatchesClientPolicy(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:8083/mcp", "http://LOCALHOST:8083", "http://[::1]:8083/mcp", "https://kagent.example.com/mcp",
		"http://remote.example", "http://10.0.0.7:8083", "http://triagelab-tools.kagent.svc:8083/mcp",
		"http://triagelab-tools.kagent.svc.cluster.local:8083/mcp", "http://triagelab-tools.kagent.svc.example.net",
		"https://user:password@example.com", "https://host/?secret=1", "https://host/#fragment", "file:///tmp/controller", "",
	} {
		for _, allow := range []bool{false, true} {
			var opts []Option
			if allow {
				opts = append(opts, AllowClusterHTTP())
			}
			_, clientErr := New(endpoint, "user", "", time.Second, opts...)
			checkErr := CheckEndpoint(endpoint, allow)
			if (clientErr == nil) != (checkErr == nil) {
				t.Errorf("%q allow=%v: client=%v check=%v", endpoint, allow, clientErr, checkErr)
			}
		}
	}
}

func TestClientInfoAnnouncesSuppliedBuildVersion(t *testing.T) {
	for _, test := range []struct {
		options []Option
		want    string
	}{{nil, "unknown"}, {[]Option{ClientVersion("v9.9.9")}, "v9.9.9"}} {
		t.Run(test.want, func(t *testing.T) {
			var announced atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := testRequest(t, r)
				info, _ := request["params"].(map[string]any)["_meta"].(map[string]any)["io.modelcontextprotocol/clientInfo"].(map[string]any)
				announced.Store(fmt.Sprint(info["name"], " ", info["version"]))
				testResponse(w, request, map[string]any{"structuredContent": map[string]any{}})
			}))
			defer server.Close()
			client, err := New(server.URL+"/mcp", "user", "", time.Second, test.options...)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err := client.CallTool(context.Background(), "list_sandbox_templates", nil); err != nil {
				t.Fatal(err)
			}
			if got := announced.Load(); got != "triagelab "+test.want {
				t.Fatalf("clientInfo=%v, want triagelab %s", got, test.want)
			}
		})
	}
}

func TestUncertainRequestNotRetried(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	})
	client.http.Timeout = 20 * time.Millisecond
	_, err := client.CallTool(context.Background(), "start_sandbox_process", nil)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestWrongResponseIDAndTextEvidenceFail(t *testing.T) {
	for _, wrongID := range []bool{false, true} {
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			request := testRequest(t, r)
			if wrongID {
				request["id"] = "different"
			}
			testResponse(w, request, map[string]any{"content": []any{map[string]any{"text": `{"exit_code":0}`}}})
		})
		if _, err := client.CallTool(context.Background(), "get_sandbox_process", nil); err == nil {
			t.Fatal("untrusted response accepted")
		}
	}
}

func TestTranscriptPaginationAndTruncation(t *testing.T) {
	for _, omitted := range []bool{false, true} {
		t.Run(fmt.Sprint(omitted), func(t *testing.T) {
			var cursors []int
			var cursorsMu sync.Mutex
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				request := testRequest(t, r)
				args := request["params"].(map[string]any)["arguments"].(map[string]any)
				cursor := int(args["from_item"].(float64))
				cursorsMu.Lock()
				cursors = append(cursors, cursor)
				cursorsMu.Unlock()
				entry := map[string]any{"index": cursor, "message": map[string]any{"parts": []any{}}}
				if omitted {
					entry["omitted_bytes"] = 5000
				}
				page := map[string]any{"session_id": "s", "task_id": "t", "total_items": 2, "items": []any{entry}}
				if cursor == 0 {
					page["next_item"] = 1
					page["truncated"] = true
				}
				testResponse(w, request, map[string]any{"structuredContent": page})
			})
			items, err := client.TaskItems(context.Background(), "s", "t")
			if omitted {
				if err == nil {
					t.Fatal("omitted evidence accepted")
				}
				return
			}
			cursorsMu.Lock()
			defer cursorsMu.Unlock()
			if err != nil || len(items) != 2 || !reflect.DeepEqual(cursors, []int{0, 1}) {
				t.Fatalf("items=%v err=%v cursors=%v", items, err, cursors)
			}
		})
	}
}

func TestCancellationUpstreamReference(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		request := testRequest(t, r)
		params := request["params"].(map[string]any)
		ref := params["taskId"].(string)
		if request["method"] != "tasks/cancel" || r.Header.Get("Mcp-Name") != ref {
			t.Error("wrong cancel routing")
		}
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, "v1."))
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]string
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		if value["sessionId"] != "session" || value["taskId"] != "task" {
			t.Errorf("reference=%v", value)
		}
		testResponse(w, request, map[string]any{"resultType": "complete"})
	})
	if err := client.CancelTask(context.Background(), "session", "task"); err != nil {
		t.Fatal(err)
	}
}
