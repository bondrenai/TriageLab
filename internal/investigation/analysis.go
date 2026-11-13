package investigation

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bondrenai/TriageLab/internal/agent"
)

// ValidateAnalysis validates the model's claim and references, not the truth of its causal reasoning.
func ValidateAnalysis(text string, experiments []Experiment) (map[string]any, []string, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "```")
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, nil, fmt.Errorf("agent did not return valid JSON")
	}
	// Retained sessions keep the earlier investigator prompt and output schema.
	// Normalize only absent explanatory fields; malformed supplied values still
	// fail the same strict contract as answers from an updated investigator.
	if value != nil {
		if _, present := value["experiment_assessments"]; !present {
			value["experiment_assessments"] = []any{}
		}
		hypotheses, _ := value["hypotheses"].([]any)
		for _, raw := range hypotheses {
			if hypothesis, ok := raw.(map[string]any); ok {
				if _, present := hypothesis["contradicting_process_ids"]; !present {
					hypothesis["contradicting_process_ids"] = []any{}
				}
				if _, present := hypothesis["distinguishing_observation"]; !present {
					hypothesis["distinguishing_observation"] = ""
				}
			}
		}
	}
	if err := validateValue(value, agent.Schema()); err != nil {
		return nil, nil, err
	}
	verified := map[string]bool{}
	for _, e := range experiments {
		truncated := e.Output != nil && e.Output.Truncated
		if e.ProcessID != "" && e.exited() && e.OutputsComplete && !truncated && !e.TimedOut && e.ExitCode != nil {
			verified[e.ProcessID] = true
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
		references, _ := h["contradicting_process_ids"].([]any)
		contradictions := []any{}
		for _, reference := range references {
			if verified[str(reference)] {
				contradictions = append(contradictions, reference)
			}
		}
		if len(contradictions) != len(references) {
			h["status"] = "untested"
			h["contradicting_process_ids"] = contradictions
			warnings = append(warnings, "Hypothesis cited incomplete or unavailable contradicting evidence; references removed and marked untested")
		}
	}
	if provided, ok := value["experiment_assessments"].([]any); ok {
		assessments := []any{}
		assessed := map[string]bool{}
		for _, raw := range provided {
			assessment := raw.(map[string]any)
			id := str(assessment["process_id"])
			if !verified[id] {
				warnings = append(warnings, "Experiment assessment lacked complete current-round evidence; omitted")
				continue
			}
			if assessed[id] {
				warnings = append(warnings, "Duplicate experiment assessment omitted")
				continue
			}
			assessed[id] = true
			assessments = append(assessments, assessment)
		}
		value["experiment_assessments"] = assessments
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
		properties := schema["properties"].(map[string]any)
		for _, raw := range required {
			key := raw.(string)
			if _, ok := object[key]; !ok {
				return invalid()
			}
		}
		for key, child := range object {
			property, ok := properties[key]
			if !ok {
				return invalid()
			}
			if err := validateValue(child, property.(map[string]any)); err != nil {
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
