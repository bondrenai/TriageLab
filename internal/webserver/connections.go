package webserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

// A workspace keeps several ticket connections open at once. The registry
// file under .tickets is the durable record; the live entries hold the open
// flow for each record. A record without a live flow failed to open after an
// edit or restart and is retried on the next start.
type ticketConn struct {
	id      string
	label   string
	flow    *ticketflow.Flow
	managed *managedTicket
}

var connectionIDPath = regexp.MustCompile(`^[0-9a-f]{8}$`)

func (s *Server) ticketRoot() string { return filepath.Join(s.directory, ".tickets") }

func (s *Server) ticketConnections() []*ticketConn {
	s.ticketMu.RLock()
	defer s.ticketMu.RUnlock()
	return append([]*ticketConn(nil), s.ticketConns...)
}

func (s *Server) ticketConnection(id string) *ticketConn {
	s.ticketMu.RLock()
	defer s.ticketMu.RUnlock()
	for _, conn := range s.ticketConns {
		if conn.id == id {
			return conn
		}
	}
	return nil
}

// replaceTicketConnection swaps the live entry for a connection id.
func (s *Server) replaceTicketConnection(id string, next *ticketConn) {
	s.ticketMu.Lock()
	defer s.ticketMu.Unlock()
	replaced := false
	for i, conn := range s.ticketConns {
		if conn.id == id {
			if next == nil {
				s.ticketConns = append(s.ticketConns[:i], s.ticketConns[i+1:]...)
			} else {
				s.ticketConns[i] = next
			}
			replaced = true
			break
		}
	}
	if !replaced && next != nil {
		s.ticketConns = append(s.ticketConns, next)
	}
}

// openTicketConnection validates a record, opens its flow with the standard
// run wiring, and registers any stored credential for redaction.
func (s *Server) openTicketConnection(record ticketflow.Connection) (*ticketConn, error) {
	if _, manual := s.config.ManualAgents[s.config.Agent]; manual {
		return nil, errors.New("the default agent for ticket investigations must be separate from manual_agents")
	}
	config, err := record.Prepared()
	if err != nil {
		return nil, err
	}
	stateDir, err := ticketflow.ConnectionStateDir(s.ticketRoot(), record.ID)
	if err != nil {
		return nil, err
	}
	flow, err := ticketflow.Open(config, stateDir, s.config, s.secretList()...)
	if err != nil {
		return nil, err
	}
	flow.ResolveTarget = s.resolveTicketTarget
	if config.Policy.AutoRuns {
		flow.AutoLaunch = s.launchTicketAs("ticket policy")
	}
	s.addSecret(record.Token)
	return &ticketConn{id: record.ID, label: record.Label, flow: flow}, nil
}

// EnableTicketConnections opens every saved ticket connection. The optional
// bootstrap configurations seed an empty registry the first time the
// workspace starts; after that the registry is canonical and the workspace
// UI owns the connections.
func (s *Server) EnableTicketConnections(bootstrap []ticketflow.Config) (opened int, seeded bool, err error) {
	records, err := ticketflow.LoadConnections(s.ticketRoot())
	if err != nil {
		return 0, false, err
	}
	if len(records) == 0 && len(bootstrap) > 0 {
		for _, config := range bootstrap {
			id, idErr := ticketflow.NewConnectionID()
			if idErr != nil {
				return 0, false, idErr
			}
			records = append(records, ticketflow.Connection{ID: id, Config: config})
		}
		if err = ticketflow.SaveConnections(s.ticketRoot(), records); err != nil {
			return 0, false, err
		}
		seeded = true
	}
	for _, record := range records {
		conn, openErr := s.openTicketConnection(record)
		if openErr != nil {
			for _, openedConn := range s.ticketConnections() {
				_ = openedConn.flow.Close()
			}
			s.ticketMu.Lock()
			s.ticketConns = nil
			s.ticketMu.Unlock()
			return 0, seeded, openErr
		}
		s.replaceTicketConnection(record.ID, conn)
	}
	return len(records), seeded, nil
}

// connectionStatus is the admin projection of a saved connection. The stored
// credential value never appears; only how the connection authorizes.
type connectionStatus struct {
	ID         string `json:"id"`
	Label      string `json:"label,omitempty"`
	Source     string `json:"source"`
	Account    string `json:"account"`
	Running    bool   `json:"running"`
	Connected  bool   `json:"connected"`
	ManagedBy  string `json:"managed_by,omitempty"`
	Error      string `json:"error,omitempty"`
	Credential struct {
		Mode string `json:"mode"`
		Env  string `json:"env,omitempty"`
	} `json:"credential"`
	Config ticketflow.Config       `json:"config"`
	OAuth  *ticketflow.OAuthStatus `json:"oauth,omitempty"`
}

type connectionInput struct {
	Label        string            `json:"label"`
	Config       ticketflow.Config `json:"config"`
	Token        *string           `json:"token"`
	ConfirmReset bool              `json:"confirm_reset"`
}

func (s *Server) connectionsRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/tickets/connections")
	path = strings.Trim(path, "/")
	if path == "" {
		switch r.Method {
		case http.MethodGet:
			s.listConnections(w)
		case http.MethodPost:
			s.createConnection(w, r)
		default:
			fail(w, 405, "Method not allowed")
		}
		return
	}
	parts := strings.Split(path, "/")
	if !connectionIDPath.MatchString(parts[0]) {
		fail(w, 404, "Unknown ticket connection route")
		return
	}
	if s.gitopsOwns(parts[0]) {
		fail(w, http.StatusConflict, "This connection is managed by GitOps. Edit its Kubernetes resources.")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodPut:
		s.updateConnection(w, r, parts[0])
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.deleteConnection(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "test" && r.Method == http.MethodPost:
		s.testSavedConnection(w, r, parts[0])
	default:
		fail(w, 404, "Unknown ticket connection route")
	}
}

func (s *Server) listConnections(w http.ResponseWriter) {
	records, err := ticketflow.LoadConnections(s.ticketRoot())
	if err != nil {
		fail(w, 500, "Could not read the ticket connection registry")
		return
	}
	live := map[string]*ticketConn{}
	for _, conn := range s.ticketConnections() {
		live[conn.id] = conn
	}
	statuses := make([]connectionStatus, 0, len(records))
	for _, record := range records {
		status := connectionStatus{ID: record.ID, Label: record.Label, Source: record.Config.Provider(),
			Account: ticketflow.SourceAccount(record.Config), Config: record.Config,
			Running: false, Connected: false}
		status.Credential.Mode = record.CredentialMode()
		status.Credential.Env = record.Config.TokenEnv
		if conn, ok := live[record.ID]; ok {
			status.Running = true
			status.Connected = true
			if conn.flow.Provider() == "zendesk" {
				oauth := conn.flow.OAuthStatus()
				status.Connected = oauth.Connected
				status.OAuth = &oauth
			}
		}
		statuses = append(statuses, status)
	}
	statuses = append(statuses, s.gitopsStatuses()...)
	jsonResponse(w, 200, map[string]any{"connections": statuses})
}

func decodeConnectionInput(w http.ResponseWriter, r *http.Request, limit int64) (connectionInput, bool) {
	var input connectionInput
	if !decodeTicketInput(w, r, &input, limit) {
		return input, false
	}
	return input, true
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	input, ok := decodeConnectionInput(w, r, 32<<10)
	if !ok {
		return
	}
	id, err := ticketflow.NewConnectionID()
	if err != nil {
		fail(w, 500, "Could not allocate a ticket connection")
		return
	}
	record := ticketflow.Connection{ID: id, Label: input.Label, Config: input.Config, Token: derefToken(input.Token)}
	conn, err := s.openTicketConnection(record)
	if err != nil {
		fail(w, 400, fmt.Sprint(report.Redact(err.Error(), s.secretList()...)))
		return
	}
	records, err := ticketflow.LoadConnections(s.ticketRoot())
	if err == nil {
		err = ticketflow.SaveConnections(s.ticketRoot(), append(records, record))
	}
	if err != nil {
		_ = conn.flow.Close()
		fail(w, 500, "Could not save the ticket connection registry")
		return
	}
	s.replaceTicketConnection(id, conn)
	conn.flow.Start(s.ctx)
	jsonResponse(w, 201, map[string]string{"id": id})
}

func (s *Server) updateConnection(w http.ResponseWriter, r *http.Request, id string) {
	input, ok := decodeConnectionInput(w, r, 32<<10)
	if !ok {
		return
	}
	records, err := ticketflow.LoadConnections(s.ticketRoot())
	if err != nil {
		fail(w, 500, "Could not read the ticket connection registry")
		return
	}
	position := -1
	for i, record := range records {
		if record.ID == id {
			position = i
			break
		}
	}
	if position < 0 {
		fail(w, 404, "Ticket connection not found")
		return
	}
	previous := records[position]
	if previous.Config.Provider() != input.Config.Provider() {
		fail(w, 400, "The ticket source of a connection cannot change. Remove it and add a new connection.")
		return
	}
	record := ticketflow.Connection{ID: id, Label: input.Label, Config: input.Config,
		Token: previous.Token}
	if input.Token != nil {
		record.Token = *input.Token
	}
	if _, err := record.Prepared(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if ticketflow.SourceAccount(record.Config) != ticketflow.SourceAccount(previous.Config) && !input.ConfirmReset {
		fail(w, 409, "Changing the account or repository resets this connection's tracked cases and procedure. Confirm the reset to continue.")
		return
	}
	records[position] = record
	if err := ticketflow.SaveConnections(s.ticketRoot(), records); err != nil {
		fail(w, 500, "Could not save the ticket connection registry")
		return
	}
	if existing := s.ticketConnection(id); existing != nil {
		_ = existing.flow.Close()
		s.replaceTicketConnection(id, nil)
	}
	if ticketflow.SourceAccount(record.Config) != ticketflow.SourceAccount(previous.Config) {
		if stateDir, dirErr := ticketflow.ConnectionStateDir(s.ticketRoot(), id); dirErr == nil {
			_ = os.RemoveAll(stateDir)
		}
	}
	conn, err := s.openTicketConnection(record)
	if err != nil {
		fail(w, 502, fmt.Sprint(report.Redact(err.Error(), s.secretList()...)))
		return
	}
	s.replaceTicketConnection(id, conn)
	conn.flow.Start(s.ctx)
	jsonResponse(w, 200, map[string]string{"id": id})
}

func (s *Server) deleteConnection(w http.ResponseWriter, _ *http.Request, id string) {
	records, err := ticketflow.LoadConnections(s.ticketRoot())
	if err != nil {
		fail(w, 500, "Could not read the ticket connection registry")
		return
	}
	remaining := records[:0]
	found := false
	for _, record := range records {
		if record.ID == id {
			found = true
			continue
		}
		remaining = append(remaining, record)
	}
	if !found {
		fail(w, 404, "Ticket connection not found")
		return
	}
	if err := ticketflow.SaveConnections(s.ticketRoot(), remaining); err != nil {
		fail(w, 500, "Could not save the ticket connection registry")
		return
	}
	if existing := s.ticketConnection(id); existing != nil {
		_ = existing.flow.Close()
		s.replaceTicketConnection(id, nil)
	}
	if stateDir, dirErr := ticketflow.ConnectionStateDir(s.ticketRoot(), id); dirErr == nil {
		_ = os.RemoveAll(stateDir)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request, config ticketflow.Config, token string) {
	probe, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := ticketflow.VerifySource(probe, config.WithToken(token))
	if err != nil {
		if probe.Err() != nil {
			fail(w, 504, "The source did not answer the connection test in time")
			return
		}
		jsonResponse(w, 200, map[string]any{"ok": false, "detail": fmt.Sprint(report.Redact(err.Error(), s.secretList()...))})
		return
	}
	if config.Provider() == "zendesk" {
		jsonResponse(w, 200, map[string]any{"ok": true, "detail": "Settings check out. Connect Zendesk through its authorization page after saving."})
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true})
}

func (s *Server) testSavedConnection(w http.ResponseWriter, r *http.Request, id string) {
	input, ok := decodeConnectionInput(w, r, 32<<10)
	if !ok {
		return
	}
	token := derefToken(input.Token)
	if input.Token == nil {
		if records, err := ticketflow.LoadConnections(s.ticketRoot()); err == nil {
			for _, record := range records {
				if record.ID == id {
					token = record.Token
				}
			}
		}
	}
	s.testConnection(w, r, input.Config, token)
}

func derefToken(token *string) string {
	if token == nil {
		return ""
	}
	return *token
}
