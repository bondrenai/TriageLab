package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

// bondrenOrigin serves issue 42 for previews and refreshes.
func bondrenOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/issues/00000000-0000-4000-8000-000000000042":
			_, _ = w.Write([]byte(`{"id":"00000000-0000-4000-8000-000000000042","number":42,"title":"Timeout",` +
				`"state":"on_you","operational_state":"native","requester_id":"00000000-0000-4000-8000-000000000010",` +
				`"requester_name":"Reporter","updated_at":"2026-11-09T12:00:00Z","tags":["reproduce"]}`))
		case "/api/v1/issues/00000000-0000-4000-8000-000000000042/messages":
			_, _ = w.Write([]byte(`{"items":[],"page_info":{"has_more":false}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(origin.Close)
	return origin
}

const issueReference = "00000000-0000-4000-8000-000000000042"

func createConnection(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	w := request(s, http.MethodPost, "/api/tickets/connections", strings.NewReader(body))
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("create connection: HTTP %d %s", w.Code, w.Body)
	}
	id, _ := created["id"].(string)
	return w.Code, id
}

func TestConnectionsAreAdminOnlyAndNeverDiscloseTheStoredToken(t *testing.T) {
	s := newTestServer(t, nil)
	viewer := loginAs(t, s, "observer", "viewer-password-1")
	worker := loginAs(t, s, "worker", "user-password-1")
	if w := requestAs(s, viewer, http.MethodGet, "/api/tickets/connections", nil); w.Code != 403 {
		t.Fatalf("viewer listed connections: %d", w.Code)
	}
	if w := requestAs(s, worker, http.MethodGet, "/api/tickets/connections", nil); w.Code != 403 {
		t.Fatalf("user listed connections: %d", w.Code)
	}
	if w := requestAs(s, worker, http.MethodPost, "/api/tickets/connections",
		strings.NewReader(`{"config":{"source":"jira"}}`)); w.Code != 403 {
		t.Fatalf("user created a connection: %d %s", w.Code, w.Body)
	}
	const token = "fixture-jira-api-token"
	code, id := createConnection(t, s, `{"label":"Support desk","token":"`+token+`","config":{`+
		`"source":"jira","origin":"https://test-site.atlassian.net","email":"triagelab@example.com",`+
		`"policy":{"tag":"reproduce"}}}`)
	if code != 201 || id == "" {
		t.Fatalf("create connection: HTTP %d %s", code, id)
	}
	w := request(s, http.MethodGet, "/api/tickets/connections", nil)
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("connection list disclosed the stored credential")
	}
	var list struct {
		Connections []struct {
			ID         string `json:"id"`
			Label      string `json:"label"`
			Source     string `json:"source"`
			Account    string `json:"account"`
			Running    bool   `json:"running"`
			Connected  bool   `json:"connected"`
			Credential struct {
				Mode string `json:"mode"`
			} `json:"credential"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Connections) != 1 {
		t.Fatalf("connection list: %s", w.Body)
	}
	connection := list.Connections[0]
	if connection.ID != id || connection.Label != "Support desk" || connection.Source != "jira" ||
		connection.Account != "https://test-site.atlassian.net" || !connection.Running || !connection.Connected ||
		connection.Credential.Mode != "stored" {
		t.Fatalf("wrong connection status: %+v", connection)
	}
}

func TestCreateRejectsInvalidInputWithoutEchoingTheCredential(t *testing.T) {
	s := newTestServer(t, nil)
	w := request(s, http.MethodPost, "/api/tickets/connections", strings.NewReader(
		`{"token":"not a valid token","config":{"source":"jira","origin":"https://test-site.atlassian.net",`+
			`"email":"triagelab@example.com","policy":{"tag":"reproduce"}}}`))
	if w.Code != 400 {
		t.Fatalf("invalid credential accepted: HTTP %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "not a valid token") {
		t.Fatal("create error disclosed the rejected credential")
	}
	w = request(s, http.MethodPost, "/api/tickets/connections", strings.NewReader(`{"config":{"source":"github"}}`))
	if w.Code != 400 {
		t.Fatalf("invalid configuration accepted: HTTP %d %s", w.Code, w.Body)
	}
}

func TestTwoConnectionsTrackTheSameCaseNumberIndependently(t *testing.T) {
	s := newTestServer(t, nil)
	first := bondrenOrigin(t)
	second := bondrenOrigin(t)
	_, firstID := createConnection(t, s, `{"token":"first-bondren-token","config":{"source":"bondren",`+
		`"origin":"`+first.URL+`","policy":{"tag":"reproduce"}}}`)
	_, secondID := createConnection(t, s, `{"token":"second-bondren-token","config":{"source":"bondren",`+
		`"origin":"`+second.URL+`","policy":{"tag":"reproduce"}}}`)
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("connection ids: %q %q", firstID, secondID)
	}
	var previews []ticketflow.Case
	for _, id := range []string{firstID, secondID} {
		w := request(s, http.MethodPost, "/api/tickets/c/"+id+"/"+issueReference+"/preview", nil)
		var c ticketflow.Case
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || w.Code != 200 || c.TicketID != 42 {
			t.Fatalf("preview through %s: HTTP %d %s", id, w.Code, w.Body)
		}
		previews = append(previews, c)
	}
	if previews[0].Account != first.URL || previews[1].Account != second.URL {
		t.Fatalf("case keys collided across connections: %+v %+v", previews[0], previews[1])
	}
	if first.URL == second.URL {
		t.Fatal("fixtures must use distinct origins")
	}
	// Each connection keeps its own state directory.
	for _, id := range []string{firstID, secondID} {
		if info, err := os.Stat(filepath.Join(s.directory, ".tickets", id)); err != nil || !info.IsDir() {
			t.Fatalf("state directory for %s: %v", id, err)
		}
	}
}

func TestUpdateConnectionResetsStateOnlyWithConfirmation(t *testing.T) {
	s := newTestServer(t, nil)
	origin := bondrenOrigin(t)
	_, id := createConnection(t, s, `{"token":"bondren-token","config":{"source":"bondren",`+
		`"origin":"`+origin.URL+`","policy":{"tag":"reproduce"}}}`)
	if _, err := os.Stat(filepath.Join(s.directory, ".tickets", id)); err != nil {
		t.Fatalf("state directory: %v", err)
	}
	replacement := bondrenOrigin(t)
	update := `{"config":{"source":"bondren","origin":"` + replacement.URL + `","policy":{"tag":"reproduce"}}}`
	w := request(s, http.MethodPut, "/api/tickets/connections/"+id, strings.NewReader(update))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "Confirm the reset") {
		t.Fatalf("account change without confirmation: HTTP %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(s.directory, ".tickets", id)); err != nil {
		t.Fatalf("unconfirmed change already reset state: %v", err)
	}
	update = `{"confirm_reset":true,"config":{"source":"bondren","origin":"` + replacement.URL + `","policy":{"tag":"reproduce"}}}`
	w = request(s, http.MethodPut, "/api/tickets/connections/"+id, strings.NewReader(update))
	if w.Code != 200 {
		t.Fatalf("confirmed reset rejected: HTTP %d %s", w.Code, w.Body)
	}
	workspace := request(s, http.MethodGet, "/api/tickets", nil)
	if !strings.Contains(workspace.Body.String(), replacement.URL) {
		t.Fatalf("workspace did not pick up the new account: %s", workspace.Body)
	}
	// The replaced flow's poller and lock are released; the new one serves.
	w = request(s, http.MethodPost, "/api/tickets/c/"+id+"/"+issueReference+"/preview", nil)
	if w.Code != 200 {
		t.Fatalf("preview after reset: HTTP %d %s", w.Code, w.Body)
	}
	// Changing the source type outright is refused.
	w = request(s, http.MethodPut, "/api/tickets/connections/"+id, strings.NewReader(
		`{"config":{"source":"github","repository":"owner/repo","policy":{"tag":"reproduce"}}}`))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "cannot change") {
		t.Fatalf("source change accepted: HTTP %d %s", w.Code, w.Body)
	}
}

func TestUpdateConnectionKeepsStoredTokenWhenOmitted(t *testing.T) {
	s := newTestServer(t, nil)
	origin := bondrenOrigin(t)
	_, id := createConnection(t, s, `{"token":"bondren-token","config":{"source":"bondren",`+
		`"origin":"`+origin.URL+`","policy":{"tag":"reproduce"},"poll_seconds":60}}`)
	w := request(s, http.MethodPut, "/api/tickets/connections/"+id, strings.NewReader(
		`{"config":{"source":"bondren","origin":"`+origin.URL+`","policy":{"tag":"reproduce"},"poll_seconds":120}}`))
	if w.Code != 200 {
		t.Fatalf("token-keeping update rejected: HTTP %d %s", w.Code, w.Body)
	}
	w = request(s, http.MethodPost, "/api/tickets/c/"+id+"/"+issueReference+"/preview", nil)
	if w.Code != 200 {
		t.Fatalf("preview with retained credential: HTTP %d %s", w.Code, w.Body)
	}
	// Clearing the token without a replacement environment leaves the record
	// without a credential and is rejected before anything changes.
	w = request(s, http.MethodPut, "/api/tickets/connections/"+id, strings.NewReader(
		`{"token":"","config":{"source":"bondren","origin":"`+origin.URL+`","policy":{"tag":"reproduce"}}}`))
	if w.Code != 400 {
		t.Fatalf("credential removal accepted: HTTP %d %s", w.Code, w.Body)
	}
	w = request(s, http.MethodPost, "/api/tickets/c/"+id+"/"+issueReference+"/preview", nil)
	if w.Code != 200 {
		t.Fatalf("connection broke after a rejected update: HTTP %d %s", w.Code, w.Body)
	}
}

func TestDeleteConnectionRemovesRegistryAndState(t *testing.T) {
	s := newTestServer(t, nil)
	origin := bondrenOrigin(t)
	_, id := createConnection(t, s, `{"token":"bondren-token","config":{"source":"bondren",`+
		`"origin":"`+origin.URL+`","policy":{"tag":"reproduce"}}}`)
	w := request(s, http.MethodDelete, "/api/tickets/connections/"+id, nil)
	if w.Code != 204 {
		t.Fatalf("delete connection: HTTP %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(s.directory, ".tickets", id)); !os.IsNotExist(err) {
		t.Fatalf("state directory survived deletion: %v", err)
	}
	w = request(s, http.MethodGet, "/api/tickets", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("workspace after deletion: %s", w.Body)
	}
	if w := request(s, http.MethodDelete, "/api/tickets/connections/"+id, nil); w.Code != 404 {
		t.Fatalf("second delete: HTTP %d", w.Code)
	}
}

func TestEnableTicketConnectionsBootstrapsOnlyAnEmptyRegistry(t *testing.T) {
	s := newTestServer(t, nil)
	first := ticketflow.Config{Source: "github", Repository: "owner/first", Policy: ticketflow.Policy{Tag: "reproduce"}}
	opened, seeded, err := s.EnableTicketConnections([]ticketflow.Config{first})
	if err != nil || opened != 1 || !seeded {
		t.Fatalf("bootstrap: opened=%d seeded=%v err=%v", opened, seeded, err)
	}
	// A restart closes the old flows before the registry is read again.
	for _, conn := range s.ticketConnections() {
		_ = conn.flow.Close()
	}
	s.ticketMu.Lock()
	s.ticketConns = nil
	s.ticketMu.Unlock()
	replacement := ticketflow.Config{Source: "github", Repository: "owner/second", Policy: ticketflow.Policy{Tag: "reproduce"}}
	opened, seeded, err = s.EnableTicketConnections([]ticketflow.Config{replacement})
	if err != nil || opened != 1 || seeded {
		t.Fatalf("second start reseeded the registry: opened=%d seeded=%v err=%v", opened, seeded, err)
	}
	workspace := request(s, http.MethodGet, "/api/tickets", nil)
	if !strings.Contains(workspace.Body.String(), "owner/first") || strings.Contains(workspace.Body.String(), "owner/second") {
		t.Fatalf("registry is not canonical after seeding: %s", workspace.Body)
	}
}

func TestZendeskOAuthCookieRoutesToItsOwnConnection(t *testing.T) {
	s := newTestServer(t, nil)
	seedTickets(t, s, "0a1b2c3d", ticketflow.Config{Subdomain: "example", ClientID: "triagelab-local",
		RedirectURI: "http://127.0.0.1:8080" + ticketflow.OAuthCallbackPath, Policy: ticketflow.Policy{Tag: "reproduce"}})
	seedTickets(t, s, "0a1b2c3e", ticketflow.Config{Subdomain: "other", ClientID: "triagelab-local",
		RedirectURI: "http://127.0.0.1:8080" + ticketflow.OAuthCallbackPath, Policy: ticketflow.Policy{Tag: "support"}})
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/tickets/c/0a1b2c3e/oauth/start", nil)
	r.Header.Set("X-TriageLab-Request", "1")
	r.AddCookie(adminCookie(s))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("authorize second connection: %d %s", w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !strings.HasPrefix(cookies[0].Value, "0a1b2c3e.") {
		t.Fatalf("cookie does not carry its connection id: %+v", cookies)
	}
	var body map[string]string
	if json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal("authorize response")
	}
	state := strings.Split(body["url"], "state=")[1]
	callback := ticketflow.OAuthCallbackPath + "?state=" + state + "&error=access_denied"
	r = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+callback, nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/tickets?zendesk=denied" {
		t.Fatalf("callback for the second connection: %d %s", w.Code, w.Body)
	}
	// A callback carrying the first connection's cookie targets only that one.
	r = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+callback, nil)
	r.AddCookie(&http.Cookie{Name: zendeskBrowserCookie, Value: "0a1b2c3d." + strings.SplitN(cookies[0].Value, ".", 2)[1], Path: ticketflow.OAuthCallbackPath})
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("state from another connection accepted: HTTP %d", w.Code)
	}
}

func TestUnsavedConnectionTestRoute(t *testing.T) {
	s := newTestServer(t, nil)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/api/v4/projects/acme%2Ftooling" {
			if r.Header.Get("PRIVATE-TOKEN") != "good-gl-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"id":1,"path_with_namespace":"acme/tooling"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(origin.Close)

	worker := loginAs(t, s, "worker", "user-password-1")
	if w := requestAs(s, worker, http.MethodPost, "/api/tickets/connections/test", strings.NewReader(`{}`)); w.Code != 403 {
		t.Fatalf("non-admin tested connection: %d", w.Code)
	}
	if w := request(s, http.MethodGet, "/api/tickets/connections/test", nil); w.Code != 405 {
		t.Fatalf("GET test route: %d", w.Code)
	}
	w := request(s, http.MethodPost, "/api/tickets/connections/test", strings.NewReader(
		`{"token":"good-gl-token","config":{"source":"gitlab","repository":"acme/tooling","origin":"`+origin.URL+`","policy":{"tag":"reproduce"}}}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("unsaved connection test failed: HTTP %d %s", w.Code, w.Body)
	}
	// A rejected provider credential produces a failed test result.
	w = request(s, http.MethodPost, "/api/tickets/connections/test", strings.NewReader(
		`{"token":"bad-secret-token-value","config":{"source":"gitlab","repository":"acme/tooling","origin":"`+origin.URL+`","policy":{"tag":"reproduce"}}}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatalf("unsaved connection test failure: HTTP %d %s", w.Code, w.Body)
	}

	// Use a fixture credential that also occurs in the validation message so
	// the test exercises redaction independently of provider error formatting.
	const secret = "GitLab"
	t.Setenv("TRIAGELAB_PROBE_TOKEN", secret)
	for _, credential := range []struct{ token, config string }{
		{token: `"token":"` + secret + `",`},
		{config: `"token_env":"TRIAGELAB_PROBE_TOKEN",`},
	} {
		w := request(s, http.MethodPost, "/api/tickets/connections/test", strings.NewReader(`{`+credential.token+
			`"config":{`+credential.config+`"source":"gitlab","policy":{"tag":"reproduce"}}}`))
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "[REDACTED]") || strings.Contains(body, secret) {
			t.Fatalf("unsaved connection test did not redact its credential: HTTP %d %s", w.Code, body)
		}
	}
}

func TestUpdateConnectionClearsStoredTokenWhenSwitchingMode(t *testing.T) {
	s := newTestServer(t, nil)
	const secret = "stored-github-secret-value"
	code, id := createConnection(t, s, `{"label":"Repo","token":"`+secret+`","config":{`+
		`"source":"github","repository":"owner/repo","policy":{"tag":"reproduce"}}}`)
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	// Switching to an environment variable clears the stored token even if token is omitted.
	t.Setenv("TRIAGELAB_SWITCHED_GH_TOKEN", "env-token-value")
	w := request(s, http.MethodPut, "/api/tickets/connections/"+id, strings.NewReader(
		`{"label":"Repo","config":{"source":"github","repository":"owner/repo","token_env":"TRIAGELAB_SWITCHED_GH_TOKEN","policy":{"tag":"reproduce"}}}`))
	if w.Code != 200 {
		t.Fatalf("switch to env: %d %s", w.Code, w.Body)
	}
	records, err := ticketflow.LoadConnections(s.ticketRoot())
	if err != nil || len(records) != 1 || records[0].Token != "" || records[0].CredentialMode() != "env" {
		t.Fatalf("stored token survived switch to env: %+v err=%v", records, err)
	}
	// Re-store a token, then explicitly clear it to anonymous read.
	w = request(s, http.MethodPut, "/api/tickets/connections/"+id, strings.NewReader(
		`{"label":"Repo","token":"`+secret+`","config":{"source":"github","repository":"owner/repo","policy":{"tag":"reproduce"}}}`))
	if w.Code != 200 {
		t.Fatalf("re-store token: %d %s", w.Code, w.Body)
	}
	w = request(s, http.MethodPut, "/api/tickets/connections/"+id, strings.NewReader(
		`{"label":"Repo","token":"","config":{"source":"github","repository":"owner/repo","policy":{"tag":"reproduce"}}}`))
	if w.Code != 200 {
		t.Fatalf("switch to anonymous: %d %s", w.Code, w.Body)
	}
	records, err = ticketflow.LoadConnections(s.ticketRoot())
	if err != nil || len(records) != 1 || records[0].Token != "" || records[0].CredentialMode() != "none" {
		t.Fatalf("stored token survived switch to anonymous: %+v err=%v", records, err)
	}
}
