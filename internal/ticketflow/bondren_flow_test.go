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
// delivery through the Bondren provider branches. Connector HTTP behavior and
// live integration are deliberately outside these tests.
type bondrenFlowSource struct {
	origin     string
	issueUUID  string
	snapshot   snapshot
	contents   map[string][]byte
	reads      []string
	writes     int
	confirmErr error
}

func (s *bondrenFlowSource) account() string { return s.origin }
func (s *bondrenFlowSource) ticket(context.Context, int64) (ticket, error) {
	return s.snapshot.Ticket, nil
}
func (s *bondrenFlowSource) read(context.Context, int64) (snapshot, error) {
	return s.snapshot, nil
}
func (s *bondrenFlowSource) confirm(context.Context, ticket) error { return s.confirmErr }
func (s *bondrenFlowSource) file(_ context.Context, a attachment) ([]byte, error) {
	s.reads = append(s.reads, a.URL)
	data, found := s.contents[a.URL]
	if !found {
		return nil, errors.New("unknown fixture attachment")
	}
	return data, nil
}
func (s *bondrenFlowSource) postComment(context.Context, ticket, string, bool) (int64, error) {
	s.writes++
	return 0, ErrBlocked
}

func bondrenAttachmentFixtureURL() string {
	return bondrenTestOrigin + "/api/v1/issues/" + bondrenIssueUUIDF + "/attachments/" + bondrenFileUUID + "/download"
}

func newBondrenFlowSource() *bondrenFlowSource {
	const origin = bondrenTestOrigin
	issueURL := origin + "/work/support/" + bondrenIssueUUIDF
	return &bondrenFlowSource{
		origin: origin, issueUUID: bondrenIssueUUIDF,
		snapshot: snapshot{
			Ticket: ticket{ID: 42, Kind: "bondren", URL: issueURL, uuid: bondrenIssueUUIDF,
				Requester: bondrenID(bondrenAuthorUUID), Subject: "Timeout is too short", Status: "on_you",
				Tags:             []string{"reproduce"},
				Updated:          "2026-11-09T12:00:00Z",
				RequesterProfile: Requester{ID: bondrenID(bondrenAuthorUUID), Name: "Reporter"}},
			Comments: []comment{
				{ID: bondrenID(bondrenMessageA), Author: bondrenID(bondrenAuthorUUID), Public: true, URL: issueURL + "#message-" + bondrenMessageA,
					Body: "Python version: 3.12.4\nExpected: Wait five seconds.\nActual: Stops after five milliseconds."},
				{ID: bondrenID(bondrenMessageC), Author: bondrenID(bondrenAuthorUUID), Public: true,
					URL: issueURL + "#message-" + bondrenMessageC, Body: "The smallest reproduction.",
					Files: []attachment{{Name: "app.py", Size: 18, URL: bondrenAttachmentFixtureURL()}},
				},
			},
		},
		contents: map[string][]byte{bondrenAttachmentFixtureURL(): []byte("print('timeout')\n")},
	}
}

type bondrenWriteSource struct {
	*bondrenFlowSource
	posted     int
	postErr    error
	beforePost func()
	nextUUID   int
}

func (s *bondrenWriteSource) postComment(_ context.Context, t ticket, body string, public bool) (int64, error) {
	if s.beforePost != nil {
		s.beforePost()
	}
	s.posted++
	s.writes++
	uuid := fmt.Sprintf("00000000-0000-4000-8000-%012d", 3000+s.nextUUID)
	s.nextUUID++
	// The agent token authors messages as a team member, never as the requester.
	s.snapshot.Comments = append(s.snapshot.Comments, comment{ID: bondrenID(uuid), Author: bondrenID(bondrenMemberUUID),
		Public: public, Body: body, URL: t.URL + "#message-" + uuid})
	if s.postErr != nil {
		return 0, s.postErr
	}
	return bondrenID(uuid), nil
}

func bondrenWriteConfig() Config {
	return Config{Source: "bondren", Origin: bondrenTestOrigin, TokenEnv: "TEST_BONDREN_TOKEN",
		InternalNotes: true, Policy: Policy{Tag: "reproduce", AutoQuestions: true, MaxQuestions: 2}}
}

func openBondrenFlow(t *testing.T, s source, dir string, config Config, secrets ...string) *Flow {
	t.Helper()
	named, ok := s.(interface{ account() string })
	if !ok {
		t.Fatal("source without account")
	}
	store, err := openStore(dir, named.account())
	if err != nil {
		t.Fatal(err)
	}
	flow := newFlow(config, s, store, investigation.DefaultConfig(), append([]string{"test-bondren-write-credential"}, secrets...))
	t.Cleanup(func() {
		if !flow.closed {
			if err := flow.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return flow
}

func TestBondrenFlowPreservesAuthorEvidenceAndDurableRun(t *testing.T) {
	s := newBondrenFlowSource()
	issueURL := s.snapshot.Ticket.URL
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: bondrenID(bondrenMemberUUID), Author: bondrenID(bondrenMemberUUID), Public: true,
		URL: issueURL + "#message-x", Body: "Python version: 9.99\nExpected: Outsider claim.\nActual: Outsider result.",
	})
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openBondrenFlow(t, s, dir, Config{Source: "bondren", Origin: bondrenTestOrigin, Policy: Policy{Tag: "reproduce"}})
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || preview.State != "approval" || preview.Source != "bondren" || preview.URL != issueURL || len(flow.Cases()) != 0 {
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
		if input.Source["kind"] != "bondren" || input.Source["bondren_origin"] != bondrenTestOrigin ||
			input.Source["bondren_issue"] != bondrenIssueUUIDF || input.Source["ticket_id"] != "42" ||
			input.Source["url"] != issueURL || input.Source["approval_plan"] != tracked.Plan {
			t.Fatalf("incorrect investigation source: %+v", input.Source)
		}
		evidence := strings.Join(input.Evidence, "\n")
		if !strings.Contains(evidence, "Bondren issue author at "+issueURL+"#message-"+bondrenMessageC) ||
			strings.Contains(evidence, "Bondren issue author at "+issueURL+"#message-x") ||
			strings.Contains(evidence, "Outsider") {
			t.Fatalf("incorrect evidence provenance: %s", evidence)
		}
		if !strings.Contains(input.Description, "reported in Bondren") || strings.Contains(input.Description, "Outsider") {
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
	flow = openBondrenFlow(t, s, dir, Config{Source: "bondren", Origin: bondrenTestOrigin, Policy: Policy{Tag: "reproduce"}})
	if _, err := flow.Approve(t.Context(), 42, tracked.Plan, launch); !errors.Is(err, ErrBlocked) || launches != 1 || flow.Cases()[0].RunID != launched.RunID {
		t.Fatalf("restarted flow admitted another run: launches=%d, %v", launches, err)
	}
}

func trackBondrenClarification(t *testing.T, flow *Flow) Case {
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

func addBondrenRequesterReply(s *bondrenWriteSource, body string, files []attachment) {
	uuid := fmt.Sprintf("00000000-0000-4000-8000-%012d", 4000+s.nextUUID)
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: bondrenID(uuid), Author: s.snapshot.Ticket.Requester, Public: true,
		URL: s.snapshot.Ticket.URL + "#message-" + uuid, Body: body, Files: files,
	})
}

func TestBondrenClarificationSendsPublicMessageAndWaitsForRequester(t *testing.T) {
	s := &bondrenWriteSource{bondrenFlowSource: newBondrenFlowSource()}
	s.snapshot.Comments = s.snapshot.Comments[:1]
	s.snapshot.Comments[0].Body = "My Python script times out."
	dir := filepath.Join(t.TempDir(), "cases")
	config := bondrenWriteConfig()
	flow := openBondrenFlow(t, s, dir, config)
	c := trackBondrenClarification(t, flow)
	c, err := flow.Clarify(t.Context(), 42, c.Revision)
	if err != nil || len(c.Questions) != 1 || c.Questions[0].Status != "sent" || c.Questions[0].CommentID <= 0 {
		t.Fatalf("clarification receipt: %+v, %v", c, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openBondrenFlow(t, s, dir, config)
	c, err = flow.Refresh(t.Context(), 42)
	if err != nil || c.State != "waiting" || len(c.Facts) != 0 || s.posted != 1 {
		t.Fatalf("agent clarification became requester evidence: %+v, posts=%d, %v", c, s.posted, err)
	}
	addBondrenRequesterReply(s, "Python version: 3.12.4", nil)
	c, err = flow.Refresh(t.Context(), 42)
	if err != nil || c.State != "waiting" || len(c.Facts) != 1 || s.posted != 2 {
		t.Fatalf("real requester reply did not permit follow-up: %+v, posts=%d, %v", c, s.posted, err)
	}
}

func completedBondrenReport(t *testing.T, flow *Flow) (string, map[string]any) {
	t.Helper()
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || preview.State != "approval" {
		t.Fatalf("ready preview: %+v, %v", preview, err)
	}
	tracked, err := flow.Track(t.Context(), 42, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
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
		"experiments": []any{map[string]any{"process_id": "proc-bondren", "command": []any{"python3", "app.py"},
			"exit_code": json.Number("1"), "stderr": "private process output"}},
	}
}

func bondrenReportSource(saved map[string]any) map[string]any {
	return saved["case"].(map[string]any)["source"].(map[string]any)
}

func TestBondrenInvestigationNoteBindsExactReportAndConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, *Flow)
	}{
		{"disabled notes", func(_ map[string]any, f *Flow) { f.settings.InternalNotes = false }},
		{"wrong source kind", func(saved map[string]any, _ *Flow) { bondrenReportSource(saved)["kind"] = "linear" }},
		{"wrong origin", func(saved map[string]any, _ *Flow) {
			bondrenReportSource(saved)["bondren_origin"] = "https://other.bondren.example"
		}},
		{"wrong issue", func(saved map[string]any, _ *Flow) {
			bondrenReportSource(saved)["bondren_issue"] = "00000000-0000-4000-8000-000000000099"
		}},
		{"wrong URL", func(saved map[string]any, _ *Flow) {
			bondrenReportSource(saved)["url"] = bondrenTestOrigin + "/work/support/00000000-0000-4000-8000-000000000099"
		}},
		{"wrong run approval", func(saved map[string]any, _ *Flow) { bondrenReportSource(saved)["approval_plan"] = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &bondrenWriteSource{bondrenFlowSource: newBondrenFlowSource()}
			flow := openBondrenFlow(t, s, filepath.Join(t.TempDir(), "cases"), bondrenWriteConfig())
			id, saved := completedBondrenReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil || preview.Source != "bondren" || preview.Visibility != "internal" || !preview.CanPost {
				t.Fatalf("fixture did not produce an internal note preview: %+v", preview)
			}
			if !strings.HasPrefix(preview.Body, "```text\n") {
				t.Fatalf("imported Markdown was not fenced literally: %q", preview.Body)
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

func TestBondrenInvestigationNotePreviewAndDurableReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		err          error
	}{
		{"confirmed", "posted", nil},
		{"uncertain", "unconfirmed", errors.New("connection closed after sending")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &bondrenWriteSource{bondrenFlowSource: newBondrenFlowSource()}
			s.postErr = tc.err
			dir := filepath.Join(t.TempDir(), "cases")
			config := bondrenWriteConfig()
			flow := openBondrenFlow(t, s, dir, config)
			id, saved := completedBondrenReport(t, flow)
			preview := flow.NotePreview(id, saved)
			if preview == nil || !preview.CanPost || preview.TicketURL != s.snapshot.Ticket.URL || preview.Plan == "" || s.posted != 0 {
				t.Fatalf("Bondren investigation note preview: %+v", preview)
			}
			if !strings.Contains(preview.Body, "Five seconds passed.") || !strings.Contains(preview.Body, "proc-bondren") ||
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
			flow = openBondrenFlow(t, s, dir, config)
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

func TestBondrenClosedIssueBlocksInvestigationNote(t *testing.T) {
	s := &bondrenWriteSource{bondrenFlowSource: newBondrenFlowSource()}
	flow := openBondrenFlow(t, s, filepath.Join(t.TempDir(), "cases"), bondrenWriteConfig())
	id, saved := completedBondrenReport(t, flow)
	if flow.NotePreview(id, saved) == nil {
		t.Fatal("fixture did not produce a note preview")
	}
	s.snapshot.Ticket.Status = "closed"
	preview := flow.NotePreview(id, saved)
	if preview == nil || preview.Status != "ready" {
		t.Fatalf("closed issue still offers a fresh preview: %+v", preview)
	}
	if _, err := flow.PostNote(t.Context(), id, saved, preview.Plan, ""); !errors.Is(err, ErrBlocked) || s.posted != 0 {
		t.Fatalf("closed issue admitted a note: %v", err)
	}
}

func TestBondrenFlowStoreCannotBeReusedForAnotherOrigin(t *testing.T) {
	s := newBondrenFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openBondrenFlow(t, s, dir, Config{Source: "bondren", Origin: bondrenTestOrigin, Policy: Policy{Tag: "reproduce"}})
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || preview.State != "approval" {
		t.Fatalf("ready preview: %+v, %v", preview, err)
	}
	if _, err := flow.Track(t.Context(), 42, preview.Revision); err != nil {
		t.Fatal(err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := openStore(dir, "https://other.bondren.example"); err == nil {
		store.lock.Close()
		t.Fatal("another workspace reused the same issue number's stored approval")
	}
}
