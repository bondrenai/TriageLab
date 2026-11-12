package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/resultchat"
)

type fakeChat func(context.Context, map[string]any, []resultchat.Message, string) (resultchat.Answer, error)

func (f fakeChat) Generate(ctx context.Context, saved map[string]any, history []resultchat.Message, message string) (resultchat.Answer, error) {
	return f(ctx, saved, history, message)
}

func savedChatReport(t *testing.T, s *Server, status string) string {
	t.Helper()
	dir := filepath.Join(s.directory, workerUUID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := report.Write(dir, map[string]any{"run_id": workerUUID, "status": status, "round": 1, "case": map[string]any{"description": "case"}, "analysis": map[string]any{"summary": "A hypothesis"}, "experiments": []any{map[string]any{"process_id": "p1", "state": "exited", "exit_code": 1, "outputs_complete": true, "stdout": "", "stderr": "missing setting"}}}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func getChat(t *testing.T, s *Server) chatView {
	t.Helper()
	w := request(s, "GET", "/api/runs/"+workerUUID+"/chat", nil)
	if w.Code != 200 {
		t.Fatalf("get chat: %d %s", w.Code, w.Body)
	}
	var view chatView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func waitChatJob(t *testing.T, s *Server) {
	t.Helper()
	s.mu.Lock()
	job := s.chatActive
	s.mu.Unlock()
	if job != nil {
		select {
		case <-job.done:
		case <-time.After(3 * time.Second):
			t.Fatal("chat did not finish")
		}
	}
}

func TestChatPersistsBeforeGenerationAndKeepsCanonicalReport(t *testing.T) {
	s := newTestServer(t, nil)
	dir := savedChatReport(t, s, "completed")
	before, _ := os.ReadFile(filepath.Join(dir, "report.json"))
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.chat = fakeChat(func(ctx context.Context, saved map[string]any, history []resultchat.Message, message string) (resultchat.Answer, error) {
		calls.Add(1)
		if message != "Explain [REDACTED]" || len(history) != 0 || saved["run_id"] != workerUUID {
			t.Errorf("wrong chat inputs: %q, %d", message, len(history))
		}
		data, err := os.ReadFile(filepath.Join(dir, "chat.json"))
		if err != nil || !strings.Contains(string(data), `"status": "answering"`) {
			t.Error("question was not durable before model call")
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return resultchat.Answer{}, ctx.Err()
		}
		return resultchat.Answer{Content: "The setting is missing. server-test-secret", Citations: []resultchat.Citation{{Round: 1, ProcessID: "p1"}}}, nil
	})
	w := request(s, "POST", "/api/runs/"+workerUUID+"/chat", strings.NewReader(`{"message":"Explain server-test-secret"}`))
	if w.Code != 202 {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}
	<-started
	view := getChat(t, s)
	if view.Status != "answering" || view.CanAsk || len(view.Messages) != 1 {
		t.Fatalf("pending: %+v", view)
	}
	if w := request(s, "POST", "/api/runs/"+workerUUID+"/chat", strings.NewReader(`{"message":"Duplicate"}`)); w.Code != 409 {
		t.Fatalf("duplicate: %d", w.Code)
	}
	close(release)
	waitChatJob(t, s)
	view = getChat(t, s)
	if !view.CanAsk || view.Status != "idle" || len(view.Messages) != 2 || len(view.Messages[1].Citations) != 1 || strings.Contains(view.Messages[1].Content, "server-test-secret") || calls.Load() != 1 {
		t.Fatalf("finished: %+v calls %d", view, calls.Load())
	}
	after, _ := os.ReadFile(filepath.Join(dir, "report.json"))
	if string(after) != string(before) {
		t.Fatal("canonical report changed")
	}
	info, err := os.Stat(filepath.Join(dir, "chat.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("chat not private", err)
	}
}

func TestChatAuthorizationAvailabilityAndInputBounds(t *testing.T) {
	s := newTestServer(t, nil)
	savedChatReport(t, s, "completed")
	if view := getChat(t, s); view.CanAsk || view.Messages == nil || !strings.Contains(view.UnavailableReason, "configured") {
		t.Fatalf("unconfigured: %+v", view)
	}
	var calls atomic.Int32
	s.chat = fakeChat(func(context.Context, map[string]any, []resultchat.Message, string) (resultchat.Answer, error) {
		calls.Add(1)
		return resultchat.Answer{}, errors.New("private provider error")
	})
	viewer := loginAs(t, s, "observer", "viewer-password-1")
	if w := requestAs(s, viewer, "POST", "/api/runs/"+workerUUID+"/chat", strings.NewReader(`{"message":"Explain"}`)); w.Code != 403 {
		t.Fatal("viewer could ask", w.Code)
	}
	if w := requestAs(s, nil, "GET", "/api/runs/"+workerUUID+"/chat", nil); w.Code != 401 {
		t.Fatal("anonymous could read", w.Code)
	}
	for _, input := range []string{`{"message":""}`, `{"message":"ok","extra":true}`, `{"message":"ok"}{}`, `{"message":"` + strings.Repeat("x", resultchat.MaxMessageBytes+1) + `"}`} {
		if w := request(s, "POST", "/api/runs/"+workerUUID+"/chat", strings.NewReader(input)); w.Code != 400 {
			t.Fatalf("invalid input accepted: %d", w.Code)
		}
	}
	for _, status := range []string{"running", "queued", "paused", "pausing", "resuming", "closing"} {
		savedChatReport(t, s, status)
		if view := getChat(t, s); view.CanAsk {
			t.Errorf("can ask while %s", status)
		}
		if w := request(s, "POST", "/api/runs/"+workerUUID+"/chat", strings.NewReader(`{"message":"Explain"}`)); w.Code != 409 {
			t.Errorf("post while %s: %d", status, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected request contacted model")
	}
}

func TestChatFailureIsSavedWithoutRetryAndRestartIsExplicit(t *testing.T) {
	s := newTestServer(t, nil)
	savedChatReport(t, s, "completed")
	var calls atomic.Int32
	s.chat = fakeChat(func(context.Context, map[string]any, []resultchat.Message, string) (resultchat.Answer, error) {
		calls.Add(1)
		return resultchat.Answer{}, errors.New("secret provider error")
	})
	if w := request(s, "POST", "/api/runs/"+workerUUID+"/chat", strings.NewReader(`{"message":"Explain"}`)); w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	waitChatJob(t, s)
	view := getChat(t, s)
	if view.Status != "error" || len(view.Messages) != 1 || !view.CanAsk || strings.Contains(view.Error, "secret") || calls.Load() != 1 {
		t.Fatalf("failure: %+v", view)
	}
	root, err := s.reportRoot(workerUUID)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	record := view.chatRecord
	record.Status = "answering"
	record.Error = ""
	if err := writeChat(root, record); err != nil {
		t.Fatal(err)
	}
	view = getChat(t, s)
	if view.Status != "interrupted" || !view.CanAsk || calls.Load() != 1 {
		t.Fatalf("restart: %+v", view)
	}
	lock, err := lockChat(root)
	if err != nil {
		t.Fatal(err)
	}
	view = getChat(t, s)
	if view.Status != "answering" || view.CanAsk {
		t.Fatalf("other process active: %+v", view)
	}
	releaseChat(lock)
}

func TestChatRejectsLinkedJournalAndStopsOnShutdown(t *testing.T) {
	s := newTestServer(t, nil)
	dir := savedChatReport(t, s, "completed")
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "chat.json")); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "GET", "/api/runs/"+workerUUID+"/chat", nil); w.Code != 500 {
		t.Fatal("linked journal read", w.Code)
	}
	if err := os.Remove(filepath.Join(dir, "chat.json")); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.chat = fakeChat(func(ctx context.Context, _ map[string]any, _ []resultchat.Message, _ string) (resultchat.Answer, error) {
		close(started)
		<-ctx.Done()
		return resultchat.Answer{}, ctx.Err()
	})
	if w := request(s, "POST", "/api/runs/"+workerUUID+"/chat", strings.NewReader(`{"message":"Explain"}`)); w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "chat.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record chatRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != "interrupted" || len(record.Messages) != 1 {
		t.Fatalf("shutdown: %+v", record)
	}
}

func TestChatRejectsCorruptSavedMessages(t *testing.T) {
	s := newTestServer(t, nil)
	savedChatReport(t, s, "completed")
	root, err := s.reportRoot(workerUUID)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	valid := chatMessage{ID: "question-1", Role: "user", Content: "Why?", CreatedAt: "2026-11-11T12:00:00Z"}
	for _, corrupt := range []func(*chatRecord){
		func(r *chatRecord) { r.Messages[0].Content = "" },
		func(r *chatRecord) { r.Messages[0].ID = "" },
		func(r *chatRecord) { r.Messages[0].CreatedAt = "yesterday" },
		func(r *chatRecord) { r.Messages = append(r.Messages, r.Messages[0]) },
		func(r *chatRecord) { r.Messages[0].Citations = []resultchat.Citation{{Round: 1, ProcessID: "p1"}} },
		func(r *chatRecord) {
			r.Messages[0].Role = "assistant"
			r.Messages[0].Citations = []resultchat.Citation{{Round: 0, ProcessID: "p1"}}
		},
	} {
		record := emptyChat(workerUUID)
		record.Messages = []chatMessage{valid}
		corrupt(&record)
		if err := writeChat(root, record); err != nil {
			t.Fatal(err)
		}
		if _, err := readChat(root, workerUUID); err == nil {
			t.Fatal("corrupt chat accepted")
		}
	}
}

func TestChatSubmissionRedactsHistoricalMessages(t *testing.T) {
	s := newTestServer(t, nil)
	savedChatReport(t, s, "completed")
	root, err := s.reportRoot(workerUUID)
	if err != nil {
		t.Fatal(err)
	}
	record := emptyChat(workerUUID)
	record.Messages = []chatMessage{{ID: "old-question", Role: "user", Content: "Earlier server-test-secret", CreatedAt: "2026-11-11T12:00:00Z"}}
	if err := writeChat(root, record); err != nil {
		t.Fatal(err)
	}
	root.Close()
	s.chat = fakeChat(func(ctx context.Context, _ map[string]any, history []resultchat.Message, _ string) (resultchat.Answer, error) {
		if len(history) != 1 || strings.Contains(history[0].Content, "server-test-secret") {
			t.Error("historical credential sent to model")
		}
		<-ctx.Done()
		return resultchat.Answer{}, ctx.Err()
	})
	w := request(s, "POST", "/api/runs/"+workerUUID+"/chat", strings.NewReader(`{"message":"What remains unknown?"}`))
	if w.Code != 202 || strings.Contains(w.Body.String(), "server-test-secret") || !strings.Contains(w.Body.String(), "[REDACTED]") {
		t.Fatalf("historical credential in submission response: %d %s", w.Code, w.Body)
	}
}
