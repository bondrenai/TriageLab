package investigation

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bondrenai/TriageLab/internal/agent"
)

// ValidateAnalysis validates the model's claim and references, not the truth of its causal reasoning.
func ValidateAnalysis(text string, experiments []map[string]any) (map[string]any, []string, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "```")
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, nil, fmt.Errorf("agent did not return valid JSON")
	}
	if err := validateValue(value, agent.Schema()); err != nil {
		return nil, nil, err
	}
	verified := map[string]bool{}
	for _, e := range experiments {
		_, valid := integer(e["exit_code"])
		if str(e["state"]) == "PROCESS_STATE_EXITED" && e["truncated"] != true && e["timed_out"] != true && valid {
			verified[str(e["process_id"])] = true
		}
	}
	supported := func(v any) bool {
		refs, ok := v.([]any)
		if !ok || len(refs) == 0 {
			return false
		}
		for _, ref := range refs {
			if !verified[str(ref)] {
				return false
			}
		}
		return true
	}
	warnings := []string{}
	for _, raw := range value["hypotheses"].([]any) {
		h := raw.(map[string]any)
		if h["status"] != "untested" && !supported(h["evidence_process_ids"]) {
			h["status"] = "untested"
			warnings = append(warnings, "Hypothesis lacked complete evidence; marked untested")
		}
	}
	root := value["root_cause"].(map[string]any)
	if root["statement"] != "" && !supported(root["evidence_process_ids"]) {
		root["statement"] = "No supported root-cause hypothesis was established."
		root["evidence_process_ids"] = []any{}
		warnings = append(warnings, "Unsupported root-cause claim removed")
	}
	if (value["outcome"] == "reproduced" && !supported(value["reproduced_process_ids"])) || (value["outcome"] == "not_reproduced" && len(verified) == 0) {
		value["outcome"] = "inconclusive"
		value["reproduced_process_ids"] = []any{}
		value["summary"] = "Reproduction was not established by the captured execution evidence."
		warnings = append(warnings, "Outcome lacked complete experimental evidence; marked inconclusive")
	}
	return value, warnings, nil
}

func validateValue(value any, schema map[string]any) error {
	invalid := func() error { return fmt.Errorf("agent output does not match investigation schema") }
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return invalid()
		}
		required := schema["required"].([]any)
		if len(object) != len(required) {
			return invalid()
		}
		properties := schema["properties"].(map[string]any)
		for _, raw := range required {
			key := raw.(string)
			child, ok := object[key]
			if !ok {
				return invalid()
			}
			if err := validateValue(child, properties[key].(map[string]any)); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok || len(array) > 128 {
			return invalid()
		}
		for _, item := range array {
			if err := validateValue(item, schema["items"].(map[string]any)); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok || len(text) > 32768 {
			return invalid()
		}
	}
	if options, ok := schema["enum"].([]any); ok {
		found := false
		for _, option := range options {
			if option == value {
				found = true
			}
		}
		if !found {
			return invalid()
		}
	}
	return nil
}
