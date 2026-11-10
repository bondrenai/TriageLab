package webserver

import "testing"

func TestRoutePatternCollapsesVariableSegments(t *testing.T) {
	runID := "0123abcd-0000-0000-0000-000000000000"
	cases := []struct{ path, want string }{
		{"/api/settings", "/api/settings"},
		{"/api/runs", "/api/runs"},
		{"/api/runs/" + runID, "/api/runs/{id}"},
		{"/api/runs/" + runID + "/report.json", "/api/runs/{id}/report.json"},
		{"/api/runs/" + runID + "/report.md", "/api/runs/{id}/report.md"},
		{"/api/runs/" + runID + "/cancel", "/api/runs/{id}/cancel"},
		{"/api/runs/" + runID + "/ticket-note", "/api/runs/{id}/ticket-note"},
		{"/api/tickets", "/api/tickets"},
		{"/api/tickets/1051/preview", "/api/tickets/{id}/preview"},
		{"/api/tickets/1051/approve", "/api/tickets/{id}/approve"},
		{"/api/tickets/oauth/start", "/api/tickets/oauth/start"},
		{"/api/tickets/procedure", "/api/tickets/procedure"},
		{"/api/agents/status", "/api/agents/status"},
		{"/api/egress-policies/definition", "/api/egress-policies/definition"},
		{"/api/auth/session", "/api/auth/session"},
		{"/api/auth/oidc/callback", "/api/auth/oidc/callback"},
		{"/api/auth/admin/users", "/api/auth/admin/users"},
		{"/api/auth/admin/users/jsmith", "/api/auth/admin/users"},
		{"/oauth/zendesk/callback", "/oauth/zendesk/callback"},
		{"/healthz", "/healthz"},
		{"/tickets", "static"},
		{"/assets/index-a1b2c3.js", "static"},
		{"/api/nonsense/deeper/path/here", "/api/other"},
		{"/runaway/path", "static"},
	}
	for _, tc := range cases {
		if got := routePattern(tc.path); got != tc.want {
			t.Errorf("routePattern(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
