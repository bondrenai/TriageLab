package webserver

import (
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
func (s *Server) EnableTickets(config ticketflow.Config, token string) error {
	flow, err := ticketflow.Open(config, token, filepath.Join(s.directory, ".tickets"), s.config, s.secrets...)
	if err != nil {
		return err
	}
	s.secrets = append(s.secrets, token)
	s.tickets = flow
	return nil
}

func (s *Server) ticketRoute(w http.ResponseWriter, r *http.Request) {
	flow := s.tickets
	if r.URL.Path == "/api/tickets" && r.Method == http.MethodGet {
		if flow == nil {
			jsonResponse(w, 200, map[string]bool{"enabled": false})
			return
		}
		jsonResponse(w, 200, map[string]any{"enabled": true, "settings": flow.Settings(), "cases": flow.Cases()})
		return
	}
	if flow == nil {
		fail(w, 404, "Zendesk is not configured")
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "Method not allowed")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tickets/"), "/")
	if len(parts) != 2 {
		fail(w, 404, "Unknown ticket action")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[0] {
		fail(w, 400, "Use a numeric Zendesk ticket ID")
		return
	}

	var input struct {
		Revision string `json:"revision"`
		Plan     string `json:"plan"`
	}
	if parts[1] == "track" || parts[1] == "approve" {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			fail(w, 400, "Invalid ticket action")
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
	case "approve":
		c, err = flow.Approve(r.Context(), id, input.Plan, s.launch)
	case "pause":
		c, err = flow.Pause(id, true)
	case "resume":
		c, err = flow.Pause(id, false)
	default:
		fail(w, 404, "Unknown ticket action")
		return
	}
	switch {
	case err == nil:
		jsonResponse(w, 200, c)
	case errors.Is(err, ticketflow.ErrChanged), errors.Is(err, ticketflow.ErrBusy), errors.Is(err, ticketflow.ErrBlocked):
		fail(w, 409, err.Error())
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
