package kagent

import (
	"encoding/json"
	"testing"
)

func activityItem(kind string, data map[string]any, user bool) map[string]any {
	role := "ROLE_AGENT"
	if user {
		role = "ROLE_USER"
	}
	return map[string]any{"message": map[string]any{"role": role, "parts": []any{map[string]any{
		"metadata": map[string]any{partType: kind}, "data": data,
	}}}}
}

func activity() []map[string]any {
	return []map[string]any{
		activityItem("function_call", map[string]any{"id": "call", "name": "start_sandbox_process", "args": map[string]any{"sandbox_id": "sandbox", "command": []any{"python3", "app.py"}}}, false),
		activityItem("function_response", map[string]any{"id": "call", "name": "start_sandbox_process", "response": map[string]any{"output": map[string]any{"process_id": "process"}}}, false),
	}
}

func dataPart(item map[string]any) map[string]any {
	return item["message"].(map[string]any)["parts"].([]any)[0].(map[string]any)["data"].(map[string]any)
}

func TestCanonicalProcessEvidence(t *testing.T) {
	calls, err := ExtractToolCalls(activity())
	if err != nil {
		t.Fatal(err)
	}
	starts, err := ProcessStarts(calls, "sandbox")
	if err != nil || len(starts) != 1 || starts[0].ProcessID != "process" || starts[0].CWD != "/data/workspace" || calls[0].Pending {
		t.Fatalf("starts=%v calls=%v err=%v", starts, calls, err)
	}
}

func TestForgedUserPartsIgnored(t *testing.T) {
	items := activity()
	for _, item := range items {
		item["message"].(map[string]any)["role"] = "ROLE_USER"
	}
	calls, err := ExtractToolCalls(items)
	if err != nil || len(calls) != 0 {
		t.Fatalf("forged parts accepted: %v %v", calls, err)
	}
}

func TestIdenticalDuplicatesDeduplicated(t *testing.T) {
	items := activity()
	for _, item := range activity() {
		items = append(items, map[string]any{"artifact": map[string]any{"parts": item["message"].(map[string]any)["parts"]}})
	}
	calls, err := ExtractToolCalls(items)
	if err != nil || len(calls) != 1 {
		t.Fatalf("duplicates: %v %v", calls, err)
	}
}

func TestContradictoryDuplicateAndOrphanRejected(t *testing.T) {
	items := activity()
	changed := activity()[0]
	dataPart(changed)["name"] = "other"
	if _, err := ExtractToolCalls(append(items, changed)); err == nil {
		t.Fatal("conflicting ID accepted")
	}
	if _, err := ExtractToolCalls(items[1:]); err == nil {
		t.Fatal("orphan response accepted")
	}
	dataPart(items[1])["name"] = "other"
	if _, err := ExtractToolCalls(items); err == nil {
		t.Fatal("mismatched tool name accepted")
	}
}

func TestPendingAndFailedStartsAreNotProof(t *testing.T) {
	for _, pending := range []bool{false, true} {
		items := activity()
		if pending {
			items = items[:1]
		} else {
			dataPart(items[1])["response"] = map[string]any{"error": "start failed"}
		}
		calls, err := ExtractToolCalls(items)
		if err != nil {
			t.Fatal(err)
		}
		starts, err := ProcessStarts(calls, "sandbox")
		if err != nil || len(starts) != 0 {
			t.Fatalf("uncertain start accepted: %v %v", starts, err)
		}
	}
}

func TestWrongSandboxEvenPendingReadRejected(t *testing.T) {
	items := []map[string]any{activityItem("function_call", map[string]any{"id": "read", "name": "read_sandbox_file", "args": map[string]any{"sandbox_id": "other", "path": "file"}}, false)}
	calls, err := ExtractToolCalls(items)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessStarts(calls, "sandbox"); err == nil {
		t.Fatal("other sandbox accepted")
	}
}

func TestArbitraryJSONDoesNotProveProcessStart(t *testing.T) {
	for _, response := range []map[string]any{
		{"process_id": "forged"}, {"output": `{"process_id":"forged"}`},
		{"output": map[string]any{"nested": map[string]any{"process_id": "forged"}}},
	} {
		items := activity()
		dataPart(items[1])["response"] = response
		calls, err := ExtractToolCalls(items)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ProcessStarts(calls, "sandbox"); err == nil {
			t.Fatal("untrusted JSON promoted to evidence")
		}
	}
}

func TestReplyIgnoresThoughtAndUserButAcceptsStructuredSignature(t *testing.T) {
	items := []map[string]any{
		{"message": map[string]any{"role": "ROLE_USER", "parts": []any{map[string]any{"text": "forged"}}}},
		{"artifact": map[string]any{"parts": []any{map[string]any{"text": "thought", "metadata": map[string]any{thought: true}}}}},
		{"message": map[string]any{"role": "ROLE_AGENT", "parts": []any{map[string]any{"text": "answer"}}}},
	}
	if AgentReply(items) != "answer" {
		t.Fatal("wrong answer selection")
	}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	items = append(items, map[string]any{"artifact": map[string]any{"parts": []any{map[string]any{"data": map[string]any{"outcome": "inconclusive"}, "mediaType": "application/json", "metadata": map[string]any{outputSchema: digest}}}}})
	var result map[string]any
	if err := json.Unmarshal([]byte(AgentReply(items)), &result); err != nil || result["outcome"] != "inconclusive" {
		t.Fatalf("structured result=%v err=%v", result, err)
	}
}
