package webserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/auth"
)

const mcpTestToken = "test-mcp-credential-with-at-least-32-characters"

func TestMCPAuthenticationIsSeparateAndOriginBound(t *testing.T) {
	s := newTestServer(t, nil)
	t.Setenv("TEST_MCP_TOKEN", mcpTestToken)
	if err := s.EnableMCP(MCPConfig{Principals: []MCPPrincipal{{Name: "test", Role: auth.RoleUser, TokenEnv: "TEST_MCP_TOKEN"}}, AllowedOrigins: []string{"https://client.example"}}); err != nil {
		t.Fatal(err)
	}
	called := 0
	s.mcp.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		i := actorFrom(r.Context())
		if i.Username != "mcp:test" || i.Role != auth.RoleUser || i.Via != "mcp" {
			t.Errorf("wrong identity: %+v", i)
		}
		w.WriteHeader(204)
	})
	for _, test := range []struct {
		name, credential, origin, host, path string
		cookie                               bool
		want                                 int
	}{
		{"missing", "", "", "localhost:8080", "/mcp", false, 401},
		{"cookie only", "", "", "localhost:8080", "/mcp", true, 401},
		{"invalid", "Bearer wrong", "", "localhost:8080", "/mcp", false, 401},
		{"valid", "Bearer " + mcpTestToken, "", "localhost:8080", "/mcp", false, 204},
		{"origin denied", "Bearer " + mcpTestToken, "https://evil.example", "localhost:8080", "/mcp", false, 403},
		{"origin allowed", "Bearer " + mcpTestToken, "https://client.example", "localhost:8080", "/mcp", false, 204},
		{"host denied", "Bearer " + mcpTestToken, "", "remote.example", "/mcp", false, 403},
		{"query denied", "Bearer " + mcpTestToken, "", "localhost:8080", "/mcp?token=oops", false, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://"+test.host+test.path, strings.NewReader(`{}`))
			if test.credential != "" {
				r.Header.Set("Authorization", test.credential)
			}
			if test.origin != "" {
				r.Header.Set("Origin", test.origin)
			}
			r.Header.Set("X-Forwarded-User", "admin")
			r.Header.Set("X-Forwarded-Role", "admin")
			if test.cookie {
				r.AddCookie(adminCookie(s))
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("%d want %d: %s", w.Code, test.want, w.Body)
			}
			if strings.Contains(w.Body.String(), mcpTestToken) {
				t.Fatal("credential leaked")
			}
		})
	}
	if called != 2 {
		t.Fatal("unauthenticated call reached MCP", called)
	}
	// An MCP credential grants no browser API session.
	clients, err := auth.OpenClientStore(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableAPIClients(clients); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://localhost:8080/api/runs", nil)
	r.Header.Set("Authorization", "Bearer "+mcpTestToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("MCP authenticated browser API", w.Code)
	}
}

func TestMCPConfigFailsClosed(t *testing.T) {
	t.Setenv("TEST_MCP_TOKEN", mcpTestToken)
	principal := MCPPrincipal{Name: "gateway", Role: auth.RoleUser, TokenEnv: "TEST_MCP_TOKEN"}
	for _, config := range []MCPConfig{
		{},
		{Principals: []MCPPrincipal{{Name: "gateway", Role: auth.RoleAdmin, TokenEnv: "TEST_MCP_TOKEN"}}},
		{Principals: []MCPPrincipal{{Name: "gateway", Role: auth.RoleUser, TokenEnv: "MISSING_MCP_TEST_TOKEN"}}},
		{Principals: []MCPPrincipal{principal, principal}},
		{Principals: []MCPPrincipal{principal}, AllowedOrigins: []string{"https://client.example/"}},
		{Principals: []MCPPrincipal{principal}, AllowedOrigins: []string{"http://public.example"}},
	} {
		s := newTestServer(t, nil)
		if err := s.EnableMCP(config); err == nil {
			t.Fatal("invalid configuration accepted", config)
		}
		if s.mcp != nil {
			t.Fatal("partially enabled MCP")
		}
	}
}
