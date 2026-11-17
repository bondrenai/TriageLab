package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/auth"
)

func TestLiveUserStoreReloadAffectsActiveSessions(t *testing.T) {
	s := newTestServer(t, nil)
	workerCookie := loginAs(t, s, "worker", "user-password-1")

	cliStore, err := auth.OpenStore(s.auth.users.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Demote worker via separate store handle (simulating `triagelab user set-role`).
	if err := cliStore.SetRole("worker", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if w := requestAs(s, workerCookie, http.MethodPost, "/api/runs/"+workerUUID+"/cancel", nil); w.Code != http.StatusForbidden {
		t.Fatalf("demoted active session was not restricted to viewer: %d %s", w.Code, w.Body)
	}
	w := requestAs(s, workerCookie, http.MethodGet, "/api/auth/session", nil)
	var session sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil || !session.Authenticated || session.Role != string(auth.RoleViewer) {
		t.Fatalf("session info did not reflect live role update: %d %s (%v)", w.Code, w.Body, err)
	}

	// Disable worker via CLI store -> active cookie is revoked immediately.
	if err := cliStore.SetDisabled("worker", true); err != nil {
		t.Fatal(err)
	}
	if w := requestAs(s, workerCookie, http.MethodGet, "/api/settings", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user session survived: %d %s", w.Code, w.Body)
	}
	w = requestAs(s, workerCookie, http.MethodGet, "/api/auth/session", nil)
	session = sessionResponse{}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil || session.Authenticated {
		t.Fatalf("session info should report unauthenticated after disable: %d %s", w.Code, w.Body)
	}

	// New account added via CLI store can log in immediately without server restart.
	if err := cliStore.Create("cli.user", "cli-password-1", auth.RoleUser); err != nil {
		t.Fatal(err)
	}
	if cliCookie := loginAs(t, s, "cli.user", "cli-password-1"); cliCookie == nil {
		t.Fatal("newly created CLI user could not sign in")
	}
}

func TestAuditLogRecordsRedactedActionsAndRequiresAdmin(t *testing.T) {
	s := newTestServer(t, testBackend{call: func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, context.Canceled
	}})
	admin := loginAs(t, s, "admin", "admin-password-1")
	viewer := loginAs(t, s, "observer", "viewer-password-1")
	worker := loginAs(t, s, "worker", "user-password-1")

	// Failed login attempt + user creation + run upload (including a secret value
	// in description to ensure case text and secrets stay out of the audit log).
	_ = requestAs(s, nil, http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"observer","password":"wrong-password"}`))
	if w := requestAs(s, admin, http.MethodPost, "/api/auth/admin/users",
		strings.NewReader(`{"username":"auditee","password":"password-123","role":"user"}`)); w.Code != http.StatusOK {
		t.Fatalf("create user: %d %s", w.Code, w.Body)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("description", "Secret prose with server-test-secret should never appear in audit.jsonl")
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/runs", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-TriageLab-Request", "1")
	req.AddCookie(worker)
	rw := httptest.NewRecorder()
	s.ServeHTTP(rw, req)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("start run: %d %s", rw.Code, rw.Body)
	}

	// Viewer, user, and non-GET requests cannot access or alter the audit log.
	if w := requestAs(s, viewer, http.MethodGet, "/api/auth/admin/audit", nil); w.Code != http.StatusForbidden {
		t.Fatalf("viewer audit access: %d", w.Code)
	}
	if w := requestAs(s, worker, http.MethodGet, "/api/auth/admin/audit", nil); w.Code != http.StatusForbidden {
		t.Fatalf("user audit access: %d", w.Code)
	}
	if w := requestAs(s, admin, http.MethodPost, "/api/auth/admin/audit", strings.NewReader(`{}`)); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("audit POST should be 405: %d", w.Code)
	}
	if w := requestAs(s, admin, http.MethodGet, "/api/auth/admin/audit?limit=0", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit should be 400: %d", w.Code)
	}

	w := requestAs(s, admin, http.MethodGet, "/api/auth/admin/audit?limit=10", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("admin audit read: %d %s", w.Code, w.Body)
	}
	var payload struct {
		Entries []auditEntry `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Entries) < 5 {
		t.Fatalf("expected >=5 audit entries, got %+v", payload.Entries)
	}
	// Verify actions exist and are returned newest-first.
	seen := map[string]bool{}
	for _, entry := range payload.Entries {
		seen[entry.Action+":"+entry.Outcome] = true
	}
	for _, want := range []string{"auth.login:ok", "auth.login:denied", "admin.user.create:ok", "run.start:accepted"} {
		if !seen[want] {
			t.Fatalf("missing audit entry %q in %+v", want, payload.Entries)
		}
	}

	raw, err := os.ReadFile(s.auditPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"server-test-secret", "admin-password-1", "password-123", "Secret prose"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("audit log leaked sensitive content %q", secret)
		}
	}
	info, err := os.Stat(s.auditPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 permissions on audit log, got %v %v", info.Mode().Perm(), err)
	}
	// Wait for the dummy background run to finish cleanly before TempDir cleanup.
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		<-active.done
	}
}

func TestAuditLogRefusesSymlinksAndRedactsDynamicSecrets(t *testing.T) {
	s := newTestServer(t, nil)
	s.addSecret("later-added-token-value")
	s.recordAudit(auth.Identity{Username: "admin", Via: "password"}, "admin.test", "secret=later-added-token-value", "ok", "later-added-token-value")
	entries, err := s.readAuditEntries(10)
	if err != nil || len(entries) != 2 || entries[0].Action != "admin.test" {
		t.Fatalf("read audit: %+v %v", entries, err)
	}
	if strings.Contains(entries[0].Target, "later-added-token-value") || strings.Contains(entries[0].Detail, "later-added-token-value") {
		t.Fatalf("audit entry was not redacted: %+v", entries[0])
	}

	// Replace audit.jsonl with a symlink and verify read/write refuse to follow it.
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(target, []byte(`{"at":"2026-01-01T00:00:00Z","actor":"x","action":"y","outcome":"ok"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(s.auditPath())
	if err := os.Symlink(target, s.auditPath()); err != nil {
		t.Fatal(err)
	}
	s.recordAudit(auth.Identity{Username: "admin", Via: "password"}, "should.not.write", "", "ok", "")
	after, _ := os.ReadFile(target)
	if strings.Contains(string(after), "should.not.write") {
		t.Fatal("recordAudit followed a symlink")
	}
	if _, err := s.readAuditEntries(10); err == nil {
		t.Fatal("readAuditEntries followed a symlink")
	}
}

func TestUnreadableUserStoreKeepsSessionsAndFailsClosed(t *testing.T) {
	s := newTestServer(t, nil)
	workerCookie := loginAs(t, s, "worker", "user-password-1")
	path := s.auth.users.Path()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// An operator mid-edit leaves the file unparseable for a moment.
	if err := os.WriteFile(path, []byte("{\"users\": ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := requestAs(s, workerCookie, http.MethodGet, "/api/settings", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable store did not fail closed: %d %s", w.Code, w.Body)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if w := requestAs(s, workerCookie, http.MethodGet, "/api/settings", nil); w.Code != http.StatusOK {
		t.Fatalf("session was ended by a transient store error: %d %s", w.Code, w.Body)
	}
}

func TestAuditLogOmitsUnknownLoginNamesAndRawErrors(t *testing.T) {
	s := newTestServer(t, nil)
	pasted := "Sup3r-secret-pass"
	body := `{"username":"` + pasted + `","password":"x"}`
	if w := requestAs(s, nil, http.MethodPost, "/api/auth/login", strings.NewReader(body)); w.Code != http.StatusUnauthorized {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	if w := requestAs(s, nil, http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"worker","password":"wrong-password"}`)); w.Code != http.StatusUnauthorized {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	raw, err := os.ReadFile(s.auditPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pasted) {
		t.Fatalf("audit log kept an unknown login name: %s", raw)
	}
	if !strings.Contains(string(raw), `"target":"unknown-account"`) || !strings.Contains(string(raw), `"target":"worker"`) {
		t.Fatalf("audit log lost failed-login targets: %s", raw)
	}
	if got := auditError(fmt.Errorf("zendesk said: %s", "Ticket title: payroll outage")); got != "failed" {
		t.Fatalf("raw error text reached the audit log: %q", got)
	}
}

func TestAuditLogRotatesAndSkipsOversizedLines(t *testing.T) {
	s := newTestServer(t, nil)
	path := s.auditPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// An oversized line in the middle must not hide newer entries.
	s.recordAudit(auth.Identity{Username: "admin"}, "test.old", "a", "ok", "")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"action":"` + strings.Repeat("x", 64<<10) + `"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s.recordAudit(auth.Identity{Username: "admin"}, "test.new", "b", "ok", "")
	entries, err := s.readAuditEntries(10)
	if err != nil {
		t.Fatal(err)
	}
	actions := make([]string, len(entries))
	for i, entry := range entries {
		actions[i] = entry.Action
	}
	if len(entries) < 3 || actions[0] != "test.new" || actions[2] != "test.old" || len(actions[1]) > maxAuditFieldRunes {
		t.Fatalf("entries around an oversized line: %+v", entries)
	}

	// Past the size cap the log rotates to one previous generation, and
	// reads still return entries from both.
	if err := os.Truncate(path, maxAuditFileBytes); err != nil {
		t.Fatal(err)
	}
	s.recordAudit(auth.Identity{Username: "admin"}, "test.rotated", "c", "ok", "")
	if info, err := os.Stat(path); err != nil || info.Size() > 4096 {
		t.Fatalf("audit log did not rotate: %v %v", info, err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("previous generation missing: %v", err)
	}
	entries, err = s.readAuditEntries(10)
	if err != nil || len(entries) == 0 || entries[0].Action != "test.rotated" {
		t.Fatalf("entries after rotation: %+v %v", entries, err)
	}
}
