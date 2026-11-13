package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bondrenai/TriageLab/internal/auth"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/resultchat"
)

type mcpTestResult struct {
	IsError           bool            `json:"isError"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	Content           []struct {
		Text string `json:"text"`
	} `json:"content"`
}

// Exercise the real SDK transport and argument schema, without a model or
// sandbox. Authentication-gate tests separately cover bearer verification.
func mcpRPC(t *testing.T, handler http.Handler, identity auth.Identity, method string, params any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), identityKey{}, identity))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || w.Header().Get("Mcp-Session-Id") != "" {
		t.Fatalf("MCP HTTP response: %d %v %s", w.Code, w.Header(), w.Body)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || len(envelope.Error) != 0 || len(envelope.Result) == 0 {
		t.Fatalf("MCP envelope: %s (%v)", w.Body, err)
	}
	return envelope.Result
}

func mcpCall(t *testing.T, handler http.Handler, identity auth.Identity, name string, args any) mcpTestResult {
	t.Helper()
	data := mcpRPC(t, handler, identity, "tools/call", map[string]any{"name": name, "arguments": args})
	var result mcpTestResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func mcpIdentity(role auth.Role) auth.Identity {
	return auth.Identity{Username: "mcp-fixture", Role: role, Via: "mcp"}
}

func mcpRequireError(t *testing.T, result mcpTestResult, contains string) {
	t.Helper()
	encoded, _ := json.Marshal(result)
	if !result.IsError || !strings.Contains(string(encoded), contains) {
		t.Fatalf("expected MCP tool error containing %q, got %s", contains, encoded)
	}
}

func TestMCPTransportSchemasAndPerCallRoles(t *testing.T) {
	s := newTestServer(t, nil)
	handler := s.newMCPHandler()
	viewer, user := mcpIdentity(auth.RoleViewer), mcpIdentity(auth.RoleUser)
	var listing struct {
		Tools []struct {
			Name        string
			InputSchema map[string]any `json:"inputSchema"`
		}
	}
	if err := json.Unmarshal(mcpRPC(t, handler, viewer, "tools/list", map[string]any{}), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Tools) != 11 {
		t.Fatalf("tools = %d", len(listing.Tools))
	}
	for _, tool := range listing.Tools {
		closed, _ := tool.InputSchema["additionalProperties"].(bool)
		if _, present := tool.InputSchema["additionalProperties"]; !present || closed {
			t.Fatalf("%s schema permits unknown fields: %v", tool.Name, tool.InputSchema)
		}
	}
	for _, identity := range []auth.Identity{{}, {Username: "browser", Role: auth.RoleAdmin, Via: "password"}} {
		mcpRequireError(t, mcpCall(t, handler, identity, "list_investigations", map[string]any{}), "not authorized")
	}
	if result := mcpCall(t, handler, viewer, "list_investigations", map[string]any{}); result.IsError {
		t.Fatalf("viewer cannot read: %+v", result)
	}
	for name, args := range map[string]any{
		"start_investigation":  map[string]any{"description": "A problem"},
		"pause_investigation":  map[string]any{"run_id": retainedRunID},
		"steer_investigation":  map[string]any{"run_id": retainedRunID, "round": 1, "resume_settings": strings.Repeat("a", 64), "description": "Check another file"},
		"resume_investigation": map[string]any{"run_id": retainedRunID, "round": 1, "resume_settings": strings.Repeat("a", 64), "description": "More information"},
		"close_investigation":  map[string]any{"run_id": retainedRunID, "round": 1, "resume_settings": strings.Repeat("a", 64)},
		"ask_investigation":    map[string]any{"run_id": retainedRunID, "message": "Why?"},
	} {
		t.Run(name, func(t *testing.T) { mcpRequireError(t, mcpCall(t, handler, viewer, name, args), "not authorized") })
	}
	// A successful request does not lend its identity to the next request.
	mcpRequireError(t, mcpCall(t, handler, auth.Identity{}, "list_investigations", map[string]any{}), "not authorized")
	mcpRequireError(t, mcpCall(t, handler, user, "start_investigation", map[string]any{"description": "An issue", "source": map[string]any{"kind": "ticket"}}), "validating")
	mcpRequireError(t, mcpCall(t, handler, user, "start_investigation", map[string]any{"description": "An issue", "files": []any{map[string]any{"path": "a.txt", "content": "safe", "host_path": "/etc/passwd"}}}), "validating")
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, "http://localhost/mcp", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("stateless %s: %d", method, w.Code)
		}
	}
}

func TestMCPRejectsUnsafeInputsWithoutUpstreamCalls(t *testing.T) {
	s := newTestServer(t, nil)
	handler := s.newMCPHandler()
	user := mcpIdentity(auth.RoleUser)
	for _, path := range []string{"../secret", "/etc/passwd", "nested/../../secret", `nested\secret`, ".env", "node_modules/package/index.js"} {
		t.Run(path, func(t *testing.T) {
			result := mcpCall(t, handler, user, "start_investigation", mcpStartInput{Description: "A problem", Files: []mcpFile{{Path: path, Content: "safe"}}})
			if !result.IsError {
				t.Fatalf("accepted attachment %q", path)
			}
		})
	}
	for _, input := range []mcpStartInput{
		{Description: " "}, {Description: strings.Repeat("x", casefile.MaxDescriptionBytes+1)},
		{Description: "A problem", Files: []mcpFile{{Path: "a", Content: strings.Repeat("x", casefile.MaxFileBytes+1)}}},
		{Description: "A problem", Files: []mcpFile{{Path: "a", Content: "a"}, {Path: "a", Content: "b"}}},
		{Description: "A problem", Agent: "unapproved-agent"}, {Description: "A problem", Sandbox: "unapproved-template"},
	} {
		if result := mcpCall(t, handler, user, "start_investigation", input); !result.IsError {
			t.Fatalf("unsafe start accepted")
		}
	}
	for _, id := range []string{"../../etc/passwd", retainedRunID + "/../../lifecycle.json", "http://evil.example/report.json"} {
		mcpRequireError(t, mcpCall(t, handler, user, "get_investigation", mcpGetInput{RunID: id}), "UUID")
	}
	state := retainedState(s)
	saveRetainedRun(t, s, state)
	for _, action := range []string{"resume_investigation", "close_investigation"} {
		args := map[string]any{"run_id": state.RunID, "round": state.Round, "resume_settings": strings.Repeat("a", 64)}
		if action == "resume_investigation" {
			args["description"] = "More information"
		}
		mcpRequireError(t, mcpCall(t, handler, user, action, args), "investigation changed")
	}
	state.Trigger = "ticket"
	saveRetainedRun(t, s, state)
	mcpRequireError(t, mcpCall(t, handler, user, "resume_investigation", mcpResumeInput{
		mcpReviewInput: mcpReviewInput{RunID: state.RunID, Round: state.Round, ResumeSettings: resumeSettings(state)}, Description: "More information",
	}), "ticket approval is unavailable")
}

func TestMCPStartReturnsBeforeExecutionAndKeepsActorAndBusySlot(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	s := newTestServer(t, testBackend{call: func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errors.New("fixture upstream unavailable")
	}})
	handler := s.newMCPHandler()
	user := mcpIdentity(auth.RoleUser)
	result := mcpCall(t, handler, user, "start_investigation", mcpStartInput{Description: "Investigate the provided source", Files: []mcpFile{{Path: "nested/app.py", Content: "print('ok')"}}})
	if result.IsError {
		t.Fatalf("start failed: %+v", result)
	}
	var receipt struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result.StructuredContent, &receipt); err != nil || !runID.MatchString(receipt.ID) {
		t.Fatalf("receipt: %s", result.StructuredContent)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("execution did not start")
	}
	state, err := s.loadRunState(receipt.ID)
	if err != nil || state.Actor != user.Username || state.Trigger != "mcp" {
		t.Fatalf("actor/trigger: %+v %v", state, err)
	}
	saved, err := s.readReport(receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := saved["case"].(map[string]any)
	if c["source"].(map[string]any)["kind"] != "upload" {
		t.Fatalf("manual run provenance: %v", c["source"])
	}
	mcpRequireError(t, mcpCall(t, handler, user, "start_investigation", mcpStartInput{Description: "Concurrent work"}), "already running")
	s.mu.Lock()
	done := s.active.done
	s.mu.Unlock()
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture run did not finish")
	}
}

func TestMCPReadSeparatesModelFromRoundScopedEvidenceAndPrivateState(t *testing.T) {
	s := newTestServer(t, nil)
	state := retainedState(s)
	state.Round = 2
	state.Config.TokenEnv = "PRIVATE_CREDENTIAL_VARIABLE"
	saveRetainedRun(t, s, state)
	saved, err := s.readReport(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	saved["analysis"] = map[string]any{"summary": "A model hypothesis", "observations": []string{"Not process output"}}
	saved["experiments"] = []any{map[string]any{"process_id": "same-id", "state": "PROCESS_STATE_EXITED", "exit_code": 0, "stdout": "current server-test-secret output", "outputs_complete": true}}
	saved["rounds"] = []any{map[string]any{"round": 1, "experiments": []any{map[string]any{"process_id": "same-id", "state": "PROCESS_STATE_EXITED", "exit_code": 1, "stdout": "historical output", "truncated": true}}}}
	if err := report.Write(filepath.Join(s.directory, state.RunID), saved); err != nil {
		t.Fatal(err)
	}
	handler := s.newMCPHandler()
	viewer := mcpIdentity(auth.RoleViewer)
	result := mcpCall(t, handler, viewer, "get_investigation", mcpGetInput{RunID: state.RunID, ProcessLimit: 1})
	if result.IsError {
		t.Fatalf("get failed: %+v", result)
	}
	for _, secret := range []string{"PRIVATE_CREDENTIAL_VARIABLE", "private-sandbox", "private-session", "server-test-secret", "historical output", "current "} {
		if strings.Contains(string(result.StructuredContent), secret) {
			t.Fatalf("unexpected detail in summary: %s", result.StructuredContent)
		}
	}
	var view struct {
		Lifecycle    lifecycleView
		ProcessCount int `json:"process_count"`
		Processes    []map[string]any
		ModelSummary string `json:"model_summary"`
	}
	if err := json.Unmarshal(result.StructuredContent, &view); err != nil {
		t.Fatal(err)
	}
	if view.Lifecycle.Round != 2 || view.Lifecycle.ResumeSettings != resumeSettings(state) || view.ProcessCount != 2 || len(view.Processes) != 1 || view.ModelSummary != "A model hypothesis" {
		t.Fatalf("summary projection: %+v", view)
	}
	for round, want := range map[int]string{1: "historical output", 2: "current [REDACTED] output"} {
		result = mcpCall(t, handler, viewer, "get_process_evidence", mcpEvidenceInput{RunID: state.RunID, Round: round, ProcessID: "same-id"})
		var evidence struct {
			Page            mcpTextPage
			StreamAvailable bool `json:"stream_available"`
		}
		if result.IsError || json.Unmarshal(result.StructuredContent, &evidence) != nil || evidence.Page.Content != want || !evidence.StreamAvailable {
			t.Fatalf("round %d evidence: %+v", round, result)
		}
		if strings.Contains(string(result.StructuredContent), "model hypothesis") {
			t.Fatal("model text became process evidence")
		}
	}
	result = mcpCall(t, handler, viewer, "get_process_evidence", mcpEvidenceInput{RunID: state.RunID, Round: 2, ProcessID: "same-id", Stream: "stderr"})
	if result.IsError || !strings.Contains(string(result.StructuredContent), `"stream_available":false`) {
		t.Fatalf("missing stream not distinguished: %+v", result)
	}
	mcpRequireError(t, mcpCall(t, handler, viewer, "get_process_evidence", mcpEvidenceInput{RunID: state.RunID, Round: 3, ProcessID: "same-id"}), "No unique saved process")
	result = mcpCall(t, handler, viewer, "read_investigation_report", mcpReportInput{RunID: state.RunID, mcpPageInput: mcpPageInput{MaxBytes: 32}})
	var document struct{ Page mcpTextPage }
	if result.IsError || json.Unmarshal(result.StructuredContent, &document) != nil || !document.Page.HasMore || len(document.Page.Content) > 32 {
		t.Fatalf("report page: %+v", result)
	}
}

func TestMCPTextPagesAreBoundedAndSnapshotBound(t *testing.T) {
	data := []byte("A€😀B" + strings.Repeat("z", 100))
	var complete strings.Builder
	input := mcpPageInput{MaxBytes: 4}
	for {
		page, err := mcpTextWindow(data, input)
		if err != nil || !utf8.ValidString(page.Content) || len(page.Content) > 4 {
			t.Fatalf("page: %+v %v", page, err)
		}
		complete.WriteString(page.Content)
		if !page.HasMore {
			break
		}
		if page.NextOffset <= input.Offset {
			t.Fatal("paging did not advance")
		}
		input.Offset, input.Snapshot = page.NextOffset, page.Snapshot
	}
	if complete.String() != string(data) {
		t.Fatal("pages changed saved content")
	}
	for _, bad := range []mcpPageInput{{Offset: 1}, {MaxBytes: -1}, {MaxBytes: mcpMaxPageBytes + 1}, {Offset: -1}, {Snapshot: "changed"}, {Offset: 2, Snapshot: input.Snapshot}, {Offset: len(data) + 1, Snapshot: input.Snapshot}} {
		if _, err := mcpTextWindow(data, bad); err == nil {
			t.Fatalf("accepted invalid page: %+v", bad)
		}
	}
	if _, err := mcpTextWindow(append(data, '!'), input); err == nil {
		t.Fatal("accepted a changed snapshot")
	}
}

func TestMCPEvidenceRejectsMalformedOrAmbiguousRoundAttribution(t *testing.T) {
	s := newTestServer(t, nil)
	handler := s.newMCPHandler()
	viewer := mcpIdentity(auth.RoleViewer)
	process := map[string]any{"process_id": "p", "stdout": "observed output"}
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"null current round", func(saved map[string]any) { saved["round"] = nil }},
		{"string current round", func(saved map[string]any) { saved["round"] = "2" }},
		{"zero current round", func(saved map[string]any) { saved["round"] = 0 }},
		{"fractional current round", func(saved map[string]any) { saved["round"] = 1.5 }},
		{"out of range round", func(saved map[string]any) { saved["round"] = 33 }},
		{"missing historical round", func(saved map[string]any) { saved["rounds"] = []any{map[string]any{"experiments": []any{process}}} }},
		{"duplicated historical round", func(saved map[string]any) {
			saved["rounds"] = []any{map[string]any{"round": 1}, map[string]any{"round": 1}}
		}},
		{"historical current round", func(saved map[string]any) {
			saved["rounds"] = []any{map[string]any{"round": 2, "experiments": []any{process}}}
		}},
		{"malformed history", func(saved map[string]any) { saved["rounds"] = "not an array" }},
		{"malformed historical record", func(saved map[string]any) { saved["rounds"] = []any{"not an object"} }},
		{"duplicate process", func(saved map[string]any) { saved["experiments"] = []any{process, process} }},
		{"missing process identity", func(saved map[string]any) { saved["experiments"] = []any{map[string]any{"stdout": "unknown process"}} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved := map[string]any{"run_id": retainedRunID, "round": 2, "experiments": []any{process}}
			test.change(saved)
			if err := report.Write(filepath.Join(s.directory, retainedRunID), saved); err != nil {
				t.Fatal(err)
			}
			for name, input := range map[string]any{
				"get_investigation":    mcpGetInput{RunID: retainedRunID},
				"get_process_evidence": mcpEvidenceInput{RunID: retainedRunID, Round: 1, ProcessID: "p"},
			} {
				result := mcpCall(t, handler, viewer, name, input)
				if !result.IsError || len(result.StructuredContent) != 0 {
					t.Fatalf("%s attributed malformed evidence: %+v", name, result)
				}
			}
		})
	}
	// Before multi-round reports, an absent current round meant round one.
	legacy := map[string]any{"run_id": retainedRunID, "experiments": []any{process}}
	if err := report.Write(filepath.Join(s.directory, retainedRunID), legacy); err != nil {
		t.Fatal(err)
	}
	result := mcpCall(t, handler, viewer, "get_process_evidence", mcpEvidenceInput{RunID: retainedRunID, Round: 1, ProcessID: "p"})
	if result.IsError || !strings.Contains(string(result.StructuredContent), "observed output") {
		t.Fatalf("legacy round one lost: %+v", result)
	}
}

func TestMCPChatPagesAndMutationReceiptDoNotReturnTheWholeJournal(t *testing.T) {
	s := newTestServer(t, nil)
	savedChatReport(t, s, "completed")
	record := emptyChat(workerUUID)
	for index := 0; index < 60; index++ {
		role, size := "user", resultchat.MaxMessageBytes
		if index%2 == 1 {
			role, size = "assistant", resultchat.MaxAnswerBytes
		}
		record.Messages = append(record.Messages, chatMessage{ID: fmt.Sprintf("saved-%d", index), Role: role, Content: strings.Repeat("x", size), CreatedAt: "2026-11-11T12:00:00Z"})
	}
	root, err := s.reportRoot(workerUUID)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writeChat(root, record); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	s.chat = fakeChat(func(ctx context.Context, _ map[string]any, _ []resultchat.Message, message string) (resultchat.Answer, error) {
		if message != "Explain this result" {
			t.Errorf("unexpected question %q", message)
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return resultchat.Answer{}, ctx.Err()
		}
		return resultchat.Answer{Content: "Saved evidence suggests a missing setting."}, nil
	})
	handler := s.newMCPHandler()
	viewer, user := mcpIdentity(auth.RoleViewer), mcpIdentity(auth.RoleUser)
	result := mcpCall(t, handler, viewer, "get_investigation_chat", mcpChatInput{RunID: workerUUID})
	var page struct {
		Messages      []chatMessage
		TotalMessages int  `json:"total_messages"`
		NextOffset    int  `json:"next_offset"`
		HasMore       bool `json:"has_more"`
		CanAsk        bool `json:"can_ask"`
	}
	if result.IsError || json.Unmarshal(result.StructuredContent, &page) != nil || len(page.Messages) != 2 || page.TotalMessages != 60 || page.NextOffset != 2 || !page.HasMore || page.CanAsk {
		t.Fatalf("chat page: %+v", result)
	}
	mcpRequireError(t, mcpCall(t, handler, user, "ask_investigation", mcpAskInput{RunID: workerUUID, Message: strings.Repeat("x", resultchat.MaxMessageBytes+1)}), "4096")
	result = mcpCall(t, handler, user, "ask_investigation", mcpAskInput{RunID: workerUUID, Message: "Explain this result"})
	var receipt struct {
		Status            string
		AcceptedMessageID string `json:"accepted_message_id"`
		TotalMessages     int    `json:"total_messages"`
	}
	if result.IsError || json.Unmarshal(result.StructuredContent, &receipt) != nil || receipt.Status != "answering" || receipt.AcceptedMessageID == "" || receipt.TotalMessages != 61 || len(result.StructuredContent) > 1024 {
		t.Fatalf("chat mutation receipt: %+v", result)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("answer did not start")
	}
	mcpRequireError(t, mcpCall(t, handler, user, "ask_investigation", mcpAskInput{RunID: workerUUID, Message: "Duplicate"}), "being prepared")
	releaseOnce.Do(func() { close(release) })
	waitChatJob(t, s)
	result = mcpCall(t, handler, viewer, "get_investigation_chat", mcpChatInput{RunID: workerUUID, Offset: 60})
	if result.IsError || json.Unmarshal(result.StructuredContent, &page) != nil || len(page.Messages) != 2 || page.TotalMessages != 62 || page.HasMore || page.Messages[0].ID != receipt.AcceptedMessageID || page.Messages[1].Role != "assistant" {
		t.Fatalf("chat completion page: %+v", result)
	}
}
