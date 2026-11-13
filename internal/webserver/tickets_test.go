package webserver

import (
	"bytes"
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

func TestSaveTicketProcedureChecksRevisionAndSourceCapabilities(t *testing.T) {
	s := newTestServer(t, nil)
	seedTickets(t, s, githubConnection, ticketflow.Config{Source: "github", Repository: "owner/example", Policy: ticketflow.Policy{Tag: "reproduce"}})
	var workspace struct {
		Connections []struct {
			Procedure ticketflow.ProcedureVersion `json:"procedure"`
		} `json:"connections"`
	}
	w := request(s, http.MethodGet, "/api/tickets", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &workspace); err != nil || w.Code != 200 || len(workspace.Connections) != 1 || workspace.Connections[0].Procedure.Revision == "" {
		t.Fatalf("get procedure: HTTP %d %s", w.Code, w.Body)
	}
	version := workspace.Connections[0].Procedure
	version.Procedure.Policy.Tag = "timeout"
	version.Procedure.Routes = []ticketflow.RouteRule{{Tag: "product:network", Agent: "network-investigator"}}
	post := func(version ticketflow.ProcedureVersion) *httptest.ResponseRecorder {
		data, err := json.Marshal(version)
		if err != nil {
			t.Fatal(err)
		}
		return request(s, http.MethodPost, "/api/tickets/c/"+githubConnection+"/procedure", bytes.NewReader(data))
	}
	w = post(version)
	var saved ticketflow.ProcedureVersion
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || w.Code != 200 || saved.Revision == version.Revision || saved.Procedure.Policy.Tag != "timeout" {
		t.Fatalf("save procedure: HTTP %d %s", w.Code, w.Body)
	}
	if w := post(version); w.Code != 409 {
		t.Fatalf("accepted stale save: HTTP %d %s", w.Code, w.Body)
	}
	saved.Procedure.Policy.AutoQuestions = true
	saved.Procedure.Policy.MaxQuestions = 1
	if w := post(saved); w.Code != 400 {
		t.Fatalf("procedure granted new connector write access: HTTP %d %s", w.Code, w.Body)
	}
}

func TestTicketWorkspaceSavesProductProcedures(t *testing.T) {
	s := newTestServer(t, nil)
	seedTickets(t, s, githubConnection, ticketflow.Config{Source: "github", Repository: "owner/example", Policy: ticketflow.Policy{Tag: "reproduce"}})
	w := request(s, http.MethodGet, "/api/tickets", nil)
	var workspace struct {
		Connections []struct {
			Procedure ticketflow.ProcedureVersion `json:"procedure"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &workspace); err != nil || w.Code != 200 || len(workspace.Connections) != 1 {
		t.Fatalf("read workspace: %d %s", w.Code, w.Body)
	}
	product := workspace.Connections[0].Procedure.Procedure
	product.Policy.Tag = "product:network"
	product.Clarification = "Can you share the failing request?"
	data, err := json.Marshal(map[string]any{
		"revision": workspace.Connections[0].Procedure.Revision, "procedure": workspace.Connections[0].Procedure.Procedure,
		"additional_procedures": []ticketflow.Procedure{product},
	})
	if err != nil {
		t.Fatal(err)
	}
	w = request(s, http.MethodPost, "/api/tickets/c/"+githubConnection+"/procedure", bytes.NewReader(data))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"tag":"product:network"`) {
		t.Fatalf("save product procedure: %d %s", w.Code, w.Body)
	}
	w = request(s, http.MethodGet, "/api/tickets", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"clarification":"Can you share the failing request?"`) {
		t.Fatalf("product procedure missing on reload: %d %s", w.Code, w.Body)
	}
}

func TestCompletedTicketReportOffersInternalNotePreview(t *testing.T) {
	s := newTestServer(t, nil)
	const id = "037c54da-437c-48ed-b898-a36a27334e9c"
	const origin = "https://example.zendesk.com"
	store := filepath.Join(s.directory, ".tickets", zendeskConnection)
	if err := os.MkdirAll(store, 0700); err != nil {
		t.Fatal(err)
	}
	tracked, err := json.Marshal(ticketflow.Case{TicketID: 42, Account: origin, State: "launched", RunID: id, Plan: "run-approval"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "42.json"), tracked, 0600); err != nil {
		t.Fatal(err)
	}
	seedTickets(t, s, zendeskConnection, ticketflow.Config{
		Subdomain: "example", ClientID: "test-local", RedirectURI: "http://localhost:8080/oauth/zendesk/callback",
		InternalNotes: true, Policy: ticketflow.Policy{Tag: "reproduce"},
	})
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
	// An operator edit travels with the plan of the reviewed preview; it can
	// never ride a stale or absent plan.
	if w := request(s, http.MethodPost, path, strings.NewReader(`{"plan":"stale","body":"injected"}`)); w.Code != 409 {
		t.Fatalf("edited body on a stale plan accepted: HTTP %d", w.Code)
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

func TestAutomaticInternalNoteKeepsHumanInLoopWithoutConnection(t *testing.T) {
	s := newTestServer(t, nil)
	const id = "18b1c7aa-9be4-4d34-97a6-6c4f0fbb1a70"
	const origin = "https://example.zendesk.com"
	store := filepath.Join(s.directory, ".tickets", zendeskConnection)
	if err := os.MkdirAll(store, 0700); err != nil {
		t.Fatal(err)
	}
	tracked, err := json.Marshal(ticketflow.Case{TicketID: 42, Account: origin, State: "launched", RunID: id, Plan: "run-approval", AutoComment: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "42.json"), tracked, 0600); err != nil {
		t.Fatal(err)
	}
	seedTickets(t, s, zendeskConnection, ticketflow.Config{
		Subdomain: "example", ClientID: "test-local", RedirectURI: "http://localhost:8080/oauth/zendesk/callback",
		InternalNotes: true, Policy: ticketflow.Policy{Tag: "reproduce"},
	})
	saved := map[string]any{
		"run_id": id, "status": "completed", "finished_at": "2026-11-13T09:00:00Z", "outcome": "reproduced",
		"case": map[string]any{"source": map[string]any{"kind": "zendesk", "ticket_id": "42", "url": origin + "/agent/tickets/42", "approval_plan": "run-approval"}},
	}
	if err := report.Write(filepath.Join(s.directory, id), saved); err != nil {
		t.Fatal(err)
	}
	// The policy flagged the case, but Zendesk OAuth is not connected, so the
	// automatic attempt must skip and leave the note ready for a human.
	s.postAutomaticTicketNote(id)
	path := "/api/runs/" + id
	w := request(s, http.MethodGet, path, nil)
	var result struct {
		Note *ticketflow.NotePreview `json:"ticket_note"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Note == nil ||
		result.Note.Status != "ready" || !result.Note.Automatic || !strings.Contains(result.Note.Body, "Reproduced") {
		t.Fatalf("automatic skip did not leave a ready review preview: HTTP %d %s", w.Code, w.Body)
	}
}
