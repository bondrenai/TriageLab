package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/report"
)

const (
	cleanupRun   = "11111111-1111-4111-8111-111111111111"
	cleanupOther = "22222222-2222-4222-8222-222222222222"
)

func savedRun(t *testing.T, s *Server, id string, edit func(map[string]any)) {
	t.Helper()
	saved := map[string]any{
		"schema_version": 1, "run_id": id, "status": "running", "outcome": "inconclusive",
		"case":        map[string]any{"description": "fixture"},
		"sandbox":     map[string]any{"id": "sandbox-1"},
		"session":     map[string]any{"id": "session-1", "task_id": "task-1"},
		"task_state":  "TASK_STATE_COMPLETED",
		"connection":  map[string]any{"endpoint": s.config.Endpoint, "user_id": s.config.UserID, "namespace": s.config.Namespace},
		"cleanup":     map[string]any{"sandbox_deleted": false, "session_deleted": false, "errors": []string{"sandbox cleanup: earlier failure"}},
		"experiments": []any{}, "errors": []string{}, "warnings": []string{},
	}
	if edit != nil {
		edit(saved)
	}
	dir := filepath.Join(s.directory, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := report.Write(dir, saved); err != nil {
		t.Fatal(err)
	}
}

func deleting(calls *[]string, mu *sync.Mutex) testBackend {
	return testBackend{call: func(_ context.Context, name string, _ map[string]any) (map[string]any, error) {
		mu.Lock()
		*calls = append(*calls, name)
		mu.Unlock()
		switch name {
		case "delete_sandbox":
			return map[string]any{"state": "RUNTIME_STATE_DELETED"}, nil
		case "delete_session":
			return map[string]any{"session": map[string]any{"state": "RUNTIME_STATE_DELETED"}}, nil
		}
		return nil, fmt.Errorf("unexpected tool %s", name)
	}}
}

func waitIdle(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		idle := s.cleaning == nil
		s.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cleanup did not finish")
}

func readBack(t *testing.T, s *Server, id string) (map[string]any, string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(s.directory, id, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err = json.Unmarshal(content, &saved); err != nil {
		t.Fatal(err)
	}
	return saved, string(content)
}

func getRunBody(t *testing.T, s *Server, id string) map[string]any {
	t.Helper()
	w := request(s, "GET", "/api/runs/"+id, nil)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCleanupDeletesResourcesAndPersistsReport(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	s := newTestServer(t, deleting(&calls, &mu))
	savedRun(t, s, cleanupRun, nil)
	w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil)
	if w.Code != 202 {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
	waitIdle(t, s)
	saved, _ := readBack(t, s, cleanupRun)
	cleanup := saved["cleanup"].(map[string]any)
	if cleanup["sandbox_deleted"] != true || cleanup["session_deleted"] != true || len(cleanup["errors"].([]any)) != 0 {
		t.Fatalf("cleanup not recorded: %v", cleanup)
	}
	if saved["status"] != "running" {
		t.Fatalf("cleanup must not rewrite status: %v", saved["status"])
	}
	if body := getRunBody(t, s, cleanupRun); body["cleanup_running"] != false {
		t.Fatalf("still reported as running: %v", body["cleanup_running"])
	}
}

func TestCleanupPersistsFailuresWithoutClaimingDeletion(t *testing.T) {
	backend := testBackend{call: func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("controller unavailable server-test-secret")
	}}
	s := newTestServer(t, backend)
	s.config.PollInterval = 1
	savedRun(t, s, cleanupRun, nil)
	if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 202 {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
	// Closing cancels the retry pauses and waits for the report to be written.
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	saved, content := readBack(t, s, cleanupRun)
	cleanup := saved["cleanup"].(map[string]any)
	if cleanup["sandbox_deleted"] == true || cleanup["session_deleted"] == true || len(cleanup["errors"].([]any)) == 0 {
		t.Fatalf("failure not recorded honestly: %v", cleanup)
	}
	if strings.Contains(content, "server-test-secret") {
		t.Fatal("secret persisted")
	}
}

func TestCleanupRefusesBeforeAnyBackendCall(t *testing.T) {
	s := newTestServer(t, nil) // the default backend fails the test on any call
	savedRun(t, s, cleanupRun, func(r map[string]any) {
		r["connection"] = map[string]any{"endpoint": "http://elsewhere.example/mcp", "user_id": s.config.UserID, "namespace": s.config.Namespace}
	})
	if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 409 {
		t.Fatalf("mismatch: %d %s", w.Code, w.Body)
	}
	for _, field := range []string{"cleanup", "connection"} {
		savedRun(t, s, cleanupRun, func(r map[string]any) { delete(r, field) })
		if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 422 {
			t.Fatalf("without %s: %d %s", field, w.Code, w.Body)
		}
	}
	if w := request(s, "POST", "/api/runs/33333333-3333-4333-8333-333333333333/cleanup", nil); w.Code != 404 {
		t.Fatalf("unknown run: %d", w.Code)
	}
	if w := request(s, "POST", "/api/runs/not-an-id/cleanup", nil); w.Code != 404 {
		t.Fatalf("bad id: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "http://localhost:8080/api/runs/"+cleanupRun+"/cleanup", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("missing header: %d", w.Code)
	}
	waitIdle(t, s)
}

func TestCleanupRefusesActiveRunAndShutdown(t *testing.T) {
	s := newTestServer(t, nil)
	savedRun(t, s, cleanupRun, nil)
	s.mu.Lock()
	s.activeID, s.active = cleanupRun, &runningCase{cancel: func() {}, done: make(chan struct{})}
	s.mu.Unlock()
	if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 409 {
		t.Fatalf("active run: %d %s", w.Code, w.Body)
	}
	s.mu.Lock()
	s.activeID, s.active = "", nil
	s.closed = true
	s.mu.Unlock()
	if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 503 {
		t.Fatalf("closing: %d %s", w.Code, w.Body)
	}
}

func TestConcurrentCleanupRunsOnce(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var calls []string
	inner := deleting(&calls, &mu)
	backend := testBackend{call: func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		<-release
		return inner.call(ctx, name, args)
	}}
	s := newTestServer(t, backend)
	savedRun(t, s, cleanupRun, nil)
	savedRun(t, s, cleanupOther, nil)
	if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 202 {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 409 {
		t.Fatalf("same run: %d %s", w.Code, w.Body)
	}
	if w := request(s, "POST", "/api/runs/"+cleanupOther+"/cleanup", nil); w.Code != 409 {
		t.Fatalf("other run: %d %s", w.Code, w.Body)
	}
	if body := getRunBody(t, s, cleanupRun); body["cleanup_running"] != true {
		t.Fatalf("running state missing: %v", body["cleanup_running"])
	}
	close(release)
	waitIdle(t, s)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected one session and one sandbox delete, got %v", calls)
	}
}

func TestCleanupAllowedWhileDifferentRunIsActive(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	s := newTestServer(t, deleting(&calls, &mu))
	savedRun(t, s, cleanupRun, nil)
	s.mu.Lock()
	s.activeID, s.active = cleanupOther, &runningCase{cancel: func() {}, done: make(chan struct{})}
	s.mu.Unlock()
	if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 202 {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
	waitIdle(t, s)
	s.mu.Lock()
	s.activeID, s.active = "", nil
	s.mu.Unlock()
}

func exactJSON(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// The preserved live report, written verbatim as this workspace's run with a
// pending cleanup and members this build does not model.
func savedDemoRun(t *testing.T, s *Server, id string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "verification", "agent-demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	saved := exactJSON(t, data).(map[string]any)
	saved["run_id"] = id
	saved["connection"] = map[string]any{"endpoint": s.config.Endpoint, "user_id": s.config.UserID, "namespace": s.config.Namespace}
	saved["cleanup"] = map[string]any{"sandbox_deleted": false, "session_deleted": false, "errors": []any{"sandbox cleanup: earlier failure"}, "task_canceled": true}
	saved["reviewer_notes"] = map[string]any{"text": "written by a newer build", "count": json.Number("9007199254740993")}
	saved["experiments"].([]any)[0].(map[string]any)["duration_ms"] = json.Number("1234")
	input, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.directory, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), input, 0o600); err != nil {
		t.Fatal(err)
	}
	return input
}

func TestCleanupRewriteKeepsMembersItDoesNotChange(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	s := newTestServer(t, deleting(&calls, &mu))
	input := savedDemoRun(t, s, cleanupRun)
	if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 202 {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
	waitIdle(t, s)
	_, written := readBack(t, s, cleanupRun)
	want := exactJSON(t, input).(map[string]any)
	cleanup := want["cleanup"].(map[string]any)
	cleanup["sandbox_deleted"], cleanup["session_deleted"], cleanup["errors"] = true, true, []any{}
	if got := exactJSON(t, []byte(written)); !reflect.DeepEqual(got, any(want)) {
		t.Fatalf("cleanup changed more than its record:\n%s", written)
	}
}

func TestCleanupRefusesReportSchemaItDoesNotWrite(t *testing.T) {
	s := newTestServer(t, nil) // the default backend fails the test on any call
	for name, edit := range map[string]func(map[string]any){
		"newer":   func(r map[string]any) { r["schema_version"] = 2 },
		"missing": func(r map[string]any) { delete(r, "schema_version") },
	} {
		savedRun(t, s, cleanupRun, edit)
		before, _ := os.ReadFile(filepath.Join(s.directory, cleanupRun, "report.json"))
		w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil)
		if w.Code != 422 || !strings.Contains(w.Body.String(), "schema version") {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if after, _ := os.ReadFile(filepath.Join(s.directory, cleanupRun, "report.json")); !bytes.Equal(before, after) {
			t.Fatalf("%s: refused report was modified", name)
		}
		// The read-only views still serve the report as stored.
		if body := getRunBody(t, s, cleanupRun); body["run_id"] != cleanupRun {
			t.Fatalf("%s: report no longer served: %v", name, body)
		}
	}
	waitIdle(t, s)
}

// A run the investigator never started keeps only its queued record, which
// names no runtime resources and so is not eligible for cleanup.
func TestQueuedReportIsNotEligibleForCleanup(t *testing.T) {
	s := newTestServer(t, nil)
	c := casefile.Case{ID: cleanupRun, Description: "queued", Source: map[string]any{"kind": "upload"}, Environment: map[string]any{}}
	if err := s.launchRun(c, nil, s.config, []string{"Not A Tool!"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		idle := s.active == nil
		s.mu.Unlock()
		if idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("investigation did not stop")
		}
		time.Sleep(5 * time.Millisecond)
	}
	saved, _ := readBack(t, s, cleanupRun)
	if saved["status"] != "queued" || saved["finished_at"] != nil {
		t.Fatalf("queued record changed: %v", saved)
	}
	for _, member := range []string{"cleanup", "connection", "sandbox", "session", "sandbox_request", "tool_calls", "analysis"} {
		if _, present := saved[member]; present {
			t.Fatalf("queued record names %s: %v", member, saved)
		}
	}
	if w := request(s, "POST", "/api/runs/"+cleanupRun+"/cleanup", nil); w.Code != 422 {
		t.Fatalf("queued run accepted for cleanup: %d %s", w.Code, w.Body)
	}
}
