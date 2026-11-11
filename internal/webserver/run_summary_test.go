package webserver

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/report"
)

func TestRunSummaryAttention(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		active    bool
		failed    bool
		lifecycle lifecycleView
		cleanup   map[string]any
		questions []any
		resources string
		reason    string
		action    string
	}{
		{name: "active run does not need a second operator", status: "running", active: true, resources: "active"},
		{name: "completed and cleaned", status: "completed", cleanup: map[string]any{"sandbox_deleted": true, "session_deleted": true}, resources: "deleted"},
		{name: "paused for information", status: "paused", lifecycle: lifecycleView{Phase: "paused", CanResume: true, CanClose: true}, questions: []any{"Private customer question"}, resources: "retained", reason: "Waiting for additional information", action: "Add information and resume"},
		{name: "operator paused", status: "paused", lifecycle: lifecycleView{Phase: "paused", CanResume: true, CanClose: true}, questions: []any{" "}, resources: "retained", reason: "Investigation is paused", action: "Resume or close"},
		{name: "paused at round limit", status: "paused", lifecycle: lifecycleView{Phase: "paused", CanClose: true}, questions: []any{"Private customer question"}, resources: "retained", reason: "Waiting for additional information", action: "Review and close"},
		{name: "paused without lifecycle does not promise retention", status: "paused", resources: "unconfirmed", reason: "Investigation is paused", action: "Review paused investigation"},
		{name: "interrupted with retained resources unknown", status: "running", resources: "unconfirmed", reason: "Interrupted; resource cleanup is not confirmed", action: "Review cleanup"},
		{name: "interrupted but cleaned", status: "interrupted", cleanup: map[string]any{"sandbox_deleted": true, "session_deleted": true}, resources: "deleted", reason: "Investigation was interrupted", action: "Review saved evidence"},
		{name: "cleanup failed after completed experiment", status: "completed", cleanup: map[string]any{"sandbox_deleted": false, "session_deleted": true, "errors": []any{"sensitive upstream response"}}, resources: "unconfirmed", reason: "Cleanup reported an error", action: "Review cleanup"},
		{name: "partial cleanup has no success label", status: "completed", cleanup: map[string]any{"sandbox_deleted": true}, resources: "unconfirmed", reason: "Resource cleanup is not confirmed", action: "Review cleanup"},
		{name: "cleanup still has errors despite deletion", status: "completed", cleanup: map[string]any{"sandbox_deleted": true, "session_deleted": true, "errors": []any{"a task could not be observed"}}, resources: "deleted", reason: "Cleanup reported an error", action: "Review cleanup"},
		{name: "uncertain invocation never offers replay", status: "invoking", lifecycle: lifecycleView{Phase: "invoking", CanClose: true}, resources: "unconfirmed", reason: "The continuation outcome is uncertain", action: "Review saved evidence"},
		{name: "reconcile continuation", status: "resuming", lifecycle: lifecycleView{Phase: "resuming", CanResume: true}, resources: "unconfirmed", reason: "A workspace operation needs to finish", action: "Review continuation"},
		{name: "connection mismatch does not offer resume", status: "paused", lifecycle: lifecycleView{Phase: "paused", Message: "Restore the original connection."}, resources: "retained", reason: "Restore the original connection.", action: "Review run"},
		{name: "completed run does not need old connection", status: "completed", lifecycle: lifecycleView{Phase: "closed", Message: "Restore the original connection."}, cleanup: map[string]any{"sandbox_deleted": true, "session_deleted": true}, resources: "deleted"},
		{name: "action failure survives unchanged paused report", status: "paused", failed: true, lifecycle: lifecycleView{Phase: "paused", CanResume: true}, resources: "retained", reason: "The last workspace action failed", action: "Review run"},
		{name: "timed out after cleanup", status: "timed_out", cleanup: map[string]any{"sandbox_deleted": true, "session_deleted": true}, resources: "deleted", reason: "Investigation ended before completing", action: "Review report"},
		{name: "closed report does not request abandoned questions", status: "closed", cleanup: map[string]any{"sandbox_deleted": true, "session_deleted": true}, questions: []any{"Old question"}, resources: "deleted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			saved := map[string]any{"status": test.status, "lifecycle": test.lifecycle, "cleanup": test.cleanup,
				"analysis": map[string]any{"missing_information": test.questions}}
			got := summarizeRun(retainedRunID, saved, test.active, test.failed)
			if got.ResourceState != test.resources || got.Active != test.active {
				t.Fatalf("wrong operational state: %+v", got)
			}
			if test.reason == "" {
				if got.Attention != nil {
					t.Fatalf("unexpected attention: %+v", got.Attention)
				}
			} else if got.Attention == nil || got.Attention.Reason != test.reason || got.Attention.Action != test.action {
				t.Fatalf("attention: got %+v, want %q / %q", got.Attention, test.reason, test.action)
			}
		})
	}
}

func TestRunListUsesJournalAndCountsArchivedEvidence(t *testing.T) {
	s := newTestServer(t, nil)
	state := retainedState(s)
	state.Round = 3
	saveRetainedRun(t, s, state)
	saved, err := s.readReport(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	saved["status"] = "running" // The lifecycle journal must override this stale snapshot.
	saved["analysis"] = map[string]any{"missing_information": []string{"Unshared question content"}, "summary": "Unshared model conclusion"}
	saved["experiments"] = []any{map[string]any{"process_id": "current"}}
	saved["rounds"] = []any{
		map[string]any{"round": 1, "experiments": []any{map[string]any{"process_id": "first"}}},
		map[string]any{"round": 2, "experiments": []any{map[string]any{"process_id": "second"}, map[string]any{"process_id": "third"}}},
	}
	if err := report.Write(filepath.Join(s.directory, state.RunID), saved); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/runs", nil)
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	var result struct {
		Runs []runSummary `json:"runs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("wrong run count: %s", w.Body)
	}
	got := result.Runs[0]
	if got.Round != 3 || got.ExperimentCount != 4 || got.Status != "paused" || got.ResourceState != "retained" || got.Attention == nil || got.Attention.Action != "Add information and resume" {
		t.Fatalf("wrong queue projection: %+v", got)
	}
	for _, private := range []string{"Unshared question content", "Unshared model conclusion", state.SandboxID, state.SessionID, "resume_settings", "current_provenance", "process_id"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("queue exposed private detail %q", private)
		}
	}
	s.mu.Lock()
	s.actionErrors = map[string]string{state.RunID: "Private workspace failure detail"}
	s.mu.Unlock()
	w = request(s, "GET", "/api/runs", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(result.Runs) != 1 || result.Runs[0].Attention == nil || result.Runs[0].Attention.Reason != "The last workspace action failed" || strings.Contains(w.Body.String(), "Private workspace failure detail") {
		t.Fatalf("failed action was not safely projected: %d %s", w.Code, w.Body)
	}
}
