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
	// clients and oauth are attached by EnableAPIClients and stay nil when
	// API clients are not configured.
	clients *auth.ClientStore
	oauth   http.Handler
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

// currentSessionIdentity returns the active session identity, re-checking local
// accounts so CLI or admin role changes, disables, and removals take effect on
// running sessions immediately. An unreadable user file fails the request
// closed with errUserStoreUnavailable but keeps the session, so a transient
// error or a mid-edit file does not sign everybody out.
func (s *Server) currentSessionIdentity(g *gate, token string) (auth.Identity, bool, error) {
	identity, ok := g.sessions.Lookup(token)
	if !ok {
		return auth.Identity{}, false, nil
	}
	if identity.Via == "password" {
		user, exists, err := g.users.Lookup(identity.Username)
		if err != nil {
			return auth.Identity{}, false, errUserStoreUnavailable
		}
		if !exists || user.Disabled {
			g.sessions.End(token)
			return auth.Identity{}, false, nil
		}
		identity.Role = user.Role
	}
	return identity, true, nil
}

var errUserStoreUnavailable = errors.New("the account store is temporarily unreadable")

// authorize enforces session sign-in and the route's minimum role, or Bearer
// authentication for an API client. It writes the response itself when access
// is denied.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, required auth.Role) bool {
	g := s.auth
	if g == nil {
		fail(w, http.StatusServiceUnavailable, "Sign-in is not configured for this workspace")
		return false
	}
	// A Bearer header selects API-client authentication exclusively; the
	// session cookie is never consulted afterwards.
	if bearerRequest(r) {
		if g.clients == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="TriageLab API clients"`)
			fail(w, http.StatusUnauthorized, "API client authentication is unavailable")
			return false
		}
		return s.authorizeClient(w, r, g)
	}
	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Sign in to use the workspace")
		return false
	}
	identity, ok, err := s.currentSessionIdentity(g, cookie.Value)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "The account store is unreadable; try again shortly")
		return false
	}
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
// definition surface (saving kagent resources, ticket connections and
// procedures, and account management) needs admin.
func requiredRole(method, path string) auth.Role {
	if strings.HasPrefix(path, "/api/tickets/connections") {
		// The connection registry carries configuration and credential
		// references, not just workspace state.
		return auth.RoleAdmin
	}
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
	if strings.HasPrefix(path, "/api/tickets/c/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/tickets/c/"), "/")
		if len(parts) >= 2 && parts[1] == "procedure" {
			return auth.RoleAdmin
		}
		if len(parts) == 3 && parts[1] == "oauth" && parts[2] == "start" {
			return auth.RoleAdmin
		}
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
	case r.URL.Path == "/api/auth/admin/audit":
		s.adminAuditRoute(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/auth/admin/users"):
		s.adminUsersRoute(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/auth/admin/clients"):
		s.adminClientsRoute(w, r)
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
			identity, ok, err := s.currentSessionIdentity(g, cookie.Value)
			if err != nil {
				fail(w, http.StatusServiceUnavailable, "The account store is unreadable; try again shortly")
				return
			}
			if ok {
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
		s.recordAudit(auth.Identity{Via: "password"}, "auth.login", s.loginAuditTarget(g, input.Username), "locked", "rate limited")
		fail(w, http.StatusTooManyRequests, "Too many failed sign-in attempts; wait before trying again")
		return
	}
	user, ok := g.users.Authenticate(input.Username, input.Password)
	if !ok {
		g.limiter.RecordFailure(input.Username)
		s.recordAudit(auth.Identity{Via: "password"}, "auth.login", s.loginAuditTarget(g, input.Username), "denied", "invalid credentials or disabled")
		fail(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}
	g.limiter.RecordSuccess(input.Username)
	identity := auth.Identity{Username: user.Username, Role: user.Role, Via: "password"}
	s.recordAudit(identity, "auth.login", user.Username, "ok", string(user.Role))
	s.startSession(w, identity)
}

// loginAuditTarget names a failed login's account only when it exists. The
// typed username is untrusted and is sometimes a password pasted into the
// wrong field, so unknown names are never written to the audit log.
func (s *Server) loginAuditTarget(g *gate, username string) string {
	if !auth.ValidUsername(username) {
		return "unknown-account"
	}
	if _, exists, err := g.users.Lookup(username); err != nil || !exists {
		return "unknown-account"
	}
	return username
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
		if identity, ok := s.auth.sessions.Lookup(cookie.Value); ok {
			s.recordAudit(identity, "auth.logout", identity.Username, "ok", "")
		}
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
		s.recordAudit(auth.Identity{Via: "oidc"}, "auth.oidc.login", "", "denied", "invalid callback")
		s.oidcFailure(w, r)
		return
	}
	identity, err := s.auth.oidc.FinishLogin(r.Context(), os.Getenv, state, query.Get("code"))
	if err != nil {
		s.recordAudit(auth.Identity{Via: "oidc"}, "auth.oidc.login", "", "denied", "provider rejected login")
		s.oidcFailure(w, r)
		return
	}
	token, err := s.auth.sessions.Start(identity)
	if err != nil {
		s.oidcFailure(w, r)
		return
	}
	s.recordAudit(identity, "auth.oidc.login", identity.Username, "ok", string(identity.Role))
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
				target := input.Username
				if !auth.ValidUsername(target) {
					target = "invalid-username"
				}
				s.recordAudit(actor, "admin.user.create", target, "error", auditError(err))
				writeUserError(w, err)
				return
			}
			s.recordAudit(actor, "admin.user.create", input.Username, "ok", string(role))
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
			s.recordAudit(actor, "admin.user.password", username, "error", auditError(err))
			writeUserError(w, err)
			return
		}
		s.recordAudit(actor, "admin.user.password", username, "ok", "")
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
		if username == actor.Username {
			err := fmt.Errorf("%w: your own account is managed by another admin or the CLI", errAccountGuard)
			s.recordAudit(actor, "admin.user.update", username, "conflict", auditError(err))
			writeUserError(w, err)
			return
		}
		change := auth.AccountChange{Disabled: input.Disabled, KeepEnabledAdmin: true}
		var details []string
		if input.Role != nil {
			role, err := auth.ParseRole(*input.Role)
			if err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			change.Role = &role
			details = append(details, "role="+string(role))
		}
		if input.Disabled != nil {
			details = append(details, fmt.Sprintf("disabled=%t", *input.Disabled))
		}
		if err := g.users.ChangeAccount(username, change); err != nil {
			s.recordAudit(actor, "admin.user.update", username, auditOutcome(err), auditError(err))
			writeUserError(w, err)
			return
		}
		s.recordAudit(actor, "admin.user.update", username, "ok", strings.Join(details, " "))
		jsonResponse(w, http.StatusOK, map[string]any{"users": g.users.Accounts()})
	case http.MethodDelete:
		if username == actor.Username {
			err := fmt.Errorf("%w: your own account is managed by another admin or the CLI", errAccountGuard)
			s.recordAudit(actor, "admin.user.delete", username, "conflict", auditError(err))
			writeUserError(w, err)
			return
		}
		if err := g.users.ChangeAccount(username, auth.AccountChange{Remove: true, KeepEnabledAdmin: true}); err != nil {
			s.recordAudit(actor, "admin.user.delete", username, auditOutcome(err), auditError(err))
			writeUserError(w, err)
			return
		}
		s.recordAudit(actor, "admin.user.delete", username, "ok", "")
		jsonResponse(w, http.StatusOK, map[string]any{"users": g.users.Accounts()})
	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// errAccountGuard marks rejected account changes that would lock admins out.
// The last-admin check itself runs inside the user store's write lock.
var errAccountGuard = errors.New("account change refused")

// auditOutcome labels refused account changes as conflicts.
func auditOutcome(err error) string {
	if errors.Is(err, errAccountGuard) || errors.Is(err, auth.ErrLastAdmin) {
		return "conflict"
	}
	return "error"
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
	case errors.Is(err, errAccountGuard), errors.Is(err, auth.ErrLastAdmin):
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
