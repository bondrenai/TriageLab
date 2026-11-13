package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

func TestRunsTargetAllowedNamespaces(t *testing.T) {
	sandboxNamespace := make(chan string, 1)
	agentNamespace := make(chan string, 1)
	s := newTestServer(t, testBackend{call: func(_ context.Context, tool string, args map[string]any) (map[string]any, error) {
		switch tool {
		case "create_sandbox":
			sandboxNamespace <- args["namespace"].(string)
			return map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE", "retain_while_suspended": true}, nil
		case "get_sandbox":
			return map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE", "retain_while_suspended": true}, nil
		case "create_session":
			agentNamespace <- args["agent"].(map[string]any)["namespace"].(string)
			return nil, fmt.Errorf("stop after observing session target")
		case "delete_sandbox":
			return map[string]any{"state": "RUNTIME_STATE_DELETED"}, nil
		default:
			return nil, fmt.Errorf("unexpected tool %s", tool)
		}
	}})
	s.config.ManualAgents = map[string][]string{"network": nil}
	s.config.RunNamespaces = []string{"triagelab-runs"}
	for _, tc := range []struct {
		namespace string
		want      int
	}{
		{"elsewhere", 400}, {"", 400}, {"triagelab-runs", 202},
	} {
		r := formRequest(t, s, "An issue")
		r.Header.Set("X-TriageLab-Namespace", tc.namespace)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%q: %d %s", tc.namespace, w.Code, w.Body)
		}
	}
	select {
	case got := <-sandboxNamespace:
		if got != "triagelab-runs" {
			t.Fatalf("sandbox created in %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no sandbox was created")
	}
	select {
	case got := <-agentNamespace:
		if got != "triagelab-runs" {
			t.Fatalf("session agent referenced %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no session was attempted")
	}
}

func TestSettingsExposeRunNamespaces(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.RunNamespaces = []string{"beta-runs", "alpha-runs"}
	w := requestAs(s, adminCookie(s), http.MethodGet, "/api/settings", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", w.Code, w.Body)
	}
	var settings struct {
		Namespace     string   `json:"namespace"`
		RunNamespaces []string `json:"run_namespaces"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Namespace != "kagent" {
		t.Fatalf("default namespace = %q", settings.Namespace)
	}
	if len(settings.RunNamespaces) != 2 || settings.RunNamespaces[0] != "alpha-runs" || settings.RunNamespaces[1] != "beta-runs" {
		t.Fatalf("run namespaces = %v", settings.RunNamespaces)
	}
}

func TestConnectionMatchesAllowedRunNamespace(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.RunNamespaces = []string{"triagelab-runs"}
	saved := s.config
	saved.Namespace = "triagelab-runs"
	if !s.connectionMatches(saved) {
		t.Fatal("allowlisted run namespace does not match the connection")
	}
	saved.Namespace = "removed-runs"
	if s.connectionMatches(saved) {
		t.Fatal("namespace outside the allowlist matches the connection")
	}
}

func TestRunNamespaceReadersVerifyTargets(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.RunNamespaces = []string{"triagelab-runs"}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TRIAGELAB_NAMESPACE_FIXTURES", dir)
	command := `#!/bin/sh
case "$5:$7" in
  get:/apis/api.kagent.dev/v1alpha3/namespaces/triagelab-runs/*)
    cat "$TRIAGELAB_NAMESPACE_FIXTURES/${7##*/}.json" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	agentConditions := []map[string]any{}
	for _, kind := range []string{"Accepted", "ResolvedRefs", "Compatible", "Ready"} {
		agentConditions = append(agentConditions, map[string]any{"type": kind, "status": "True", "observedGeneration": 2})
	}
	for name, object := range map[string]map[string]any{
		"saved": {
			"kind": "Agent", "apiVersion": "api.kagent.dev/v1alpha3",
			"metadata": map[string]any{"name": "saved", "namespace": "triagelab-runs", "uid": "agent-uid", "generation": 2,
				"annotations": map[string]string{"triagelab.bondren.ai/definition": `{"name":"saved","model_ref":"preview-model","mcp_servers":[{"name":"docs","tools":["lookup_status"]}]}`}},
			"status": map[string]any{"observedGeneration": 2, "conditions": agentConditions, "latestSuccessfulRevision": "rev-7"},
		},
		"triagelab-investigator": {
			"kind": "Agent", "apiVersion": "api.kagent.dev/v1alpha3",
			"metadata": map[string]any{"name": "triagelab-investigator", "namespace": "triagelab-runs", "uid": "default-agent-uid", "generation": 2,
				"annotations": map[string]string{"triagelab.bondren.ai/definition": `{"name":"triagelab-investigator","model_ref":"preview-model"}`}},
			"status": map[string]any{"observedGeneration": 2, "conditions": agentConditions, "latestSuccessfulRevision": "rev-1"},
		},
		"hardened": {
			"kind": "SandboxTemplate", "apiVersion": "api.kagent.dev/v1alpha3",
			"metadata": map[string]any{"name": "hardened", "namespace": "triagelab-runs", "uid": "template-uid", "generation": 1},
			"status": map[string]any{"observedGeneration": 1, "conditions": []map[string]any{
				{"type": "Ready", "status": "True", "observedGeneration": 1},
			}, "latestSuccessfulRevision": "rev-3"},
		},
	} {
		data, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnableDeploymentStatus("/explicit/editor", "disposable"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableResourceWrites(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.selectInvestigator(context.Background(), "saved", "triagelab-runs"); err != nil {
		t.Fatalf("saved agent in the run namespace: %v", err)
	}
	if _, _, err := s.selectInvestigator(context.Background(), "saved", "kagent"); err == nil {
		t.Fatal("saved agent resolved in a namespace that does not hold it")
	}
	template, err := s.selectSandbox(context.Background(), "hardened", "triagelab-runs")
	if err != nil || template != "hardened" {
		t.Fatalf("hardened template in the run namespace: %q %v", template, err)
	}
	target, err := s.resolveTicketTarget(context.Background(), s.config.Agent, "hardened", "triagelab-runs")
	if err != nil {
		t.Fatalf("ticket target in the run namespace: %v", err)
	}
	if target.Namespace != "triagelab-runs" || target.SandboxRevision == "" {
		t.Fatalf("ticket target = %+v", target)
	}
	if _, err := s.resolveTicketTarget(context.Background(), s.config.Agent, s.config.Template, "triagelab-runs"); err == nil {
		t.Fatal("default template resolved in a namespace that does not hold it")
	}
}

func TestTicketTargetBlocksNamespaceWithoutResourceWrites(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.RunNamespaces = []string{"triagelab-runs"}
	if _, err := s.resolveTicketTarget(context.Background(), s.config.Agent, s.config.Template, "triagelab-runs"); !errors.Is(err, ticketflow.ErrBlocked) {
		t.Fatalf("namespace without resource writes: %v", err)
	}
	target, err := s.resolveTicketTarget(context.Background(), s.config.Agent, s.config.Template, "")
	if err != nil || target.Namespace != "" {
		t.Fatalf("default namespace without resource writes: %+v %v", target, err)
	}
}
