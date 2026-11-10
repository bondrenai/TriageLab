package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

func TestDeploymentStatusUsesExplicitScopeAndCurrentDiscovery(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Namespace = "investigations"
	const endpoint = "/api/mcp-servers/status"
	w := request(s, "GET", endpoint, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("expected opt-in status: %d %s", w.Code, w.Body)
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TRIAGELAB_TEST_KUBECONFIG", filepath.Join(dir, "reader-config"))
	command := `#!/bin/sh
set -eu
[ "$1" = '--kubeconfig' ] && [ "$2" = "$TRIAGELAB_TEST_KUBECONFIG" ] && [ "$3" = '--context' ] && [ "$4" = 'sandbox-dev' ] && [ "$5" = 'get' ] && [ "$6" = '--raw' ] || exit 1
case "$7" in
  '/apis/api.kagent.dev/v1alpha3/namespaces/investigations/remotemcpservers?limit=100')
    cat <<'JSON'
{"metadata":{"continue":"next+page"},"items":[
  {"metadata":{"name":"demo","namespace":"investigations","generation":2},"status":{"observedGeneration":2,"conditions":[{"type":"Accepted","status":"True","reason":"DiscoverySucceeded","observedGeneration":2,"message":"private raw diagnostic"}],"discoveredTools":[{"name":"lookup_runbook","description":"Read a runbook"},{"name":"bash"}]}},
  {"metadata":{"name":"empty","namespace":"investigations","generation":2},"status":{"observedGeneration":2,"conditions":[{"type":"Accepted","status":"True","reason":"DiscoverySucceeded","observedGeneration":2}]}}
]}
JSON
    ;;
  '/apis/api.kagent.dev/v1alpha3/namespaces/investigations/remotemcpservers?continue=next%2Bpage&limit=100')
    cat <<'JSON'
{"items":[{"metadata":{"name":"old","namespace":"investigations","generation":2},"status":{"observedGeneration":1,"conditions":[{"type":"Accepted","status":"True","reason":"DiscoverySucceeded","observedGeneration":1}],"discoveredTools":[{"name":"stale_tool"}]}}]}
JSON
    ;;
  *) printf 'private kubectl diagnostic' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableDeploymentStatus(os.Getenv("TRIAGELAB_TEST_KUBECONFIG"), "sandbox-dev"); err != nil {
		t.Fatal(err)
	}
	w = request(s, "GET", endpoint+"?namespace=other", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"enabled":true`) ||
		!strings.Contains(body, `"name":"demo","state":"ready"`) || !strings.Contains(body, `"name":"lookup_runbook","description":"Read a runbook"`) ||
		!strings.Contains(body, `"name":"empty","state":"ready","tools":[]`) || !strings.Contains(body, `"selection_error":"an MCP tool name conflicts`) ||
		!strings.Contains(body, `"name":"old","state":"pending","tools":null`) || strings.Contains(body, "stale_tool") || strings.Contains(body, "private raw diagnostic") {
		t.Fatalf("unexpected deployment status: %d %s", w.Code, body)
	}
	w = request(s, "GET", "/api/models/status", nil)
	if w.Code != 502 || strings.Contains(w.Body.String(), "private kubectl diagnostic") {
		t.Fatalf("expected a safe upstream error: %d %s", w.Code, w.Body)
	}
}

func TestAgentManifestPreviewUsesWorkspaceNamespace(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Namespace = "investigations"
	w := request(s, "POST", "/api/agents/manifest", strings.NewReader(`{"name":"network","model_ref":"triagelab-model"}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"namespace":"investigations"`) || !strings.Contains(w.Body.String(), `"kind":"Agent"`) {
		t.Fatalf("manifest response: %d %s", w.Code, w.Body)
	}
	w = request(s, "POST", "/api/agents/manifest", strings.NewReader(`{"name":"network","namespace":"external","model_ref":"triagelab-model"}`))
	if w.Code != 400 {
		t.Fatalf("unexpected namespace override accepted: %d %s", w.Code, w.Body)
	}
}

func TestDefinitionWritesRequireExplicitAccessAndProtectExistingResources(t *testing.T) {
	s := newTestServer(t, nil)
	const endpoint = "/api/agents/definition"
	const definition = `{"name":"external","model_ref":"triagelab-model"}`
	if w := request(s, "POST", endpoint, strings.NewReader(definition)); w.Code != 403 {
		t.Fatalf("writes enabled by default: %d %s", w.Code, w.Body)
	}
	if err := s.EnableResourceWrites(); err == nil {
		t.Fatal("write access enabled without an explicit cluster")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	command := `#!/bin/sh
if [ "$5" != 'get' ] || [ "$7" != '/apis/api.kagent.dev/v1alpha3/namespaces/kagent/agents/external' ]; then exit 1; fi
cat <<'JSON'
{"apiVersion":"api.kagent.dev/v1alpha3","kind":"Agent","metadata":{"name":"external","namespace":"kagent","resourceVersion":"2"},"spec":{"template":{"inline":{"systemPrompt":"do not expose raw spec"}}}}
JSON
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableDeploymentStatus("/explicit/reader", "disposable"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableResourceWrites(); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "GET", endpoint+"?name=external", nil); w.Code != 404 || strings.Contains(w.Body.String(), "raw spec") {
		t.Fatalf("unmanaged definition exposed: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("POST", "http://localhost:8080"+endpoint, strings.NewReader(definition))
	r.Header.Set("X-TriageLab-Request", "1")
	r.AddCookie(adminCookie(s))
	r.Header.Set("If-Match", `"stale-revision"`)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("unmanaged resource could be edited: %d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest("POST", "http://localhost:8080"+endpoint, strings.NewReader(definition))
	r.Header.Set("X-TriageLab-Request", "1")
	r.Header.Set("Origin", "https://external.example")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin write accepted: %d %s", w.Code, w.Body)
	}
}

func TestStandaloneResourcePreviews(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Namespace = "investigations"
	for _, tc := range []struct{ path, input, name, kind, specKey, specValue, invalid string }{
		{"/api/models/manifest", `{"name":"incident-model","model":{"provider":"OpenAI","name":"gpt-4.1"}}`, "incident-model", "ModelConfig", "model", "gpt-4.1",
			`{"name":"triagelab-model","model":{"provider":"OpenAI","name":"gpt-4.1"}}`},
		{"/api/mcp-servers/manifest", `{"name":"docs","url":"https://docs.example/mcp"}`, "docs", "RemoteMCPServer", "url", "https://docs.example/mcp",
			`{"name":"docs","url":"https://docs.example/mcp","tools":["search_docs"]}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			w := request(s, "POST", tc.path, strings.NewReader(tc.input))
			var resource struct {
				Kind     string
				Metadata map[string]string
				Spec     map[string]any
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resource) != nil ||
				resource.Kind != tc.kind || resource.Metadata["name"] != tc.name ||
				resource.Metadata["namespace"] != "investigations" || resource.Spec[tc.specKey] != tc.specValue {
				t.Fatalf("wrong standalone resource: %d %s", w.Code, w.Body)
			}
			w = request(s, "POST", tc.path, strings.NewReader(tc.invalid))
			if w.Code != 400 {
				t.Fatalf("invalid definition accepted: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestManualRunUsesOnlyAnAllowedAgent(t *testing.T) {
	called := make(chan string, 1)
	s := newTestServer(t, testBackend{call: func(_ context.Context, name string, args map[string]any) (map[string]any, error) {
		switch name {
		case "create_sandbox":
			return map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE", "retain_while_suspended": true}, nil
		case "get_sandbox":
			return map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE", "retain_while_suspended": true}, nil
		case "create_session":
			called <- args["agent"].(map[string]any)["name"].(string)
			return nil, fmt.Errorf("stop after observing session target")
		case "delete_sandbox":
			return map[string]any{"state": "RUNTIME_STATE_DELETED"}, nil
		default:
			return nil, fmt.Errorf("unexpected tool %s", name)
		}
	}})
	s.config.ManualAgents = map[string][]string{"network": nil}
	for _, tc := range []struct {
		agent string
		want  int
	}{
		{"some-other-agent", 400}, {"network", 202},
	} {
		r := formRequest(t, s, "An issue")
		r.Header.Set("X-TriageLab-Agent", tc.agent)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.agent, w.Code, w.Body)
		}
	}
	select {
	case got := <-called:
		if got != "network" {
			t.Fatalf("session used %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no session was attempted")
	}
}

func TestManualRunUsesOnlyReadySavedAgents(t *testing.T) {
	called := make(chan string, 1)
	s := newTestServer(t, testBackend{call: func(_ context.Context, tool string, args map[string]any) (map[string]any, error) {
		switch tool {
		case "create_sandbox", "get_sandbox":
			return map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE", "retain_while_suspended": true}, nil
		case "create_session":
			called <- args["agent"].(map[string]any)["name"].(string)
			return nil, fmt.Errorf("stop after observing session target")
		case "delete_sandbox":
			return map[string]any{"state": "RUNTIME_STATE_DELETED"}, nil
		default:
			return nil, fmt.Errorf("unexpected tool %s", tool)
		}
	}})
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TRIAGELAB_AGENT_FIXTURES", dir)
	command := `#!/bin/sh
case "$5:$7" in
  get:/apis/api.kagent.dev/v1alpha3/namespaces/kagent/agents/*)
    cat "$TRIAGELAB_AGENT_FIXTURES/${7##*/}.json" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		definition string
		observed   int
	}{
		{"saved", `{"name":"saved","model_ref":"preview-model","mcp_servers":[{"name":"docs","tools":["lookup_status"]}]}`, 2},
		{"pending", `{"name":"pending","model_ref":"preview-model"}`, 1},
		{"external", "", 2},
	} {
		conditions := []map[string]any{}
		for _, kind := range []string{"Accepted", "ResolvedRefs", "Compatible", "Ready"} {
			conditions = append(conditions, map[string]any{"type": kind, "status": "True", "observedGeneration": tc.observed})
		}
		data, err := json.Marshal(map[string]any{
			"kind": "Agent", "apiVersion": "api.kagent.dev/v1alpha3",
			"metadata": map[string]any{"name": tc.name, "namespace": "kagent", "generation": 2,
				"annotations": map[string]string{"triagelab.bondren.ai/definition": tc.definition}},
			"status": map[string]any{"observedGeneration": tc.observed, "conditions": conditions},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, tc.name+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnableDeploymentStatus("/explicit/editor", "disposable"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableResourceWrites(); err != nil {
		t.Fatal(err)
	}
	_, tools, err := s.selectInvestigator(context.Background(), "saved")
	if err != nil || len(tools) != 1 || tools[0] != "lookup_status" {
		t.Fatalf("saved tool allowlist = %v, error = %v", tools, err)
	}
	for _, tc := range []struct {
		name string
		want int
	}{{"external", 400}, {"pending", 409}, {"saved", 202}} {
		r := formRequest(t, s, "An issue")
		r.Header.Set("X-TriageLab-Agent", tc.name)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s: expected %d, got %d %s", tc.name, tc.want, w.Code, w.Body)
		}
	}
	select {
	case got := <-called:
		if got != "saved" {
			t.Fatalf("session used %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no saved-agent session was attempted")
	}
}

func TestTicketWorkspaceRejectsAManualAgentAsItsDefault(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.ManualAgents = map[string][]string{s.config.Agent: {"search_docs"}}
	err := s.EnableTickets(ticketflow.Config{Source: "github", Repository: "example/reproduction", Policy: ticketflow.Policy{Tag: "reproduce"}})
	if err == nil || !strings.Contains(err.Error(), "default agent") {
		t.Fatalf("ticket workspace accepted a manual default: %v", err)
	}
}
