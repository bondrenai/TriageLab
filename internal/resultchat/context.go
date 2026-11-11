package resultchat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bondrenai/TriageLab/internal/report"
)

const maxContextBytes = 128 << 10
const maxContextRounds = 8

// buildContext projects known report fields into detached, bounded data. Newest
// rounds receive evidence first. Only included process records become citeable;
// model hypotheses, tool-call arguments and arbitrary report keys never do.
func buildContext(saved map[string]any, omittedHistory int, key string) ([]byte, map[Citation]bool, error) {
	if saved == nil {
		return nil, nil, fmt.Errorf("result chat requires a saved report")
	}
	current := 1
	if raw, exists := saved["round"]; exists {
		var ok bool
		current, ok = roundNumber(raw)
		if !ok {
			return nil, nil, fmt.Errorf("saved report has an invalid round")
		}
	}
	prior, ok := array(saved["rounds"])
	if !ok || len(prior) > 31 {
		return nil, nil, fmt.Errorf("saved report has invalid round history")
	}
	type sourceRound struct {
		number int
		data   map[string]any
	}
	rounds := []sourceRound{{current, saved}}
	seenRounds := map[int]bool{current: true}
	for _, raw := range prior {
		data, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("saved report has invalid round history")
		}
		number, ok := roundNumber(data["round"])
		if !ok || number >= current || seenRounds[number] {
			return nil, nil, fmt.Errorf("saved report has ambiguous round history")
		}
		seenRounds[number] = true
		rounds = append(rounds, sourceRound{number, data})
	}
	sort.Slice(rounds, func(i, j int) bool { return rounds[i].number > rounds[j].number })
	omittedRounds := 0
	if len(rounds) > maxContextRounds {
		omittedRounds = len(rounds) - maxContextRounds
		rounds = rounds[:maxContextRounds]
	}
	root := map[string]any{
		"current_round":            current,
		"omitted_rounds":           omittedRounds,
		"omitted_history_messages": omittedHistory,
		"context_policy":           "Newest eight rounds at most; known report fields only; bounded excerpts. Omitted or truncated material is unknown, not absent. Model analysis is interpretation. Independently collected process records are evidence, with completeness limits preserved.",
	}
	textField(root, "run_id", saved["run_id"], 256, key)
	caseData, _ := saved["case"].(map[string]any)
	textField(root, "case_description", caseData["description"], 4096, key)
	views := make([]any, 0, len(rounds))
	for _, round := range rounds {
		view := map[string]any{"round": round.number, "experiments": []any{}, "omitted_experiments": 0}
		textField(view, "status", round.data["status"], 128, key)
		textField(view, "outcome", round.data["outcome"], 128, key)
		continuation, _ := round.data["continuation"].(map[string]any)
		textField(view, "additional_information", continuation["description"], 2048, key)
		analysis, _ := round.data["analysis"].(map[string]any)
		budget := valueBudget{bytes: 3072, nodes: 160, key: key}
		selected := map[string]any{}
		for _, name := range []string{"summary", "root_cause", "missing_information", "hypotheses", "observations", "experiment_assessments", "reproduced_process_ids", "reproduction_steps", "next_actions"} {
			if value, exists := analysis[name]; exists {
				selected[name] = budget.copy(value, 0)
			}
		}
		view["model_analysis"] = selected
		view["model_analysis_context_truncated"] = budget.truncated
		for _, name := range []string{"errors", "warnings"} {
			budget := valueBudget{bytes: 512, nodes: 20, key: key}
			view[name] = budget.copy(round.data[name], 0)
			if budget.truncated {
				view[name+"_context_truncated"] = true
			}
		}
		views = append(views, view)
	}
	root["rounds"] = views
	initial, _ := json.Marshal(root)
	if len(initial) > maxContextBytes-4096 {
		return nil, nil, fmt.Errorf("saved report summary exceeds result chat context bounds")
	}
	used := len(initial)
	allowed := map[Citation]bool{}
	for i, round := range rounds {
		experiments, ok := array(round.data["experiments"])
		if !ok || len(experiments) > 32 {
			return nil, nil, fmt.Errorf("saved report has invalid experiment records")
		}
		view := views[i].(map[string]any)
		seen := map[string]bool{}
		included := []any{}
		for _, raw := range experiments {
			evidence, ok := raw.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("saved report has invalid experiment records")
			}
			id, _ := evidence["process_id"].(string)
			redactedID, _ := report.Redacted(id, key)
			if id == "" || len(id) > 256 || strings.TrimSpace(id) != id || redactedID != id || seen[id] {
				return nil, nil, fmt.Errorf("saved report has ambiguous process evidence")
			}
			seen[id] = true
			projection := map[string]any{"process_id": id}
			textField(projection, "state", evidence["state"], 128, key)
			textField(projection, "cwd", evidence["cwd"], 256, key)
			textField(projection, "stdout", evidence["stdout"], 4096, key)
			textField(projection, "stderr", evidence["stderr"], 4096, key)
			budget := valueBudget{bytes: 1024, nodes: 40, key: key}
			projection["command"] = budget.copy(evidence["command"], 0)
			if budget.truncated {
				projection["command_context_truncated"] = true
			}
			for _, name := range []string{"exit_code", "outputs_complete", "truncated", "timed_out"} {
				if value, exists := evidence[name]; exists && scalar(value) {
					projection[name] = value
				}
			}
			encoded, _ := json.Marshal(projection)
			if used+len(encoded)+2 > maxContextBytes-4096 {
				continue
			}
			used += len(encoded) + 2
			included = append(included, projection)
			allowed[Citation{Round: round.number, ProcessID: id}] = true
		}
		view["experiments"] = included
		view["omitted_experiments"] = len(experiments) - len(included)
	}
	encoded, err := json.Marshal(root)
	if err != nil || len(encoded) > maxContextBytes {
		return nil, nil, fmt.Errorf("could not prepare bounded result chat context")
	}
	return encoded, allowed, nil
}

func roundNumber(value any) (int, bool) {
	var result int
	switch number := value.(type) {
	case int:
		result = number
	case int64:
		if number < 1 || number > 32 {
			return 0, false
		}
		result = int(number)
	case float64:
		if number < 1 || number > 32 || number != float64(int(number)) {
			return 0, false
		}
		result = int(number)
	case json.Number:
		n, err := number.Int64()
		if err != nil || n < 1 || n > 32 {
			return 0, false
		}
		result = int(n)
	default:
		return 0, false
	}
	return result, result >= 1 && result <= 32
}

func array(value any) ([]any, bool) {
	switch items := value.(type) {
	case nil:
		return nil, true
	case []any:
		return items, true
	case []map[string]any:
		result := make([]any, len(items))
		for i, item := range items {
			result[i] = item
		}
		return result, true
	case []string:
		result := make([]any, len(items))
		for i, item := range items {
			result[i] = item
		}
		return result, true
	default:
		return nil, false
	}
}

func textField(destination map[string]any, name string, value any, limit int, key string) {
	text, ok := value.(string)
	if !ok {
		return
	}
	text, _ = report.Redacted(text, key)
	text, truncated := excerpt(text, limit)
	destination[name] = text
	if truncated {
		destination[name+"_context_truncated"] = true
	}
}

func excerpt(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end], true
}

type valueBudget struct {
	bytes     int
	nodes     int
	key       string
	truncated bool
}

func (b *valueBudget) copy(value any, depth int) any {
	if value == nil {
		return nil
	}
	if b.nodes <= 0 || b.bytes <= 0 || depth > 6 {
		b.truncated = true
		return nil
	}
	b.nodes--
	switch data := value.(type) {
	case string:
		text, _ := report.Redacted(data, b.key)
		limit := min(b.bytes, 1024)
		text, truncated := excerpt(text, limit)
		b.bytes -= len(text)
		b.truncated = b.truncated || truncated
		return text
	case map[string]any:
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 16 {
			keys = keys[:16]
			b.truncated = true
		}
		result := map[string]any{}
		for _, key := range keys {
			redactedKey, _ := report.Redacted(key, b.key)
			if len(key) > 80 || redactedKey != key {
				b.truncated = true
				continue
			}
			if b.nodes <= 0 || b.bytes <= 0 {
				b.truncated = true
				break
			}
			result[key] = b.copy(data[key], depth+1)
		}
		return result
	default:
		if items, ok := array(value); ok {
			if len(items) > 16 {
				items = items[:16]
				b.truncated = true
			}
			result := []any{}
			for _, item := range items {
				if b.nodes <= 0 || b.bytes <= 0 {
					b.truncated = true
					break
				}
				result = append(result, b.copy(item, depth+1))
			}
			return result
		}
		if scalar(value) {
			return value
		}
		b.truncated = true
		return nil
	}
}

func scalar(value any) bool {
	switch value.(type) {
	case bool, int, int64, float64, json.Number:
		return true
	default:
		return false
	}
}
