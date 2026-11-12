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
		{"/api/tickets/c/0a1b2c3d/1051/preview", "/api/tickets/c/{id}/{ref}/preview"},
		{"/api/tickets/c/0a1b2c3d/1051/approve", "/api/tickets/c/{id}/{ref}/approve"},
		{"/api/tickets/c/0a1b2c3d/OPS-42/preview", "/api/tickets/c/{id}/{ref}/preview"},
		{"/api/tickets/c/0a1b2c3d/oauth/start", "/api/tickets/c/{id}/oauth/start"},
		{"/api/tickets/c/0a1b2c3d/procedure", "/api/tickets/c/{id}/procedure"},
		{"/api/tickets/c/0a1b2c3d/procedure/validate-condition", "/api/tickets/c/{id}/procedure/validate-condition"},
		{"/api/tickets/connections", "/api/tickets/connections"},
		{"/api/tickets/connections/0a1b2c3d", "/api/tickets/connections"},
		{"/api/agents/status", "/api/agents/status"},
		{"/api/egress-policies/definition", "/api/egress-policies/definition"},
		{"/api/auth/session", "/api/auth/session"},
		{"/api/auth/oidc/callback", "/api/auth/oidc/callback"},
		{"/api/auth/admin/users", "/api/auth/admin/users"},
		{"/api/auth/admin/users/jsmith", "/api/auth/admin/users"},
		{"/api/auth/admin/clients", "/api/auth/admin/clients"},
		{"/api/auth/admin/clients/ci/secret", "/api/auth/admin/clients"},
		{"/oauth/zendesk/callback", "/oauth/zendesk/callback"},
		{"/oauth/token", "/oauth/token"},
		{"/oauth/revoke", "/oauth/revoke"},
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
