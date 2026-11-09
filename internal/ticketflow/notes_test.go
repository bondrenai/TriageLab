package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

func completedTicketReport(t *testing.T, flow *Flow, f *fixture) (string, map[string]any) {
	t.Helper()
	ctx := t.Context()
	f.reply()
	preview, err := flow.Preview(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	tracked, err := flow.Track(ctx, 42, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	launched, err := flow.Approve(ctx, 42, tracked.Plan, func(c casefile.Case, _ RunTarget, reserve func() error) error {
		source = c.Source
		return reserve()
	})
	if err != nil {
		t.Fatal(err)
	}
	return launched.RunID, map[string]any{
		"run_id": launched.RunID, "status": "completed", "outcome": "reproduced", "finished_at": "2026-11-08T12:10:00Z",
		"case":     map[string]any{"source": source},
		"analysis": map[string]any{"summary": "The 5 ms timeout was reproduced. Raising it to 5 seconds passed. {{ticket.description}}"},
		"experiments": []any{
			map[string]any{"process_id": "proc-a", "command": []any{"python3", "app.py"}, "exit_code": json.Number("1"), "stderr": "private output"},
			map[string]any{"process_id": "proc-b", "command": []any{"python3", "app.py", "--timeout-ms=5000"}, "exit_code": json.Number("0")},
		},
	}
}

func noteTestFlow(t *testing.T, dir string, f *fixture) *Flow {
	t.Helper()
	flow := openTestFlow(t, dir, f, false)
	flow.settings.InternalNotes = true
	flow.oauth = flow.source.(*zendesk).oauth
	return flow
}

func TestInternalNotePreviewAndDurableDelivery(t *testing.T) {
	for _, tc := range []struct {
		name           string
		providerStatus int
		status         string
		commentID      int64
	}{
		{"posted", 0, "posted", 123},
		{"unconfirmed", http.StatusServiceUnavailable, "unconfirmed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.mu.Lock()
			f.noteStatus = tc.providerStatus
			f.mu.Unlock()
			dir := filepath.Join(t.TempDir(), "cases")
			flow := noteTestFlow(t, dir, f)
			id, saved := completedTicketReport(t, flow, f)
			preview := flow.NotePreview(id, saved)
			if preview == nil || !preview.CanPost || preview.TicketID != 42 || preview.Plan == "" || f.count() != 0 {
				t.Fatalf("expected a read-only note preview: %+v", preview)
			}
			if !strings.Contains(preview.Body, "Raising it to 5 seconds passed.") || !strings.Contains(preview.Body, "proc-b") ||
				strings.Contains(preview.Body, "private output") || strings.Contains(preview.Body, "{{") {
				t.Fatalf("unexpected note content: %q", preview.Body)
			}
			if _, err := flow.PostNote(t.Context(), id, saved, "stale"); !errors.Is(err, ErrChanged) || f.count() != 0 {
				t.Fatalf("stale preview posted a note: %v", err)
			}
			note, err := flow.PostNote(t.Context(), id, saved, preview.Plan)
			if err != nil || note.Status != tc.status || note.CommentID != tc.commentID || f.count() != 1 || f.noteBody != preview.Body {
				t.Fatalf("note delivery differed from preview: %+v %v", note, err)
			}
			if err := flow.Close(); err != nil {
				t.Fatal(err)
			}
			flow = noteTestFlow(t, dir, f)
			restored := flow.NotePreview(id, saved)
			if restored == nil || restored.Status != tc.status || restored.CommentID != tc.commentID || restored.CanPost {
				t.Fatalf("delivery outcome did not survive restart: %+v", restored)
			}
			_, _ = flow.PostNote(context.Background(), id, saved, preview.Plan)
			if f.count() != 1 {
				t.Fatal("repeated approval posted another note")
			}
		})
	}
}

func TestInternalNoteHeldWhenSummaryMayDisclose(t *testing.T) {
	f := newFixture(t)
	flow := noteTestFlow(t, filepath.Join(t.TempDir(), "cases"), f)
	id, saved := completedTicketReport(t, flow, f)
	if err := flow.store.save(Case{TicketID: 43, Account: f.server.URL, State: "review",
		Requester: &Requester{Name: "Rival Customer", Email: "rival@example.test"}}); err != nil {
		t.Fatal(err)
	}
	saved["analysis"].(map[string]any)["summary"] = "The 5 ms timeout was reproduced. Rival Customer reported the same."
	preview := flow.NotePreview(id, saved)
	if preview == nil || preview.Status != "blocked" || preview.Plan != "" || preview.CanPost ||
		len(preview.Findings) != 1 || !strings.Contains(preview.Findings[0], "another tracked requester's identity") {
		t.Fatalf("disclosing note was not held: %+v", preview)
	}
	if _, err := flow.PostNote(t.Context(), id, saved, ""); !errors.Is(err, ErrBlocked) || f.count() != 0 {
		t.Fatalf("held note was posted: %v %d", err, f.count())
	}
}

func TestInternalNoteRequiresCompletedLinkedReport(t *testing.T) {
	f := newFixture(t)
	flow := noteTestFlow(t, filepath.Join(t.TempDir(), "cases"), f)
	id, saved := completedTicketReport(t, flow, f)
	saved["finished_at"] = nil
	if flow.NotePreview(id, saved) != nil {
		t.Fatal("incomplete report was eligible for a note")
	}
	saved["finished_at"] = "2026-11-08T12:10:00Z"
	saved["case"].(map[string]any)["source"].(map[string]any)["url"] = "https://other.zendesk.com/agent/tickets/42"
	if flow.NotePreview(id, saved) != nil {
		t.Fatal("note preview used a report from another account")
	}
}
