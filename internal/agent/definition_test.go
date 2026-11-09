package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeImageAgentKeepsTheInvestigationContract(t *testing.T) {
	data, err := RenderDefinition("kagent", Definition{
		Name: "custom", SystemPrompt: "Prefer DNS checks.",
		Runtime: &RuntimeImage{
			Image:   "registry.example/custom@sha256:" + strings.Repeat("a", 64),
			Command: []string{"/app/agent"}, Args: []string{"--label", "one argument"},
		},
		MCPServers: []MCPServer{
			{Name: "existing-docs", Tools: []string{"lookup"}},
			{Name: "search", URL: "https://search.example/mcp", Protocol: "SSE", Tools: []string{"search_docs"}, SecretName: "mcp-auth", SecretKey: "header"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Items []struct {
			Kind     string
			Metadata map[string]string
			Spec     map[string]any
		}
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Items) != 3 || bundle.Items[0].Kind != "RemoteMCPServer" || bundle.Items[1].Kind != "Harness" || bundle.Items[2].Kind != "Agent" {
		t.Fatalf("incorrect resource set: %s", data)
	}
	server := bundle.Items[0]
	auth := server.Spec["headersFrom"].([]any)[0].(map[string]any)
	ref := auth["valueFrom"].(map[string]any)
	if server.Metadata["name"] != "custom-mcp-search" || server.Spec["protocol"] != "SSE" ||
		server.Spec["tokenPassthrough"] != nil || auth["name"] != "Authorization" ||
		ref["type"] != "Secret" || ref["name"] != "mcp-auth" || ref["key"] != "header" {
		t.Fatalf("unexpected MCP server: %v", server)
	}
	harness := bundle.Items[1]
	workload := harness.Spec["workload"].(map[string]any)
	if harness.Spec["byo"] == nil || workload["command"].([]any)[0] != "/app/agent" || workload["args"].([]any)[1] != "one argument" {
		t.Fatalf("unexpected custom harness: %v", harness)
	}
	spec := bundle.Items[2].Spec
	template := spec["template"].(map[string]any)["inline"].(map[string]any)
	prompt := template["systemPrompt"].(string)
	if spec["harnessRef"].(map[string]any)["name"] != "custom-runtime" || template["modelConfig"] != nil || template["outputSchema"] != nil ||
		!strings.Contains(prompt, "Prefer DNS checks.") || !strings.Contains(prompt, "reproduced_process_ids") {
		t.Fatalf("custom agent lost its runtime or report contract: %v", spec)
	}
	tools := template["tools"].([]any)
	for index, name := range []string{"triagelab-sandbox-api", "existing-docs", "custom-mcp-search"} {
		binding := tools[index].(map[string]any)["mcp"].(map[string]any)
		if binding["server"].(map[string]any)["name"] != name || len(binding["tools"].([]any)) == 0 {
			t.Fatalf("incorrect tool binding: %v", binding)
		}
	}
}

func TestDefinitionRejectsInvalidAndOverbroadConfiguration(t *testing.T) {
	for name, change := range map[string]func(*Definition){
		"reserved name":   func(d *Definition) { d.Name = "triagelab-investigator" },
		"missing model":   func(d *Definition) { d.ModelRef = "" },
		"ambiguous model": func(d *Definition) { d.Model = &Model{Provider: "OpenAI", Name: "example"} },
		"unpaired secret": func(d *Definition) {
			d.ModelRef = ""
			d.Model = &Model{Provider: "OpenAI", Name: "example", SecretName: "auth"}
		},
		"tag only":             func(d *Definition) { d.Runtime = &RuntimeImage{Image: "runtime:latest", Command: []string{"/agent"}} },
		"missing command":      func(d *Definition) { d.Runtime = &RuntimeImage{Image: "runtime@sha256:" + strings.Repeat("a", 64)} },
		"credentials in URL":   func(d *Definition) { d.MCPServers[0].URL = "https://user:secret@tools.example/mcp" },
		"all server tools":     func(d *Definition) { d.MCPServers[0].Tools = nil },
		"duplicate tools":      func(d *Definition) { d.MCPServers[0].Tools = []string{"lookup", "lookup"} },
		"spoofed sandbox tool": func(d *Definition) { d.MCPServers[0].Tools = []string{"start_sandbox_process"} },
		"builtin tool":         func(d *Definition) { d.MCPServers[0].Tools = []string{"read_file"} },
		"ambiguous generated server name": func(d *Definition) {
			d.MCPServers[0].Name = "ops-mcp-tools"
			d.MCPServers[0].URL = "https://tools.example/mcp"
		},
		"overlapping server delimiter": func(d *Definition) {
			d.MCPServers[0].Name = "mcp-tools"
			d.MCPServers[0].URL = "https://tools.example/mcp"
		},
		"reserved server": func(d *Definition) { d.MCPServers[0].Name = "triagelab-sandbox-api" },
	} {
		t.Run(name, func(t *testing.T) {
			d := Definition{Name: "example", ModelRef: "triagelab-model", MCPServers: []MCPServer{{Name: "docs", Tools: []string{"lookup"}}}}
			change(&d)
			if _, err := RenderDefinition("kagent", d); err == nil {
				t.Fatal("invalid definition accepted")
			}
		})
	}
}
