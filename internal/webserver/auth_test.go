package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/auth"
	"github.com/bondrenai/TriageLab/internal/investigation"
)

const workerUUID = "00000000-0000-4000-8000-000000000001"

func TestAPIRequiresSignIn(t *testing.T) {
	s := newTestServer(t, nil)
	if w := requestAs(s, nil, "GET", "/api/settings", nil); w.Code != 401 {
		t.Fatalf("unauthenticated settings: %d", w.Code)
	}
	forged := &http.Cookie{Name: auth.SessionCookie, Value: "forgery"}
	if w := requestAs(s, forged, "GET", "/api/settings", nil); w.Code != 401 {
		t.Fatalf("forged session: %d", w.Code)
	}
	// Static assets and health stay open for the sign-in page and probes.
	if w := requestAs(s, nil, "GET", "/", nil); w.Code != 200 {
		t.Fatalf("index: %d", w.Code)
	}
	probe := httptest.NewRequest(http.MethodGet, "http://localhost:8080/healthz", nil)
	pw := httptest.NewRecorder()
	s.ServeHTTP(pw, probe)
	if pw.Code != 200 {
		t.Fatalf("health: %d %s", pw.Code, pw.Body)
	}

	admin := loginAs(t, s, "admin", "admin-password-1")
	if w := requestAs(s, admin, "GET", "/api/settings", nil); w.Code != 200 {
		t.Fatalf("authenticated settings: %d %s", w.Code, w.Body)
	}
	// Logout revokes the session server-side.
	if w := requestAs(s, admin, "POST", "/api/auth/logout", nil); w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := requestAs(s, admin, "GET", "/api/settings", nil); w.Code != 401 {
		t.Fatalf("session survived logout: %d", w.Code)
	}
}

func TestLoginFailuresAndLockout(t *testing.T) {
	s := newTestServer(t, nil)
	for i := 0; i < 5; i++ {
		if w := requestAs(s, nil, "POST", "/api/auth/login",
			strings.NewReader(`{"username":"admin","password":"wrong-password"}`)); w.Code != 401 {
			t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := requestAs(s, nil, "POST", "/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"admin-password-1"}`)); w.Code != 429 {
		t.Fatalf("lockout: %d %s", w.Code, w.Body)
	}
	// The message must not reveal whether the account exists.
	if w := requestAs(s, nil, "POST", "/api/auth/login",
		strings.NewReader(`{"username":"ghost","password":"whatever-1"}`)); w.Code != 401 ||
		w.Body.String() != `{"error":"Invalid username or password"}`+"\n" {
		t.Fatalf("unknown account: %d %s", w.Code, w.Body)
	}
	if w := requestAs(s, nil, "POST", "/api/auth/login", strings.NewReader(`not json`)); w.Code != 400 {
		t.Fatalf("malformed login: %d", w.Code)
	}
}

func TestRoleEnforcement(t *testing.T) {
	s := newTestServer(t, nil)
	viewer := loginAs(t, s, "observer", "viewer-password-1")
	user := loginAs(t, s, "worker", "user-password-1")
	admin := loginAs(t, s, "admin", "admin-password-1")

	// Viewers read workspace state but cannot change it.
	if w := requestAs(s, viewer, "GET", "/api/settings", nil); w.Code != 200 {
		t.Fatalf("viewer read: %d %s", w.Code, w.Body)
	}
	if w := requestAs(s, viewer, "GET", "/api/tickets", nil); w.Code != 200 {
		t.Fatalf("viewer tickets: %d", w.Code)
	}
	for _, attempt := range []struct {
		method, path string
		body         io.Reader
	}{
		{"POST", "/api/runs", strings.NewReader("")},
		{"POST", "/api/runs/" + workerUUID + "/cancel", nil},
		{"POST", "/api/tickets/1/preview", nil},
		{"POST", "/api/agents/definition", strings.NewReader(`{}`)},
		{"GET", "/api/auth/admin/users", nil},
	} {
		if w := requestAs(s, viewer, attempt.method, attempt.path, attempt.body); w.Code != 403 {
			t.Fatalf("viewer mutation %s %s: %d %s", attempt.method, attempt.path, w.Code, w.Body)
		}
	}

	// Users may interact and steer runs but not define agents or procedures.
	if w := requestAs(s, user, "POST", "/api/runs/"+workerUUID+"/cancel", nil); w.Code != 409 {
		t.Fatalf("user cancel should pass the role gate: %d %s", w.Code, w.Body)
	}
	for _, attempt := range []struct {
		method, path string
		body         io.Reader
	}{
		{"POST", "/api/agents/definition", strings.NewReader(`{"name":"x","model_ref":"m"}`)},
		{"POST", "/api/sandboxes/definition", strings.NewReader(`{}`)},
		{"POST", "/api/tickets/procedure", strings.NewReader(`{}`)},
		{"POST", "/api/tickets/oauth/start", nil},
		{"GET", "/api/auth/admin/users", nil},
		{"POST", "/api/auth/admin/users", strings.NewReader(`{}`)},
	} {
		if w := requestAs(s, user, attempt.method, attempt.path, attempt.body); w.Code != 403 {
			t.Fatalf("user admin route %s %s: %d %s", attempt.method, attempt.path, w.Code, w.Body)
		}
	}

	// Admins manage accounts and definitions (cluster errors differ from 403).
	if w := requestAs(s, admin, "GET", "/api/auth/admin/users", nil); w.Code != 200 {
		t.Fatalf("admin accounts: %d %s", w.Code, w.Body)
	}
	if w := requestAs(s, admin, "POST", "/api/agents/definition", strings.NewReader(`{}`)); w.Code == 401 || strings.Contains(w.Body.String(), "admin role") {
		t.Fatalf("admin definition blocked by roles: %d %s", w.Code, w.Body)
	}
}

func TestAccountManagementAPI(t *testing.T) {
	s := newTestServer(t, nil)
	admin := loginAs(t, s, "admin", "admin-password-1")
	viewer := loginAs(t, s, "observer", "viewer-password-1")

	create := func(body string) *httptest.ResponseRecorder {
		return requestAs(s, admin, "POST", "/api/auth/admin/users", strings.NewReader(body))
	}
	if w := create(`{"username":"pat.dev","password":"password-123","role":"user"}`); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if pat := loginAs(t, s, "pat.dev", "password-123"); pat == nil {
		t.Fatal("created account cannot sign in")
	}
	if w := create(`{"username":"pat.dev","password":"password-456","role":"user"}`); w.Code != 409 {
		t.Fatalf("duplicate: %d", w.Code)
	}
	if w := create(`{"username":"UPPER","password":"password-123","role":"user"}`); w.Code != 400 {
		t.Fatalf("bad name: %d", w.Code)
	}
	if w := create(`{"username":"ok.name","password":"password-123","role":"root"}`); w.Code != 400 {
		t.Fatalf("bad role: %d", w.Code)
	}
	if w := create(`{"username":"ok.name","password":"short","role":"user"}`); w.Code != 400 {
		t.Fatalf("weak password: %d", w.Code)
	}

	// Viewer sessions never touch account management.
	if w := requestAs(s, viewer, "GET", "/api/auth/admin/users", nil); w.Code != 403 {
		t.Fatalf("viewer accounts: %d", w.Code)
	}

	// Role change and disable.
	if w := requestAs(s, admin, "PUT", "/api/auth/admin/users/pat.dev",
		strings.NewReader(`{"role":"viewer"}`)); w.Code != 200 {
		t.Fatalf("role change: %d %s", w.Code, w.Body)
	}
	if w := requestAs(s, admin, "PUT", "/api/auth/admin/users/pat.dev",
		strings.NewReader(`{"disabled":true}`)); w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if w := requestAs(s, nil, "POST", "/api/auth/login",
		strings.NewReader(`{"username":"pat.dev","password":"password-123"}`)); w.Code != 401 {
		t.Fatalf("disabled account accepted: %d", w.Code)
	}

	// Password reset re-enables sign-in.
	if w := requestAs(s, admin, "POST", "/api/auth/admin/users/pat.dev/password",
		strings.NewReader(`{"password":"password-789"}`)); w.Code != 200 {
		t.Fatalf("password reset: %d %s", w.Code, w.Body)
	}
	if w := requestAs(s, nil, "POST", "/api/auth/login",
		strings.NewReader(`{"username":"pat.dev","password":"password-789"}`)); w.Code != 401 {
		t.Fatalf("disabled account accepted after reset: %d", w.Code)
	}

	// Self-management is refused so an admin cannot lock themselves out.
	if w := requestAs(s, admin, "PUT", "/api/auth/admin/users/admin",
		strings.NewReader(`{"role":"viewer"}`)); w.Code != 409 {
		t.Fatalf("self demotion: %d %s", w.Code, w.Body)
	}
	if w := requestAs(s, admin, "DELETE", "/api/auth/admin/users/admin", nil); w.Code != 409 {
		t.Fatalf("self removal: %d", w.Code)
	}

	// Removal works for other accounts.
	if w := requestAs(s, admin, "DELETE", "/api/auth/admin/users/pat.dev", nil); w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if w := requestAs(s, admin, "DELETE", "/api/auth/admin/users/pat.dev", nil); w.Code != 404 {
		t.Fatalf("double remove: %d", w.Code)
	}
	// Account listings never expose hash material.
	w := requestAs(s, admin, "GET", "/api/auth/admin/users", nil)
	if strings.Contains(w.Body.String(), "pbkdf2") || strings.Contains(w.Body.String(), `"password":"`) {
		t.Fatalf("hash leak: %s", w.Body)
	}
}

func TestLastLocalAdminGuard(t *testing.T) {
	s := newTestServer(t, nil)
	ssoAdmin := auth.Identity{Username: "sso.admin", Role: auth.RoleAdmin}
	// The sole local admin cannot be disabled or demoted by an admin whose
	// account lives in the identity provider.
	if err := s.guardAdminDisable(s.auth, ssoAdmin, "admin", true); err == nil {
		t.Fatal("last local admin may be disabled")
	}
	if err := s.guardAdminChange(s.auth, ssoAdmin, "admin", auth.RoleViewer); err == nil {
		t.Fatal("last local admin may be demoted")
	}
	// With a second local admin both operations are allowed.
	if w := requestAs(s, adminCookie(s), "POST", "/api/auth/admin/users",
		strings.NewReader(`{"username":"admin2","password":"password-123","role":"admin"}`)); w.Code != 200 {
		t.Fatalf("second admin: %d %s", w.Code, w.Body)
	}
	if err := s.guardAdminDisable(s.auth, ssoAdmin, "admin", true); err != nil {
		t.Fatalf("guard too strict with two admins: %v", err)
	}
}

func TestSessionEndpointReportsState(t *testing.T) {
	s := newTestServer(t, nil)
	w := requestAs(s, nil, "GET", "/api/auth/session", nil)
	var response struct {
		Authenticated bool   `json:"authenticated"`
		OIDCEnabled   bool   `json:"oidc_enabled"`
		Username      string `json:"username"`
		Role          string `json:"role"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil || w.Code != 200 {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
	if response.Authenticated || response.OIDCEnabled || response.Username != "" {
		t.Fatalf("anonymous session leaked state: %+v", response)
	}
	admin := loginAs(t, s, "admin", "admin-password-1")
	w = requestAs(s, admin, "GET", "/api/auth/session", nil)
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.Authenticated || response.Username != "admin" || response.Role != "admin" {
		t.Fatalf("session state: %+v", response)
	}
}

func TestOIDCRedirectGlue(t *testing.T) {
	// A provider whose token endpoint always rejects: the glue around the
	// redirect flow is under test here, not token verification.
	mux := http.NewServeMux()
	var issuerURL string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
			issuerURL, issuerURL+"/authorize", issuerURL+"/token", issuerURL+"/jwks")
	})
	idp := httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	issuerURL = idp.URL

	config := auth.OIDCConfig{Issuer: idp.URL, ClientID: "client", RedirectURI: "http://127.0.0.1:8080/api/auth/oidc/callback"}
	normalized, err := config.Validate("")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := auth.NewOIDCProvider(context.Background(), normalized)
	if err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	assets := base + "/assets"
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assets+"/index.html", []byte("<html>workspace</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), investigation.DefaultConfig(), filepath.Join(base, "reports"), assets, testBackend{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	users, err := auth.OpenStore(filepath.Join(base, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableAuth(users, provider); err != nil {
		t.Fatal(err)
	}

	// The session endpoint advertises single sign-on.
	w := requestAs(s, nil, "GET", "/api/auth/session", nil)
	if !strings.Contains(w.Body.String(), `"oidc_enabled":true`) {
		t.Fatalf("oidc not advertised: %s", w.Body)
	}

	// Starting single sign-on redirects to the provider with a state cookie.
	start := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/auth/oidc/start", nil)
	sw := httptest.NewRecorder()
	s.ServeHTTP(sw, start)
	if sw.Code != 303 || !strings.HasPrefix(sw.Header().Get("Location"), idp.URL+"/authorize?") {
		t.Fatalf("start: %d %s", sw.Code, sw.Header().Get("Location"))
	}
	cookies := sw.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "triagelab_oidc_state" || cookies[0].Value == "" {
		t.Fatalf("state cookie: %v", cookies)
	}

	// A callback without the matching state cookie fails closed.
	callback := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/auth/oidc/callback?state="+cookies[0].Value+"&code=x", nil)
	cw := httptest.NewRecorder()
	s.ServeHTTP(cw, callback)
	if cw.Code != 303 || cw.Header().Get("Location") != "/login?signin=failed" {
		t.Fatalf("forged callback: %d %s", cw.Code, cw.Header().Get("Location"))
	}

	// With the cookie, a provider error still fails closed (token endpoint 400).
	callback = httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/auth/oidc/callback?state="+cookies[0].Value+"&code=x", nil)
	callback.AddCookie(cookies[0])
	cw = httptest.NewRecorder()
	s.ServeHTTP(cw, callback)
	if cw.Code != 303 || cw.Header().Get("Location") != "/login?signin=failed" {
		t.Fatalf("failed exchange: %d %s", cw.Code, cw.Header().Get("Location"))
	}
}

func TestServeRequiresAuth(t *testing.T) {
	s := newTestServer(t, nil)
	s.auth = nil
	if err := Serve(context.Background(), "127.0.0.1:0", s, io.Discard); err == nil || !strings.Contains(err.Error(), "sign-in") {
		t.Fatalf("serve without auth: %v", err)
	}
}

func TestRunsRecordWhoStartedThem(t *testing.T) {
	backend := testBackend{call: func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	}}
	s := newTestServer(t, backend)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, formRequest(t, s, "An issue"))
	if w.Code != 202 {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		if active == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never settled")
		}
		time.Sleep(50 * time.Millisecond)
	}
	saved, err := s.readReport(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved["started_by"] != "admin" {
		t.Fatalf("started_by: %v", saved["started_by"])
	}
}
