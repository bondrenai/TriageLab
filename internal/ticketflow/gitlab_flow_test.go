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

// These source fixtures exercise preparation, clarification, and
// investigation-comment delivery through the GitLab provider branches.
// Connector HTTP behavior and live integration are deliberately outside
// these tests.
type gitlabFlowSource struct {
	origin     string
	project    string
	snapshot   snapshot
	contents   map[string][]byte
	reads      []string
	writes     int
	confirmErr error
}

func (s *gitlabFlowSource) account() string { return s.origin + "/" + s.project }
func (s *gitlabFlowSource) ticket(context.Context, int64) (ticket, error) {
	return s.snapshot.Ticket, nil
}
func (s *gitlabFlowSource) read(context.Context, int64) (snapshot, error) {
	return s.snapshot, nil
}
func (s *gitlabFlowSource) confirm(context.Context, ticket) error { return s.confirmErr }
func (s *gitlabFlowSource) file(_ context.Context, a attachment) ([]byte, error) {
	s.reads = append(s.reads, a.URL)
	data, found := s.contents[a.URL]
	if !found {
		return nil, errors.New("unknown fixture attachment")
	}
	return data, nil
}
func (s *gitlabFlowSource) postComment(context.Context, ticket, string, bool) (int64, error) {
	s.writes++
	return 0, ErrBlocked
}

func newGitLabFlowSource() *gitlabFlowSource {
	issueURL := gitlabIssueWebURL()
	return &gitlabFlowSource{
		origin: gitlabTestOrigin, project: gitlabTestProject,
		snapshot: snapshot{
			Ticket: ticket{ID: gitlabTestIID, Kind: "gitlab", URL: issueURL,
				Requester: gitlabTestReporter, Subject: "Timeout is too short", Status: "opened",
				Tags:             []string{"reproduce"},
				RequesterProfile: Requester{ID: gitlabTestReporter, Name: "Reporter"}},
			Comments: []comment{
				{ID: gitlabDescriptionCommentID(gitlabTestProject, gitlabTestIID), Author: gitlabTestReporter, Public: true, URL: issueURL,
					Body: "Python version: 3.12.4\nExpected: Wait five seconds.\nActual: Stops after five milliseconds."},
				{ID: 10010, Author: gitlabTestReporter, Public: true, URL: issueURL + "#note_10010",
					Body:  "The smallest reproduction.",
					Files: []attachment{{Name: "app.py", Size: -1, URL: gitlabUploadWebURL()}},
				},
			},
		},
		contents: map[string][]byte{gitlabUploadWebURL(): []byte("print('timeout')\n")},
	}
}

type gitlabWriteSource struct {
	*gitlabFlowSource
	posted     int
	postErr    error
	beforePost func()
	nextID     int
}

func (s *gitlabWriteSource) postComment(_ context.Context, t ticket, body string, public bool) (int64, error) {
	if s.beforePost != nil {
		s.beforePost()
	}
	s.posted++
	s.writes++
	s.nextID++
	id := int64(10050 + s.nextID)
	// The access token authors notes as its own account. That differs from
	// the requester unless the same account authored the issue; the note
	// anchor URL still tells a posted clarification apart from a reply.
	s.snapshot.Comments = append(s.snapshot.Comments, comment{ID: id, Author: gitlabTestAgent,
		Public: public, Body: body, URL: t.URL + "#note_" + strconv.FormatInt(id, 10)})
	if s.postErr != nil {
		return 0, s.postErr
	}
	return id, nil
}

func gitlabWriteConfig() Config {
	return Config{Source: "gitlab", Repository: gitlabTestProject, Origin: gitlabTestOrigin,
		TokenEnv: "TEST_GITLAB_TOKEN", InvestigationComments: true,
		Policy: Policy{Tag: "reproduce", AutoQuestions: true, MaxQuestions: 2}}
}

func openGitLabFlow(t *testing.T, s source, dir string, config Config, secrets ...string) *Flow {
	t.Helper()
	named, ok := s.(interface{ account() string })
	if !ok {
		t.Fatal("source without account")
	}
	store, err := openStore(dir, named.account())
	if err != nil {
		t.Fatal(err)
	}
	flow := newFlow(config, s, store, investigation.DefaultConfig(), append([]string{"test-gitlab-write-credential"}, secrets...))
	t.Cleanup(func() {
		if !flow.closed {
			if err := flow.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return flow
}

func TestGitLabFlowPreservesAuthorEvidenceAndDurableRun(t *testing.T) {
	s := newGitLabFlowSource()
	issueURL := s.snapshot.Ticket.URL
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: 10020, Author: gitlabTestAgent, Public: true,
		URL: issueURL + "#note_10020", Body: "Python version: 9.99\nExpected: Outsider claim.\nActual: Outsider result.",
	})
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openGitLabFlow(t, s, dir, Config{Source: "gitlab", Repository: gitlabTestProject, Origin: gitlabTestOrigin, Policy: Policy{Tag: "reproduce"}})
	preview, err := flow.Preview(t.Context(), gitlabTestIID)
	if err != nil || preview.State != "approval" || preview.Source != "gitlab" || preview.URL != issueURL || len(flow.Cases()) != 0 {
		t.Fatalf("preview identity or state: %+v, %v", preview, err)
	}
	if len(preview.Facts) != 3 || len(preview.Files) != 1 {
		t.Fatalf("wrong selected facts/files: %+v", preview)
	}
	tracked, err := flow.Track(t.Context(), gitlabTestIID, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	launches := 0
	launch := func(input casefile.Case, _ RunTarget, reserve func() error) error {
		launches++
		if input.Source["kind"] != "gitlab" || input.Source["gitlab_project"] != gitlabTestProject ||
			input.Source["issue_iid"] != "42" || input.Source["ticket_id"] != "42" ||
			input.Source["url"] != issueURL || input.Source["approval_plan"] != tracked.Plan {
			t.Fatalf("incorrect investigation source: %+v", input.Source)
		}
		evidence := strings.Join(input.Evidence, "\n")
		if !strings.Contains(evidence, "GitLab issue author at "+issueURL+"#note_10010") ||
			strings.Contains(evidence, "GitLab issue author at "+issueURL+"#note_10020") ||
			strings.Contains(evidence, "Outsider") {
			t.Fatalf("incorrect evidence provenance: %s", evidence)
		}
		if !strings.Contains(input.Description, "reported in GitLab") || strings.Contains(input.Description, "Outsider") {
			t.Fatalf("wrong approved description: %+v", input)
		}
		if !strings.Contains(evidence, "app.py attached at "+gitlabUploadWebURL()) {
			t.Fatalf("missing attachment provenance: %v", input.Evidence)
		}
		return reserve()
	}
	launched, err := flow.Approve(t.Context(), gitlabTestIID, tracked.Plan, launch)
	if err != nil || launches != 1 || launched.RunID == "" || s.writes != 0 {
		t.Fatalf("launch: %+v, writes=%d, %v", launched, s.writes, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openGitLabFlow(t, s, dir, Config{Source: "gitlab", Repository: gitlabTestProject, Origin: gitlabTestOrigin, Policy: Policy{Tag: "reproduce"}})
	if _, err := flow.Approve(t.Context(), gitlabTestIID, tracked.Plan, launch); !errors.Is(err, ErrBlocked) || launches != 1 || flow.Cases()[0].RunID != launched.RunID {
		t.Fatalf("restarted flow admitted another run: launches=%d, %v", launches, err)
	}
}

func trackGitLabClarification(t *testing.T, flow *Flow) Case {
	t.Helper()
	c, err := flow.Preview(t.Context(), gitlabTestIID)
	if err != nil || c.State != "ask" || c.Draft == "" {
		t.Fatalf("clarification preview: %+v, %v", c, err)
	}
	c, err = flow.Track(t.Context(), gitlabTestIID, c.Revision)
	if err != nil || c.State != "ask" {
		t.Fatalf("clarification tracking: %+v, %v", c, err)
	}
	return c
}

func clarificationGitLabSource() *gitlabWriteSource {
	s := &gitlabWriteSource{gitlabFlowSource: newGitLabFlowSource()}
	s.snapshot.Comments = s.snapshot.Comments[:1]
	s.snapshot.Comments[0].Body = "My Python script times out."
	return s
}

func TestGitLabClarificationSendsPublicNoteAndWaitsForRequester(t *testing.T) {
	s := clarificationGitLabSource()
	dir := filepath.Join(t.TempDir(), "cases")
	config := gitlabWriteConfig()
	flow := openGitLabFlow(t, s, dir, config)
	c := trackGitLabClarification(t, flow)
	c, err := flow.Clarify(t.Context(), gitlabTestIID, c.Revision)
	if err != nil || len(c.Questions) != 1 || c.Questions[0].Status != "sent" || c.Questions[0].CommentID <= 0 {
		t.Fatalf("clarification receipt: %+v, %v", c, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openGitLabFlow(t, s, dir, config)
	c, err = flow.Refresh(t.Context(), gitlabTestIID)
	if err != nil || c.State != "waiting" || len(c.Facts) != 0 || s.posted != 1 {
		t.Fatalf("agent clarification became requester evidence: %+v, posts=%d, %v", c, s.posted, err)
	}
	// A real requester reply permits the follow-up under the reviewed policy.
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: 10011, Author: s.snapshot.Ticket.Requester, Public: true,
		URL: s.snapshot.Ticket.URL + "#note_10011", Body: "Python version: 3.12.4",
	})
	c, err = flow.Refresh(t.Context(), gitlabTestIID)
	if err != nil || c.State != "waiting" || len(c.Facts) != 1 || s.posted != 2 {
		t.Fatalf("real requester reply did not permit follow-up: %+v, posts=%d, %v", c, s.posted, err)
	}
}

func TestGitLabClarificationByTheIssueAuthorIsNotARequesterReply(t *testing.T) {
	// The token's account also authored the issue, so posted notes carry
	// the requester's own identity and only the note anchor URL tells them
	// apart from a reply.
	s := clarificationGitLabSource()
	s.snapshot.Ticket.Requester = gitlabTestAgent
	s.snapshot.Comments[0].Author = s.snapshot.Ticket.Requester
	dir := filepath.Join(t.TempDir(), "cases")
	config := gitlabWriteConfig()
	flow := openGitLabFlow(t, s, dir, config)
	c := trackGitLabClarification(t, flow)
	if _, err := flow.Clarify(t.Context(), gitlabTestIID, c.Revision); err != nil {
		t.Fatal(err)
	}
	c, err := flow.Refresh(t.Context(), gitlabTestIID)
	if err != nil || c.State != "waiting" || len(c.Facts) != 0 || s.posted != 1 {
		t.Fatalf("self-authored clarification counted as requester reply: %+v, posts=%d, %v", c, s.posted, err)
	}
}

func completedGitLabReport(t *testing.T, flow *Flow) (string, map[string]any) {
	t.Helper()
	preview, err := flow.Preview(t.Context(), gitlabTestIID)
	if err != nil || preview.State != "approval" {
		t.Fatalf("ready preview: %+v, %v", preview, err)
	}
	tracked, err := flow.Track(t.Context(), gitlabTestIID, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	launched, err := flow.Approve(t.Context(), gitlabTestIID, tracked.Plan, func(input casefile.Case, _ RunTarget, reserve func() error) error {
		source = input.Source
		return reserve()
	})
	if err != nil {
		t.Fatal(err)
	}
	return launched.RunID, map[string]any{
		"run_id": launched.RunID, "status": "completed", "outcome": "reproduced", "finished_at": "2026-11-11T12:30:00Z",
		"case":     map[string]any{"source": source},
		"analysis": map[string]any{"summary": "The deadline was reproduced. Five seconds passed."},
		"experiments": []any{map[string]any{"process_id": "proc-gitlab", "command": []any{"python3", "app.py"},
			"exit_code": json.Number("1"), "stderr": "private process output"}},
	}
}

func gitlabReportSource(saved map[string]any) map[string]any {
	return saved["case"].(map[string]any)["source"].(map[string]any)
}

func TestGitLabInvestigationCommentBindsExactReportAndConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, *Flow)
	}{
		{"disabled comments", func(_ map[string]any, f *Flow) { f.settings.InvestigationComments = false }},
		{"wrong source kind", func(saved map[string]any, _ *Flow) { gitlabReportSource(saved)["kind"] = "github" }},
		{"wrong project", func(saved map[string]any, _ *Flow) {
			gitlabReportSource(saved)["gitlab_project"] = "acme/other"
		}},
		{"wrong issue", func(saved map[string]any, _ *Flow) {
			gitlabReportSource(saved)["issue_iid"] = "99"
		}},
		{"wrong URL", func(saved map[string]any, _ *Flow) {
			gitlabReportSource(saved)["url"] = gitlabTestOrigin + "/" + gitlabTestProject + "/-/issues/99"
		}},
		{"wrong run approval", func(saved map[string]any, _ *Flow) { gitlabReportSource(saved)["approval_plan"] = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &gitlabWriteSource{gitlabFlowSource: newGitLabFlowSource()}
			flow := openGitLabFlow(t, s, filepath.Join(t.TempDir(), "cases"), gitlabWriteConfig())
			id, saved := completedGitLabReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil || preview.Source != "gitlab" || preview.Visibility != "project" || !preview.CanPost {
				t.Fatalf("fixture did not produce an investigation comment preview: %+v", preview)
			}
			if !strings.HasPrefix(preview.Body, "```text\n") {
				t.Fatalf("GitLab Markdown note was not fenced: %q", preview.Body)
			}
			tc.change(saved, flow)
			if flow.NotePreview(id, saved) != nil {
				t.Fatal("mismatched report or disabled configuration offered a comment")
			}
			if _, err := flow.PostNote(t.Context(), id, saved, preview.Plan); !errors.Is(err, ErrBlocked) || s.posted != 0 {
				t.Fatalf("mismatched report admitted a comment: %v", err)
			}
		})
	}
}

func TestGitLabInvestigationCommentPreviewAndDurableReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		err          error
	}{
		{"confirmed", "posted", nil},
		{"uncertain", "unconfirmed", errors.New("connection closed after sending")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &gitlabWriteSource{gitlabFlowSource: newGitLabFlowSource()}
			s.postErr = tc.err
			dir := filepath.Join(t.TempDir(), "cases")
			config := gitlabWriteConfig()
			flow := openGitLabFlow(t, s, dir, config)
			id, saved := completedGitLabReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil || !preview.CanPost || preview.TicketURL != s.snapshot.Ticket.URL || preview.Plan == "" || s.posted != 0 {
				t.Fatalf("GitLab investigation comment preview: %+v", preview)
			}
			if !strings.Contains(preview.Body, "Five seconds passed.") || !strings.Contains(preview.Body, "proc-gitlab") ||
				strings.Contains(preview.Body, "private process output") {
				t.Fatalf("unexpected investigation comment content: %q", preview.Body)
			}
			s.beforePost = func() {
				if note := flow.Cases()[0].Note; note == nil || note.Status != "unconfirmed" {
					t.Fatal("investigation comment sent before durable reservation")
				}
			}
			note, err := flow.PostNote(t.Context(), id, saved, preview.Plan)
			if err != nil || note.Status != tc.status || s.posted != 1 {
				t.Fatalf("investigation comment differed from approval: %+v, posts=%d, %v", note, s.posted, err)
			}
			if err := flow.Close(); err != nil {
				t.Fatal(err)
			}
			flow = openGitLabFlow(t, s, dir, config)
			s.beforePost = nil
			s.postErr = nil
			restored := flow.NotePreview(id, saved)
			if restored == nil || restored.Status != tc.status || restored.CanPost || restored.CommentID != note.CommentID {
				t.Fatalf("comment receipt did not survive restart: %+v", restored)
			}
			_, err = flow.PostNote(t.Context(), id, saved, preview.Plan)
			if s.posted != 1 || (tc.err == nil && err != nil) || (tc.err != nil && !errors.Is(err, ErrBlocked)) {
				t.Fatalf("repeated approval retried a comment: posts=%d, %v", s.posted, err)
			}
		})
	}
}

func TestGitLabClosedIssueBlocksInvestigationComment(t *testing.T) {
	s := &gitlabWriteSource{gitlabFlowSource: newGitLabFlowSource()}
	flow := openGitLabFlow(t, s, filepath.Join(t.TempDir(), "cases"), gitlabWriteConfig())
	id, saved := completedGitLabReport(t, flow)
	if flow.NotePreview(id, saved) == nil {
		t.Fatal("fixture did not produce a comment preview")
	}
	s.snapshot.Ticket.Status = "closed"
	preview := flow.NotePreview(id, saved)
	if preview == nil || preview.Status != "ready" {
		t.Fatalf("closed issue still offers a fresh preview: %+v", preview)
	}
	if _, err := flow.PostNote(t.Context(), id, saved, preview.Plan); !errors.Is(err, ErrBlocked) || s.posted != 0 {
		t.Fatalf("closed issue admitted a comment: %v", err)
	}
}

func TestGitLabFlowStoreCannotBeReusedForAnotherProject(t *testing.T) {
	s := newGitLabFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openGitLabFlow(t, s, dir, Config{Source: "gitlab", Repository: gitlabTestProject, Origin: gitlabTestOrigin, Policy: Policy{Tag: "reproduce"}})
	preview, err := flow.Preview(t.Context(), gitlabTestIID)
	if err != nil || preview.State != "approval" {
		t.Fatalf("ready preview: %+v, %v", preview, err)
	}
	if _, err := flow.Track(t.Context(), gitlabTestIID, preview.Revision); err != nil {
		t.Fatal(err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := openStore(dir, gitlabTestOrigin+"/acme/other"); err == nil {
		store.lock.Close()
		t.Fatal("another project reused the same issue iid's stored approval")
	}
}
