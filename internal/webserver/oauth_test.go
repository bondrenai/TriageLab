package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

func TestZendeskOAuthRoutes(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.EnableTickets(ticketflow.Config{
		Subdomain: "example", ClientID: "triagelab-local", RedirectURI: "http://127.0.0.1:8080" + ticketflow.OAuthCallbackPath,
		Policy: ticketflow.Policy{Tag: "reproduce"},
	}); err != nil {
		t.Fatal(err)
	}

	call := func(method, host, path, site string, header bool, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), method, "http://"+host+path, nil)
		r.Header.Set("Sec-Fetch-Site", site)
		if header {
			r.Header.Set("X-TriageLab-Request", "1")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	const host = "127.0.0.1:8080"
	const start = "/api/tickets/oauth/start"
	for _, attempt := range []struct {
		host, site string
		header     bool
		code       int
	}{
		{host, "same-origin", false, 403},
		{host, "cross-site", true, 403},
		{"localhost:8080", "same-origin", true, 409},
	} {
		if w := call("POST", attempt.host, start, attempt.site, attempt.header, nil); w.Code != attempt.code {
			t.Fatalf("authorize returned %d, want %d", w.Code, attempt.code)
		}
	}
	w := call("POST", host, start, "same-origin", true, nil)
	if w.Code != 200 {
		t.Fatalf("authorize: %d %s", w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	var body map[string]string
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(cookies) != 1 || cookies[0].SameSite != http.SameSiteLaxMode ||
		!cookies[0].HttpOnly || cookies[0].Path != ticketflow.OAuthCallbackPath {
		t.Fatal("missing protected authorization cookie")
	}
	u, err := url.Parse(body["url"])
	if err != nil || u.Host != "example.zendesk.com" || u.Query().Get("scope") != "ticket_attachments:read tickets:read" {
		t.Fatal("wrong OAuth destination or scope")
	}
	callback := ticketflow.OAuthCallbackPath + "?" + url.Values{"state": {u.Query().Get("state")}, "error": {"access_denied"}}.Encode()
	if w := call("GET", host, callback, "cross-site", false, nil); w.Code != 400 {
		t.Fatal("callback accepted without initiating cookie")
	}
	if w := call("POST", host, callback, "cross-site", false, cookies[0]); w.Code != 405 {
		t.Fatal("callback accepted non-navigation method")
	}
	if w := call("GET", host, callback, "cross-site", false, cookies[0]); w.Code != 303 || w.Header().Get("Location") != "/tickets?zendesk=denied" {
		t.Fatal("valid cross-site callback was not handled")
	}
	if w := call("GET", host, callback, "cross-site", false, cookies[0]); w.Code != 400 {
		t.Fatal("callback state was reusable")
	}
}

func TestZendeskReturnAllowsTopLevelTicketNavigation(t *testing.T) {
	s := newTestServer(t, nil)
	for _, test := range []struct {
		path, mode, dest string
		want             int
	}{
		{"/tickets?zendesk=connected", "navigate", "document", http.StatusOK},
		{"/tickets", "navigate", "iframe", http.StatusForbidden},
		{"/tickets", "cors", "empty", http.StatusForbidden},
		{"/api/tickets", "navigate", "document", http.StatusForbidden},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+test.path, nil)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Sec-Fetch-Mode", test.mode)
		r.Header.Set("Sec-Fetch-Dest", test.dest)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Errorf("%s with %s/%s returned %d, want %d", test.path, test.mode, test.dest, w.Code, test.want)
		}
	}
}
