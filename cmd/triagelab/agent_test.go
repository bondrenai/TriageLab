package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentManifestRendersNativeResources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	definition := `{
		"name":"network-investigator","system_prompt":"Check network symptoms.",
		"model":{"provider":"OpenAI","name":"gateway-model","endpoint":"https://models.example/v1","secret_name":"model-key","secret_key":"API_KEY"},
		"mcp_servers":[{"name":"docs","url":"https://docs.example/mcp","tools":["search"]}]
	}`
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"agent-manifest", "--definition", path, "--namespace", "investigations"}, &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	var bundle struct {
		Items []struct {
			Kind     string
			Metadata map[string]string
			Spec     map[string]any
		}
	}
	if err := json.Unmarshal(out.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Items) != 3 || bundle.Items[0].Kind != "ModelConfig" || bundle.Items[1].Kind != "RemoteMCPServer" || bundle.Items[2].Kind != "Agent" {
		t.Fatalf("expected model, server, and agent: %s", &out)
	}
	for _, item := range bundle.Items {
		if item.Metadata["namespace"] != "investigations" {
			t.Fatalf("wrong namespace for %s: %v", item.Kind, item.Metadata)
		}
	}
	if bundle.Items[0].Spec["apiKeySecret"] != "model-key" || bundle.Items[0].Spec["openAI"].(map[string]any)["baseUrl"] != "https://models.example/v1" {
		t.Fatalf("incorrect model configuration: %v", bundle.Items[0].Spec)
	}
}

func TestInvestigateRejectsUnconfiguredManualAgent(t *testing.T) {
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"investigate", "--agent", "unlisted", "--description", "A case"}, &out, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "configured for manual investigations") {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
}
