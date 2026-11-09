package kagent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

const partType = "kagent.dev/a2a/part-type"
const outputSchema = "kagent.dev/a2a/output-schema-sha256"
const thought = "kagent.dev/a2a/thought"

// ToolCall is a paired canonical Go ADK function call and response. Response
// remains raw provenance; MCP structured content is nested under output.
type ToolCall struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Args     map[string]any `json:"args"`
	Response map[string]any `json:"response"`
	Pending  bool           `json:"pending"`
}

// ProcessStart records a process actually started by the agent. Its state and
// output must still be read directly from the sandbox service.
type ProcessStart struct {
	CallID    string            `json:"call_id"`
	ProcessID string            `json:"process_id"`
	Command   []string          `json:"command"`
	CWD       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
}

func trustedEntries(items []map[string]any) ([]map[string]any, error) {
	var entries []map[string]any
	for _, item := range items {
		if nonzero(item["omitted_bytes"]) {
			return nil, fmt.Errorf("incomplete durable transcript item")
		}
		if message, ok := item["message"].(map[string]any); ok {
			// Imported A2A user parts can contain arbitrary forged metadata.
			if message["role"] == "ROLE_AGENT" {
				entries = append(entries, message)
			}
		} else if artifact, ok := item["artifact"].(map[string]any); ok {
			entries = append(entries, artifact)
		}
	}
	return entries, nil
}

// ExtractToolCalls pairs canonical function events by ID. It ignores all user
// messages and deduplicates identical message/artifact copies. Contradictory
// duplicates or orphan results fail closed.
func ExtractToolCalls(items []map[string]any) ([]ToolCall, error) {
	entries, err := trustedEntries(items)
	if err != nil {
		return nil, err
	}
	calls := map[string]ToolCall{}
	results := map[string]ToolCall{}
	var order []string
	for _, entry := range entries {
		parts, ok := entry["parts"].([]any)
		if !ok && entry["parts"] != nil {
			return nil, fmt.Errorf("invalid A2A content parts")
		}
		for _, value := range parts {
			part, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid A2A part")
			}
			meta, _ := part["metadata"].(map[string]any)
			kind, _ := meta[partType].(string)
			if kind != "function_call" && kind != "function_response" {
				continue
			}
			data, ok := part["data"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("canonical tool activity has no structured data")
			}
			id, _ := data["id"].(string)
			name, _ := data["name"].(string)
			if id == "" || name == "" {
				return nil, fmt.Errorf("canonical tool activity is missing ID or name")
			}
			activity := ToolCall{ID: id, Name: name}
			if kind == "function_call" {
				activity.Args, ok = data["args"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("canonical tool arguments are invalid")
				}
				if old, exists := calls[id]; exists {
					if !reflect.DeepEqual(old, activity) {
						return nil, fmt.Errorf("conflicting durable tool call for same ID")
					}
				} else {
					calls[id] = activity
					order = append(order, id)
				}
			} else {
				activity.Response, ok = data["response"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("canonical tool response is invalid")
				}
				if old, exists := results[id]; exists && !reflect.DeepEqual(old, activity) {
					return nil, fmt.Errorf("conflicting durable tool response for same ID")
				}
				results[id] = activity
			}
		}
	}
	for id, result := range results {
		call, exists := calls[id]
		if !exists {
			return nil, fmt.Errorf("durable tool response has no matching call")
		}
		if call.Name != result.Name {
			return nil, fmt.Errorf("durable tool response name differs from call")
		}
	}
	paired := make([]ToolCall, 0, len(order))
	for _, id := range order {
		call := calls[id]
		result, exists := results[id]
		call.Pending, call.Response = !exists, result.Response
		paired = append(paired, call)
	}
	return paired, nil
}

// ProcessStarts validates every sandbox-targeted call's scope, including failed
// and pending calls. Only the pinned native MCP output wrapper proves a start.
func ProcessStarts(calls []ToolCall, sandboxID string) ([]ProcessStart, error) {
	starts := []ProcessStart{}
	seen := map[string]bool{}
	for _, call := range calls {
		if target, present := call.Args["sandbox_id"]; present && target != sandboxID {
			return nil, fmt.Errorf("agent attempted sandbox access outside this investigation")
		}
		if call.Name != "start_sandbox_process" {
			continue
		}
		if call.Args["sandbox_id"] != sandboxID {
			return nil, fmt.Errorf("agent process call has no investigation sandbox")
		}
		command, ok := stringList(call.Args["command"])
		if !ok || len(command) == 0 || command[0] == "" {
			return nil, fmt.Errorf("agent command is not an argv array")
		}
		for _, argument := range command {
			if strings.ContainsRune(argument, 0) {
				return nil, fmt.Errorf("agent command contains NUL")
			}
		}
		cwd := "/data/workspace"
		if raw, present := call.Args["cwd"]; present && raw != "" {
			cwd, ok = raw.(string)
			if !ok {
				return nil, fmt.Errorf("agent process cwd is invalid")
			}
		}
		env := map[string]string{}
		if raw, present := call.Args["env"]; present {
			values, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("agent process environment is invalid")
			}
			for key, value := range values {
				text, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("agent process environment must contain strings")
				}
				env[key] = text
			}
		}
		if call.Pending {
			continue
		}
		if _, failed := call.Response["error"]; failed {
			continue
		}
		output, ok := call.Response["output"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("process response missing upstream structured output")
		}
		pid, _ := output["process_id"].(string)
		if pid == "" || len(pid) > 256 || !printable(pid, false) {
			return nil, fmt.Errorf("process response has no valid process ID")
		}
		if seen[pid] {
			return nil, fmt.Errorf("distinct tool calls claim the same process ID")
		}
		seen[pid] = true
		starts = append(starts, ProcessStart{CallID: call.ID, ProcessID: pid, Command: command, CWD: cwd, Env: env})
	}
	return starts, nil
}

func stringList(value any) ([]string, bool) {
	if values, ok := value.([]string); ok {
		return values, true
	}
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	strings := make([]string, len(values))
	for i, value := range values {
		var ok bool
		strings[i], ok = value.(string)
		if !ok {
			return nil, false
		}
	}
	return strings, true
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// AgentReply returns the last final answer or canonical enforced JSON output,
// excluding thought/tool parts. It remains a model claim, never execution proof.
func AgentReply(items []map[string]any) string {
	entries, err := trustedEntries(items)
	if err != nil {
		return ""
	}
	lastText, lastStructured := "", ""
	for _, entry := range entries {
		parts, _ := entry["parts"].([]any)
		var text strings.Builder
		for _, value := range parts {
			part, ok := value.(map[string]any)
			if !ok {
				continue
			}
			meta, _ := part["metadata"].(map[string]any)
			if meta[thought] == true || meta[partType] != nil {
				continue
			}
			digest, _ := meta[outputSchema].(string)
			mediaType, _ := part["mediaType"].(string)
			data, hasData := part["data"].(map[string]any)
			if strings.EqualFold(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]), "application/json") && digestPattern.MatchString(digest) && hasData {
				encoded, err := json.Marshal(data)
				if err == nil {
					lastStructured = string(encoded)
				}
			} else if value, ok := part["text"].(string); ok {
				text.WriteString(value)
			}
		}
		if strings.TrimSpace(text.String()) != "" {
			lastText = text.String()
		}
	}
	if lastStructured != "" {
		return lastStructured
	}
	return lastText
}
