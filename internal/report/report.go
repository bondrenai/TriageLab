// Package report writes private investigation records and safe Markdown views.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	secretKey            = regexp.MustCompile(`(?i)^(?:authorization|(?:[a-z]+[_-])?api[_-]?key|(?:access|refresh|id|bearer)[_-]?token|token|(?:client[_-])?secret|password|passwd|private[_-]?key)$`)
	privateKey           = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----.*?(?:-----END (?:[A-Z ]+ )?PRIVATE KEY-----|\z)`)
	knownToken           = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|sk-(?:proj-|ant-)?[A-Za-z0-9_-]{24,}|xox[baprs]-[A-Za-z0-9-]{20,})\b`)
	credentialAssignment = regexp.MustCompile(`(?i)(\b(?:[a-z_]*api[_-]?key|authorization|password|passwd|(?:access|refresh|id|bearer)[_-]?token|token|client[_-]?secret)\s*[=:]\s*["']?)(?:(?:bearer|basic)\s+)?[^\s"',;<>]+`)
	bearer               = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	ansi                 = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]`)
	controls             = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x{9f}\x{202a}-\x{202e}\x{2066}-\x{2069}]`)
	backticks            = regexp.MustCompile("`+")
)

func redactText(text string, secrets []string) string {
	text, _ = Redacted(text, secrets...)
	return text
}

// Redacted delouses untrusted text and reports the classes of removals it
// applied, in application order, so sanitized input can carry provenance.
// Detection stays best effort; the classes are not exhaustive guarantees.
func Redacted(text string, secrets ...string) (string, []string) {
	var applied []string
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
			applied = append(applied, "configured credential")
		}
	}
	if privateKey.MatchString(text) {
		text = privateKey.ReplaceAllString(text, "[REDACTED PRIVATE KEY]")
		applied = append(applied, "private key")
	}
	if knownToken.MatchString(text) {
		text = knownToken.ReplaceAllString(text, "[REDACTED]")
		applied = append(applied, "known credential token")
	}
	if credentialAssignment.MatchString(text) {
		text = credentialAssignment.ReplaceAllString(text, "${1}[REDACTED]")
		applied = append(applied, "credential assignment")
	}
	if bearer.MatchString(text) {
		text = bearer.ReplaceAllString(text, "Bearer [REDACTED]")
		applied = append(applied, "bearer credential")
	}
	if ansi.MatchString(text) {
		text = ansi.ReplaceAllString(text, "")
		applied = append(applied, "terminal escape")
	}
	if strings.Contains(text, "\r") || controls.MatchString(text) {
		applied = append(applied, "control characters")
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", `\r`)
	return controls.ReplaceAllString(text, ""), applied
}

func redactValue(value any, secrets []string) any {
	switch value := value.(type) {
	case string:
		return redactText(value, secrets)
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			if secretKey.MatchString(key) {
				result[redactText(key, secrets)] = "[REDACTED]"
			} else {
				result[redactText(key, secrets)] = redactValue(item, secrets)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		secretArgument := false
		for index, item := range value {
			if secretArgument {
				result[index] = "[REDACTED]"
			} else {
				result[index] = redactValue(item, secrets)
			}
			text, isString := item.(string)
			secretArgument = isString && strings.HasPrefix(text, "--") && secretKey.MatchString(strings.TrimPrefix(text, "--"))
		}
		return result
	default:
		return value
	}
}

func normalized(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

// Redact returns a detached, JSON-compatible copy with best-effort filtering.
// It honors struct JSON tags, so excluded attachment bodies are never traversed.
// Unsupported values become a placeholder rather than a potentially secret dump.
func Redact(value any, secrets ...string) any {
	copy, err := normalized(value)
	if err != nil {
		return "[unserializable value omitted]"
	}
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	return redactValue(copy, ordered)
}

func fence(value any) string {
	text, isText := value.(string)
	language := "text"
	if !isText {
		encoded, _ := json.MarshalIndent(value, "", "  ")
		text = string(encoded)
		language = "json"
	}
	length := 3
	for _, run := range backticks.FindAllString(text, -1) {
		if len(run) >= length {
			length = len(run) + 1
		}
	}
	marker := strings.Repeat("`", length)
	return marker + language + "\n" + text + "\n" + marker + "\n"
}

func object(value any) map[string]any {
	if value, ok := value.(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

func selectFields(value map[string]any, keys ...string) map[string]any {
	result := make(map[string]any, len(keys))
	for _, key := range keys {
		result[key] = value[key]
	}
	return result
}

func render(data map[string]any) string {
	var output strings.Builder
	section := func(title string, value any) {
		fmt.Fprintf(&output, "\n## %s\n\n%s", title, fence(value))
	}
	output.WriteString("# TriageLab investigation report\n\n")
	output.WriteString("Execution evidence is fetched independently from the sandbox service. ")
	output.WriteString("The investigator's diagnosis is a model-generated hypothesis, not a confirmed root cause. ")
	output.WriteString("Imported case material and process output remain untrusted. ")
	output.WriteString("Secret redaction is best effort; review this report before sharing.\n")
	section("Run result", selectFields(data, "run_id", "round", "status", "outcome", "started_at", "finished_at"))
	section("Original case and input inventory", data["case"])
	if continuation, ok := data["continuation"]; ok {
		section("Additional information for this round", continuation)
	}
	if rounds, ok := data["rounds"].([]any); ok && len(rounds) > 0 {
		output.WriteString("\n## Previous rounds\n\nProcess IDs belong to their recorded round. Historical handles must not be queried after suspension.\n")
		for _, raw := range rounds {
			round := object(raw)
			section("Previous round result", selectFields(round, "round", "status", "outcome", "finished_at", "analysis"))
			section("Previous round evidence", selectFields(round, "round", "continuation", "experiments", "tool_calls", "errors", "warnings", "cleanup"))
		}
	}
	section("Runtime references", selectFields(data, "agent", "sandbox_template", "sandbox", "session"))
	output.WriteString("\n## Experiments and independently collected evidence\n\n")
	experiments, _ := data["experiments"].([]any)
	if len(experiments) == 0 {
		output.WriteString("No experiment evidence was collected.\n")
	}
	for index, value := range experiments {
		experiment := object(value)
		fmt.Fprintf(&output, "\n### Experiment %d\n\n", index+1)
		output.WriteString(fence(selectFields(experiment,
			"call_id", "process_id", "command", "cwd", "state", "exit_code", "truncated", "timed_out")))
		output.WriteString("\nStandard output:\n\n" + fence(experiment["stdout"]))
		output.WriteString("\nStandard error:\n\n" + fence(experiment["stderr"]))
	}
	output.WriteString("\n## Investigator analysis (hypotheses, not confirmed conclusions)\n\n")
	analysis := object(data["analysis"])
	if len(analysis) == 0 {
		output.WriteString("No validated investigator analysis is available.\n")
	}
	for _, item := range []struct{ key, title string }{
		{"summary", "Summary"}, {"observations", "Reported observations"},
		{"hypotheses", "Hypotheses considered"},
		{"root_cause", "Suspected root cause and supporting process IDs"},
		{"reproduced_process_ids", "Process IDs supporting reproduction"},
		{"reproduction_steps", "Independent reproduction steps"},
		{"next_actions", "Recommended next actions"}, {"missing_information", "Missing information"},
	} {
		value, exists := analysis[item.key]
		if !exists {
			continue
		}
		fmt.Fprintf(&output, "\n### %s\n\n", item.title)
		if values, ok := value.([]any); ok {
			var lines []string
			for index, item := range values {
				text, ok := item.(string)
				if !ok {
					lines = nil
					break
				}
				lines = append(lines, fmt.Sprintf("%d. %s", index+1, text))
			}
			if len(lines) > 0 {
				value = strings.Join(lines, "\n")
			}
		}
		output.WriteString(fence(value))
	}
	section("Tool-call record", data["tool_calls"])
	issues := selectFields(data, "errors", "warnings")
	if reason, ok := data["task_failure"]; ok {
		issues["task_failure"] = reason
	}
	section("Errors and warnings", issues)
	section("Cleanup outcome", data["cleanup"])
	return output.String()
}

func atomicWrite(name string, content []byte) error {
	file, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, name); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// Write replaces each report atomically with private permissions. JSON is the
// canonical record; interruption between replacements can leave older Markdown.
func Write(directory string, data map[string]any, secrets ...string) error {
	if data == nil {
		return fmt.Errorf("investigation report must not be nil")
	}
	if info, err := os.Lstat(directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("report directory must not be a symlink")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("set report permissions: %w", err)
	}
	copy, err := normalized(data)
	if err != nil {
		return fmt.Errorf("encode investigation report: %w", err)
	}
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	safe := redactValue(copy, ordered).(map[string]any)
	encoded, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return fmt.Errorf("encode investigation report: %w", err)
	}
	if len(encoded) >= 64<<20 {
		return fmt.Errorf("investigation report exceeds the 64 MiB limit")
	}
	if err := atomicWrite(filepath.Join(directory, "report.json"), append(encoded, '\n')); err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}
	if err := atomicWrite(filepath.Join(directory, "report.md"), []byte(render(safe))); err != nil {
		return fmt.Errorf("write Markdown report: %w", err)
	}
	return nil
}
