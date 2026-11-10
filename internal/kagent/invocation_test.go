package kagent

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const invocationSession = "11111111-1111-4111-8111-111111111111"
const invocationTaskID = "22222222-2222-4222-8222-222222222222"

func TestInvokeAgentReturnsAcceptedTask(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		request := testRequest(t, r)
		params := request["params"].(map[string]any)
		args := params["arguments"].(map[string]any)
		if params["name"] != "invoke_agent" || args["session_id"] != invocationSession || args["message_id"] != "round-2" || args["message"] != "new evidence" {
			t.Error("continuation arguments changed")
		}
		capabilities := params["_meta"].(map[string]any)["io.modelcontextprotocol/clientCapabilities"].(map[string]any)
		if _, ok := capabilities["extensions"].(map[string]any)[tasksExtension]; !ok {
			t.Error("missing asynchronous task capability")
		}
		testResponse(w, request, map[string]any{
			"resultType": "task", "taskId": invocationReference(`{"sessionId":"` + invocationSession + `","taskId":"` + invocationTaskID + `"}`),
			"status": "working", "ttlMs": nil,
		})
	})
	got, err := client.CallTool(context.Background(), "invoke_agent", map[string]any{
		"session_id": invocationSession, "message_id": "round-2", "message": "new evidence",
	})
	want := map[string]any{"session_id": invocationSession, "task_id": invocationTaskID}
	if err != nil || !reflect.DeepEqual(got, want) || calls.Load() != 1 {
		t.Fatalf("result=%v err=%v calls=%d", got, err, calls.Load())
	}
}

func TestInvokeAgentRejectsInvalidReceipts(t *testing.T) {
	validJSON := `{"sessionId":"` + invocationSession + `","taskId":"` + invocationTaskID + `"}`
	scenarios := []struct {
		name      string
		receipt   map[string]any
		sessionID string
	}{
		{name: "wrong session", receipt: map[string]any{"resultType": "task", "taskId": invocationReference(validJSON)}, sessionID: "33333333-3333-4333-8333-333333333333"},
		{name: "missing requested session", receipt: map[string]any{"resultType": "task", "taskId": invocationReference(validJSON)}},
		{name: "missing task reference", receipt: map[string]any{"resultType": "task"}, sessionID: invocationSession},
		{name: "numeric task reference", receipt: map[string]any{"resultType": "task", "taskId": 123}, sessionID: invocationSession},
		{name: "unsupported result", receipt: map[string]any{"resultType": "complete", "taskId": invocationReference(validJSON)}, sessionID: invocationSession},
		{name: "unexpected structured output", receipt: map[string]any{"structuredContent": map[string]any{"session_id": invocationSession, "task_id": invocationTaskID}}, sessionID: invocationSession},
		{name: "unsupported reference version", receipt: map[string]any{"resultType": "task", "taskId": "v2." + strings.TrimPrefix(invocationReference(validJSON), "v1.")}, sessionID: invocationSession},
		{name: "invalid base64", receipt: map[string]any{"resultType": "task", "taskId": "v1.!invalid!"}, sessionID: invocationSession},
		{name: "noncanonical base64", receipt: map[string]any{"resultType": "task", "taskId": invocationReference(validJSON) + "\n"}, sessionID: invocationSession},
		{name: "oversized reference", receipt: map[string]any{"resultType": "task", "taskId": "v1." + strings.Repeat("a", 1024)}, sessionID: invocationSession},
		{name: "invalid JSON", receipt: map[string]any{"resultType": "task", "taskId": invocationReference("not JSON")}, sessionID: invocationSession},
		{name: "trailing JSON", receipt: map[string]any{"resultType": "task", "taskId": invocationReference(validJSON + "{}")}, sessionID: invocationSession},
		{name: "unknown reference field", receipt: map[string]any{"resultType": "task", "taskId": invocationReference(strings.TrimSuffix(validJSON, "}") + `,"unexpected":true}`)}, sessionID: invocationSession},
		{name: "missing task ID", receipt: map[string]any{"resultType": "task", "taskId": invocationReference(`{"sessionId":"` + invocationSession + `"}`)}, sessionID: invocationSession},
		{name: "invalid task ID", receipt: map[string]any{"resultType": "task", "taskId": invocationReference(strings.ReplaceAll(validJSON, invocationTaskID, "invalid"))}, sessionID: invocationSession},
		{name: "invalid session ID", receipt: map[string]any{"resultType": "task", "taskId": invocationReference(strings.ReplaceAll(validJSON, invocationSession, "invalid"))}, sessionID: invocationSession},
		{name: "zero task ID", receipt: map[string]any{"resultType": "task", "taskId": invocationReference(strings.ReplaceAll(validJSON, invocationTaskID, "00000000-0000-0000-0000-000000000000"))}, sessionID: invocationSession},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				testResponse(w, testRequest(t, r), scenario.receipt)
			})
			_, err := client.CallTool(context.Background(), "invoke_agent", map[string]any{"session_id": scenario.sessionID, "message": "new evidence"})
			var upstream *Error
			if !errors.As(err, &upstream) || upstream.Kind != "protocol" || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestInvokeAgentDoesNotRetryUncertainOutcome(t *testing.T) {
	for _, interruption := range []string{"timeout", "connection closed"} {
		t.Run(interruption, func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_ = testRequest(t, r)
				if interruption == "timeout" {
					select {
					case <-r.Context().Done():
					case <-time.After(100 * time.Millisecond):
					}
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			})
			client.http.Timeout = 20 * time.Millisecond
			_, err := client.CallTool(context.Background(), "invoke_agent", map[string]any{"session_id": invocationSession, "message": "new evidence"})
			var upstream *Error
			if !errors.As(err, &upstream) || (upstream.Kind != "timeout" && upstream.Kind != "transport") || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func invocationReference(data string) string {
	return "v1." + base64.RawURLEncoding.EncodeToString([]byte(data))
}
