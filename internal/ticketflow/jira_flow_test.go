package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
)

// These source fixtures exercise preparation, clarification, and internal-note
// delivery through the Jira provider branches. Connector HTTP behavior and
// live integration are deliberately outside these tests.
type jiraFlowSource struct {
	origin     string
	issueID    string
	snapshot   snapshot
	contents   map[string][]byte
	reads      []string
	writes     int
	confirmErr error
}

func (s *jiraFlowSource) account() string { return s.origin }
func (s *jiraFlowSource) ticket(context.Context, int64) (ticket, error) {
	return s.snapshot.Ticket, nil
}
func (s *jiraFlowSource) read(context.Context, int64) (snapshot, error) {
	return s.snapshot, nil
}
func (s *jiraFlowSource) confirm(context.Context, ticket) error { return s.confirmErr }
func (s *jiraFlowSource) file(_ context.Context, a attachment) ([]byte, error) {
	s.reads = append(s.reads, a.URL)
	data, found := s.contents[a.URL]
	if !found {
		return nil, errors.New("unknown fixture attachment")
	}
	return data, nil
}
func (s *jiraFlowSource) postComment(context.Context, ticket, string, bool) (int64, error) {
	s.writes++
	return 0, ErrBlocked
}

func jiraAttachmentFixtureURL() string {
	return jiraTestOrigin + "/rest/api/3/attachment/content/" + jiraAttachmentID
}

func newJiraFlowSource() *jiraFlowSource {
	const origin = jiraTestOrigin
	issueURL := origin + "/browse/" + jiraIssueKeyValue
	return &jiraFlowSource{
		origin: origin, issueID: jiraIssueID,
		snapshot: snapshot{
			Ticket: ticket{ID: 10042, Kind: "jira", URL: issueURL, uuid: jiraIssueID,
				Requester: jiraAccountHash(jiraReporterID), Subject: "Timeout is too short", Status: "indeterminate",
				Tags:             []string{"reproduce"},
				Updated:          "2026-11-11T12:00:00.000+0000",
				RequesterProfile: Requester{ID: jiraAccountHash(jiraReporterID), Name: "Reporter"}},
			Comments: []comment{
				{ID: 10001, Author: jiraAccountHash(jiraReporterID), Public: true, URL: issueURL,
					Body: "Python version: 3.12.4\nExpected: Wait five seconds.\nActual: Stops after five milliseconds."},
				{ID: 10010, Author: jiraAccountHash(jiraReporterID), Public: true, URL: issueURL + "?focusedCommentId=10010",
					Body:  "The smallest reproduction.",
					Files: []attachment{{Name: "app.py", Size: 17, ID: 10001, URL: jiraAttachmentFixtureURL()}},
				},
			},
		},
		contents: map[string][]byte{jiraAttachmentFixtureURL(): []byte("print('timeout')\n")},
	}
}

type jiraWriteSource struct {
	*jiraFlowSource
	posted     int
	postErr    error
	beforePost func()
	nextID     int
}

func (s *jiraWriteSource) postComment(_ context.Context, t ticket, body string, public bool) (int64, error) {
	if s.beforePost != nil {
		s.beforePost()
	}
	s.posted++
	s.writes++
	s.nextID++
	id := int64(10050 + s.nextID)
	// The API token authors comments as its own account. That differs from the
	// requester unless the same account authored the issue; the focusedCommentId
	// URL still tells a posted clarification apart from a reply.
	s.snapshot.Comments = append(s.snapshot.Comments, comment{ID: id, Author: jiraAccountHash(jiraAgentID),
		Public: public, Body: body, URL: t.URL + "?focusedCommentId=" + fmt.Sprintf("%d", id)})
	if s.postErr != nil {
		return 0, s.postErr
	}
	return id, nil
}

func jiraWriteConfig() Config {
	return Config{Source: "jira", Origin: jiraTestOrigin, Email: jiraTestEmail, TokenEnv: "TEST_JIRA_TOKEN",
		InternalNotes: true, Policy: Policy{Tag: "reproduce", AutoQuestions: true, MaxQuestions: 2}}
}

func openJiraFlow(t *testing.T, s source, dir string, config Config, secrets ...string) *Flow {
	t.Helper()
	named, ok := s.(interface{ account() string })
	if !ok {
		t.Fatal("source without account")
	}
	store, err := openStore(dir, named.account())
	if err != nil {
		t.Fatal(err)
	}
	flow := newFlow(config, s, store, investigation.DefaultConfig(), append([]string{"test-jira-write-credential"}, secrets...))
	t.Cleanup(func() {
		if !flow.closed {
			if err := flow.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return flow
}

func TestJiraFlowPreservesAuthorEvidenceAndDurableRun(t *testing.T) {
	s := newJiraFlowSource()
	issueURL := s.snapshot.Ticket.URL
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: 10020, Author: jiraAccountHash(jiraAgentID), Public: true,
		URL: issueURL + "?focusedCommentId=10020", Body: "Python version: 9.99\nExpected: Outsider claim.\nActual: Outsider result.",
	})
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openJiraFlow(t, s, dir, Config{Source: "jira", Origin: jiraTestOrigin, Email: jiraTestEmail, Policy: Policy{Tag: "reproduce"}})
	preview, err := flow.Preview(t.Context(), 10042)
	if err != nil || preview.State != "approval" || preview.Source != "jira" || preview.URL != issueURL || len(flow.Cases()) != 0 {
		t.Fatalf("preview identity or state: %+v, %v", preview, err)
	}
	if len(preview.Facts) != 3 || len(preview.Files) != 1 {
		t.Fatalf("wrong selected facts/files: %+v", preview)
	}
	tracked, err := flow.Track(t.Context(), 10042, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	launches := 0
	launch := func(input casefile.Case, _ RunTarget, reserve func() error) error {
		launches++
		if input.Source["kind"] != "jira" || input.Source["jira_site"] != jiraTestOrigin ||
			input.Source["jira_issue"] != jiraIssueID || input.Source["ticket_id"] != jiraIssueID ||
			input.Source["url"] != issueURL || input.Source["approval_plan"] != tracked.Plan {
			t.Fatalf("incorrect investigation source: %+v", input.Source)
		}
		evidence := strings.Join(input.Evidence, "\n")
		if !strings.Contains(evidence, "Jira issue author at "+issueURL+"?focusedCommentId=10010") ||
			strings.Contains(evidence, "Jira issue author at "+issueURL+"?focusedCommentId=10020") ||
			strings.Contains(evidence, "Outsider") {
			t.Fatalf("incorrect evidence provenance: %s", evidence)
		}
		if !strings.Contains(input.Description, "reported in Jira") || strings.Contains(input.Description, "Outsider") {
			t.Fatalf("wrong approved description: %+v", input)
		}
		return reserve()
	}
	launched, err := flow.Approve(t.Context(), 10042, tracked.Plan, launch)
	if err != nil || launches != 1 || launched.RunID == "" || s.writes != 0 {
		t.Fatalf("launch: %+v, writes=%d, %v", launched, s.writes, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openJiraFlow(t, s, dir, Config{Source: "jira", Origin: jiraTestOrigin, Email: jiraTestEmail, Policy: Policy{Tag: "reproduce"}})
	if _, err := flow.Approve(t.Context(), 10042, tracked.Plan, launch); !errors.Is(err, ErrBlocked) || launches != 1 || flow.Cases()[0].RunID != launched.RunID {
		t.Fatalf("restarted flow admitted another run: launches=%d, %v", launches, err)
	}
}

func trackJiraClarification(t *testing.T, flow *Flow) Case {
	t.Helper()
	c, err := flow.Preview(t.Context(), 10042)
	if err != nil || c.State != "ask" || c.Draft == "" {
		t.Fatalf("clarification preview: %+v, %v", c, err)
	}
	c, err = flow.Track(t.Context(), 10042, c.Revision)
	if err != nil || c.State != "ask" {
		t.Fatalf("clarification tracking: %+v, %v", c, err)
	}
	return c
}

func clarificationFlowSource() *jiraWriteSource {
	s := &jiraWriteSource{jiraFlowSource: newJiraFlowSource()}
	s.snapshot.Comments = s.snapshot.Comments[:1]
	s.snapshot.Comments[0].Body = "My Python script times out."
	return s
}

func TestJiraClarificationSendsPublicCommentAndWaitsForRequester(t *testing.T) {
	s := clarificationFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	config := jiraWriteConfig()
	flow := openJiraFlow(t, s, dir, config)
	c := trackJiraClarification(t, flow)
	c, err := flow.Clarify(t.Context(), 10042, c.Revision)
	if err != nil || len(c.Questions) != 1 || c.Questions[0].Status != "sent" || c.Questions[0].CommentID <= 0 {
		t.Fatalf("clarification receipt: %+v, %v", c, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openJiraFlow(t, s, dir, config)
	c, err = flow.Refresh(t.Context(), 10042)
	if err != nil || c.State != "waiting" || len(c.Facts) != 0 || s.posted != 1 {
		t.Fatalf("agent clarification became requester evidence: %+v, posts=%d, %v", c, s.posted, err)
	}
	// A real requester reply permits the follow-up under the reviewed policy.
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: 10011, Author: s.snapshot.Ticket.Requester, Public: true,
		URL: s.snapshot.Ticket.URL + "?focusedCommentId=10011", Body: "Python version: 3.12.4",
	})
	c, err = flow.Refresh(t.Context(), 10042)
	if err != nil || c.State != "waiting" || len(c.Facts) != 1 || s.posted != 2 {
		t.Fatalf("real requester reply did not permit follow-up: %+v, posts=%d, %v", c, s.posted, err)
	}
}

func TestJiraClarificationByTheIssueAuthorIsNotARequesterReply(t *testing.T) {
	// The token's account also authored the issue, so posted comments carry
	// the requester's own identity and only the focusedCommentId URL tells
	// them apart from a reply.
	s := clarificationFlowSource()
	s.snapshot.Ticket.Requester = jiraAccountHash(jiraAgentID)
	s.snapshot.Comments[0].Author = s.snapshot.Ticket.Requester
	dir := filepath.Join(t.TempDir(), "cases")
	config := jiraWriteConfig()
	flow := openJiraFlow(t, s, dir, config)
	c := trackJiraClarification(t, flow)
	if _, err := flow.Clarify(t.Context(), 10042, c.Revision); err != nil {
		t.Fatal(err)
	}
	c, err := flow.Refresh(t.Context(), 10042)
	if err != nil || c.State != "waiting" || len(c.Facts) != 0 || s.posted != 1 {
		t.Fatalf("self-authored clarification counted as requester reply: %+v, posts=%d, %v", c, s.posted, err)
	}
}

func completedJiraReport(t *testing.T, flow *Flow) (string, map[string]any) {
	t.Helper()
	preview, err := flow.Preview(t.Context(), 10042)
	if err != nil || preview.State != "approval" {
		t.Fatalf("ready preview: %+v, %v", preview, err)
	}
	tracked, err := flow.Track(t.Context(), 10042, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	launched, err := flow.Approve(t.Context(), 10042, tracked.Plan, func(input casefile.Case, _ RunTarget, reserve func() error) error {
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
		"experiments": []any{map[string]any{"process_id": "proc-jira", "command": []any{"python3", "app.py"},
			"exit_code": json.Number("1"), "stderr": "private process output"}},
	}
}

func jiraReportSource(saved map[string]any) map[string]any {
	return saved["case"].(map[string]any)["source"].(map[string]any)
}

func TestJiraInvestigationNoteBindsExactReportAndConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, *Flow)
	}{
		{"disabled notes", func(_ map[string]any, f *Flow) { f.settings.InternalNotes = false }},
		{"wrong source kind", func(saved map[string]any, _ *Flow) { jiraReportSource(saved)["kind"] = "linear" }},
		{"wrong site", func(saved map[string]any, _ *Flow) {
			jiraReportSource(saved)["jira_site"] = "https://other.atlassian.net"
		}},
		{"wrong issue", func(saved map[string]any, _ *Flow) {
			jiraReportSource(saved)["jira_issue"] = "10099"
		}},
		{"wrong URL", func(saved map[string]any, _ *Flow) {
			jiraReportSource(saved)["url"] = jiraTestOrigin + "/browse/OPS-99"
		}},
		{"wrong run approval", func(saved map[string]any, _ *Flow) { jiraReportSource(saved)["approval_plan"] = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &jiraWriteSource{jiraFlowSource: newJiraFlowSource()}
			flow := openJiraFlow(t, s, filepath.Join(t.TempDir(), "cases"), jiraWriteConfig())
			id, saved := completedJiraReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil || preview.Source != "jira" || preview.Visibility != "internal" || !preview.CanPost {
				t.Fatalf("fixture did not produce an internal note preview: %+v", preview)
			}
			if strings.HasPrefix(preview.Body, "```") {
				t.Fatalf("plain-text Jira note was wrapped in a Markdown fence: %q", preview.Body)
			}
			tc.change(saved, flow)
			if flow.NotePreview(id, saved) != nil {
				t.Fatal("mismatched report or disabled configuration offered a note")
			}
			if _, err := flow.PostNote(t.Context(), id, saved, preview.Plan, ""); !errors.Is(err, ErrBlocked) || s.posted != 0 {
				t.Fatalf("mismatched report admitted a note: %v", err)
			}
		})
	}
}

func TestJiraInvestigationNotePreviewAndDurableReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		err          error
	}{
		{"confirmed", "posted", nil},
		{"uncertain", "unconfirmed", errors.New("connection closed after sending")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &jiraWriteSource{jiraFlowSource: newJiraFlowSource()}
			s.postErr = tc.err
			dir := filepath.Join(t.TempDir(), "cases")
			config := jiraWriteConfig()
			flow := openJiraFlow(t, s, dir, config)
			id, saved := completedJiraReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil || !preview.CanPost || preview.TicketURL != s.snapshot.Ticket.URL || preview.Plan == "" || s.posted != 0 {
				t.Fatalf("Jira investigation note preview: %+v", preview)
			}
			if !strings.Contains(preview.Body, "Five seconds passed.") || !strings.Contains(preview.Body, "proc-jira") ||
				strings.Contains(preview.Body, "private process output") {
				t.Fatalf("unexpected investigation note content: %q", preview.Body)
			}
			s.beforePost = func() {
				if note := flow.Cases()[0].Note; note == nil || note.Status != "unconfirmed" {
					t.Fatal("investigation note sent before durable reservation")
				}
			}
			note, err := flow.PostNote(t.Context(), id, saved, preview.Plan, "")
			if err != nil || note.Status != tc.status || s.posted != 1 {
				t.Fatalf("investigation note differed from approval: %+v, posts=%d, %v", note, s.posted, err)
			}
			if err := flow.Close(); err != nil {
				t.Fatal(err)
			}
			flow = openJiraFlow(t, s, dir, config)
			s.beforePost = nil
			s.postErr = nil
			restored := flow.NotePreview(id, saved)
			if restored == nil || restored.Status != tc.status || restored.CanPost || restored.CommentID != note.CommentID {
				t.Fatalf("note receipt did not survive restart: %+v", restored)
			}
			_, err = flow.PostNote(t.Context(), id, saved, preview.Plan, "")
			if s.posted != 1 || (tc.err == nil && err != nil) || (tc.err != nil && !errors.Is(err, ErrBlocked)) {
				t.Fatalf("repeated approval retried a note: posts=%d, %v", s.posted, err)
			}
		})
	}
}

func TestJiraDoneIssueBlocksInvestigationNote(t *testing.T) {
	s := &jiraWriteSource{jiraFlowSource: newJiraFlowSource()}
	flow := openJiraFlow(t, s, filepath.Join(t.TempDir(), "cases"), jiraWriteConfig())
	id, saved := completedJiraReport(t, flow)
	if flow.NotePreview(id, saved) == nil {
		t.Fatal("fixture did not produce a note preview")
	}
	s.snapshot.Ticket.Status = "done"
	preview := flow.NotePreview(id, saved)
	if preview == nil || preview.Status != "ready" {
		t.Fatalf("done issue still offers a fresh preview: %+v", preview)
	}
	if _, err := flow.PostNote(t.Context(), id, saved, preview.Plan, ""); !errors.Is(err, ErrBlocked) || s.posted != 0 {
		t.Fatalf("done issue admitted a note: %v", err)
	}
}

func TestJiraFlowStoreCannotBeReusedForAnotherSite(t *testing.T) {
	s := newJiraFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openJiraFlow(t, s, dir, Config{Source: "jira", Origin: jiraTestOrigin, Email: jiraTestEmail, Policy: Policy{Tag: "reproduce"}})
	preview, err := flow.Preview(t.Context(), 10042)
	if err != nil || preview.State != "approval" {
		t.Fatalf("ready preview: %+v, %v", preview, err)
	}
	if _, err := flow.Track(t.Context(), 10042, preview.Revision); err != nil {
		t.Fatal(err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := openStore(dir, "https://other.atlassian.net"); err == nil {
		store.lock.Close()
		t.Fatal("another site reused the same issue id's stored approval")
	}
}

func TestJiraReplyNumberedLikeTheIssueStillPermitsFollowUp(t *testing.T) {
	// The live 2026-11-11 pilot observed Jira assigning a requester comment
	// the same number as its issue (separate id sequences). The reply must
	// not be mistaken for the issue description's pseudo-comment, whose key
	// is derived rather than the issue id.
	s := clarificationFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	config := jiraWriteConfig()
	flow := openJiraFlow(t, s, dir, config)
	c := trackJiraClarification(t, flow)
	if _, err := flow.Clarify(t.Context(), 10042, c.Revision); err != nil {
		t.Fatal(err)
	}
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: 10042, Author: s.snapshot.Ticket.Requester, Public: true,
		URL: s.snapshot.Ticket.URL + "?focusedCommentId=10042", Body: "Python version: 3.12.4",
	})
	c, err := flow.Refresh(t.Context(), 10042)
	if err != nil || len(c.Facts) != 1 || len(c.Questions) != 2 || c.Questions[1].Status != "sent" || s.posted != 2 {
		t.Fatalf("issue-numbered reply was mistaken for the description: %+v posts=%d err=%v", c.Questions, s.posted, err)
	}
}
