// Package agent contains trusted configuration installed into KAgent, not an agent framework.
package agent

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

//go:embed prompt.txt schema.json resources.json
var assets embed.FS

// PromptSHA256 and SchemaSHA256 are hex SHA-256 digests of the investigator
// prompt and output schema files embedded in this binary. They identify this
// build's definitions only; a deployed Agent may have been rendered by another build.
func PromptSHA256() string { return embeddedDigest("prompt.txt") }
func SchemaSHA256() string { return embeddedDigest("schema.json") }

func embeddedDigest(name string) string {
	data, err := assets.ReadFile(name)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func Prompt() string { data, _ := assets.ReadFile("prompt.txt"); return string(data) }
func Schema() map[string]any {
	data, _ := assets.ReadFile("schema.json")
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		panic(err)
	}
	return value
}

var digestImage = regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`)

// Manifest renders reviewed v1alpha3 objects. It has no cluster side effects.
func Manifest(image string) ([]byte, error) {
	if image != "" && !digestImage.MatchString(image) {
		return nil, fmt.Errorf("workload image must be pinned by SHA-256 digest")
	}
	data, _ := assets.ReadFile("resources.json")
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	for _, raw := range value["items"].([]any) {
		item := raw.(map[string]any)
		spec, _ := item["spec"].(map[string]any)
		switch item["kind"] {
		case "AgentTemplate":
			spec["systemPrompt"] = Prompt()
			spec["outputSchema"] = Schema()
			spec["tools"] = []map[string]any{mcpBinding("triagelab-sandbox-api", sandboxTools)}
		case "SandboxTemplate":
			if image != "" {
				spec["workload"].(map[string]any)["image"] = image
			}
		}
	}
	return json.MarshalIndent(value, "", "  ")
}
