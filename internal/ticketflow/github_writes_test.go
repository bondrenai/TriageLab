package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
)

type githubRecordedComment struct {
	id     int64
	body   string
	public bool
}

// These tests simulate delivery receipts to verify the workflow's durable
// reservations. They do not demonstrate a live GitHub write.
type githubWriteSource struct {
	*githubFlowSource
	actor      int64
	posted     []githubRecordedComment
	postErr    error
	beforePost func()
}

func (s *githubWriteSource) postComment(_ context.Context, _ ticket, body string, public bool) (int64, error) {
	if s.beforePost != nil {
		s.beforePost()
	}
	id := int64(1)
	for _, existing := range s.snapshot.Comments {
		if existing.ID >= id {
			id = existing.ID + 1
		}
	}
	s.posted = append(s.posted, githubRecordedComment{id: id, body: body, public: public})
	if s.postErr != nil {
		return 0, s.postErr
	}
	s.snapshot.Comments = append(s.snapshot.Comments, comment{ID: id, Author: s.actor, Public: true,
		Body: body, URL: s.snapshot.Ticket.URL + "#issuecomment-" + strconv.FormatInt(id, 10)})
	return id, nil
}

func newGitHubWriteSource(ready bool) *githubWriteSource {
	s := &githubWriteSource{githubFlowSource: newGitHubFlowSource(), actor: 99}
	if !ready {
		s.snapshot.Comments = s.snapshot.Comments[:1]
		s.snapshot.Comments[0].Body = "My Python script times out."
	}
	return s
}

func githubWriteConfig() Config {
	return Config{Source: "github", Repository: "example/reproduction", TokenEnv: "TEST_GITHUB_TOKEN",
		InvestigationComments: true, Policy: Policy{Tag: "reproduce", AutoQuestions: true, MaxQuestions: 2}}
}

func openGitHubWriteFlow(t *testing.T, s *githubWriteSource, dir string, config Config) *Flow {
	t.Helper()
	store, err := openStore(dir, s.account())
	if err != nil {
		t.Fatal(err)
	}
	flow := newFlow(config, s, store, investigation.DefaultConfig(), []string{"test-github-write-credential"})
	t.Cleanup(func() {
		if !flow.closed {
			if err := flow.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return flow
}

func trackGitHubClarification(t *testing.T, flow *Flow) Case {
	t.Helper()
	c, err := flow.Preview(t.Context(), 42)
	if err != nil || c.State != "ask" || c.Draft == "" {
		t.Fatalf("clarification preview: %+v, %v", c, err)
	}
	c, err = flow.Track(t.Context(), 42, c.Revision)
	if err != nil || c.State != "ask" {
		t.Fatalf("clarification tracking: %+v, %v", c, err)
	}
	return c
}

func addGitHubAuthorReply(s *githubWriteSource, id int64, body string) {
	s.snapshot.Comments = append(s.snapshot.Comments, comment{ID: id, Author: s.snapshot.Ticket.Requester,
		Public: true, Body: body, URL: s.snapshot.Ticket.URL + "#issuecomment-" + strconv.FormatInt(id, 10)})
}

func TestGitHubClarificationReviewFollowupAndReadyEvidence(t *testing.T) {
	s := newGitHubWriteSource(false)
	dir := filepath.Join(t.TempDir(), "cases")
	config := githubWriteConfig()
	flow := openGitHubWriteFlow(t, s, dir, config)
	c := trackGitHubClarification(t, flow)
	if len(s.posted) != 0 {
		t.Fatal("preview or tracking sent an unapproved clarification")
	}
	if _, err := flow.Clarify(t.Context(), 42, "stale"); !errors.Is(err, ErrChanged) || len(s.posted) != 0 {
		t.Fatalf("stale clarification approval sent a comment: %v", err)
	}
	s.beforePost = func() {
		current := flow.Cases()[0]
		if len(current.Questions) == 0 || current.Questions[len(current.Questions)-1].Status != "unconfirmed" {
			t.Fatal("clarification was sent before its durable reservation")
		}
	}
	waiting, err := flow.Clarify(t.Context(), 42, c.Revision)
	if err != nil || waiting.State != "waiting" || waiting.ClarificationApproval == "" || len(s.posted) != 1 ||
		!s.posted[0].public || s.posted[0].body != c.Draft || len(waiting.Questions) != 1 || waiting.Questions[0].CommentID != s.posted[0].id {
		t.Fatalf("approved clarification delivery: %+v, %v", waiting, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openGitHubWriteFlow(t, s, dir, config)
	s.beforePost = nil
	waiting, err = flow.Refresh(t.Context(), 42)
	if err != nil || waiting.State != "waiting" || len(s.posted) != 1 {
		t.Fatalf("restart repeated the clarification: %+v, %v", waiting, err)
	}
	addGitHubAuthorReply(s, 2000, "Python version: 3.12.4")
	waiting, err = flow.Refresh(t.Context(), 42)
	if err != nil || waiting.State != "waiting" || len(s.posted) != 2 || strings.Contains(s.posted[1].body, "Which Python version") {
		t.Fatalf("authorized follow-up did not narrow missing evidence: %+v, %v", waiting, err)
	}
	addGitHubAuthorReply(s, 3000, "Expected: Wait five seconds.\nActual: Stops after five milliseconds.")
	s.snapshot.Comments[len(s.snapshot.Comments)-1].Files = newGitHubFlowSource().snapshot.Comments[1].Files
	ready, err := flow.Refresh(t.Context(), 42)
	if err != nil || ready.State != "approval" || ready.Plan == "" || ready.RunID != "" || len(s.posted) != 2 {
		t.Fatalf("reply did not prepare evidence without launching: %+v, %v", ready, err)
	}
}

func TestGitHubClarificationDoesNotUseItsOwnCommentAsRequesterReply(t *testing.T) {
	s := newGitHubWriteSource(false)
	s.actor = s.snapshot.Ticket.Requester
	dir := filepath.Join(t.TempDir(), "cases")
	config := githubWriteConfig()
	flow := openGitHubWriteFlow(t, s, dir, config)
	c := trackGitHubClarification(t, flow)
	c, err := flow.Clarify(t.Context(), 42, c.Revision)
	if err != nil || len(c.Questions) != 1 || c.Questions[0].CommentID != s.posted[0].id {
		t.Fatalf("self-authored clarification receipt: %+v, %v", c, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openGitHubWriteFlow(t, s, dir, config)
	c, err = flow.Refresh(t.Context(), 42)
	if err != nil || c.State != "waiting" || len(c.Facts) != 0 || len(s.posted) != 1 {
		t.Fatalf("our own clarification became evidence or progress: %+v, %v", c, err)
	}
	addGitHubAuthorReply(s, 2000, "Python version: 3.12.4")
	c, err = flow.Refresh(t.Context(), 42)
	if err != nil || c.State != "waiting" || len(c.Facts) != 1 || len(s.posted) != 2 {
		t.Fatalf("real author reply did not permit follow-up: %+v, %v", c, err)
	}
}

func TestGitHubClarificationStopsAtPolicyAndRecipientBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		max        int
		change     func(*githubWriteSource, *Flow)
		wantState  string
		wantReview bool
	}{
		{"maximum attempts", 1, func(s *githubWriteSource, _ *Flow) { addGitHubAuthorReply(s, 2000, "Python version: 3.12.4") }, "review", false},
		{"reply without progress", 2, func(s *githubWriteSource, _ *Flow) { addGitHubAuthorReply(s, 2000, "It still fails.") }, "review", false},
		{"changed requester", 2, func(s *githubWriteSource, _ *Flow) {
			s.snapshot.Ticket.Requester = 12
			s.snapshot.Ticket.RequesterProfile = Requester{ID: 12, Name: "new-reporter", URL: "https://github.com/new-reporter"}
			addGitHubAuthorReply(s, 2000, "Python version: 3.12.4")
		}, "ask", true},
		{"changed policy", 2, func(s *githubWriteSource, f *Flow) {
			f.settings.Policy.MaxQuestions = 3
			addGitHubAuthorReply(s, 2000, "Python version: 3.12.4")
		}, "ask", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGitHubWriteSource(false)
			config := githubWriteConfig()
			config.Policy.MaxQuestions = tc.max
			flow := openGitHubWriteFlow(t, s, filepath.Join(t.TempDir(), "cases"), config)
			c := trackGitHubClarification(t, flow)
			if _, err := flow.Clarify(t.Context(), 42, c.Revision); err != nil {
				t.Fatal(err)
			}
			tc.change(s, flow)
			c, err := flow.Refresh(t.Context(), 42)
			if err != nil || c.State != tc.wantState || len(s.posted) != 1 || len(c.Questions) != 1 || (tc.wantReview && c.ClarificationApproval != "") {
				t.Fatalf("clarification policy boundary: %+v, posts=%d, %v", c, len(s.posted), err)
			}
		})
	}
}

func TestGitHubUncertainClarificationIsNotRetriedAfterRestart(t *testing.T) {
	s := newGitHubWriteSource(false)
	s.postErr = errors.New("connection closed after sending")
	dir := filepath.Join(t.TempDir(), "cases")
	config := githubWriteConfig()
	flow := openGitHubWriteFlow(t, s, dir, config)
	c := trackGitHubClarification(t, flow)
	c, err := flow.Clarify(t.Context(), 42, c.Revision)
	if err != nil || c.State != "review" || len(c.Questions) != 1 || c.Questions[0].Status != "unconfirmed" || len(s.posted) != 1 {
		t.Fatalf("uncertain clarification was not reserved: %+v, %v", c, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openGitHubWriteFlow(t, s, dir, config)
	s.postErr = nil
	addGitHubAuthorReply(s, 2000, "Python version: 3.12.4")
	c, err = flow.Refresh(t.Context(), 42)
	if err != nil || c.State != "review" || len(s.posted) != 1 {
		t.Fatalf("uncertain clarification retried after a reply: %+v, %v", c, err)
	}
	if _, err := flow.Clarify(t.Context(), 42, c.Revision); !errors.Is(err, ErrBlocked) || len(s.posted) != 1 {
		t.Fatalf("manual action duplicated an uncertain delivery: %v", err)
	}
}

func TestGitHubChangedClarificationDoesNotAuthorizeFollowups(t *testing.T) {
	s := newGitHubWriteSource(false)
	s.postErr = ErrChanged
	flow := openGitHubWriteFlow(t, s, filepath.Join(t.TempDir(), "cases"), githubWriteConfig())
	c := trackGitHubClarification(t, flow)
	c, err := flow.Clarify(t.Context(), 42, c.Revision)
	if err != nil || c.State != "review" || c.ClarificationApproval != "" || len(c.Questions) != 1 || c.Questions[0].Status != "conflict" {
		t.Fatalf("changed issue was treated as a sent clarification: %+v, %v", c, err)
	}
	s.postErr = nil
	addGitHubAuthorReply(s, 2000, "Python version: 3.12.4")
	c, err = flow.Refresh(t.Context(), 42)
	if err != nil || c.State != "ask" || c.ClarificationApproval != "" || len(s.posted) != 1 {
		t.Fatalf("failed initial send authorized follow-up: %+v, %v", c, err)
	}
}

func completedGitHubReport(t *testing.T, flow *Flow) (string, map[string]any) {
	t.Helper()
	tracked := trackGitHubFlowFixture(t, flow)
	var source map[string]any
	launched, err := flow.Approve(t.Context(), 42, tracked.Plan, func(input casefile.Case, _ RunTarget, reserve func() error) error {
		source = input.Source
		return reserve()
	})
	if err != nil {
		t.Fatal(err)
	}
	return launched.RunID, map[string]any{
		"run_id": launched.RunID, "status": "completed", "outcome": "reproduced", "finished_at": "2026-11-09T12:30:00Z",
		"case":     map[string]any{"source": source},
		"analysis": map[string]any{"summary": "The deadline was reproduced. Five seconds passed."},
		"experiments": []any{map[string]any{"process_id": "proc-github", "command": []any{"python3", "app.py"},
			"exit_code": json.Number("1"), "stderr": "private process output"}},
	}
}

func TestGitHubInvestigationCommentPreviewAndDurableReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		err          error
	}{
		{"confirmed", "posted", nil},
		{"uncertain", "unconfirmed", errors.New("connection closed after sending")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGitHubWriteSource(true)
			s.postErr = tc.err
			dir := filepath.Join(t.TempDir(), "cases")
			config := githubWriteConfig()
			flow := openGitHubWriteFlow(t, s, dir, config)
			id, saved := completedGitHubReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil || !preview.CanPost || preview.Source != "github" || preview.Visibility != "repository" ||
				preview.TicketURL != s.snapshot.Ticket.URL || preview.Plan == "" || len(s.posted) != 0 {
				t.Fatalf("GitHub investigation comment preview: %+v", preview)
			}
			if !strings.Contains(preview.Body, "Five seconds passed.") || !strings.Contains(preview.Body, "proc-github") || strings.Contains(preview.Body, "private process output") {
				t.Fatalf("unexpected investigation comment content: %q", preview.Body)
			}
			if _, err := flow.PostNote(t.Context(), id, saved, "stale", ""); !errors.Is(err, ErrChanged) || len(s.posted) != 0 {
				t.Fatalf("stale note approval sent a comment: %v", err)
			}
			s.beforePost = func() {
				if note := flow.Cases()[0].Note; note == nil || note.Status != "unconfirmed" {
					t.Fatal("investigation comment sent before durable reservation")
				}
			}
			note, err := flow.PostNote(t.Context(), id, saved, preview.Plan, "")
			if err != nil || note.Status != tc.status || len(s.posted) != 1 || !s.posted[0].public || s.posted[0].body != preview.Body {
				t.Fatalf("investigation comment differed from approval: %+v, %v", note, err)
			}
			if tc.err == nil && note.CommentID != s.posted[0].id {
				t.Fatalf("missing confirmed comment receipt: %+v", note)
			}
			if err := flow.Close(); err != nil {
				t.Fatal(err)
			}
			flow = openGitHubWriteFlow(t, s, dir, config)
			s.beforePost = nil
			s.postErr = nil
			restored := flow.NotePreview(id, saved)
			if restored == nil || restored.Status != tc.status || restored.CanPost || restored.CommentID != note.CommentID {
				t.Fatalf("comment receipt did not survive restart: %+v", restored)
			}
			_, err = flow.PostNote(t.Context(), id, saved, preview.Plan, "")
			if len(s.posted) != 1 || (tc.err == nil && err != nil) || (tc.err != nil && !errors.Is(err, ErrBlocked)) {
				t.Fatalf("repeated approval retried a comment: posts=%d, %v", len(s.posted), err)
			}
		})
	}
}

func TestGitHubInvestigationCommentBindsExactReportAndConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, *Flow)
	}{
		{"disabled comments", func(_ map[string]any, f *Flow) { f.settings.InvestigationComments = false }},
		{"incomplete report", func(saved map[string]any, _ *Flow) { saved["finished_at"] = nil }},
		{"different run", func(saved map[string]any, _ *Flow) { saved["run_id"] = "different" }},
		{"wrong source", func(saved map[string]any, _ *Flow) { githubReportSource(saved)["kind"] = "zendesk" }},
		{"wrong repository", func(saved map[string]any, _ *Flow) { githubReportSource(saved)["repository"] = "example/other" }},
		{"wrong issue number", func(saved map[string]any, _ *Flow) { githubReportSource(saved)["issue_number"] = "43" }},
		{"wrong URL", func(saved map[string]any, _ *Flow) {
			githubReportSource(saved)["url"] = "https://github.com/example/other/issues/42"
		}},
		{"wrong run approval", func(saved map[string]any, _ *Flow) { githubReportSource(saved)["approval_plan"] = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGitHubWriteSource(true)
			flow := openGitHubWriteFlow(t, s, filepath.Join(t.TempDir(), "cases"), githubWriteConfig())
			id, saved := completedGitHubReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil {
				t.Fatal("fixture did not produce a comment preview")
			}
			tc.change(saved, flow)
			if flow.NotePreview(id, saved) != nil {
				t.Fatal("mismatched report or disabled configuration offered a comment")
			}
			if _, err := flow.PostNote(t.Context(), id, saved, preview.Plan, ""); !errors.Is(err, ErrBlocked) || len(s.posted) != 0 {
				t.Fatalf("mismatched report admitted a comment: %v", err)
			}
		})
	}
}

func githubReportSource(saved map[string]any) map[string]any {
	return saved["case"].(map[string]any)["source"].(map[string]any)
}

func TestGitHubInvestigationCommentKeepsImportedMarkdownLiteral(t *testing.T) {
	s := newGitHubWriteSource(true)
	flow := openGitHubWriteFlow(t, s, filepath.Join(t.TempDir(), "cases"), githubWriteConfig())
	id, saved := completedGitHubReport(t, flow)
	saved["analysis"].(map[string]any)["summary"] = "@someone ![image](https://example.invalid/image) ```` test-github-write-credential"
	preview := flow.NotePreview(id, saved)
	if preview == nil || !strings.HasPrefix(preview.Body, "`````text\n") || !strings.HasSuffix(preview.Body, "\n`````") ||
		strings.Contains(preview.Body, "test-github-write-credential") || !strings.Contains(preview.Body, "@someone") {
		t.Fatalf("imported Markdown was not redacted and fenced literally: %+v", preview)
	}
}

func TestGitHubInvestigationCommentRejectsChangedPreviewAndSafeConflict(t *testing.T) {
	s := newGitHubWriteSource(true)
	flow := openGitHubWriteFlow(t, s, filepath.Join(t.TempDir(), "cases"), githubWriteConfig())
	id, saved := completedGitHubReport(t, flow)
	preview := flow.NotePreview(id, saved)
	if preview == nil {
		t.Fatal("missing comment preview")
	}
	saved["analysis"].(map[string]any)["summary"] = "The verified summary changed."
	if _, err := flow.PostNote(t.Context(), id, saved, preview.Plan, ""); !errors.Is(err, ErrChanged) || len(s.posted) != 0 {
		t.Fatalf("approval did not bind exact comment body: %v", err)
	}
	preview = flow.NotePreview(id, saved)
	s.postErr = ErrChanged
	if _, err := flow.PostNote(t.Context(), id, saved, preview.Plan, ""); !errors.Is(err, ErrChanged) || len(s.posted) != 1 || flow.Cases()[0].Note != nil {
		t.Fatalf("safe conflict retained an uncertain delivery reservation: %v", err)
	}
	s.postErr = nil
	note, err := flow.PostNote(t.Context(), id, saved, preview.Plan, "")
	if err != nil || note.Status != "posted" || len(s.posted) != 2 || s.posted[1].body != preview.Body {
		t.Fatalf("safe conflict could not be reviewed and retried: %+v, %v", note, err)
	}
}
