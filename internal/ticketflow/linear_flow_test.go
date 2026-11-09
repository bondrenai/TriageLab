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

// These source fixtures exercise preparation, clarification, and summary
// delivery through the Linear provider branches. Connector HTTP behavior and
// live integration are deliberately outside these tests.
type linearFlowSource struct {
	origin     string
	snapshot   snapshot
	contents   map[string][]byte
	reads      []string
	writes     int
	confirmErr error
}

func (s *linearFlowSource) account() string { return s.origin }
func (s *linearFlowSource) ticket(context.Context, int64) (ticket, error) {
	return s.snapshot.Ticket, nil
}
func (s *linearFlowSource) read(context.Context, int64) (snapshot, error) {
	return s.snapshot, nil
}
func (s *linearFlowSource) confirm(context.Context, ticket) error { return s.confirmErr }
func (s *linearFlowSource) file(_ context.Context, a attachment) ([]byte, error) {
	s.reads = append(s.reads, a.URL)
	data, found := s.contents[a.URL]
	if !found {
		return nil, errors.New("unknown fixture attachment")
	}
	return data, nil
}
func (s *linearFlowSource) postComment(context.Context, ticket, string, bool) (int64, error) {
	s.writes++
	return 0, ErrBlocked
}

func newLinearFlowSource() *linearFlowSource {
	const origin = "https://linear.app/acme/ENG"
	const issueURL = origin + "/issue/ENG-42"
	return &linearFlowSource{
		origin: origin,
		snapshot: snapshot{
			Ticket: ticket{ID: 42, Kind: "linear", URL: issueURL, Requester: linearID(linearAuthorUUID),
				Subject: "Timeout is too short", Status: "started", Tags: []string{"reproduce"},
				Updated:          "2026-11-09T12:00:00Z",
				RequesterProfile: Requester{ID: linearID(linearAuthorUUID), Name: "Reporter"}},
			Comments: []comment{
				{ID: linearID(linearIssueUUID), Author: linearID(linearAuthorUUID), Public: true, URL: issueURL,
					Body: "Python version: 3.12.4\nExpected: Wait five seconds.\nActual: Stops after five milliseconds."},
				{ID: linearID(linearCommentB), Author: linearID(linearAuthorUUID), Public: true,
					URL: issueURL + "#comment-" + linearCommentB, Body: "The smallest reproduction.",
					Files: []attachment{{Name: "app.py.txt", Size: -1, URL: linearAttachmentURL}}},
			},
		},
		contents: map[string][]byte{linearAttachmentURL: []byte("print('timeout')\n")},
	}
}

type linearWriteSource struct {
	*linearFlowSource
	actor      int64
	posted     int
	postErr    error
	beforePost func()
	nextUUID   int
}

func (s *linearWriteSource) postComment(_ context.Context, _ ticket, body string, _ bool) (int64, error) {
	if s.beforePost != nil {
		s.beforePost()
	}
	s.posted++
	s.writes++
	uuid := fmt.Sprintf("00000000-0000-4000-8000-%012d", 1000+s.nextUUID)
	s.nextUUID++
	issueURL := s.snapshot.Ticket.URL
	s.snapshot.Comments = append(s.snapshot.Comments, comment{ID: linearID(uuid), Author: s.actor, Public: true,
		Body: body, URL: issueURL + "#comment-" + uuid})
	if s.postErr != nil {
		return 0, s.postErr
	}
	return linearID(uuid), nil
}

func linearWriteConfig() Config {
	return Config{Source: "linear", Workspace: "acme", Team: "ENG", TokenEnv: "TEST_LINEAR_TOKEN",
		InvestigationComments: true, Policy: Policy{Tag: "reproduce", AutoQuestions: true, MaxQuestions: 2}}
}

func openLinearFlow(t *testing.T, s source, dir string, config Config, secrets ...string) *Flow {
	t.Helper()
	linear, ok := s.(interface{ account() string })
	if !ok {
		t.Fatal("source without account")
	}
	store, err := openStore(dir, linear.account())
	if err != nil {
		t.Fatal(err)
	}
	flow := newFlow(config, s, store, investigation.DefaultConfig(), append([]string{"test-linear-write-credential"}, secrets...))
	t.Cleanup(func() {
		if !flow.closed {
			if err := flow.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return flow
}

func trackLinearFlowFixture(t *testing.T, flow *Flow) Case {
	t.Helper()
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || preview.State != "approval" || preview.Plan == "" {
		t.Fatalf("ready preview: %+v, %v", preview, err)
	}
	tracked, err := flow.Track(t.Context(), 42, preview.Revision)
	if err != nil || tracked.State != "approval" {
		t.Fatalf("track ready issue: %+v, %v", tracked, err)
	}
	return tracked
}

func TestLinearFlowPreservesAuthorEvidenceAndDurableRun(t *testing.T) {
	s := newLinearFlowSource()
	issueURL := s.snapshot.Ticket.URL
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: linearID(linearOtherUUID), Author: linearID(linearOtherUUID), Public: true,
		URL: issueURL + "#comment-x", Body: "Python version: 9.99\nExpected: Outsider claim.\nActual: Outsider result.",
	})
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openLinearFlow(t, s, dir, Config{Source: "linear", Workspace: "acme", Team: "ENG", Policy: Policy{Tag: "reproduce"}})
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || preview.State != "approval" || preview.Source != "linear" || preview.URL != issueURL || len(flow.Cases()) != 0 {
		t.Fatalf("preview identity or state: %+v, %v", preview, err)
	}
	if len(preview.Facts) != 3 || len(preview.Files) != 1 {
		t.Fatalf("wrong selected facts/files: %+v", preview)
	}
	tracked, err := flow.Track(t.Context(), 42, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	launches := 0
	launch := func(input casefile.Case, _ RunTarget, reserve func() error) error {
		launches++
		if input.Source["kind"] != "linear" || input.Source["linear_team"] != "acme/ENG" ||
			input.Source["linear_identifier"] != "ENG-42" || input.Source["ticket_id"] != "42" ||
			input.Source["url"] != issueURL || input.Source["approval_plan"] != tracked.Plan {
			t.Fatalf("incorrect investigation source: %+v", input.Source)
		}
		evidence := strings.Join(input.Evidence, "\n")
		if !strings.Contains(evidence, "Linear issue author at "+issueURL+"#comment-"+linearCommentB) ||
			strings.Contains(evidence, "Linear issue author at "+issueURL+"#comment-x") ||
			strings.Contains(evidence, "Outsider") || strings.Contains(evidence, linearAttachmentURL) {
			t.Fatalf("incorrect evidence provenance: %s", evidence)
		}
		if !strings.Contains(input.Description, "reported in Linear") || strings.Contains(input.Description, "Outsider") {
			t.Fatalf("wrong approved description: %+v", input)
		}
		return reserve()
	}
	launched, err := flow.Approve(t.Context(), 42, tracked.Plan, launch)
	if err != nil || launches != 1 || launched.RunID == "" || s.writes != 0 {
		t.Fatalf("launch: %+v, writes=%d, %v", launched, s.writes, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openLinearFlow(t, s, dir, Config{Source: "linear", Workspace: "acme", Team: "ENG", Policy: Policy{Tag: "reproduce"}})
	if _, err := flow.Approve(t.Context(), 42, tracked.Plan, launch); !errors.Is(err, ErrBlocked) || launches != 1 || flow.Cases()[0].RunID != launched.RunID {
		t.Fatalf("restarted flow admitted another run: launches=%d, %v", launches, err)
	}
}

func TestLinearFlowRejectsConfiguredCredentialInEvidence(t *testing.T) {
	const secret = "fixture-linear-api-credential"
	for _, location := range []string{"fact", "file", "filename", "attachment URL"} {
		t.Run(location, func(t *testing.T) {
			s := newLinearFlowSource()
			switch location {
			case "fact":
				s.snapshot.Comments[0].Body += "\nActual: " + secret
			case "file":
				s.contents[linearAttachmentURL] = []byte("print('" + secret + "')\n")
			case "filename":
				s.snapshot.Comments[1].Files[0].Name = secret + ".py"
			case "attachment URL":
				s.snapshot.Comments[1].Files[0].URL += "&token=" + secret
			}
			flow := openLinearFlow(t, s, filepath.Join(t.TempDir(), "cases"),
				Config{Source: "linear", Workspace: "acme", Team: "ENG", Policy: Policy{Tag: "reproduce"}}, secret)
			c, err := flow.Preview(t.Context(), 42)
			if err != nil || c.State != "review" || c.Plan != "" || len(c.Files) != 0 {
				t.Fatalf("credential evidence was admitted: %+v, %v", c, err)
			}
			data, err := json.Marshal(c)
			if err != nil || strings.Contains(string(data), secret) {
				t.Fatal("configured credential appeared in the case response")
			}
		})
	}
}

func trackLinearClarification(t *testing.T, flow *Flow) Case {
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

func addLinearAuthorReply(s *linearWriteSource, body string, files []attachment) {
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: linearID(fmt.Sprintf("00000000-0000-4000-8000-%012d", 2000+s.nextUUID)), Author: s.snapshot.Ticket.Requester,
		Public: true, URL: s.snapshot.Ticket.URL + "#comment-" + fmt.Sprintf("%012d", 2000+s.nextUUID), Body: body, Files: files,
	})
	s.nextUUID++
}

func newLinearWriteSource(ready bool) *linearWriteSource {
	s := &linearWriteSource{linearFlowSource: newLinearFlowSource(), actor: linearID(linearOtherUUID)}
	if !ready {
		s.snapshot.Comments = s.snapshot.Comments[:1]
		s.snapshot.Comments[0].Body = "My Python script times out."
	}
	return s
}

func TestLinearClarificationDoesNotUseItsOwnCommentAsRequesterReply(t *testing.T) {
	s := newLinearWriteSource(false)
	// The token's account is also the issue author.
	s.actor = s.snapshot.Ticket.Requester
	dir := filepath.Join(t.TempDir(), "cases")
	config := linearWriteConfig()
	flow := openLinearFlow(t, s, dir, config)
	c := trackLinearClarification(t, flow)
	c, err := flow.Clarify(t.Context(), 42, c.Revision)
	if err != nil || len(c.Questions) != 1 || c.Questions[0].Status != "sent" || c.Questions[0].CommentID <= 0 {
		t.Fatalf("self-authored clarification receipt: %+v, %v", c, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openLinearFlow(t, s, dir, config)
	c, err = flow.Refresh(t.Context(), 42)
	if err != nil || c.State != "waiting" || len(c.Facts) != 0 || s.posted != 1 {
		t.Fatalf("our own clarification became evidence or progress: %+v, posts=%d, %v", c, s.posted, err)
	}
	addLinearAuthorReply(s, "Python version: 3.12.4", nil)
	c, err = flow.Refresh(t.Context(), 42)
	if err != nil || c.State != "waiting" || len(c.Facts) != 1 || s.posted != 2 {
		t.Fatalf("real author reply did not permit follow-up: %+v, posts=%d, %v", c, s.posted, err)
	}
}

func TestLinearClarificationReviewFollowupAndReadyEvidence(t *testing.T) {
	s := newLinearWriteSource(false)
	dir := filepath.Join(t.TempDir(), "cases")
	config := linearWriteConfig()
	flow := openLinearFlow(t, s, dir, config)
	c := trackLinearClarification(t, flow)
	s.beforePost = func() {
		current := flow.Cases()[0]
		if len(current.Questions) == 0 || current.Questions[len(current.Questions)-1].Status != "unconfirmed" {
			t.Fatal("clarification was sent before its durable reservation")
		}
	}
	waiting, err := flow.Clarify(t.Context(), 42, c.Revision)
	if err != nil || waiting.State != "waiting" || waiting.ClarificationApproval == "" || s.posted != 1 {
		t.Fatalf("approved clarification delivery: %+v, %v", waiting, err)
	}
	addLinearAuthorReply(s, "Python version: 3.12.4", nil)
	waiting, err = flow.Refresh(t.Context(), 42)
	if err != nil || waiting.State != "waiting" || s.posted != 2 || strings.Contains(s.snapshot.Comments[len(s.snapshot.Comments)-1].Body, "Python version") {
		t.Fatalf("authorized follow-up did not narrow missing evidence: %+v, posts=%d, %v", waiting, s.posted, err)
	}
	addLinearAuthorReply(s, "Expected: Wait.\nActual: Fails.", newLinearFlowSource().snapshot.Comments[1].Files)
	ready, err := flow.Refresh(t.Context(), 42)
	if err != nil || ready.State != "approval" || ready.Plan == "" || ready.RunID != "" || s.posted != 2 {
		t.Fatalf("reply did not prepare evidence without launching: %+v, posts=%d, %v", ready, s.posted, err)
	}
}

func TestLinearCompletedIssueBlocksInvestigationComment(t *testing.T) {
	s := newLinearWriteSource(true)
	flow := openLinearFlow(t, s, filepath.Join(t.TempDir(), "cases"), linearWriteConfig())
	id, saved := completedLinearReport(t, flow)
	if flow.NotePreview(id, saved) == nil {
		t.Fatal("fixture did not produce a comment preview")
	}
	s.snapshot.Ticket.Status = "completed"
	preview := flow.NotePreview(id, saved)
	if preview == nil || preview.Status != "ready" {
		t.Fatalf("completed issue still offers a fresh preview: %+v", preview)
	}
	if _, err := flow.PostNote(t.Context(), id, saved, preview.Plan); !errors.Is(err, ErrBlocked) || s.posted != 0 {
		t.Fatalf("completed issue admitted a comment: %v", err)
	}
}

func completedLinearReport(t *testing.T, flow *Flow) (string, map[string]any) {
	t.Helper()
	tracked := trackLinearFlowFixture(t, flow)
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
		"experiments": []any{map[string]any{"process_id": "proc-linear", "command": []any{"python3", "app.py"},
			"exit_code": json.Number("1"), "stderr": "private process output"}},
	}
}

func linearReportSource(saved map[string]any) map[string]any {
	return saved["case"].(map[string]any)["source"].(map[string]any)
}

func TestLinearInvestigationCommentBindsExactReportAndConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, *Flow)
	}{
		{"disabled comments", func(_ map[string]any, f *Flow) { f.settings.InvestigationComments = false }},
		{"wrong source kind", func(saved map[string]any, _ *Flow) { linearReportSource(saved)["kind"] = "github" }},
		{"wrong identifier", func(saved map[string]any, _ *Flow) { linearReportSource(saved)["linear_identifier"] = "OTHER-42" }},
		{"wrong team", func(saved map[string]any, _ *Flow) { linearReportSource(saved)["linear_team"] = "acme/OTHER" }},
		{"wrong URL", func(saved map[string]any, _ *Flow) {
			linearReportSource(saved)["url"] = "https://linear.app/acme/ENG/issue/OTHER-42"
		}},
		{"wrong run approval", func(saved map[string]any, _ *Flow) { linearReportSource(saved)["approval_plan"] = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newLinearWriteSource(true)
			flow := openLinearFlow(t, s, filepath.Join(t.TempDir(), "cases"), linearWriteConfig())
			id, saved := completedLinearReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil || preview.Source != "linear" || preview.Visibility != "workspace" || !preview.CanPost {
				t.Fatalf("fixture did not produce a workspace comment preview: %+v", preview)
			}
			if !strings.HasPrefix(preview.Body, "```text\n") {
				t.Fatalf("imported Markdown was not fenced literally: %q", preview.Body)
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

func TestLinearInvestigationCommentPreviewAndDurableReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		err          error
	}{
		{"confirmed", "posted", nil},
		{"uncertain", "unconfirmed", errors.New("connection closed after sending")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newLinearWriteSource(true)
			s.postErr = tc.err
			dir := filepath.Join(t.TempDir(), "cases")
			config := linearWriteConfig()
			flow := openLinearFlow(t, s, dir, config)
			id, saved := completedLinearReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil || !preview.CanPost || preview.TicketURL != s.snapshot.Ticket.URL || preview.Plan == "" || s.posted != 0 {
				t.Fatalf("Linear investigation comment preview: %+v", preview)
			}
			if !strings.Contains(preview.Body, "Five seconds passed.") || !strings.Contains(preview.Body, "proc-linear") ||
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
			flow = openLinearFlow(t, s, dir, config)
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

func TestLinearFlowStoreCannotBeReusedForAnotherTeam(t *testing.T) {
	s := newLinearFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openLinearFlow(t, s, dir, Config{Source: "linear", Workspace: "acme", Team: "ENG", Policy: Policy{Tag: "reproduce"}})
	trackLinearFlowFixture(t, flow)
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := openStore(dir, "https://linear.app/acme/OTHER"); err == nil {
		store.lock.Close()
		t.Fatal("another team reused the same issue number's stored approval")
	}
}
