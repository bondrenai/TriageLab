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

func TestCompletedTicketReportOffersInternalNotePreview(t *testing.T) {
	s := newTestServer(t, nil)
	const id = "037c54da-437c-48ed-b898-a36a27334e9c"
	const origin = "https://example.zendesk.com"
	store := filepath.Join(s.directory, ".tickets")
	if err := os.Mkdir(store, 0700); err != nil {
		t.Fatal(err)
	}
	tracked, err := json.Marshal(ticketflow.Case{TicketID: 42, Account: origin, State: "launched", RunID: id, Plan: "run-approval"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "42.json"), tracked, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTickets(ticketflow.Config{
		Subdomain: "example", ClientID: "test-local", RedirectURI: "http://localhost:8080/oauth/zendesk/callback",
		InternalNotes: true, Policy: ticketflow.Policy{Tag: "reproduce"},
	}); err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"run_id": id, "status": "completed", "finished_at": "2026-11-08T12:10:00Z", "outcome": "reproduced",
		"case": map[string]any{"source": map[string]any{"kind": "zendesk", "ticket_id": "42", "url": origin + "/agent/tickets/42", "approval_plan": "run-approval"}},
	}
	if err := report.Write(filepath.Join(s.directory, id), data); err != nil {
		t.Fatal(err)
	}
	path := "/api/runs/" + id
	preview := request(s, http.MethodGet, path, nil)
	var result struct {
		Note *ticketflow.NotePreview `json:"ticket_note"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil || preview.Code != 200 || result.Note == nil ||
		result.Note.TicketID != 42 || result.Note.CanPost || !strings.Contains(result.Note.Body, "Reproduced") {
		t.Fatalf("report preview: HTTP %d %s", preview.Code, preview.Body)
	}
	down := request(s, http.MethodGet, path+"/report.json", nil)
	if down.Code != 200 || strings.Contains(down.Body.String(), "ticket_note") {
		t.Fatalf("note state leaked into the report download: HTTP %d", down.Code)
	}
	path += "/ticket-note"
	if w := request(s, http.MethodPost, path, strings.NewReader(`{"plan":"`+result.Note.Plan+`","body":"injected"}`)); w.Code != 400 {
		t.Fatalf("caller-supplied body accepted: HTTP %d", w.Code)
	}
	approval := `{"plan":"` + result.Note.Plan + `"}`
	unmarked := httptest.NewRequest(http.MethodPost, "http://localhost:8080"+path, strings.NewReader(approval))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, unmarked)
	if w.Code != 403 {
		t.Fatalf("unmarked note request accepted: HTTP %d", w.Code)
	}
	if w := request(s, http.MethodPost, path, strings.NewReader(approval)); w.Code != 401 {
		t.Fatalf("disconnected note post: HTTP %d %s", w.Code, w.Body)
	}
}
