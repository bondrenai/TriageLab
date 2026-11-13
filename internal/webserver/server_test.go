package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/auth"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

// sessionCookies caches one admin session per server so request() stays
// authenticated; tests needing other roles call requestAs directly.
var sessionCookies sync.Map

type testBackend struct {
	call func(context.Context, string, map[string]any) (map[string]any, error)
}

func (b testBackend) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	return b.call(ctx, name, args)
}
func (b testBackend) TaskItems(context.Context, string, string) ([]map[string]any, error) {
	return nil, fmt.Errorf("unexpected task read")
}
func (b testBackend) CancelTask(context.Context, string, string) error {
	return fmt.Errorf("unexpected task cancellation")
}
func (testBackend) SandboxRetention(context.Context) (bool, error) {
	return true, nil
}

func newTestServer(t *testing.T, backend investigation.Backend) *Server {
	t.Helper()
	base := t.TempDir()
	assets := filepath.Join(base, "assets")
	if err := os.Mkdir(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("<html>workspace</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if backend == nil {
		backend = testBackend{call: func(context.Context, string, map[string]any) (map[string]any, error) {
			t.Error("unexpected upstream call")
			return nil, fmt.Errorf("unexpected upstream")
		}}
	}
	s, err := New(context.Background(), investigation.DefaultConfig(), filepath.Join(base, "reports"), assets, backend, "server-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	sessionCookies.Store(s, addAuth(t, s))
	t.Cleanup(func() { sessionCookies.Delete(s) })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s
}

// addAuth enables sign-in with the three role accounts used across tests and
// returns the admin session cookie.
func addAuth(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	users, err := auth.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	for account, password := range map[string]struct {
		password string
		role     auth.Role
	}{
		"admin":    {"admin-password-1", auth.RoleAdmin},
		"observer": {"viewer-password-1", auth.RoleViewer},
		"worker":   {"user-password-1", auth.RoleUser},
	} {
		if err := users.Create(account, password.password, password.role); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnableAuth(users, nil); err != nil {
		t.Fatal(err)
	}
	return loginAs(t, s, "admin", "admin-password-1")
}

// seedTickets saves one ticket connection with a fixed id and opens it, so
// tests can address its routes and state directory deterministically.
func seedTickets(t *testing.T, s *Server, id string, config ticketflow.Config) {
	t.Helper()
	if err := ticketflow.SaveConnections(s.ticketRoot(), []ticketflow.Connection{{ID: id, Config: config}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnableTicketConnections(nil); err != nil {
		t.Fatal(err)
	}
}

// loginAs signs in a test account and returns the session cookie.
func loginAs(t *testing.T, s *Server, username, password string) *http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"username":"` + username + `","password":"` + password + `"}`)
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/auth/login", body)
	r.Header.Set("X-TriageLab-Request", "1")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login %s: %d %s", username, w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.SessionCookie {
		t.Fatalf("login %s: unexpected cookies %v", username, cookies)
	}
	return cookies[0]
}

func request(s *Server, method, path string, body io.Reader) *httptest.ResponseRecorder {
	return requestAs(s, adminCookie(s), method, path, body)
}

// requestAs performs a request for a specific session cookie; cookie may be
// nil for unauthenticated requests.
func requestAs(s *Server, cookie *http.Cookie, method, path string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8080"+path, body)
	r.Header.Set("X-TriageLab-Request", "1")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func adminCookie(s *Server) *http.Cookie {
	if cookie, ok := sessionCookies.Load(s); ok {
		return cookie.(*http.Cookie)
	}
	return nil
}

type upload struct{ name, content string }

func formRequest(t *testing.T, s *Server, description string, files ...upload) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("description", description); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		part, err := form.CreateFormFile("files", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(part, file.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/runs", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("X-TriageLab-Request", "1")
	if cookie := adminCookie(s); cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func TestHTTPRejectsForeignHostsOriginsAndUnmarkedMutations(t *testing.T) {
	s := newTestServer(t, nil)
	for _, test := range []struct{ name, host, origin, site, header, method string }{
		{"host", "evil.example", "", "", "1", "GET"},
		{"origin", "localhost:8080", "https://evil.example", "", "1", "GET"},
		{"other-port", "localhost:8080", "http://localhost:3000", "", "1", "POST"},
		{"cross-site", "localhost:8080", "", "cross-site", "1", "GET"},
		{"same-site", "localhost:8080", "", "same-site", "1", "POST"},
		{"header", "localhost:8080", "", "", "", "POST"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, "http://localhost:8080/api/runs", nil)
			r.Host = test.host
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Sec-Fetch-Site", test.site)
			r.Header.Set("X-TriageLab-Request", test.header)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
		})
	}
	w := request(s, "GET", "/api/settings", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "server-test-secret") {
		t.Fatalf("settings: %d %s", w.Code, w.Body)
	}
	for _, directive := range []string{"script-src 'self';", "worker-src 'self';", "style-src 'self' 'unsafe-inline';"} {
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), directive) {
			t.Fatalf("missing editor CSP directive %q", directive)
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing frame isolation")
	}
}

func TestUploadKeepsRelativePathsAndUsesExistingInputValidation(t *testing.T) {
	s := newTestServer(t, nil)
	c, err := s.ingestUpload(httptest.NewRecorder(), formRequest(t, s, "An issue", upload{"nested/app.py", "print('ok')"}, upload{"config.json", "{}"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Attachments) != 2 || c.Attachments[1].Path != "nested/app.py" || string(c.Attachments[1].Content) != "print('ok')" {
		t.Fatalf("wrong attachments: %#v", c.Attachments)
	}
	if c.Source["kind"] != "upload" || len(c.Environment) != 0 {
		t.Fatalf("temporary path leaked: %#v", c)
	}
	for _, files := range [][]upload{
		{{"../outside", "x"}}, {{"nested/../outside", "x"}}, {{"/absolute", "x"}}, {{`nested\file`, "x"}},
		{{"same.py", "x"}, {"same.py", "y"}}, {{".env", "PRIVATE=true"}}, {{"nested/.ssh/id_rsa", "x"}},
		{{"key.txt", "-----BEGIN PRIVATE KEY-----\nprivate"}}, {{"node_modules/package/index.js", "x"}},
		{{"large.txt", strings.Repeat("x", casefile.MaxFileBytes+1)}},
	} {
		t.Run(files[0].name, func(t *testing.T) {
			if _, err := s.ingestUpload(httptest.NewRecorder(), formRequest(t, s, "An issue", files...)); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	if _, err := s.ingestUpload(httptest.NewRecorder(), formRequest(t, s, "")); err == nil {
		t.Fatal("empty description accepted")
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("upload staging remains: %v", entries)
	}
}

func TestRunSerializesWorkCancelsAndPersistsHistory(t *testing.T) {
	started := make(chan struct{})
	backend := testBackend{call: func(ctx context.Context, name string, _ map[string]any) (map[string]any, error) {
		if name != "create_sandbox" {
			return nil, fmt.Errorf("unexpected tool %s", name)
		}
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	s := newTestServer(t, backend)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, formRequest(t, s, "The fixture fails", upload{"app.py", "raise Exception('fixture')"}))
	if w.Code != 202 {
		t.Fatalf("start %d: %s", w.Code, w.Body)
	}
	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := response["id"]
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("investigation did not start")
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, formRequest(t, s, "Second issue"))
	if w.Code != 409 {
		t.Fatalf("parallel run accepted: %d", w.Code)
	}
	w = request(s, "GET", "/api/runs/"+id, nil)
	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["active"] != true || len(detail["progress"].([]any)) == 0 {
		t.Fatalf("missing live state: %s", w.Body)
	}
	s.mu.Lock()
	done := s.active.done
	s.mu.Unlock()
	w = request(s, "POST", "/api/runs/"+id+"/cancel", nil)
	if w.Code != 202 {
		t.Fatalf("cancel %d: %s", w.Code, w.Body)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("investigation did not cancel")
	}
	w = request(s, "GET", "/api/runs/"+id, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["active"] != false || detail["status"] != "interrupted" || detail["finished_at"] == nil {
		t.Fatalf("bad final state: %s", w.Body)
	}
	w = request(s, "GET", "/api/runs", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("missing history: %s", w.Body)
	}
	for _, name := range []string{"report.json", "report.md"} {
		w = request(s, "GET", "/api/runs/"+id+"/"+name, nil)
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatalf("bad download %d %s", w.Code, w.Body)
		}
		info, err := os.Stat(filepath.Join(s.directory, id, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("report mode %o", info.Mode().Perm())
		}
	}
	// Reports are discovered from disk rather than an in-memory list.
	reopened, err := New(context.Background(), s.config, s.directory, s.assets.Name(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	sessionCookies.Store(reopened, addAuth(t, reopened))
	t.Cleanup(func() { sessionCookies.Delete(reopened) })
	w = request(reopened, "GET", "/api/runs", nil)
	if !strings.Contains(w.Body.String(), id) {
		t.Fatalf("history missing after restart: %s", w.Body)
	}
}

func TestShutdownWaitsForInvestigationCancellation(t *testing.T) {
	started := make(chan struct{})
	backend := testBackend{call: func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	s := newTestServer(t, backend)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, formRequest(t, s, "Cancel during shutdown"))
	if w.Code != 202 {
		t.Fatalf("start %d", w.Code)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	s.mu.Lock()
	id := s.activeID
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(s.directory, id, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(`"status": "interrupted"`)) {
		t.Fatalf("report did not finish before close: %s", content)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, formRequest(t, s, "No more work"))
	if w.Code != 503 {
		t.Fatalf("accepted after shutdown: %d", w.Code)
	}
}

func TestReportsRejectSymlinksAndRedactOnRead(t *testing.T) {
	s := newTestServer(t, nil)
	id := "11111111-1111-4111-8111-111111111111"
	saved := map[string]any{"run_id": id, "case": map[string]any{"description": "server-test-secret"}, "status": "completed", "outcome": "inconclusive", "started_at": "2026-01-01T00:00:00Z", "large_integer": json.Number("9007199254740993")}
	if err := report.Write(filepath.Join(s.directory, id), saved); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/runs/"+id, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "server-test-secret") || !strings.Contains(w.Body.String(), "REDACTED") {
		t.Fatalf("read redaction failed: %d %s", w.Code, w.Body)
	}
	for _, download := range []struct{ name, contentType string }{{"report.json", "application/json"}, {"report.md", "text/markdown; charset=utf-8"}} {
		w = request(s, "GET", "/api/runs/"+id+"/"+download.name, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "server-test-secret") || !strings.Contains(w.Body.String(), "REDACTED") {
			t.Fatalf("%s download redaction failed: %d %s", download.name, w.Code, w.Body)
		}
		if got := w.Header().Get("Content-Type"); got != download.contentType {
			t.Fatalf("%s content type = %q", download.name, got)
		}
		if download.name == "report.json" {
			decoder := json.NewDecoder(w.Body)
			decoder.UseNumber()
			var canonical map[string]any
			if err := decoder.Decode(&canonical); err != nil {
				t.Fatal(err)
			}
			if canonical["large_integer"] != json.Number("9007199254740993") {
				t.Fatalf("canonical number changed: %v", canonical["large_integer"])
			}
		}
	}
	outside := t.TempDir()
	otherID := "22222222-2222-4222-8222-222222222222"
	if err := os.Symlink(outside, filepath.Join(s.directory, otherID)); err != nil {
		t.Fatal(err)
	}
	w = request(s, "GET", "/api/runs/"+otherID, nil)
	if w.Code != 404 {
		t.Fatal("report directory symlink accepted")
	}
	if err := os.Remove(filepath.Join(s.directory, id, "report.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "report.json"), []byte(`{"private":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "report.json"), filepath.Join(s.directory, id, "report.json")); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/report.json"} {
		w = request(s, "GET", "/api/runs/"+id+suffix, nil)
		if w.Code != 404 {
			t.Fatalf("report symlink accepted: %d", w.Code)
		}
	}
}

func TestStaticRoutesAndLoopbackBinding(t *testing.T) {
	s := newTestServer(t, nil)
	for _, route := range []string{"/", "/runs/11111111-1111-4111-8111-111111111111"} {
		w := request(s, "GET", route, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "workspace") {
			t.Fatalf("bad SPA route: %d %s", w.Code, w.Body)
		}
	}
	for _, route := range []string{"/assets/missing.js", "/../outside", "/api/missing"} {
		if w := request(s, "GET", route, nil); w.Code != 404 {
			t.Fatalf("bad missing path %s: %d", route, w.Code)
		}
	}
	if err := Serve(context.Background(), "0.0.0.0:0", s, io.Discard); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("public binding accepted: %v", err)
	}
}
