package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
)

func TestUsageAndInputErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"investigate"}, {"cleanup"}, {"manifest", "--workload-image", "python:latest"}} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, &out, &stderr); code != 2 {
			t.Fatalf("%v: got %d, stderr %s", args, code, stderr.String())
		}
	}
}

func TestInitialReportPersistenceFailureReturnsInfrastructureErrorWithoutNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected upstream request", http.StatusInternalServerError)
	}))
	defer server.Close()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	config, err := json.Marshal(map[string]any{"endpoint": server.URL + "/mcp", "token_env": "TRIAGELAB_PERSISTENCE_TEST_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIAGELAB_PERSISTENCE_TEST_TOKEN", "")
	blockedOutput := filepath.Join(directory, "regular-file")
	if err := os.WriteFile(blockedOutput, []byte("preserve this file"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"investigate", "--description", "A valid local failure report",
		"--config", configPath, "--output", blockedOutput,
	}, &out, &stderr)
	if code != 4 {
		t.Fatalf("exit=%d; stdout=%s; stderr=%s", code, out.String(), stderr.String())
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d network requests before journaling the run", requests.Load())
	}
	if !strings.Contains(stderr.String(), "Report could not be saved") {
		t.Fatalf("missing persistence diagnostic: %s", stderr.String())
	}
	content, err := os.ReadFile(blockedOutput)
	if err != nil || string(content) != "preserve this file" {
		t.Fatalf("output path modified: %q, %v", content, err)
	}
}
func TestManifestAndVersion(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"manifest"}, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var manifest map[string]any
	if err := json.Unmarshal(out.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	items := manifest["items"].([]any)
	if len(items) != 8 {
		t.Fatalf("expected reviewed resources, got %d", len(items))
	}
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["kind"] == "SandboxTemplate" {
			spec := item["spec"].(map[string]any)
			if _, exists := spec["egressPolicyRefs"]; exists {
				t.Fatal("default sandbox unexpectedly grants egress")
			}
			if !strings.Contains(spec["workload"].(map[string]any)["image"].(string), "@sha256:") {
				t.Fatal("image not pinned")
			}
		}
	}
	for _, build := range []string{"dev", "v9.9.9"} {
		previous := version
		version = build // what -ldflags "-X main.version=..." sets
		out.Reset()
		code := run(context.Background(), []string{"version"}, &out, &stderr)
		version = previous
		if code != 0 || !strings.HasPrefix(out.String(), "triagelab "+build+" (KAgent ") {
			t.Fatalf("version output %q for build %s", out.String(), build)
		}
	}
}

var flagName = regexp.MustCompile(`(?m)^  -(\S+)`)

// Each subcommand accepts exactly its own options; shared names keep their defaults.
func TestSubcommandsDefineOnlyTheirOwnFlags(t *testing.T) {
	for command, want := range map[string][]string{
		"investigate":    {"agent", "config", "description", "description-file", "file", "log", "output", "repo", "telemetry"},
		"serve":          {"allow-resource-writes", "assets", "auth", "config", "listen", "output", "status-context", "status-kubeconfig", "telemetry", "tickets", "users"},
		"doctor":         {"config", "telemetry"},
		"cleanup":        {"config", "report", "telemetry"},
		"manifest":       {"workload-image"},
		"agent-manifest": {"definition", "namespace"},
		"version":        nil,
	} {
		t.Run(command, func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := run(context.Background(), []string{command, "-h"}, &out, &stderr); code != 2 {
				t.Fatalf("help exit %d", code)
			}
			var got []string
			for _, match := range flagName.FindAllStringSubmatch(stderr.String(), -1) {
				got = append(got, match[1])
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("flags %v, want %v", got, want)
			}
		})
	}
	for _, args := range [][]string{
		{"doctor", "--repo", "x"}, {"doctor", "--output", "x"}, {"cleanup", "--description", "x"},
		{"serve", "--repo", "x"}, {"serve", "--report", "x"}, {"investigate", "--listen", "x"},
		{"investigate", "--report", "x"}, {"investigate", "--status-context", "x"}, {"manifest", "--config", "x"},
		{"agent-manifest", "--output", "x"}, {"version", "--repo", "x"}, {"version", "extra"},
		{"doctor", "--auth", "x"}, {"investigate", "--users", "x"}, {"cleanup", "--tickets", "x"},
	} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, &out, &stderr); code != 2 || out.Len() != 0 {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", args, code, out.String(), stderr.String())
		}
	}
}

func TestDescriptionFileUsesCaseDescriptionLimit(t *testing.T) {
	directory := t.TempDir()
	// An unreachable endpoint and a blocked output path stop the run before any network request.
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"endpoint":"http://127.0.0.1:9/mcp","token_env":"TRIAGELAB_DESCRIPTION_TEST_TOKEN"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIAGELAB_DESCRIPTION_TEST_TOKEN", "")
	blockedOutput := filepath.Join(directory, "regular-file")
	if err := os.WriteFile(blockedOutput, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		size int
		code int
	}{{40 << 10, 4}, {casefile.MaxDescriptionBytes, 4}, {casefile.MaxDescriptionBytes + 1, 2}} {
		path := filepath.Join(directory, "issue.md")
		if err := os.WriteFile(path, bytes.Repeat([]byte("a"), test.size), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run(context.Background(), []string{"investigate", "--config", configPath, "--description-file", path, "--output", blockedOutput}, &out, &stderr)
		if code != test.code {
			t.Fatalf("%d bytes: exit %d, stderr %s", test.size, code, stderr.String())
		}
		if test.code == 2 && !strings.Contains(stderr.String(), "exceeds 64 KiB") {
			t.Fatalf("missing size diagnostic: %s", stderr.String())
		}
	}
}

// fakeKAgent answers the runtime deletions `triagelab cleanup` makes, records
// each tool name, and fails any other call.
func fakeKAgent(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var tools []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		params, _ := request["params"].(map[string]any)
		name, _ := params["name"].(string)
		mu.Lock()
		tools = append(tools, name)
		mu.Unlock()
		output := map[string]any{"state": "RUNTIME_STATE_DELETED"}
		switch name {
		case "delete_sandbox":
		case "delete_session":
			output = map[string]any{"session": output}
		default:
			http.Error(w, "unexpected tool", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{"structuredContent": output}})
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), tools...)
	}
}

func exactJSON(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// savedCleanupCase writes the preserved live report as a saved run owned by
// endpoint, with a pending cleanup and members this build does not model.
func savedCleanupCase(t *testing.T, endpoint string, edit func(map[string]any)) (configPath, reportPath string, input []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "verification", "agent-demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	saved := exactJSON(t, data).(map[string]any)
	defaults := investigation.DefaultConfig()
	saved["connection"] = map[string]any{"endpoint": endpoint, "user_id": defaults.UserID, "namespace": defaults.Namespace}
	saved["cleanup"] = map[string]any{"sandbox_deleted": false, "session_deleted": false, "errors": []any{"sandbox cleanup: earlier failure"}, "task_canceled": true}
	saved["reviewer_notes"] = map[string]any{"text": "written by a newer build", "count": json.Number("9007199254740993")}
	saved["experiments"].([]any)[0].(map[string]any)["duration_ms"] = json.Number("1234")
	if edit != nil {
		edit(saved)
	}
	directory := t.TempDir()
	configPath = filepath.Join(directory, "config.json")
	config, err := json.Marshal(map[string]any{"endpoint": endpoint, "token_env": "TRIAGELAB_CLEANUP_TEST_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIAGELAB_CLEANUP_TEST_TOKEN", "")
	runDirectory := filepath.Join(directory, "run")
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if input, err = json.MarshalIndent(saved, "", "  "); err != nil {
		t.Fatal(err)
	}
	reportPath = filepath.Join(runDirectory, "report.json")
	if err := os.WriteFile(reportPath, input, 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, reportPath, input
}

func TestCleanupRewritesOnlyTheCleanupRecord(t *testing.T) {
	server, tools := fakeKAgent(t)
	configPath, reportPath, input := savedCleanupCase(t, server.URL+"/mcp", nil)
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"cleanup", "--config", configPath, "--report", reportPath}, &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got := tools(); !reflect.DeepEqual(got, []string{"delete_session", "delete_sandbox"}) {
		t.Fatalf("tool calls %v", got)
	}
	written, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	want := exactJSON(t, input).(map[string]any)
	cleanup := want["cleanup"].(map[string]any)
	cleanup["sandbox_deleted"], cleanup["session_deleted"], cleanup["errors"] = true, true, []any{}
	if got := exactJSON(t, written); !reflect.DeepEqual(got, any(want)) {
		t.Fatalf("cleanup changed more than its record:\n%s", written)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(reportPath), "report.md")); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRefusesReportSchemaItDoesNotWrite(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"newer":   func(r map[string]any) { r["schema_version"] = 2 },
		"missing": func(r map[string]any) { delete(r, "schema_version") },
	} {
		t.Run(name, func(t *testing.T) {
			server, tools := fakeKAgent(t)
			configPath, reportPath, input := savedCleanupCase(t, server.URL+"/mcp", edit)
			var out, stderr bytes.Buffer
			if code := run(context.Background(), []string{"cleanup", "--config", configPath, "--report", reportPath}, &out, &stderr); code != 2 {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "schema_version") || len(tools()) != 0 {
				t.Fatalf("stderr %q, tool calls %v", stderr.String(), tools())
			}
			if written, err := os.ReadFile(reportPath); err != nil || !bytes.Equal(written, input) {
				t.Fatalf("refused report was modified: %v", err)
			}
		})
	}
}
