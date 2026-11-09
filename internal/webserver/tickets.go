package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

// EnableTickets is startup configuration; credentials remain server-side.
func (s *Server) EnableTickets(config ticketflow.Config) error {
	if _, manual := s.config.ManualAgents[s.config.Agent]; manual {
		return errors.New("the default agent for ticket investigations must be separate from manual_agents")
	}
	flow, err := ticketflow.Open(config, filepath.Join(s.directory, ".tickets"), s.config, s.secrets...)
	if err != nil {
		return err
	}
	flow.ResolveTarget = s.resolveTicketTarget
	s.tickets = flow
	return nil
}

func (s *Server) resolveTicketTarget(ctx context.Context, agent, sandbox string) (ticketflow.RunTarget, error) {
	target := ticketflow.RunTarget{Agent: agent, Sandbox: sandbox}
	if !s.resourceWrites {
		if agent == s.config.Agent && sandbox == s.config.Template && sandbox == "triagelab-python" {
			return target, nil
		}
		return ticketflow.RunTarget{}, ticketflow.ErrBlocked
	}
	var err error
	if agent == s.config.Agent {
		target.AgentRevision, err = s.deployments.ReadyTarget(ctx, "agents", agent)
	} else {
		saved, readErr := s.deployments.ReadyAgent(ctx, agent)
		if readErr != nil {
			return ticketflow.RunTarget{}, readErr
		}
		target.AgentRevision = saved.Revision + ":" + saved.PreparedRevision
		target.Tools, err = savedAgentTools(saved)
	}
	if err != nil {
		return ticketflow.RunTarget{}, err
	}
	target.SandboxRevision, err = s.deployments.ReadyTarget(ctx, "sandboxtemplates", sandbox)
	return target, err
}

func (s *Server) ticketRoute(w http.ResponseWriter, r *http.Request) {
	flow := s.tickets
	if r.URL.Path == "/api/tickets" && r.Method == http.MethodGet {
		if flow == nil {
			jsonResponse(w, 200, map[string]bool{"enabled": false})
			return
		}
		procedure := flow.Procedure()
		settings := flow.Settings()
		settings.Policy = procedure.Procedure.Policy
		workspace := map[string]any{"enabled": true, "source": flow.Provider(), "account": flow.Account(),
			"settings": settings, "procedure": procedure, "cases": flow.Cases(), "connected": true}
		switch flow.Provider() {
		case "github":
			workspace["repository"] = strings.TrimPrefix(flow.Account(), "https://github.com/")
		case "zendesk":
			oauth := flow.OAuthStatus()
			workspace["oauth"], workspace["connected"] = oauth, oauth.Connected
		}
		jsonResponse(w, 200, workspace)
		return
	}
	if flow == nil {
		fail(w, 404, "A ticket or issue source is not configured")
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "Method not allowed")
		return
	}
	if r.URL.Path == "/api/tickets/procedure" {
		var input ticketflow.ProcedureVersion
		if !decodeTicketInput(w, r, &input, ticketflow.MaxProcedureSetBytes) {
			return
		}
		procedure, err := flow.SaveProcedures(input)
		writeTicketResult(w, procedure, err)
		return
	}
	if r.URL.Path == "/api/tickets/oauth/start" {
		if flow.Provider() != "zendesk" {
			fail(w, 409, "The configured source does not use Zendesk OAuth")
			return
		}
		s.zendeskAuthorize(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tickets/"), "/")
	if len(parts) != 2 {
		fail(w, 404, "Unknown ticket action")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[0] {
		fail(w, 400, "Use a positive numeric ticket ID or issue number")
		return
	}
	if parts[1] == "procedure-preview" {
		var input ticketflow.Procedure
		if !decodeTicketInput(w, r, &input, 16<<10) {
			return
		}
		c, err := flow.PreviewProcedure(r.Context(), id, input)
		writeTicketResult(w, c, err)
		return
	}
	if parts[1] == "route" {
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
	if parts[1] == "track" || parts[1] == "clarify" || parts[1] == "approve" {
		if !decodeTicketInput(w, r, &input, 1024) {
			return
		}
	}
	var c ticketflow.Case
	switch parts[1] {
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
	case errors.Is(err, ticketflow.ErrInvalidProcedure):
		fail(w, 400, "Check the match tags, evidence rules, clarification limits, and routing rules. Match tags must be unique and each required evidence item needs a question.")
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

func (s *Server) postTicketNote(w http.ResponseWriter, r *http.Request, id string) {
	if s.tickets == nil {
		fail(w, 404, "A ticket or issue source is not configured")
		return
	}
	var input struct {
		Plan string `json:"plan"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
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
	note, err := s.tickets.PostNote(r.Context(), id, saved, input.Plan)
	writeTicketResult(w, note, err)
}
