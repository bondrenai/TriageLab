package webserver

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

const jiraConnection = "31a4c0de"

func jiraIssueBody(key string) string {
	return `{"id":"10042","key":"` + key + `","fields":{"summary":"Timeout is too short",` +
		`"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"My Python script times out."}]}]},` +
		`"updated":"2026-11-11T12:00:00.000+0000",` +
		`"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},` +
		`"labels":["reproduce"],` +
		`"reporter":{"accountId":"5b10a2844c20165700ede21g","displayName":"Reporter"},` +
		`"attachment":[]}}`
}

func jiraCommentsBody() string {
	return `{"comments":[],"startAt":0,"maxResults":100,"total":0}`
}

func TestJiraWorkspaceExposesOnlyReadOnlyConnection(t *testing.T) {
	s := newTestServer(t, nil)
	const token = "test-jira-server-token"
	t.Setenv("TRIAGELAB_TEST_JIRA_TOKEN", token)
	seedTickets(t, s, jiraConnection, ticketflow.Config{
		Source: "jira", Origin: "https://test-site.atlassian.net", Email: "triagelab@example.com",
		TokenEnv: "TRIAGELAB_TEST_JIRA_TOKEN", Policy: ticketflow.Policy{Tag: "reproduce"},
	})
	w := request(s, http.MethodGet, "/api/tickets", nil)
	var workspace struct {
		Enabled     bool             `json:"enabled"`
		Connections []map[string]any `json:"connections"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &workspace); err != nil || w.Code != 200 {
		t.Fatalf("workspace response: HTTP %d %v", w.Code, err)
	}
	if !workspace.Enabled || len(workspace.Connections) != 1 {
		t.Fatalf("wrong workspace shape: %v", workspace)
	}
	connection := workspace.Connections[0]
	if connection["source"] != "jira" || connection["account"] != "https://test-site.atlassian.net" ||
		connection["connected"] != true || connection["oauth"] != nil {
		t.Fatalf("wrong Jira connection metadata: %v", connection)
	}
	if strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), "triagelab@example.com") {
		t.Fatal("workspace disclosed a server credential")
	}
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{http.MethodPost, "/api/tickets/c/" + jiraConnection + "/oauth/start", "", 409},
		{http.MethodGet, "/oauth/zendesk/callback?state=state&code=code", "", 400},
		{http.MethodPost, "/api/tickets/c/" + jiraConnection + "/10042/clarify", `{"revision":"reviewed"}`, 409},
		{http.MethodPost, "/api/tickets/c/" + jiraConnection + "/0/preview", "", 400},
		{http.MethodPost, "/api/tickets/c/" + jiraConnection + "/010042/preview", "", 400},
	} {
		w := request(s, tc.method, tc.path, strings.NewReader(tc.body))
		if w.Code != tc.code {
			t.Fatalf("%s: HTTP %d, want %d: %s", tc.path, w.Code, tc.code, w.Body)
		}
	}
}

func TestJiraTicketRouteAcceptsIssueKeys(t *testing.T) {
	authorization := "Basic " + base64.StdEncoding.EncodeToString([]byte("triagelab@example.com:fixture-jira-token"))
	issue := jiraIssueBody("OPS-42")
	comments := jiraCommentsBody()
	siteRequests := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != authorization {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		siteRequests++
		switch r.URL.Path {
		case "/rest/api/3/issue/OPS-42":
			if r.URL.RawQuery != "fields=summary,description,updated,status,labels,reporter,attachment" {
				http.Error(w, "unexpected fields", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(issue))
		case "/rest/api/3/issue/10042":
			_, _ = w.Write([]byte(issue))
		case "/rest/api/3/issue/10042/comment":
			_, _ = w.Write([]byte(comments))
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	s := newTestServer(t, nil)
	t.Setenv("TRIAGELAB_TEST_JIRA_TOKEN", "fixture-jira-token")
	seedTickets(t, s, jiraConnection, ticketflow.Config{Source: "jira", Origin: origin.URL, Email: "triagelab@example.com",
		TokenEnv: "TRIAGELAB_TEST_JIRA_TOKEN", Policy: ticketflow.Policy{Tag: "reproduce"}})
	// The workspace reduces pasted URLs to their key segment before sending;
	// the connector's own tests cover the full-URL reference form.
	w := request(s, http.MethodPost, "/api/tickets/c/"+jiraConnection+"/OPS-42/preview", nil)
	var c ticketflow.Case
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || w.Code != 200 {
		t.Fatalf("issue key preview: HTTP %d %s", w.Code, w.Body)
	}
	if c.TicketID != 10042 || c.Source != "jira" || c.URL != origin.URL+"/browse/OPS-42" {
		t.Fatalf("issue key resolved to the wrong case: %+v", c)
	}
	// The resolved case keeps working through its numeric reference alone.
	w = request(s, http.MethodPost, "/api/tickets/c/"+jiraConnection+"/10042/preview", nil)
	var refreshed ticketflow.Case
	if err := json.Unmarshal(w.Body.Bytes(), &refreshed); err != nil || w.Code != 200 || refreshed.TicketID != 10042 {
		t.Fatalf("numeric preview after key enrollment: HTTP %d %s", w.Code, w.Body)
	}
	for _, ref := range []string{"ops-42", "OPS-", "OPS-042", "-42"} {
		path := "/api/tickets/c/" + jiraConnection + "/" + ref + "/preview"
		before := siteRequests
		if w := request(s, http.MethodPost, path, nil); w.Code != 400 {
			t.Fatalf("%s: HTTP %d, want 400: %s", path, w.Code, w.Body)
		}
		if siteRequests != before {
			t.Fatalf("rejected reference %s reached Jira", path)
		}
	}
}
