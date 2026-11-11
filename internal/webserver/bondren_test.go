package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

const bondrenIssueURL = "https://app.bondren.example/work/support/00000000-0000-4000-8000-000000000042"

func TestBondrenWorkspaceExposesOnlyReadOnlyConnection(t *testing.T) {
	s := newTestServer(t, nil)
	const token = "test-bondren-server-token"
	t.Setenv("TRIAGELAB_TEST_BONDREN_TOKEN", token)
	if err := s.EnableTickets(ticketflow.Config{
		Source: "bondren", Origin: "https://app.bondren.example", TokenEnv: "TRIAGELAB_TEST_BONDREN_TOKEN",
		Policy: ticketflow.Policy{Tag: "reproduce"},
	}); err != nil {
		t.Fatal(err)
	}
	w := request(s, http.MethodGet, "/api/tickets", nil)
	var workspace map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &workspace); err != nil || w.Code != 200 {
		t.Fatalf("workspace response: HTTP %d %v", w.Code, err)
	}
	if workspace["source"] != "bondren" || workspace["account"] != "https://app.bondren.example" ||
		workspace["connected"] != true || workspace["oauth"] != nil {
		t.Fatalf("wrong Bondren connection metadata: %v", workspace)
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("workspace disclosed a server credential")
	}
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{http.MethodPost, "/api/tickets/oauth/start", "", 409},
		{http.MethodGet, "/oauth/zendesk/callback?state=state&code=code", "", 404},
		{http.MethodPost, "/api/tickets/42/clarify", `{"revision":"reviewed"}`, 409},
		{http.MethodPost, "/api/tickets/0/preview", "", 400},
		{http.MethodPost, "/api/tickets/01/preview", "", 400},
	} {
		w := request(s, tc.method, tc.path, strings.NewReader(tc.body))
		if w.Code != tc.code {
			t.Fatalf("%s: HTTP %d, want %d: %s", tc.path, w.Code, tc.code, w.Body)
		}
	}
}

func TestCompletedBondrenReportOffersReviewedInternalNote(t *testing.T) {
	s := newTestServer(t, nil)
	const id = "06b74da0-6b0c-4f5e-8a1a-1c6f65d94b1f"
	const account = "https://app.bondren.example"
	dir := filepath.Join(s.directory, ".tickets")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c := ticketflow.Case{TicketID: 42, Account: account, Source: "bondren", URL: bondrenIssueURL,
		State: "launched", RunID: id, Plan: "run-approval"}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "42.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIAGELAB_TEST_BONDREN_TOKEN", "fixture-bondren-token")
	if err := s.EnableTickets(ticketflow.Config{Source: "bondren", Origin: account,
		TokenEnv: "TRIAGELAB_TEST_BONDREN_TOKEN", InternalNotes: true,
		Policy: ticketflow.Policy{Tag: "reproduce"}}); err != nil {
		t.Fatal(err)
	}
	saved := map[string]any{
		"run_id": id, "status": "completed", "finished_at": "2026-11-09T12:10:00Z", "outcome": "reproduced",
		"case": map[string]any{"source": map[string]any{"kind": "bondren", "bondren_origin": account,
			"bondren_issue": "00000000-0000-4000-8000-000000000042", "ticket_id": "42", "url": bondrenIssueURL, "approval_plan": "run-approval"}},
	}
	if err := report.Write(filepath.Join(s.directory, id), saved); err != nil {
		t.Fatal(err)
	}
	path := "/api/runs/" + id
	w := request(s, http.MethodGet, path, nil)
	var result struct {
		Note *ticketflow.NotePreview `json:"ticket_note"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Note == nil {
		t.Fatalf("Bondren report preview: HTTP %d %s", w.Code, w.Body)
	}
	note := result.Note
	if note.Source != "bondren" || note.Visibility != "internal" || !note.CanPost || note.TicketURL != bondrenIssueURL || note.Plan == "" || !strings.Contains(note.Body, "Reproduced") {
		t.Fatalf("incorrect note audience or source: %+v", note)
	}
	if w := request(s, http.MethodGet, path+"/report.json", nil); w.Code != 200 || strings.Contains(w.Body.String(), "ticket_note") {
		t.Fatal("mutable note preview leaked into canonical report")
	}
	path += "/ticket-note"
	for _, input := range []string{`{"plan":"stale"}`, `{"plan":"` + note.Plan + `","body":"injected"}`} {
		w := request(s, http.MethodPost, path, strings.NewReader(input))
		if w.Code != 400 && w.Code != 409 {
			t.Fatalf("invalid approval accepted: HTTP %d %s", w.Code, w.Body)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8080"+path, strings.NewReader(`{"plan":"`+note.Plan+`"}`))
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("unauthenticated note write accepted: HTTP %d %s", w.Code, w.Body)
	}
}

func TestBondrenTicketRouteAcceptsIssueID(t *testing.T) {
	const issueID = "00000000-0000-4000-8000-000000000042"
	issue := `{"id":"` + issueID + `","number":42,"title":"Timeout","state":"on_you",` +
		`"operational_state":"native","requester_id":"00000000-0000-4000-8000-000000000010",` +
		`"requester_name":"Reporter","updated_at":"2026-11-09T12:00:00Z","tags":["reproduce"]}`
	messages := `{"items":[{"id":"00000000-0000-4000-8000-0000000000c1","issue_id":"` + issueID + `",` +
		`"body":"My Python script times out.","created_at":"2026-11-09T12:00:00Z",` +
		`"visibility":"public","author_kind":"end_user",` +
		`"author_end_user_id":"00000000-0000-4000-8000-000000000010","attachments":[]}],` +
		`"page_info":{"has_more":false,"next_cursor":null}}`
	numberLookups := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-bondren-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/issues/" + issueID:
			_, _ = w.Write([]byte(issue))
		case "/api/v1/issues/" + issueID + "/messages":
			_, _ = w.Write([]byte(messages))
		case "/api/v1/issues/query":
			numberLookups++
			http.Error(w, "unknown filter field", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	s := newTestServer(t, nil)
	t.Setenv("TRIAGELAB_TEST_BONDREN_TOKEN", "fixture-bondren-token")
	if err := s.EnableTickets(ticketflow.Config{Source: "bondren", Origin: origin.URL,
		TokenEnv: "TRIAGELAB_TEST_BONDREN_TOKEN", Policy: ticketflow.Policy{Tag: "reproduce"}}); err != nil {
		t.Fatal(err)
	}
	w := request(s, http.MethodPost, "/api/tickets/"+issueID+"/preview", nil)
	var c ticketflow.Case
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || w.Code != 200 {
		t.Fatalf("issue ID preview: HTTP %d %s", w.Code, w.Body)
	}
	if c.TicketID != 42 || c.Source != "bondren" || c.URL != origin.URL+"/work/support/"+issueID {
		t.Fatalf("issue ID resolved to the wrong case: %+v", c)
	}
	if numberLookups != 0 {
		t.Fatalf("issue ID preview used the number filter %d times", numberLookups)
	}
	// The resolved case keeps working through its numeric reference alone.
	w = request(s, http.MethodPost, "/api/tickets/42/preview", nil)
	var refreshed ticketflow.Case
	if err := json.Unmarshal(w.Body.Bytes(), &refreshed); err != nil || w.Code != 200 || refreshed.TicketID != 42 {
		t.Fatalf("numeric preview after ID enrollment: HTTP %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ path, body string }{
		{"/api/tickets/zzz-not-an-issue/preview", ""},
		{"/api/tickets/" + strings.Repeat("x", 36) + "/preview", ""},
	} {
		if w := request(s, http.MethodPost, tc.path, strings.NewReader(tc.body)); w.Code != 400 {
			t.Fatalf("%s: HTTP %d, want 400: %s", tc.path, w.Code, w.Body)
		}
	}
	if numberLookups != 0 {
		t.Fatalf("rejected references reached Bondren: %d number lookups", numberLookups)
	}
}

func TestTrackedIssueStartsInvestigationUnderPolicy(t *testing.T) {
	const issueID = "00000000-0000-4000-8000-000000000042"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/issues/" + issueID:
			_, _ = w.Write([]byte(`{"id":"` + issueID + `","number":42,"title":"Timeout","state":"on_you",
				"operational_state":"native","requester_id":"00000000-0000-4000-8000-000000000010",
				"updated_at":"2026-11-09T12:00:00Z","tags":["reproduce"]}`))
		case "/api/v1/issues/" + issueID + "/messages":
			_, _ = w.Write([]byte(`{"items":[],"page_info":{"has_more":false}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(origin.Close)
	called := make(chan string, 1)
	s := newTestServer(t, testBackend{call: func(_ context.Context, tool string, _ map[string]any) (map[string]any, error) {
		select {
		case called <- tool:
		default:
		}
		return nil, errors.New("fixture stops at upstream invocation")
	}})
	t.Setenv("TRIAGELAB_TEST_BONDREN_TOKEN", "fixture-bondren-token")
	if err := s.EnableTickets(ticketflow.Config{Source: "bondren", Origin: origin.URL,
		TokenEnv: "TRIAGELAB_TEST_BONDREN_TOKEN", Policy: ticketflow.Policy{Tag: "reproduce", AutoRuns: true}}); err != nil {
		t.Fatal(err)
	}
	version := s.tickets.Procedure()
	version.Procedure.Evidence = ticketflow.EvidenceRules{}
	if _, err := s.tickets.SaveProcedure(version.Procedure, version.Revision); err != nil {
		t.Fatal(err)
	}
	path := "/api/tickets/" + issueID
	w := request(s, http.MethodPost, path+"/preview", nil)
	var preview ticketflow.Case
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || w.Code != 200 ||
		!preview.AutoRun || preview.RunID != "" || len(s.tickets.Cases()) != 0 {
		t.Fatalf("preview: HTTP %d %s", w.Code, w.Body)
	}
	w = request(s, http.MethodPost, path+"/track", strings.NewReader(`{"revision":"`+preview.Revision+`"}`))
	var tracked ticketflow.Case
	if err := json.Unmarshal(w.Body.Bytes(), &tracked); err != nil || w.Code != 200 ||
		tracked.RunID == "" || !tracked.Unattended {
		t.Fatalf("track: HTTP %d %s", w.Code, w.Body)
	}
	select {
	case tool := <-called:
		if tool != "create_sandbox" {
			t.Fatalf("unexpected first investigation tool: %s", tool)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the investigator did not reach the backend")
	}
	state, err := s.loadRunState(tracked.RunID)
	if err != nil || state.Actor != "ticket policy" || state.Trigger != "ticket" {
		t.Fatalf("lifecycle lost ticket provenance: %+v, %v", state, err)
	}
	saved, err := s.readReport(tracked.RunID)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := saved["case"].(map[string]any)
	source, _ := input["source"].(map[string]any)
	if saved["started_by"] != "ticket policy" || source["approval_plan"] != tracked.Plan || source["unattended"] != true {
		t.Fatalf("report lost policy provenance: %+v", source)
	}
}
