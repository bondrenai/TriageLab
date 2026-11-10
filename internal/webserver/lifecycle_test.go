package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/report"
)

const retainedRunID = "11111111-1111-4111-8111-111111111111"

func retainedState(s *Server) investigation.State {
	return investigation.State{Version: 1, Config: s.config, Trigger: "ui", RunID: retainedRunID, Round: 1,
		Phase: "paused", SandboxID: "private-sandbox", SessionID: "private-session"}
}

func saveRetainedRun(t *testing.T, s *Server, state investigation.State) {
	t.Helper()
	dir := filepath.Join(s.directory, state.RunID)
	saved := map[string]any{"schema_version": 1, "run_id": state.RunID, "round": state.Round,
		"case": map[string]any{"description": "Waiting for customer logs"}, "status": "running",
		"outcome": "inconclusive", "started_at": "2026-01-01T00:00:00Z", "experiments": []any{},
		"errors": []string{}, "warnings": []string{}, "agent": map[string]string{"namespace": state.Config.Namespace, "name": state.Config.Agent},
		"sandbox_template": state.Config.Template}
	if err := report.Write(dir, saved); err != nil {
		t.Fatal(err)
	}
	if err := report.Write(filepath.Join(dir, "rounds", fmt.Sprintf("%04d", state.Round)), saved); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lifecycle.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func continuationRequest(t *testing.T, s *Server, state investigation.State, action string) *http.Request {
	t.Helper()
	r := formRequest(t, s, "The customer supplied a new trace", upload{"trace.txt", "additional evidence"})
	r.URL.Path = "/api/runs/" + state.RunID + "/" + action
	r.Header.Set("X-TriageLab-Resume-Round", strconv.Itoa(state.Round))
	r.Header.Set("X-TriageLab-Resume-Settings", resumeSettings(state))
	return r
}

func TestRetainedHistorySurvivesRestartWithoutExposingControlRecord(t *testing.T) {
	s := newTestServer(t, nil)
	state := retainedState(s)
	state.Config.TokenEnv = "PRIVATE_CREDENTIAL_VARIABLE"
	saveRetainedRun(t, s, state)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(context.Background(), s.config, s.directory, filepath.Join(filepath.Dir(s.directory), "assets"), s.backend)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	cookie := addAuth(t, restarted)
	w := requestAs(restarted, cookie, "GET", "/api/runs/"+state.RunID, nil)
	if w.Code != 200 {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	var saved struct {
		Status    string
		Active    bool
		Lifecycle lifecycleView
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "paused" || saved.Active || !saved.Lifecycle.CanResume || !saved.Lifecycle.CanClose || saved.Lifecycle.CanPause {
		t.Fatalf("wrong persisted action projection: %+v", saved)
	}
	for _, private := range []string{state.SandboxID, state.SessionID, state.Config.TokenEnv, "user_id", "additional_tools", "endpoint"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("private state exposed: %s", private)
		}
	}
	w = requestAs(restarted, cookie, "GET", "/api/runs", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"paused"`) {
		t.Fatalf("history: %d %s", w.Code, w.Body)
	}
	for _, name := range []string{"lifecycle.json", "pause-request.json", "rounds/0001/report.json"} {
		w = requestAs(restarted, cookie, "GET", "/api/runs/"+state.RunID+"/"+name, nil)
		if w.Code != 404 {
			t.Fatalf("private route %s: %d", name, w.Code)
		}
	}
	w = requestAs(restarted, cookie, "GET", "/api/runs/"+state.RunID+"/report.json", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "resume_settings") {
		t.Fatalf("projection entered export: %d %s", w.Code, w.Body)
	}
}

func TestContinuationRejectsStaleReviewIdentityAndTicketBypass(t *testing.T) {
	for _, name := range []string{"stale-round", "stale-phase", "stale-settings", "connection", "identity", "running", "ticket", "viewer", "unsafe-input"} {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t, nil)
			state := retainedState(s)
			switch name {
			case "stale-phase":
				state.Round = 2
			case "connection":
				state.Config.Endpoint = "http://127.0.0.1:9999/mcp"
			case "identity":
				state.Config.UserID = "another-owner"
			case "running":
				state.Phase = "running"
			case "ticket":
				state.Trigger = "ticket"
			}
			saveRetainedRun(t, s, state)
			r := continuationRequest(t, s, state, "resume")
			want := http.StatusConflict
			switch name {
			case "stale-round":
				r.Header.Set("X-TriageLab-Resume-Round", "2")
			case "stale-phase":
				reviewed := state
				reviewed.Phase = "resuming"
				r.Header.Set("X-TriageLab-Resume-Settings", resumeSettings(reviewed))
			case "stale-settings":
				r.Header.Set("X-TriageLab-Resume-Settings", "unreviewed")
			case "viewer":
				r.Header.Del("Cookie")
				r.AddCookie(loginAs(t, s, "observer", "viewer-password-1"))
				want = http.StatusForbidden
			case "unsafe-input":
				r = formRequest(t, s, "New evidence", upload{"../escape.txt", "no"})
				r.URL.Path = "/api/runs/" + state.RunID + "/resume"
				r.Header.Set("X-TriageLab-Resume-Round", "1")
				r.Header.Set("X-TriageLab-Resume-Settings", resumeSettings(state))
				want = http.StatusBadRequest
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("got %d, want %d: %s", w.Code, want, w.Body)
			}
			current, err := investigation.LoadState(filepath.Join(s.directory, state.RunID))
			if err != nil || current.Round != state.Round || current.Phase != state.Phase {
				t.Fatalf("rejected request mutated lifecycle: %+v %v", current, err)
			}
		})
	}
}

func TestPauseRequestsSettlementWithoutCancelingRun(t *testing.T) {
	s := newTestServer(t, nil)
	state := retainedState(s)
	state.Phase, state.TaskID = "running", "task-1"
	saveRetainedRun(t, s, state)
	lock, err := os.OpenFile(filepath.Join(s.directory, state.RunID, ".lifecycle.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	canceled := false
	s.activeID, s.active = state.RunID, &runningCase{cancel: func() { canceled = true }, done: make(chan struct{})}
	defer func() { s.activeID, s.active = "", nil }()
	w := request(s, "POST", "/api/runs/"+state.RunID+"/pause", nil)
	if w.Code != 202 || canceled {
		t.Fatalf("pause canceled run: %d %s", w.Code, w.Body)
	}
	marker, err := os.ReadFile(filepath.Join(s.directory, state.RunID, "pause-request.json"))
	if err != nil || !strings.Contains(string(marker), state.TaskID) {
		t.Fatalf("pause was not durably requested: %s %v", marker, err)
	}
	w = request(s, "POST", "/api/runs/"+state.RunID+"/cancel", nil)
	if w.Code != 202 || !canceled {
		t.Fatalf("stop lost cancellation behavior: %d", w.Code)
	}
}

func TestResumeUsesRetainedSandboxAndReservesActiveSlot(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	backend := testBackend{call: func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name == "get_session" {
			if args["session_id"] != "private-session" {
				t.Errorf("wrong retained session: %v", args)
			}
			return map[string]any{"session": map[string]any{"id": "private-session", "state": "RUNTIME_STATE_SUSPENDED"}, "turns": []any{map[string]any{"state": "TASK_STATE_COMPLETED"}}}, nil
		}
		if name != "resume_sandbox" || args["sandbox_id"] != "private-sandbox" {
			t.Errorf("unexpected continuation call %s %v", name, args)
			return nil, fmt.Errorf("unexpected call")
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, fmt.Errorf("fixture restore unavailable")
	}}
	s := newTestServer(t, backend)
	state := retainedState(s)
	saveRetainedRun(t, s, state)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, continuationRequest(t, s, state, "resume"))
	if w.Code != 202 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("restore did not start")
	}
	s.mu.Lock()
	done := s.active.done
	s.mu.Unlock()
	w = httptest.NewRecorder()
	s.ServeHTTP(w, formRequest(t, s, "Concurrent new work"))
	if w.Code != 409 {
		t.Fatalf("new run bypassed continuation slot: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, continuationRequest(t, s, state, "resume"))
	if w.Code != 409 {
		t.Fatalf("duplicate resume accepted: %d %s", w.Code, w.Body)
	}
	once.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("failed restoration did not settle")
	}
	w = request(s, "GET", "/api/runs/"+state.RunID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "fixture restore unavailable") || !strings.Contains(w.Body.String(), "The customer supplied a new trace") {
		t.Fatalf("continuation evidence/error not saved: %d %s", w.Code, w.Body)
	}
}

func TestCloseUsesPrivateResourceIDsAndNeverInvokesAgent(t *testing.T) {
	var calls []string
	backend := testBackend{call: func(_ context.Context, name string, args map[string]any) (map[string]any, error) {
		calls = append(calls, name)
		switch name {
		case "get_session":
			if args["session_id"] != "private-session" {
				t.Errorf("wrong session: %v", args)
			}
			return map[string]any{"turns": []any{}}, nil
		case "delete_session":
			if args["session_id"] != "private-session" {
				t.Errorf("wrong session deletion: %v", args)
			}
			return map[string]any{"session": map[string]any{"state": "RUNTIME_STATE_DELETED"}}, nil
		case "delete_sandbox":
			if args["sandbox_id"] != "private-sandbox" {
				t.Errorf("wrong sandbox deletion: %v", args)
			}
			return map[string]any{"state": "RUNTIME_STATE_DELETED"}, nil
		default:
			t.Errorf("close called %s", name)
			return nil, fmt.Errorf("unexpected call")
		}
	}}
	s := newTestServer(t, backend)
	state := retainedState(s)
	saveRetainedRun(t, s, state)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, continuationRequest(t, s, state, "close"))
	if w.Code != 202 {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-time.After(2 * time.Second):
			t.Fatal("cleanup did not finish")
		}
	}
	if strings.Join(calls, ",") != "get_session,delete_session,delete_sandbox" {
		t.Fatalf("wrong cleanup calls: %v", calls)
	}
	w = request(s, "GET", "/api/runs/"+state.RunID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"closed"`) || !strings.Contains(w.Body.String(), `"can_resume":false`) {
		t.Fatalf("close not reflected: %d %s", w.Code, w.Body)
	}
}

func TestResumePreflightFailureRemainsVisibleAfterSlotIsReleased(t *testing.T) {
	s := newTestServer(t, nil)
	state := retainedState(s)
	saveRetainedRun(t, s, state)
	if err := os.Remove(filepath.Join(s.directory, state.RunID, "rounds", "0001", "report.json")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, continuationRequest(t, s, state, "resume"))
	if w.Code != 202 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-time.After(2 * time.Second):
			t.Fatal("preflight did not finish")
		}
	}
	w = request(s, "GET", "/api/runs/"+state.RunID, nil)
	var saved struct {
		Active      bool
		ActionError string `json:"action_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || saved.Active || saved.ActionError == "" {
		t.Fatalf("preflight failure disappeared: %d %s", w.Code, w.Body)
	}
}
