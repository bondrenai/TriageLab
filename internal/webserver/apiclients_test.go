package webserver

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/auth"
)

// clientGrantSpec names one API client and the scopes it holds.
type clientGrantSpec struct {
	id     string
	scopes []auth.Scope
}

// enableClients attaches one API client store holding the given clients and
// returns each client's secret by ID.
func enableClients(t *testing.T, s *Server, specs ...clientGrantSpec) map[string]string {
	t.Helper()
	store, err := auth.OpenClientStore(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{}
	for _, spec := range specs {
		secret, err := store.Create(spec.id, spec.scopes, "admin")
		if err != nil {
			t.Fatal(err)
		}
		secrets[spec.id] = secret
	}
	if err = s.EnableAPIClients(store); err != nil {
		t.Fatal(err)
	}
	return secrets
}

// clientToken exchanges a client secret for an access token over HTTP, the
// way an external application does. An empty scope requests every granted one.
func clientToken(t *testing.T, s *Server, id, secret, scope string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}}
	if scope != "" {
		form.Set("scope", scope)
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8080"+auth.OAuthTokenPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth(id, secret)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &token) != nil || token.AccessToken == "" {
		t.Fatalf("token request for %s: %d %s", id, w.Code, w.Body)
	}
	return token.AccessToken
}

// mustClientSecret rotates a fresh secret through the admin API, because the
// creation secret is only known where the client was made.
func mustClientSecret(t *testing.T, s *Server, id string) string {
	t.Helper()
	w := requestAs(s, adminCookie(s), http.MethodPost, "/api/auth/admin/clients/"+id+"/secret", nil)
	var response struct {
		Secret string `json:"client_secret"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Secret == "" {
		t.Fatalf("rotate secret for %s: %d %s", id, w.Code, w.Body)
	}
	return response.Secret
}

// bearer performs a request with an Authorization header, no session cookie,
// and no cross-site request marker: exactly an API client's request.
func bearer(s *Server, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8080"+path, body)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// settleRun waits for the active investigation to finish.
func settleRun(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		if active == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("run never settled")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// clientUpload launches an investigation as an API client and returns the run ID.
func clientUpload(t *testing.T, s *Server, token, description string) string {
	t.Helper()
	var body strings.Builder
	form := multipart.NewWriter(&body)
	if err := form.WriteField("description", description); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/runs", strings.NewReader(body.String()))
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var started struct {
		ID string `json:"id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &started) != nil || started.ID == "" {
		t.Fatalf("client launch: %d %s", w.Code, w.Body)
	}
	return started.ID
}

// startSessionRun launches an investigation as the signed-in admin.
func startSessionRun(t *testing.T, s *Server, description string) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, formRequest(t, s, description))
	var started struct {
		ID string `json:"id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &started) != nil || started.ID == "" {
		t.Fatalf("session launch: %d %s", w.Code, w.Body)
	}
	return started.ID
}

func TestAPIClientLaunchesAndReadsItsRuns(t *testing.T) {
	backend := testBackend{call: func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	}}
	s := newTestServer(t, backend)
	secrets := enableClients(t, s, clientGrantSpec{"ci", []auth.Scope{auth.ScopeRun}})
	token := clientToken(t, s, "ci", secrets["ci"], "")

	run := clientUpload(t, s, token, "A reported issue")
	settleRun(t, s)

	saved, err := s.readReport(run)
	if err != nil {
		t.Fatal(err)
	}
	if saved["started_by"] != auth.ClientActor("ci") {
		t.Fatalf("started_by: %v", saved["started_by"])
	}
	state, err := s.loadRunState(run)
	if err != nil {
		t.Fatal(err)
	}
	if state.Trigger != "api" {
		t.Fatalf("trigger: %q", state.Trigger)
	}

	// The client reads its run and both report downloads.
	if w := bearer(s, http.MethodGet, "/api/runs/"+run, token, nil); w.Code != 200 {
		t.Fatalf("read own run: %d %s", w.Code, w.Body)
	}
	if w := bearer(s, http.MethodGet, "/api/runs/"+run+"/report.json", token, nil); w.Code != 200 {
		t.Fatalf("download report: %d %s", w.Code, w.Body)
	}
	if w := bearer(s, http.MethodGet, "/api/runs", token, nil); w.Code != 200 || !strings.Contains(w.Body.String(), run) {
		t.Fatalf("list runs: %d %s", w.Code, w.Body)
	}
}

func TestAPIClientScopeBoundaries(t *testing.T) {
	backend := testBackend{call: func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	}}
	s := newTestServer(t, backend)
	secrets := enableClients(t, s,
		clientGrantSpec{"ci", []auth.Scope{auth.ScopeRun}},
		clientGrantSpec{"reader", []auth.Scope{auth.ScopeRead}})

	// A session user starts a run the clients did not.
	foreign := startSessionRun(t, s, "Someone else's issue")
	settleRun(t, s)
	runToken := clientToken(t, s, "ci", secrets["ci"], "")
	readToken := clientToken(t, s, "reader", secrets["reader"], "")

	// Without read scope the client sees only its own runs.
	own := clientUpload(t, s, runToken, "A client issue")
	settleRun(t, s)
	w := bearer(s, http.MethodGet, "/api/runs", runToken, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), foreign) || !strings.Contains(w.Body.String(), own) {
		t.Fatalf("run-only listing mixes foreign runs: %d %s", w.Code, w.Body)
	}
	if w = bearer(s, http.MethodGet, "/api/runs/"+foreign, runToken, nil); w.Code != 404 {
		t.Fatalf("run-only read of a foreign run: %d %s", w.Code, w.Body)
	}
	if w = bearer(s, http.MethodGet, "/api/runs/"+foreign+"/report.json", runToken, nil); w.Code != 404 {
		t.Fatalf("run-only download of a foreign report: %d %s", w.Code, w.Body)
	}
	if w = bearer(s, http.MethodGet, "/api/runs/"+own, runToken, nil); w.Code != 200 {
		t.Fatalf("run-only read of own run: %d %s", w.Code, w.Body)
	}

	// Read scope reads every run but cannot launch or steer.
	if w = bearer(s, http.MethodGet, "/api/runs/"+foreign, readToken, nil); w.Code != 200 {
		t.Fatalf("read of a foreign run: %d %s", w.Code, w.Body)
	}
	if w = bearer(s, http.MethodGet, "/api/runs", readToken, nil); w.Code != 200 || !strings.Contains(w.Body.String(), foreign) {
		t.Fatalf("read listing: %d %s", w.Code, w.Body)
	}
	launch := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/runs", nil)
	launch.Header.Set("Authorization", "Bearer "+readToken)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, launch)
	if w.Code != 403 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="insufficient_scope"`) {
		t.Fatalf("read-only launch: %d %s", w.Code, w.Body)
	}
	if w = bearer(s, http.MethodPost, "/api/runs/"+own+"/cancel", readToken, nil); w.Code != 403 ||
		!strings.Contains(w.Header().Get("WWW-Authenticate"), "investigations:run") {
		t.Fatalf("read-only steer: %d %s", w.Code, w.Body)
	}
}

func TestAPIClientSteersOnlyItsOwnRuns(t *testing.T) {
	blocking := testBackend{call: func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	s := newTestServer(t, blocking)
	secrets := enableClients(t, s,
		clientGrantSpec{"ci", []auth.Scope{auth.ScopeRun, auth.ScopeRead}},
		clientGrantSpec{"blind", []auth.Scope{auth.ScopeRun}})
	token := clientToken(t, s, "ci", secrets["ci"], "")
	blindToken := clientToken(t, s, "blind", secrets["blind"], "")

	own := clientUpload(t, s, token, "A client issue")

	// Cancel works on the client's own active run, and a settled run answers
	// 409, which is a state answer rather than an authorization one.
	if w := bearer(s, http.MethodPost, "/api/runs/"+own+"/cancel", token, nil); w.Code != 202 {
		t.Fatalf("cancel own run: %d %s", w.Code, w.Body)
	}
	settleRun(t, s)
	if w := bearer(s, http.MethodPost, "/api/runs/"+own+"/cancel", token, nil); w.Code != 409 {
		t.Fatalf("cancel settled run: %d %s", w.Code, w.Body)
	}

	// With read scope a foreign run is visible but still not steerable; with
	// no read scope it is invisible.
	foreign := startSessionRun(t, s, "Session user's active run")
	defer func() {
		s.mu.Lock()
		if active := s.active; active != nil {
			active.cancel()
		}
		s.mu.Unlock()
		settleRun(t, s)
	}()
	if w := bearer(s, http.MethodGet, "/api/runs/"+foreign, token, nil); w.Code != 200 {
		t.Fatalf("read foreign run: %d %s", w.Code, w.Body)
	}
	if w := bearer(s, http.MethodPost, "/api/runs/"+foreign+"/cancel", token, nil); w.Code != 403 {
		t.Fatalf("steer foreign run: %d %s", w.Code, w.Body)
	}
	if w := bearer(s, http.MethodPost, "/api/runs/"+foreign+"/cancel", blindToken, nil); w.Code != 404 {
		t.Fatalf("steer without read scope leaks the run: %d %s", w.Code, w.Body)
	}
}

func TestAPIClientSurfaceRefused(t *testing.T) {
	s := newTestServer(t, nil)
	secrets := enableClients(t, s, clientGrantSpec{"ci", []auth.Scope{auth.ScopeRun, auth.ScopeRead}})
	token := clientToken(t, s, "ci", secrets["ci"], "")

	for _, path := range []string{"/api/settings", "/api/tickets", "/api/agents/status", "/api/models/definition",
		"/api/runs/not-a-uuid", "/api/runs/" + workerUUID + "/ticket-note"} {
		if w := bearer(s, http.MethodGet, path, token, nil); w.Code != 403 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := bearer(s, http.MethodPost, "/api/tickets/c/0a1b2c3d/1/preview", token, nil); w.Code != 403 {
		t.Fatalf("ticket action: %d %s", w.Code, w.Body)
	}
	// Account and client management need an admin session; a Bearer header
	// does not open them.
	if w := bearer(s, http.MethodGet, "/api/auth/admin/users", token, nil); w.Code != 401 {
		t.Fatalf("admin users: %d %s", w.Code, w.Body)
	}
	if w := bearer(s, http.MethodGet, "/api/auth/admin/clients", token, nil); w.Code != 401 {
		t.Fatalf("admin clients: %d %s", w.Code, w.Body)
	}
	// Session endpoints ignore the header entirely.
	if w := bearer(s, http.MethodGet, "/api/auth/session", token, nil); w.Code != 200 || strings.Contains(w.Body.String(), "client") {
		t.Fatalf("session endpoint: %d %s", w.Code, w.Body)
	}
}

func TestBearerAuthenticationFailures(t *testing.T) {
	s := newTestServer(t, nil)
	secrets := enableClients(t, s, clientGrantSpec{"ci", []auth.Scope{auth.ScopeRun}})
	token := clientToken(t, s, "ci", secrets["ci"], "")

	if w := bearer(s, http.MethodGet, "/api/runs", "forged", nil); w.Code != 401 ||
		!strings.Contains(w.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("forged token: %d %s", w.Code, w.Body)
	}
	// A non-Bearer scheme is answered with a Bearer challenge.
	r := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/runs", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNzd29yZA==")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 || !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("other scheme: %d %s", w.Code, w.Body)
	}

	// A valid session cookie never rescues an invalid Bearer token.
	r = httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/runs", nil)
	r.Header.Set("Authorization", "Bearer forged")
	r.AddCookie(adminCookie(s))
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("cookie fallback: %d %s", w.Code, w.Body)
	}

	// The request marker still guards session requests without a Bearer
	// header.
	r = httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/runs", nil)
	r.AddCookie(adminCookie(s))
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "request header") {
		t.Fatalf("session without marker: %d %s", w.Code, w.Body)
	}

	// Revoking the token ends API access immediately.
	form := url.Values{"token": {token}}
	r = httptest.NewRequest(http.MethodPost, "http://localhost:8080"+auth.OAuthRevocationPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("ci", secrets["ci"])
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("revocation: %d %s", w.Code, w.Body)
	}
	if w = bearer(s, http.MethodGet, "/api/runs", token, nil); w.Code != 401 {
		t.Fatalf("revoked token accepted: %d %s", w.Code, w.Body)
	}
}

func TestOAuthEndpointsWithinServerBoundaries(t *testing.T) {
	s := newTestServer(t, nil)
	secrets := enableClients(t, s, clientGrantSpec{"ci", []auth.Scope{auth.ScopeRun}})
	form := url.Values{"grant_type": {"client_credentials"}}

	// The token endpoint works without the cross-site request marker...
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8080"+auth.OAuthTokenPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("ci", secrets["ci"])
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("token without marker: %d %s", w.Code, w.Body)
	}

	// ...but stays inside the loopback and same-origin rules.
	r = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+auth.OAuthTokenPath, strings.NewReader(form.Encode()))
	r.SetBasicAuth("ci", secrets["ci"])
	r.Host = "workspace.example.com"
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("foreign host: %d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+auth.OAuthTokenPath, strings.NewReader(form.Encode()))
	r.SetBasicAuth("ci", secrets["ci"])
	r.Header.Set("Origin", "https://app.example.com")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin token request: %d %s", w.Code, w.Body)
	}

	// Without API clients configured the endpoints fail closed.
	plain := newTestServer(t, nil)
	r = httptest.NewRequest(http.MethodPost, "http://localhost:8080"+auth.OAuthTokenPath, strings.NewReader(form.Encode()))
	r.SetBasicAuth("ci", secrets["ci"])
	w = httptest.NewRecorder()
	plain.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("unconfigured token endpoint: %d %s", w.Code, w.Body)
	}
}

func TestAdminClientsAPI(t *testing.T) {
	s := newTestServer(t, nil)
	enableClients(t, s)
	admin := adminCookie(s)
	viewer := loginAs(t, s, "observer", "viewer-password-1")

	// Creating a client returns its secret exactly once.
	w := requestAs(s, admin, http.MethodPost, "/api/auth/admin/clients", strings.NewReader(`{"id":"ci-nightly","scopes":["investigations:run"]}`))
	var created struct {
		Clients []auth.APIClient `json:"clients"`
		Scopes  []auth.Scope     `json:"scopes"`
		ID      string           `json:"client_id"`
		Secret  string           `json:"client_secret"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.ID != "ci-nightly" ||
		created.Secret == "" || len(created.Scopes) != 2 || len(created.Clients) != 1 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	// The secret authenticates and never appears in listings.
	if token := clientToken(t, s, "ci-nightly", created.Secret, ""); token == "" {
		t.Fatal("created client cannot obtain a token")
	}
	w = requestAs(s, admin, http.MethodGet, "/api/auth/admin/clients", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), created.Secret) || strings.Contains(w.Body.String(), "client_secret") {
		t.Fatalf("listing leaks secrets: %d %s", w.Code, w.Body)
	}
	for name, body := range map[string]string{
		"duplicate":     `{"id":"ci-nightly","scopes":["investigations:read"]}`,
		"bad-id":        `{"id":"Not A Slug","scopes":["investigations:read"]}`,
		"no-scopes":     `{"id":"ci-empty","scopes":[]}`,
		"unknown-scope": `{"id":"ci-god","scopes":["admin"]}`,
	} {
		if w := requestAs(s, admin, http.MethodPost, "/api/auth/admin/clients", strings.NewReader(body)); w.Code != 400 && w.Code != 409 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w = requestAs(s, admin, http.MethodPut, "/api/auth/admin/clients/ci-nightly",
		strings.NewReader(`{"scopes":["investigations:read","investigations:run"]}`)); w.Code != 200 {
		t.Fatalf("scope change: %d %s", w.Code, w.Body)
	}
	if w = requestAs(s, admin, http.MethodPut, "/api/auth/admin/clients/ghost",
		strings.NewReader(`{"disabled":true}`)); w.Code != 404 {
		t.Fatalf("missing client: %d %s", w.Code, w.Body)
	}
	if w = requestAs(s, admin, http.MethodPost, "/api/auth/admin/clients/ghost/secret", nil); w.Code != 404 {
		t.Fatalf("missing rotation: %d %s", w.Code, w.Body)
	}

	// Disabling ends the client's tokens at once.
	token := clientToken(t, s, "ci-nightly", mustClientSecret(t, s, "ci-nightly"), "")
	if w = requestAs(s, admin, http.MethodPut, "/api/auth/admin/clients/ci-nightly",
		strings.NewReader(`{"disabled":true}`)); w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if w = bearer(s, http.MethodGet, "/api/runs", token, nil); w.Code != 401 {
		t.Fatalf("disabled client still reads: %d %s", w.Code, w.Body)
	}

	// Only admins manage clients.
	if w = requestAs(s, viewer, http.MethodGet, "/api/auth/admin/clients", nil); w.Code != 403 {
		t.Fatalf("viewer clients: %d %s", w.Code, w.Body)
	}

	// Removal.
	if w = requestAs(s, admin, http.MethodDelete, "/api/auth/admin/clients/ci-nightly", nil); w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if w = requestAs(s, admin, http.MethodDelete, "/api/auth/admin/clients/ci-nightly", nil); w.Code != 404 {
		t.Fatalf("double remove: %d %s", w.Code, w.Body)
	}
}

func TestClientRouteClassification(t *testing.T) {
	cases := []struct {
		method, path string
		access       clientAccess
		run          string
	}{
		{http.MethodPost, "/api/runs", clientLaunch, ""},
		{http.MethodGet, "/api/runs", clientList, ""},
		{http.MethodDelete, "/api/runs", clientRefuse, ""},
		{http.MethodGet, "/api/runs/" + workerUUID, clientRead, workerUUID},
		{http.MethodGet, "/api/runs/" + workerUUID + "/report.json", clientRead, workerUUID},
		{http.MethodGet, "/api/runs/" + workerUUID + "/report.md", clientRead, workerUUID},
		{http.MethodPost, "/api/runs/" + workerUUID + "/cancel", clientSteer, workerUUID},
		{http.MethodPost, "/api/runs/" + workerUUID + "/pause", clientSteer, workerUUID},
		{http.MethodPost, "/api/runs/" + workerUUID + "/resume", clientSteer, workerUUID},
		{http.MethodPost, "/api/runs/" + workerUUID + "/close", clientSteer, workerUUID},
		{http.MethodPost, "/api/runs/" + workerUUID + "/ticket-note", clientRefuse, ""},
		{http.MethodGet, "/api/runs/not-a-run", clientRefuse, ""},
		{http.MethodGet, "/api/settings", clientRefuse, ""},
		{http.MethodGet, "/api/runs/" + workerUUID + "/other", clientRefuse, ""},
		{http.MethodPut, "/api/runs", clientRefuse, ""},
	}
	for _, tc := range cases {
		access, run := clientRoute(tc.method, tc.path)
		if access != tc.access || run != tc.run {
			t.Errorf("clientRoute(%s %s) = %d %q, want %d %q", tc.method, tc.path, access, run, tc.access, tc.run)
		}
	}
}
