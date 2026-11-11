package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

const linearConnection = "110ea42a"

func TestLinearWorkspaceExposesOnlyReadOnlyConnection(t *testing.T) {
	s := newTestServer(t, nil)
	const token = "test-linear-server-token"
	t.Setenv("TRIAGELAB_TEST_LINEAR_TOKEN", token)
	seedTickets(t, s, linearConnection, ticketflow.Config{
		Source: "linear", Workspace: "acme", Team: "ENG", TokenEnv: "TRIAGELAB_TEST_LINEAR_TOKEN",
		Policy: ticketflow.Policy{Tag: "reproduce"},
	})
	w := request(s, http.MethodGet, "/api/tickets", nil)
	var workspace struct {
		Enabled     bool             `json:"enabled"`
		Connections []map[string]any `json:"connections"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &workspace); err != nil || w.Code != 200 {
		t.Fatalf("workspace response: HTTP %d %v", w.Code, err)
	}
	if !workspace.Enabled || len(workspace.Connections) != 1 {
		t.Fatalf("wrong workspace shape: %v", workspace)
	}
	connection := workspace.Connections[0]
	if connection["source"] != "linear" || connection["account"] != "https://linear.app/acme/ENG" ||
		connection["connected"] != true || connection["oauth"] != nil {
		t.Fatalf("wrong Linear connection metadata: %v", connection)
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("workspace disclosed a server credential")
	}
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{http.MethodPost, "/api/tickets/c/" + linearConnection + "/oauth/start", "", 409},
		{http.MethodGet, "/oauth/zendesk/callback?state=state&code=code", "", 400},
		{http.MethodPost, "/api/tickets/c/" + linearConnection + "/42/clarify", `{"revision":"reviewed"}`, 409},
		{http.MethodPost, "/api/tickets/c/" + linearConnection + "/0/preview", "", 400},
		{http.MethodPost, "/api/tickets/c/" + linearConnection + "/01/preview", "", 400},
	} {
		w := request(s, tc.method, tc.path, strings.NewReader(tc.body))
		if w.Code != tc.code {
			t.Fatalf("%s: HTTP %d, want %d: %s", tc.path, w.Code, tc.code, w.Body)
		}
	}
}

func TestCompletedLinearReportOffersReviewedWorkspaceComment(t *testing.T) {
	s := newTestServer(t, nil)
	const id = "037c54da-437c-48ed-b898-a36a27334e9d"
	const account = "https://linear.app/acme/ENG"
	const issueURL = account + "/issue/ENG-42"
	dir := filepath.Join(s.directory, ".tickets", linearConnection)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c := ticketflow.Case{TicketID: 42, Account: account, Source: "linear", URL: issueURL, State: "launched", RunID: id, Plan: "run-approval"}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "42.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIAGELAB_TEST_LINEAR_TOKEN", "fixture-linear-token")
	seedTickets(t, s, linearConnection, ticketflow.Config{Source: "linear", Workspace: "acme", Team: "ENG",
		TokenEnv: "TRIAGELAB_TEST_LINEAR_TOKEN", InvestigationComments: true,
		Policy: ticketflow.Policy{Tag: "reproduce"}})
	saved := map[string]any{
		"run_id": id, "status": "completed", "finished_at": "2026-11-09T12:10:00Z", "outcome": "reproduced",
		"case": map[string]any{"source": map[string]any{"kind": "linear", "linear_team": "acme/ENG",
			"linear_identifier": "ENG-42", "ticket_id": "42", "url": issueURL, "approval_plan": "run-approval"}},
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
		t.Fatalf("Linear report preview: HTTP %d %s", w.Code, w.Body)
	}
	note := result.Note
	if note.Source != "linear" || note.Visibility != "workspace" || !note.CanPost || note.TicketURL != issueURL || note.Plan == "" || !strings.Contains(note.Body, "Reproduced") {
		t.Fatalf("incorrect comment audience or source: %+v", note)
	}
	if w := request(s, http.MethodGet, path+"/report.json", nil); w.Code != 200 || strings.Contains(w.Body.String(), "ticket_note") {
		t.Fatal("mutable comment preview leaked into canonical report")
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
		t.Fatalf("unmarked comment write accepted: HTTP %d %s", w.Code, w.Body)
	}
}
