package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
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
	out.Reset()
	if code := run(context.Background(), []string{"version"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "0.1.0") {
		t.Fatal("version missing")
	}
}
