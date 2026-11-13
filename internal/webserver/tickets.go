package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

// resolveTicketTarget keeps approved runs bound to an exactly deployed agent
// and sandbox template. An empty namespace means the workspace default; any
// other namespace is verified in the reader for that namespace.
func (s *Server) resolveTicketTarget(ctx context.Context, agent, sandbox, namespace string) (ticketflow.RunTarget, error) {
	target := ticketflow.RunTarget{Agent: agent, Sandbox: sandbox, Namespace: namespace}
	if !s.resourceWrites {
		if agent == s.config.Agent && sandbox == s.config.Template && sandbox == "triagelab-python" && namespace == "" {
			return target, nil
		}
		return ticketflow.RunTarget{}, ticketflow.ErrBlocked
	}
	reader := s.readerFor(namespace)
	var err error
	if agent == s.config.Agent {
		target.AgentRevision, err = reader.ReadyTarget(ctx, "agents", agent)
	} else {
		saved, readErr := reader.ReadyAgent(ctx, agent)
		if readErr != nil {
			return ticketflow.RunTarget{}, readErr
		}
		target.AgentRevision = saved.Revision + ":" + saved.PreparedRevision
		target.Tools, err = savedAgentTools(saved)
	}
	if err != nil {
		return ticketflow.RunTarget{}, err
	}
	target.SandboxRevision, err = reader.ReadyTarget(ctx, "sandboxtemplates", sandbox)
	return target, err
}

func (s *Server) ticketRoute(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/tickets" {
		if r.Method != http.MethodGet {
			fail(w, 405, "Method not allowed")
			return
		}
		s.ticketWorkspace(w)
		return
	}
	if r.URL.Path == "/api/tickets/connections" || strings.HasPrefix(r.URL.Path, "/api/tickets/connections/") {
		s.connectionsRoute(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/tickets/c/") {
		s.connectionCaseRoute(w, r)
		return
	}
	fail(w, 404, "Unknown ticket route")
}

// ticketWorkspace is the viewer-facing view of every connection: settings,
// procedure, and tracked cases, without connection credentials.
func (s *Server) ticketWorkspace(w http.ResponseWriter) {
	connections := s.ticketConnections()
	if len(connections) == 0 {
		jsonResponse(w, 200, map[string]bool{"enabled": false})
		return
	}
	views := make([]map[string]any, 0, len(connections))
	for _, conn := range connections {
		views = append(views, s.connectionView(conn))
	}
	jsonResponse(w, 200, map[string]any{"enabled": true, "connections": views})
}

func (s *Server) connectionView(conn *ticketConn) map[string]any {
	flow := conn.flow
	procedure := flow.Procedure()
	settings := flow.Settings()
	settings.Policy = procedure.Procedure.Policy
	view := map[string]any{"id": conn.id, "label": conn.label, "source": flow.Provider(), "account": flow.Account(),
		"settings": settings, "procedure": procedure, "cases": flow.Cases(), "connected": true}
	if conn.managed != nil {
		view["managed_by"] = conn.managed.owner
	}
	switch flow.Provider() {
	case "github":
		view["repository"] = strings.TrimPrefix(flow.Account(), "https://github.com/")
	case "gitlab":
		// The account is the origin followed by the project path.
		if account, err := url.Parse(flow.Account()); err == nil {
			view["repository"] = strings.TrimPrefix(account.Path, "/")
		}
	case "zendesk":
		oauth := flow.OAuthStatus()
		view["oauth"], view["connected"] = oauth, oauth.Connected
	}
	return view
}

func (s *Server) connectionCaseRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tickets/c/"), "/")
	if len(parts) == 0 || !connectionIDPath.MatchString(parts[0]) {
		fail(w, 404, "Unknown ticket connection")
		return
	}
	conn := s.ticketConnection(parts[0])
	if conn == nil {
		fail(w, 404, "Unknown ticket connection")
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "Method not allowed")
		return
	}
	flow := conn.flow
	if len(parts) == 2 && parts[1] == "procedure" {
		if conn.managed != nil {
			fail(w, http.StatusConflict, "These procedures are managed by GitOps. Edit their Kubernetes resources.")
			return
		}
		var input ticketflow.ProcedureVersion
		if !decodeTicketInput(w, r, &input, ticketflow.MaxProcedureSetBytes) {
			return
		}
		procedure, err := flow.SaveProcedures(input)
		writeTicketResult(w, procedure, err)
		return
	}
	if len(parts) == 3 && parts[1] == "procedure" && parts[2] == "validate-condition" {
		var input struct {
			Expression string `json:"expression"`
		}
		if !decodeTicketInput(w, r, &input, 32<<10) {
			return
		}
		jsonResponse(w, http.StatusOK, flow.ValidateTicketCondition(input.Expression))
		return
	}
	if len(parts) == 3 && parts[1] == "oauth" && parts[2] == "start" {
		if flow.Provider() != "zendesk" {
			fail(w, 409, "This connection does not use Zendesk OAuth")
			return
		}
		s.zendeskAuthorize(w, r, conn)
		return
	}
	if len(parts) != 3 {
		fail(w, 404, "Unknown ticket action")
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[1] {
		// Bondren issues can also be referenced by the issue ID from their URL,
		// GitLab issues by the issue URL, and Jira issues by their key, URL, or
		// numeric id. The flow validates the reference before contacting the
		// source and reports the numeric case key.
		if flow.Provider() != "bondren" && flow.Provider() != "gitlab" && flow.Provider() != "jira" {
			fail(w, 400, "Use a positive numeric ticket ID or issue number")
			return
		}
		if id, err = flow.ResolveTicket(r.Context(), parts[1]); err != nil {
			writeTicketResult(w, nil, err)
			return
		}
	}
	switch parts[2] {
	case "procedure-preview":
		var input ticketflow.Procedure
		if !decodeTicketInput(w, r, &input, ticketflow.MaxProcedureSetBytes) {
			return
		}
		c, err := flow.PreviewProcedure(r.Context(), id, input)
		writeTicketResult(w, c, err)
		return
	case "route":
		var input struct {
			Revision string `json:"revision"`
			Agent    string `json:"agent"`
		}
		if !decodeTicketInput(w, r, &input, 1024) {
			return
		}
		c, err := flow.ChooseRoute(r.Context(), id, input.Revision, input.Agent)
		writeTicketResult(w, c, err)
		return
	}
	var input struct {
		Revision string `json:"revision"`
		Plan     string `json:"plan"`
	}
	if parts[2] == "track" || parts[2] == "clarify" || parts[2] == "approve" {
		if !decodeTicketInput(w, r, &input, 1024) {
			return
		}
	}
	var c ticketflow.Case
	switch parts[2] {
	case "preview":
		c, err = flow.Preview(r.Context(), id)
	case "track":
		c, err = flow.Track(r.Context(), id, input.Revision)
	case "refresh":
		c, err = flow.Refresh(r.Context(), id)
	case "clarify":
		c, err = flow.Clarify(r.Context(), id, input.Revision)
	case "approve":
		c, err = flow.Approve(r.Context(), id, input.Plan, s.launchTicketAs(actorFrom(r.Context()).Username))
	case "pause":
		c, err = flow.Pause(id, true)
	case "resume":
		c, err = flow.Pause(id, false)
	default:
		fail(w, 404, "Unknown ticket action")
		return
	}
	writeTicketResult(w, c, err)
}

func decodeTicketInput(w http.ResponseWriter, r *http.Request, input any, limit int64) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if d.Decode(input) != nil || d.Decode(new(any)) != io.EOF {
		fail(w, 400, "Invalid ticket action")
		return false
	}
	return true
}

func writeTicketResult(w http.ResponseWriter, value any, err error) {
	switch {
	case err == nil:
		jsonResponse(w, 200, value)
	case errors.Is(err, ticketflow.ErrReference), errors.Is(err, ticketflow.ErrJiraReference):
		fail(w, 400, err.Error())
	case errors.Is(err, ticketflow.ErrInvalidProcedure):
		fail(w, 400, "Check the match tags, evidence rules, clarification limits, and routing rules. Match tags must be unique and each required evidence item needs a question. Ticket conditions must be valid CEL expressions returning a boolean.")
	case errors.Is(err, ticketflow.ErrChanged), errors.Is(err, ticketflow.ErrBusy), errors.Is(err, ticketflow.ErrBlocked):
		fail(w, 409, err.Error())
	case errors.Is(err, ticketflow.ErrOAuthRequired):
		fail(w, 401, err.Error())
	case errors.Is(err, ticketflow.ErrNotFound):
		fail(w, 404, err.Error())
	case errors.Is(err, ticketflow.ErrStorage), errors.Is(err, errRunStore):
		fail(w, 500, err.Error())
	case errors.Is(err, errWorkspaceClosed):
		fail(w, 503, err.Error())
	default:
		fail(w, 502, err.Error())
	}
}

// ticketOwner returns the connection whose case launched the run, if any.
func (s *Server) ticketOwner(runID string) *ticketConn {
	for _, conn := range s.ticketConnections() {
		if conn.flow.OwnsRun(runID) {
			return conn
		}
	}
	return nil
}

func (s *Server) postTicketNote(w http.ResponseWriter, r *http.Request, id string) {
	var input struct {
		Plan string `json:"plan"`
		Body string `json:"body"`
	}
	// The body carries an operator edit of the prepared note; it is optional
	// and bounded like the server-side edit checks.
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, ticketflow.MaxNoteBodyBytes+1024))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || input.Plan == "" {
		fail(w, 400, "Invalid investigation comment approval")
		return
	}
	s.mu.Lock()
	active := s.activeID == id
	s.mu.Unlock()
	if active {
		writeTicketResult(w, nil, ticketflow.ErrBlocked)
		return
	}
	saved, err := s.readReport(id)
	if err != nil {
		fail(w, 404, "Run not found or unreadable")
		return
	}
	var owner *ticketConn
	for _, conn := range s.ticketConnections() {
		if conn.flow.NotePreview(id, saved) != nil {
			owner = conn
			break
		}
	}
	if owner == nil {
		writeTicketResult(w, nil, ticketflow.ErrBlocked)
		return
	}
	note, err := owner.flow.PostNote(r.Context(), id, saved, input.Plan, input.Body)
	writeTicketResult(w, note, err)
}

// postAutomaticTicketNote applies the case's comment policy when a
// ticket-launched run settles. The flow skips cases whose procedure does not
// post automatically; the ready preview keeps a human in the loop.
func (s *Server) postAutomaticTicketNote(id string) {
	saved, err := s.readReport(id)
	if err != nil {
		return
	}
	for _, conn := range s.ticketConnections() {
		if conn.flow.OwnsRun(id) {
			conn.flow.PostAutomaticNote(s.ctx, id, saved)
			return
		}
	}
}
