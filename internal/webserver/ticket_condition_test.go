package webserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

func TestTicketConditionValidationRequiresAdminAndReturnsLocations(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.EnableTickets(ticketflow.Config{
		Source: "github", Repository: "owner/repo",
		Policy: ticketflow.Policy{Tag: "test"},
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/tickets/procedure/validate-condition"
	body := `{"expression":"true &&\nunknown_name == 1"}`
	worker := loginAs(t, s, "worker", "user-password-1")
	if w := requestAs(s, worker, http.MethodPost, path, strings.NewReader(body)); w.Code != http.StatusForbidden {
		t.Fatalf("worker validation: %d", w.Code)
	}
	w := request(s, http.MethodPost, path, strings.NewReader(body))
	var diagnostics []ticketflow.ConditionDiagnostic
	if err := json.Unmarshal(w.Body.Bytes(), &diagnostics); w.Code != http.StatusOK || err != nil ||
		len(diagnostics) != 1 || diagnostics[0].Line != 2 || diagnostics[0].Column != 1 {
		t.Fatalf("diagnostics: %d %s, %v", w.Code, w.Body, err)
	}
	w = request(s, http.MethodPost, path, strings.NewReader(`{"expression":"ticket.source == \"github\""}`))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("valid condition: %d %s", w.Code, w.Body)
	}
	if got := s.tickets.Procedure().Procedure.Policy.AutoRunCondition; got != "" {
		t.Fatal("validation persisted the draft")
	}
}
