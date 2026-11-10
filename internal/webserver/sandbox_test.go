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
)

const testSandboxDefinition = `{"name":"network-lab","workload_image":"docker.io/library/python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","worker_pool":"kagent-default","snapshot_location":"gs://ate-snapshots/triagelab/"}`
const testEgressDefinition = `{"name":"pypi-egress","destinations":[{"origin":"https://pypi.org"}]}`

func TestSandboxResourceManifestsAndWriteGating(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Namespace = "investigations"
	for _, tc := range []struct{ path, input, kind string }{
		{"/api/sandboxes/manifest", testSandboxDefinition, "SandboxTemplate"},
		{"/api/egress-policies/manifest", testEgressDefinition, "EgressPolicy"},
	} {
		w := request(s, "POST", tc.path, strings.NewReader(tc.input))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"`+tc.kind+`"`) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
	if w := request(s, "POST", "/api/sandboxes/manifest", strings.NewReader(`{"name":"network-lab","workload_image":"python:3.12","worker_pool":"kagent-default","snapshot_location":"gs://b/"}`)); w.Code != 400 {
		t.Fatalf("unpinned image accepted: %d %s", w.Code, w.Body)
	}
	if w := request(s, "POST", "/api/egress-policies/definition", strings.NewReader(testEgressDefinition)); w.Code != 403 {
		t.Fatalf("writes enabled by default: %d %s", w.Code, w.Body)
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	command := `#!/bin/sh
if [ "$5" = 'create' ] && [ "$8" = '-f' ]; then cat; exit 0; fi
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableDeploymentStatus("/explicit/editor", "disposable"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableResourceWrites(); err != nil {
		t.Fatal(err)
	}
	reserved := `{"name":"triagelab-python","workload_image":"docker.io/library/python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","worker_pool":"kagent-default","snapshot_location":"gs://b/"}`
	if w := request(s, "POST", "/api/sandboxes/definition", strings.NewReader(reserved)); w.Code != 400 || !strings.Contains(w.Body.String(), "managed separately") {
		t.Fatalf("default template was editable: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ path, input string }{
		{"/api/sandboxes/definition", testSandboxDefinition},
		{"/api/egress-policies/definition", testEgressDefinition},
	} {
		if w := request(s, "POST", tc.path, strings.NewReader(tc.input)); w.Code != 200 ||
			!strings.Contains(w.Body.String(), `"revision"`) || !strings.Contains(w.Body.String(), `"definition"`) {
			t.Fatalf("save %s: %d %s", tc.path, w.Code, w.Body)
		}
	}
}

func TestSandboxDeploymentStatusReportsGrantsAndAgentTools(t *testing.T) {
	s := newTestServer(t, nil)
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	fixtures := t.TempDir()
	t.Setenv("TRIAGELAB_SANDBOX_FIXTURES", fixtures)
	agentConditions := []map[string]any{}
	for _, kind := range []string{"Accepted", "ResolvedRefs", "Compatible", "Ready"} {
		agentConditions = append(agentConditions, map[string]any{"type": kind, "status": "True", "observedGeneration": 2})
	}
	agentsList, err := json.Marshal(map[string]any{"items": []map[string]any{{
		"metadata": map[string]any{"name": "saved", "namespace": "kagent", "generation": 2,
			"annotations": map[string]string{"triagelab.bondren.ai/definition": `{"name":"saved","mcp_servers":[{"name":"docs","tools":["lookup_status"]}]}`}},
		"status": map[string]any{"observedGeneration": 2, "conditions": agentConditions},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtures, "agents.json"), agentsList, 0o600); err != nil {
		t.Fatal(err)
	}
	command := `#!/bin/sh
[ "$1" = '--kubeconfig' ] && [ "$5" = 'get' ] && [ "$6" = '--raw' ] || exit 1
case "$7" in
  '/apis/api.kagent.dev/v1alpha3/namespaces/kagent/egresspolicies?limit=100')
    cat <<'JSON'
{"items":[
  {"metadata":{"name":"pypi-egress","namespace":"kagent","generation":1}},
  {"metadata":{"name":"stale-egress","namespace":"kagent","generation":1,"deletionTimestamp":"2026-11-01T00:00:00Z"}}
]}
JSON
    ;;
  '/apis/api.kagent.dev/v1alpha3/namespaces/kagent/sandboxtemplates?limit=100')
    cat <<'JSON'
{"items":[
  {"metadata":{"name":"triagelab-python","namespace":"kagent","generation":1},"status":{"observedGeneration":1,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}},
  {"metadata":{"name":"network-lab","namespace":"kagent","generation":2},"status":{"observedGeneration":2,"conditions":[{"type":"Ready","status":"True","observedGeneration":2},{"type":"EgressPoliciesApplied","status":"False","reason":"EgressPolicyNotFound","observedGeneration":2}]}}
]}
JSON
    ;;
  '/apis/api.kagent.dev/v1alpha3/namespaces/kagent/agents?limit=100')
    cat "$TRIAGELAB_SANDBOX_FIXTURES/agents.json"
    ;;
  *) printf 'private kubectl diagnostic' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableDeploymentStatus(filepath.Join(dir, "unused"), "sandbox-dev"); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/sandboxes/status", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"name":"triagelab-python","state":"ready"`) ||
		!strings.Contains(body, `"name":"network-lab","state":"ready"`) || !strings.Contains(body, `"reason":"EgressPolicyNotFound"`) {
		t.Fatalf("unexpected sandbox status: %d %s", w.Code, body)
	}
	w = request(s, "GET", "/api/egress-policies/status", nil)
	body = w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"name":"pypi-egress","state":"ready"`) ||
		!strings.Contains(body, `"name":"stale-egress","state":"deleting"`) {
		t.Fatalf("unexpected egress status: %d %s", w.Code, body)
	}
	w = request(s, "GET", "/api/agents/status", nil)
	body = w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"manual_tools":["lookup_status"]`) {
		t.Fatalf("saved agent tools not surfaced: %d %s", w.Code, body)
	}
	if !strings.Contains(request(s, "GET", "/api/settings", nil).Body.String(), `"sandbox_template":"triagelab-python"`) {
		t.Fatal("settings omitted the workspace sandbox template")
	}
}

func TestManualRunsSelectAllowedSandboxTemplates(t *testing.T) {
	templates := make(chan string, 4)
	backend := testBackend{call: func(_ context.Context, tool string, args map[string]any) (map[string]any, error) {
		switch tool {
		case "create_sandbox":
			templates <- args["template"].(string)
			return map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE"}, nil
		case "get_sandbox":
			return map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE"}, nil
		case "create_session":
			return nil, fmt.Errorf("stop after observing sandbox target")
		case "delete_sandbox":
			return map[string]any{"state": "RUNTIME_STATE_DELETED"}, nil
		default:
			return nil, fmt.Errorf("unexpected tool %s", tool)
		}
	}}
	s := newTestServer(t, backend)
	r := formRequest(t, s, "An issue")
	r.Header.Add("X-TriageLab-Sandbox", "one")
	r.Header.Add("X-TriageLab-Sandbox", "two")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("ambiguous sandbox header accepted: %d %s", w.Code, w.Body)
	}
	r = formRequest(t, s, "An issue")
	r.Header.Set("X-TriageLab-Sandbox", "network-lab")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("non-default template without resource writes accepted: %d %s", w.Code, w.Body)
	}
	r = formRequest(t, s, "An issue")
	r.Header.Set("X-TriageLab-Sandbox", "triagelab-python")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("default template rejected: %d %s", w.Code, w.Body)
	}

	saved := newTestServer(t, backend)
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	command := `#!/bin/sh
[ "$5" = 'get' ] && [ "$6" = '--raw' ] || exit 1
case "$7" in
  /apis/api.kagent.dev/v1alpha3/namespaces/kagent/sandboxtemplates/ready-box)
    cat <<'JSON'
{"metadata":{"name":"ready-box","namespace":"kagent","generation":2},"status":{"observedGeneration":2,"conditions":[{"type":"Ready","status":"True","observedGeneration":2}],"latestSuccessfulRevision":"5"}}
JSON
    ;;
  /apis/api.kagent.dev/v1alpha3/namespaces/kagent/sandboxtemplates/pending-box)
    cat <<'JSON'
{"metadata":{"name":"pending-box","namespace":"kagent","generation":2},"status":{"observedGeneration":1,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}}
JSON
    ;;
  *) printf 'Error from server (NotFound)' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saved.EnableDeploymentStatus("/explicit/editor", "disposable"); err != nil {
		t.Fatal(err)
	}
	if err := saved.EnableResourceWrites(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sandbox string
		want    int
	}{
		{"ghost-box", 400}, {"pending-box", 400},
	} {
		r := formRequest(t, saved, "An issue")
		r.Header.Set("X-TriageLab-Sandbox", tc.sandbox)
		w := httptest.NewRecorder()
		saved.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d %s", tc.sandbox, tc.want, w.Code, w.Body)
		}
	}
	r = formRequest(t, saved, "An issue")
	r.Header.Set("X-TriageLab-Sandbox", "ready-box")
	w = httptest.NewRecorder()
	saved.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("ready saved template rejected: %d %s", w.Code, w.Body)
	}
	select {
	case got := <-templates:
		if got != "triagelab-python" {
			t.Fatalf("default run created sandbox from template %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no default sandbox was attempted")
	}
	select {
	case got := <-templates:
		if got != "ready-box" {
			t.Fatalf("saved template run created sandbox from template %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no saved-template sandbox was attempted")
	}
}
