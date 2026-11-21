package webserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/investigation"
)

func TestBoundedRunQueueExecutesFIFOAndSupportsCancellation(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	var s *Server
	backend := testBackend{call: func(ctx context.Context, name string, _ map[string]any) (map[string]any, error) {
		if name != "create_sandbox" {
			return map[string]any{}, nil
		}
		s.mu.Lock()
		id := s.activeID
		s.mu.Unlock()
		started <- id
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, context.Canceled
	}}

	cfg := investigation.DefaultConfig()
	cfg.MaxQueuedRuns = 2
	s = newTestServerWithConfig(t, cfg, backend)
	waitForRun := func(want string) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("started run %s, want %s", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("run %s did not start", want)
		}
	}

	startRun := func(desc string) (int, string) {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, formRequest(t, s, desc))
		var resp map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp["id"]
	}

	code1, id1 := startRun("First investigation")
	if code1 != 202 || id1 == "" {
		t.Fatalf("run 1: code=%d id=%q", code1, id1)
	}
	waitForRun(id1)

	// Runs 2 and 3 fill the bounded queue of 2; run 4 is rejected with 409.
	code2, id2 := startRun("Second investigation")
	code3, id3 := startRun("Third investigation")
	code4, _ := startRun("Fourth investigation")
	if code2 != 202 || code3 != 202 || code4 != 409 {
		t.Fatalf("queue admission codes: %d, %d, %d (want 202, 202, 409)", code2, code3, code4)
	}

	// Verify /api/runs reports queued positions without false interrupted warnings.
	w := request(s, "GET", "/api/runs", nil)
	if w.Code != 200 {
		t.Fatalf("list runs: %d %s", w.Code, w.Body)
	}
	var list struct {
		Runs []runSummary `json:"runs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]runSummary, len(list.Runs))
	for _, r := range list.Runs {
		byID[r.ID] = r
	}
	if !byID[id2].Queued || byID[id2].QueuePosition != 1 || byID[id2].ResourceState != "queued" || byID[id2].Attention != nil {
		t.Fatalf("unexpected run 2 summary: %+v", byID[id2])
	}
	if !byID[id3].Queued || byID[id3].QueuePosition != 2 || byID[id3].ResourceState != "queued" || byID[id3].Attention != nil {
		t.Fatalf("unexpected run 3 summary: %+v", byID[id3])
	}

	// A stale queued cancellation must not stop an already active run.
	w = request(s, "POST", "/api/runs/"+id1+"/cancel?queued=true", nil)
	if w.Code != 409 {
		t.Fatalf("queued-only cancel of active run: %d %s", w.Code, w.Body)
	}

	// Cancel run 2 while queued; run 3 should move to position 1 and run next.
	w = request(s, "POST", "/api/runs/"+id2+"/cancel?queued=true", nil)
	if w.Code != 202 {
		t.Fatalf("cancel queued run 2: %d %s", w.Code, w.Body)
	}
	w = request(s, "GET", "/api/runs/"+id2, nil)
	var cancelled map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled["status"] != "canceled" || cancelled["queued"] == true || cancelled["cleanup"] != nil {
		t.Fatalf("cancelled queued run state: %v", cancelled)
	}

	// A new run joins the tail, behind the surviving queued run.
	code4, id4 := startRun("Fourth investigation")
	if code4 != 202 {
		t.Fatalf("refilling the queue: status %d", code4)
	}
	close(release)
	waitForRun(id3)
	waitForRun(id4)
}
