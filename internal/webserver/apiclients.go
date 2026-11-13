package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bondrenai/TriageLab/internal/auth"
)

// EnableAPIClients attaches OAuth access for external applications. Admins
// create API clients that exchange a secret for scoped, memory-only access
// tokens; sign-in must already be enabled. Without this call the token
// endpoints and Bearer authentication stay off.
func (s *Server) EnableAPIClients(clients *auth.ClientStore) error {
	if clients == nil {
		return errors.New("an API client store is required for OAuth access")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auth == nil {
		return errors.New("sign-in must be enabled before API clients")
	}
	s.auth.clients = clients
	s.auth.oauth = auth.NewOAuthHandler(clients)
	return nil
}

// bearerRequest reports whether a request authenticates as an API client with
// a Bearer token. Session routes under /api/auth/ never do: an Authorization
// header there is ignored and the session cookie with its request header
// remain the only way in.
func bearerRequest(r *http.Request) bool {
	return r.Header.Get("Authorization") != "" &&
		strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/auth/")
}

// bearerToken extracts an RFC 6750 Bearer token from an Authorization header.
func bearerToken(header string) (string, bool) {
	if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
		return "", false
	}
	if token := strings.TrimSpace(header[7:]); token != "" {
		return token, true
	}
	return "", false
}

type clientGrantKey struct{}

// clientFrom returns the API client grant for a request, if any.
func clientFrom(ctx context.Context) (auth.Grant, bool) {
	grant, ok := ctx.Value(clientGrantKey{}).(auth.Grant)
	return grant, ok
}

// clientAccess classifies what an API client may do with a request.
type clientAccess int

const (
	// clientRefuse marks routes outside the API client surface.
	clientRefuse clientAccess = iota
	clientLaunch
	clientList
	clientRead
	clientSteer
)

// clientRoute classifies an API request for the API client surface and
// returns the run it names, if any. The surface is launching investigations,
// listing them, reading one run or its report downloads, and steering (pause,
// resume, cancel, close). Everything else, including tickets, agent
// definitions, settings, and account management, is refused.
func clientRoute(method, path string) (clientAccess, string) {
	if path == "/api/runs" {
		switch method {
		case http.MethodPost:
			return clientLaunch, ""
		case http.MethodGet:
			return clientList, ""
		}
		return clientRefuse, ""
	}
	if !strings.HasPrefix(path, "/api/runs/") {
		return clientRefuse, ""
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/runs/"), "/")
	if len(parts) > 2 || !runID.MatchString(parts[0]) {
		return clientRefuse, ""
	}
	switch {
	case len(parts) == 1 && method == http.MethodGet:
		return clientRead, parts[0]
	case len(parts) == 2 && method == http.MethodGet && (parts[1] == "report.json" || parts[1] == "report.md"):
		return clientRead, parts[0]
	case len(parts) == 2 && method == http.MethodPost:
		switch parts[1] {
		case "cancel", "pause", "steer", "resume", "close":
			return clientSteer, parts[0]
		}
	}
	return clientRefuse, ""
}

// authorizeClient authenticates a Bearer token and enforces the API client
// surface. The investigations:run scope covers launching and steering the runs
// a client started; investigations:read covers reading every run. A client
// without read scope sees only its own runs, and unknown or foreign runs
// answer like missing ones so the workspace's other work stays invisible.
func (s *Server) authorizeClient(w http.ResponseWriter, r *http.Request, g *gate) bool {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="TriageLab API clients"`)
		fail(w, http.StatusUnauthorized, "Use a Bearer token from the token endpoint")
		return false
	}
	grant, ok := g.clients.LookupToken(token)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		fail(w, http.StatusUnauthorized, "The access token is unknown, expired, or revoked; request a new one")
		return false
	}
	access, run := clientRoute(r.Method, r.URL.Path)
	if access == clientRefuse {
		fail(w, http.StatusForbidden, "This route is not available to API clients")
		return false
	}
	runs := grant.Allows(auth.ScopeRun)
	reads := grant.Allows(auth.ScopeRead)
	owned := false
	if access == clientRead || access == clientSteer {
		owned = s.runStartedBy(run) == auth.ClientActor(grant.ClientID)
	}
	switch {
	case access == clientLaunch && !runs,
		access == clientList && !runs && !reads:
		insufficientScope(w, auth.ScopeRun)
		return false
	case access == clientRead && !reads && (!runs || !owned):
		if runs {
			fail(w, http.StatusNotFound, "Report not found or unreadable")
		} else {
			insufficientScope(w, auth.ScopeRead)
		}
		return false
	case access == clientSteer && !runs:
		insufficientScope(w, auth.ScopeRun)
		return false
	case access == clientSteer && !owned:
		if reads {
			fail(w, http.StatusForbidden, "API clients steer only the investigations they started")
		} else {
			fail(w, http.StatusNotFound, "Run not found")
		}
		return false
	}
	ctx := context.WithValue(r.Context(), clientGrantKey{}, grant)
	ctx = context.WithValue(ctx, identityKey{}, auth.Identity{Username: auth.ClientActor(grant.ClientID), Via: "oauth"})
	*r = *r.WithContext(ctx)
	return true
}

func insufficientScope(w http.ResponseWriter, scope auth.Scope) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="insufficient_scope", scope=%q`, scope))
	fail(w, http.StatusForbidden, "This action needs the "+string(scope)+" scope")
}

// runStartedBy reads who launched a run. The report's top level is written by
// the workflow, never by the model, and started_by is preserved across
// resumed rounds, so it is a stable ownership record.
func (s *Server) runStartedBy(id string) string {
	root, err := s.reportRoot(id)
	if err != nil {
		return ""
	}
	defer root.Close()
	file, err := openRegular(root, "report.json")
	if err != nil {
		return ""
	}
	defer file.Close()
	var provenance struct {
		StartedBy string `json:"started_by"`
	}
	if json.NewDecoder(io.LimitReader(file, maxReportBytes+1)).Decode(&provenance) != nil {
		return ""
	}
	return provenance.StartedBy
}

// adminClientsRoute manages API clients. Every method requires the admin role;
// authorize ran before this handler in ServeHTTP.
func (s *Server) adminClientsRoute(w http.ResponseWriter, r *http.Request) {
	g := s.auth
	if g == nil || g.clients == nil {
		fail(w, http.StatusServiceUnavailable, "API clients are not configured for this workspace")
		return
	}
	if !actorFrom(r.Context()).Role.AtLeast(auth.RoleAdmin) {
		fail(w, http.StatusForbidden, "Managing API clients requires the admin role")
		return
	}
	listing := func() map[string]any {
		return map[string]any{"clients": g.clients.Clients(), "scopes": auth.Scopes()}
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/auth/admin/clients")
	if rest == "" && r.URL.Path == "/api/auth/admin/clients" {
		switch r.Method {
		case http.MethodGet:
			jsonResponse(w, http.StatusOK, listing())
		case http.MethodPost:
			var input struct {
				ID     string   `json:"id"`
				Scopes []string `json:"scopes"`
			}
			if !decodeAuthInput(w, r, &input, 8192) {
				return
			}
			scopes, ok := parseAdminScopes(w, input.Scopes)
			if !ok {
				return
			}
			id := strings.TrimSpace(input.ID)
			secret, err := g.clients.Create(id, scopes, actorFrom(r.Context()).Username)
			if err != nil {
				writeClientError(w, err)
				return
			}
			jsonResponse(w, http.StatusOK, map[string]any{"clients": g.clients.Clients(), "scopes": auth.Scopes(),
				"client_id": id, "client_secret": secret})
		default:
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}
	if rest[0] != '/' {
		fail(w, http.StatusNotFound, "Unknown API client route")
		return
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(parts) > 2 || !auth.ValidClientID(parts[0]) {
		fail(w, http.StatusNotFound, "Unknown API client route")
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "secret" && r.Method == http.MethodPost {
		secret, err := g.clients.RotateSecret(id)
		if err != nil {
			writeClientError(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"clients": g.clients.Clients(), "scopes": auth.Scopes(),
			"client_id": id, "client_secret": secret})
		return
	}
	if len(parts) != 1 {
		fail(w, http.StatusNotFound, "Unknown API client route")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var input struct {
			Scopes   []string `json:"scopes"`
			Disabled *bool    `json:"disabled"`
		}
		if !decodeAuthInput(w, r, &input, 8192) {
			return
		}
		if input.Scopes != nil {
			scopes, ok := parseAdminScopes(w, input.Scopes)
			if !ok {
				return
			}
			if err := g.clients.SetScopes(id, scopes); err != nil {
				writeClientError(w, err)
				return
			}
		}
		if input.Disabled != nil {
			if err := g.clients.SetDisabled(id, *input.Disabled); err != nil {
				writeClientError(w, err)
				return
			}
		}
		jsonResponse(w, http.StatusOK, listing())
	case http.MethodDelete:
		if err := g.clients.Remove(id); err != nil {
			writeClientError(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, listing())
	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// parseAdminScopes validates the scope names of an admin request; the client
// needs at least one. It writes the response itself when the input is invalid.
func parseAdminScopes(w http.ResponseWriter, values []string) ([]auth.Scope, bool) {
	scopes, err := auth.ParseScopes(values)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if len(scopes) == 0 {
		fail(w, http.StatusBadRequest, "Grant at least one scope")
		return nil, false
	}
	return scopes, true
}

func writeClientError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrClientNotFound):
		fail(w, http.StatusNotFound, "API client not found")
	case errors.Is(err, auth.ErrClientExists):
		fail(w, http.StatusConflict, "An API client with that ID already exists")
	default:
		fail(w, http.StatusBadRequest, err.Error())
	}
}
