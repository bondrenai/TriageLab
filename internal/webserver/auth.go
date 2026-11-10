package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bondrenai/TriageLab/internal/auth"
)

// gate holds the workspace's sign-in state. It is enabled exactly once at
// startup; a nil gate on Server means sign-in was never configured, and every
// API route fails closed.
type gate struct {
	users    *auth.Store
	sessions *auth.SessionStore
	oidc     *auth.OIDCProvider
	limiter  *auth.LoginLimiter
}

// EnableAuth attaches local accounts and, optionally, one OpenID Connect
// provider. At least one usable login method is required.
func (s *Server) EnableAuth(users *auth.Store, provider *auth.OIDCProvider) error {
	if users == nil {
		return errors.New("a user store is required for sign-in")
	}
	if provider == nil && !users.EnabledLogin() {
		return errors.New("no sign-in method is configured; create a local account (triagelab user add) or configure an identity provider")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = &gate{users: users, sessions: auth.NewSessionStore(), oidc: provider, limiter: auth.NewLoginLimiter()}
	return nil
}

type identityKey struct{}

// actorFrom returns the authenticated account for a request, or "" when the
// route is public.
func actorFrom(ctx context.Context) auth.Identity {
	identity, _ := ctx.Value(identityKey{}).(auth.Identity)
	return identity
}

// authorize enforces session sign-in and the route's minimum role. It writes
// the response itself when access is denied.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, required auth.Role) bool {
	g := s.auth
	if g == nil {
		fail(w, http.StatusServiceUnavailable, "Sign-in is not configured for this workspace")
		return false
	}
	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Sign in to use the workspace")
		return false
	}
	identity, ok := g.sessions.Lookup(cookie.Value)
	if !ok {
		fail(w, http.StatusUnauthorized, "Sign in to use the workspace")
		return false
	}
	if !identity.Role.AtLeast(required) {
		switch required {
		case auth.RoleAdmin:
			fail(w, http.StatusForbidden, "Defining agents, procedures, and accounts requires the admin role")
		default:
			fail(w, http.StatusForbidden, "The viewer role can observe but not change workspace state")
		}
		return false
	}
	*r = *r.WithContext(context.WithValue(r.Context(), identityKey{}, identity))
	return true
}

// requiredRole reports the minimum role for an authenticated API request.
// Reads need viewer; interactions that start or steer work need user; the
// definition surface (saving kagent resources and ticket procedures, and
// account management) needs admin.
func requiredRole(method, path string) auth.Role {
	if method == http.MethodGet || method == http.MethodHead {
		if strings.HasPrefix(path, "/api/auth/admin/") {
			return auth.RoleAdmin
		}
		return auth.RoleViewer
	}
	if strings.HasPrefix(path, "/api/auth/admin/") {
		return auth.RoleAdmin
	}
	for _, resource := range []string{"agents", "models", "mcp-servers", "sandboxes", "egress-policies"} {
		if path == "/api/"+resource+"/definition" {
			return auth.RoleAdmin
		}
	}
	if path == "/api/tickets/procedure" || path == "/api/tickets/oauth/start" {
		return auth.RoleAdmin
	}
	return auth.RoleUser
}

// authRoute serves the public /api/auth endpoints. It reports false when the
// path is not an auth route.
func (s *Server) authRoute(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/auth/") {
		return false
	}
	switch {
	case r.URL.Path == "/api/auth/session" && r.Method == http.MethodGet:
		s.sessionInfo(w, r)
	case r.URL.Path == "/api/auth/login" && r.Method == http.MethodPost:
		s.login(w, r)
	case r.URL.Path == "/api/auth/logout" && r.Method == http.MethodPost:
		s.logout(w, r)
	case r.URL.Path == "/api/auth/oidc/start" && r.Method == http.MethodGet:
		s.oidcStart(w, r)
	case r.URL.Path == "/api/auth/oidc/callback" && r.Method == http.MethodGet:
		s.oidcCallback(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/auth/admin/users"):
		s.adminUsersRoute(w, r)
	default:
		fail(w, http.StatusNotFound, "Unknown sign-in route")
	}
	return true
}

type sessionResponse struct {
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
	Role          string `json:"role,omitempty"`
	Via           string `json:"via,omitempty"`
	OIDCEnabled   bool   `json:"oidc_enabled"`
}

func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request) {
	response := sessionResponse{}
	if g := s.auth; g != nil {
		response.OIDCEnabled = g.oidc != nil
		if cookie, err := r.Cookie(auth.SessionCookie); err == nil {
			if identity, ok := g.sessions.Lookup(cookie.Value); ok {
				response.Authenticated = true
				response.Username = identity.Username
				response.Role = string(identity.Role)
				response.Via = identity.Via
			}
		}
	}
	jsonResponse(w, http.StatusOK, response)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	g := s.auth
	if g == nil {
		fail(w, http.StatusServiceUnavailable, "Sign-in is not configured for this workspace")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		fail(w, http.StatusBadRequest, "Use a username and password")
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if input.Username == "" || len(input.Username) > 32 || len(input.Password) > 512 {
		fail(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}
	if !g.limiter.Allowed(input.Username) {
		fail(w, http.StatusTooManyRequests, "Too many failed sign-in attempts; wait before trying again")
		return
	}
	user, ok := g.users.Authenticate(input.Username, input.Password)
	if !ok {
		g.limiter.RecordFailure(input.Username)
		fail(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}
	g.limiter.RecordSuccess(input.Username)
	s.startSession(w, auth.Identity{Username: user.Username, Role: user.Role, Via: "password"})
}

func (s *Server) startSession(w http.ResponseWriter, identity auth.Identity) {
	token, err := s.auth.sessions.Start(identity)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not start a session")
		return
	}
	http.SetCookie(w, auth.SessionCookieFor(token, int(auth.SessionLifetime/time.Second)))
	jsonResponse(w, http.StatusOK, sessionResponse{Authenticated: true, Username: identity.Username,
		Role: string(identity.Role), Via: identity.Via, OIDCEnabled: s.auth.oidc != nil})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookie); err == nil && s.auth != nil {
		s.auth.sessions.End(cookie.Value)
	}
	http.SetCookie(w, auth.SessionCookieFor("", -1))
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

const oidcStateCookie = "triagelab_oidc_state"

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || s.auth.oidc == nil {
		fail(w, http.StatusNotFound, "Single sign-on is not configured")
		return
	}
	target, state, err := s.auth.oidc.BeginLogin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not start single sign-on")
		return
	}
	// Lax (not Strict) because the identity provider redirects the browser
	// back here cross-site; the state value is the CSRF defense.
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: state, Path: "/api/auth/oidc/callback",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || s.auth.oidc == nil {
		fail(w, http.StatusNotFound, "Single sign-on is not configured")
		return
	}
	query := r.URL.Query()
	cookie, cookieErr := r.Cookie(oidcStateCookie)
	state := query.Get("state")
	if cookieErr != nil || state == "" || len(state) > 128 || cookie.Value != state || query.Get("code") == "" || len(query.Get("code")) > 2048 || len(r.URL.RawQuery) > 8192 {
		s.oidcFailure(w, r)
		return
	}
	identity, err := s.auth.oidc.FinishLogin(r.Context(), os.Getenv, state, query.Get("code"))
	if err != nil {
		s.oidcFailure(w, r)
		return
	}
	token, err := s.auth.sessions.Start(identity)
	if err != nil {
		s.oidcFailure(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: "", Path: "/api/auth/oidc/callback", MaxAge: -1})
	http.SetCookie(w, auth.SessionCookieFor(token, int(auth.SessionLifetime/time.Second)))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) oidcFailure(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: "", Path: "/api/auth/oidc/callback", MaxAge: -1})
	http.Redirect(w, r, "/login?signin=failed", http.StatusSeeOther)
}

// adminUsersRoute manages local accounts. Every method requires the admin
// role; authorize ran before this handler in ServeHTTP.
func (s *Server) adminUsersRoute(w http.ResponseWriter, r *http.Request) {
	g := s.auth
	if g == nil {
		fail(w, http.StatusServiceUnavailable, "Sign-in is not configured for this workspace")
		return
	}
	actor := actorFrom(r.Context())
	if !actor.Role.AtLeast(auth.RoleAdmin) {
		fail(w, http.StatusForbidden, "Managing accounts requires the admin role")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/auth/admin/users")
	if rest == "" && r.URL.Path == "/api/auth/admin/users" {
		switch r.Method {
		case http.MethodGet:
			jsonResponse(w, http.StatusOK, map[string]any{"users": g.users.Accounts()})
		case http.MethodPost:
			var input struct {
				Username string `json:"username"`
				Password string `json:"password"`
				Role     string `json:"role"`
			}
			if !decodeAuthInput(w, r, &input, 8192) {
				return
			}
			role, err := auth.ParseRole(input.Role)
			if err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			if err = g.users.Create(input.Username, input.Password, role); err != nil {
				writeUserError(w, err)
				return
			}
			jsonResponse(w, http.StatusOK, map[string]any{"users": g.users.Accounts()})
		default:
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}
	if rest[0] != '/' {
		fail(w, http.StatusNotFound, "Unknown account route")
		return
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 32 {
		fail(w, http.StatusNotFound, "Unknown account route")
		return
	}
	username := parts[0]
	if len(parts) == 2 && parts[1] == "password" && r.Method == http.MethodPost {
		var input struct {
			Password string `json:"password"`
		}
		if !decodeAuthInput(w, r, &input, 8192) {
			return
		}
		if err := g.users.SetPassword(username, input.Password); err != nil {
			writeUserError(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"users": g.users.Accounts()})
		return
	}
	if len(parts) != 1 {
		fail(w, http.StatusNotFound, "Unknown account route")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var input struct {
			Role     *string `json:"role"`
			Disabled *bool   `json:"disabled"`
		}
		if !decodeAuthInput(w, r, &input, 2048) {
			return
		}
		if input.Role != nil {
			role, err := auth.ParseRole(*input.Role)
			if err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			if err = s.guardAdminChange(g, actor, username, role); err != nil {
				writeUserError(w, err)
				return
			}
			if err = g.users.SetRole(username, role); err != nil {
				writeUserError(w, err)
				return
			}
		}
		if input.Disabled != nil {
			if err := s.guardAdminDisable(g, actor, username, *input.Disabled); err != nil {
				writeUserError(w, err)
				return
			}
			if err := g.users.SetDisabled(username, *input.Disabled); err != nil {
				writeUserError(w, err)
				return
			}
		}
		jsonResponse(w, http.StatusOK, map[string]any{"users": g.users.Accounts()})
	case http.MethodDelete:
		if err := s.guardAdminDisable(g, actor, username, true); err != nil {
			writeUserError(w, err)
			return
		}
		if err := g.users.Remove(username); err != nil {
			writeUserError(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"users": g.users.Accounts()})
	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// guardAdminChange blocks role changes that would lock admins out of the
// workspace: nobody edits their own role, and the last enabled admin cannot
// be demoted.
// errAccountGuard marks rejected account changes that would lock admins out.
var errAccountGuard = errors.New("account change refused")

func (s *Server) guardAdminChange(g *gate, actor auth.Identity, username string, role auth.Role) error {
	if username == actor.Username {
		return fmt.Errorf("%w: your own role is managed by another admin or the CLI", errAccountGuard)
	}
	if role != auth.RoleAdmin && s.lastAdmin(g, username) {
		return fmt.Errorf("%w: the workspace must keep at least one enabled admin", errAccountGuard)
	}
	return nil
}

// guardAdminDisable blocks self-removal and losing the last enabled admin.
func (s *Server) guardAdminDisable(g *gate, actor auth.Identity, username string, disabling bool) error {
	if username == actor.Username {
		return fmt.Errorf("%w: your own account is managed by another admin or the CLI", errAccountGuard)
	}
	if disabling && s.lastAdmin(g, username) {
		return fmt.Errorf("%w: the workspace must keep at least one enabled admin", errAccountGuard)
	}
	return nil
}

// lastAdmin reports whether username is currently the only enabled admin
// among local accounts. The CLI can always recover a locked-out workspace.
func (s *Server) lastAdmin(g *gate, username string) bool {
	for _, account := range g.users.Accounts() {
		if account.Username != username && account.Role == auth.RoleAdmin && !account.Disabled {
			return false
		}
	}
	return true
}

func decodeAuthInput(w http.ResponseWriter, r *http.Request, input any, limit int64) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if d.Decode(input) != nil || d.Decode(new(any)) != io.EOF {
		fail(w, http.StatusBadRequest, "Invalid account request")
		return false
	}
	return true
}

func writeUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		fail(w, http.StatusNotFound, "Account not found")
	case errors.Is(err, auth.ErrUserExists):
		fail(w, http.StatusConflict, "An account with that name already exists")
	case errors.Is(err, errAccountGuard):
		fail(w, http.StatusConflict, err.Error())
	default:
		fail(w, http.StatusBadRequest, err.Error())
	}
}

// healthRoute answers the deployment health probe without a session.
func healthRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
