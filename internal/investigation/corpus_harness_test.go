package investigation

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/report"
)

// The evaluation corpus lives in examples/. Each case directory is uploaded to
// the sandbox whole and its name is shown to the investigator, so expectations
// live in a manifest beside the directories, never inside them.
var (
	corpusExamplesDir  = filepath.Join("..", "..", "examples")
	corpusManifestPath = filepath.Join(corpusExamplesDir, "corpus.json")
	corpusNamePattern  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type corpusManifest struct {
	Version int          `json:"version"`
	Cases   []corpusCase `json:"cases"`
}

// corpusCase holds one case's expectations. Every case additionally requires a
// completed run, deletion of both runtimes, and only sandbox tools aimed at the
// run's own sandbox.
type corpusCase struct {
	Name               string          `json:"name"`
	Purpose            string          `json:"purpose"`
	Outcomes           []string        `json:"outcomes"`
	Rationale          string          `json:"rationale"`
	RootCauseEvidence  bool            `json:"root_cause_evidence"`
	MissingInformation bool            `json:"missing_information"`
	Experiments        []corpusMatcher `json:"experiments"`
}

// corpusMatcher must match at least one independently collected experiment.
// Command and output matching are case-insensitive substring checks.
type corpusMatcher struct {
	Label           string   `json:"label"`
	CommandContains string   `json:"command_contains,omitempty"`
	Exit            string   `json:"exit"`
	OutputContains  []string `json:"output_contains"`
}

func loadCorpus(path string) (corpusManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return corpusManifest{}, err
	}
	defer file.Close()
	return decodeCorpus(io.LimitReader(file, 1<<20))
}

func decodeCorpus(r io.Reader) (corpusManifest, error) {
	var manifest corpusManifest
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return corpusManifest{}, fmt.Errorf("corpus manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return corpusManifest{}, fmt.Errorf("corpus manifest must contain one JSON object")
	}
	return manifest, manifest.validate()
}

// corpusOutcomes reads the permitted outcomes from the investigator's output schema.
func corpusOutcomes() []string {
	properties, _ := agent.Schema()["properties"].(map[string]any)
	outcome, _ := properties["outcome"].(map[string]any)
	values, _ := outcome["enum"].([]any)
	outcomes := []string{}
	for _, value := range values {
		if text, ok := value.(string); ok {
			outcomes = append(outcomes, text)
		}
	}
	return outcomes
}

func (m corpusManifest) validate() error {
	if m.Version != 1 {
		return fmt.Errorf("corpus manifest version must be 1")
	}
	if len(m.Cases) == 0 {
		return fmt.Errorf("corpus manifest has no cases")
	}
	outcomes := corpusOutcomes()
	if len(outcomes) == 0 {
		return fmt.Errorf("investigation schema defines no outcomes")
	}
	seen := map[string]bool{}
	for _, c := range m.Cases {
		if !corpusNamePattern.MatchString(c.Name) {
			return fmt.Errorf("corpus case %q must be one lowercase directory name under examples/", c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate corpus case %q", c.Name)
		}
		seen[c.Name] = true
		for _, outcome := range outcomes {
			if strings.Contains(strings.ReplaceAll(c.Name, "-", "_"), outcome) {
				return fmt.Errorf("corpus case %q: the directory name reaches the investigator and must not name an outcome", c.Name)
			}
		}
		if strings.TrimSpace(c.Purpose) == "" || strings.TrimSpace(c.Rationale) == "" {
			return fmt.Errorf("corpus case %q needs a purpose and a rationale", c.Name)
		}
		if len(c.Outcomes) == 0 {
			return fmt.Errorf("corpus case %q accepts no outcome", c.Name)
		}
		for i, outcome := range c.Outcomes {
			if !slices.Contains(outcomes, outcome) {
				return fmt.Errorf("corpus case %q: %q is not an investigation outcome", c.Name, outcome)
			}
			if slices.Contains(c.Outcomes[:i], outcome) {
				return fmt.Errorf("corpus case %q repeats outcome %q", c.Name, outcome)
			}
		}
		for _, matcher := range c.Experiments {
			if strings.TrimSpace(matcher.Label) == "" {
				return fmt.Errorf("corpus case %q has an unlabelled experiment matcher", c.Name)
			}
			if matcher.Exit != "zero" && matcher.Exit != "nonzero" {
				return fmt.Errorf("corpus case %q matcher %q: exit must be zero or nonzero", c.Name, matcher.Label)
			}
			if len(matcher.OutputContains) == 0 || slices.ContainsFunc(matcher.OutputContains, func(s string) bool { return strings.TrimSpace(s) == "" }) {
				return fmt.Errorf("corpus case %q matcher %q needs nonblank output_contains", c.Name, matcher.Label)
			}
		}
	}
	return nil
}

// selectCases returns the comma-separated cases in manifest order; empty selects all.
func (m corpusManifest) selectCases(spec string) ([]corpusCase, error) {
	if strings.TrimSpace(spec) == "" {
		return m.Cases, nil
	}
	names := make([]string, len(m.Cases))
	for i, c := range m.Cases {
		names[i] = c.Name
	}
	wanted := map[string]bool{}
	for _, name := range strings.Split(spec, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(names, name) {
			return nil, fmt.Errorf("unknown corpus case %q; available: %s", name, strings.Join(names, ", "))
		}
		wanted[name] = true
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("the case selection names no corpus case")
	}
	selected := []corpusCase{}
	for _, c := range m.Cases {
		if wanted[c.Name] {
			selected = append(selected, c)
		}
	}
	return selected, nil
}

func corpusString(v any) string         { s, _ := v.(string); return s }
func corpusObject(v any) map[string]any { m, _ := v.(map[string]any); return m }
func corpusList(v any) []any            { l, _ := v.([]any); return l }

// corpusCanonical converts a report (a *Report or a fixture map) to its JSON
// form, so scoring sees the same shape as a saved report.json regardless of
// in-memory Go types. A nil report scores as an empty one.
func corpusCanonical(result any) (map[string]any, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var canonical map[string]any
	if err := json.Unmarshal(encoded, &canonical); err != nil {
		return nil, err
	}
	if canonical == nil {
		canonical = map[string]any{}
	}
	return canonical, nil
}

// matches accepts only a verified experiment: exited, with an integral exit code
// and complete output, as the evidence validator requires.
func (m corpusMatcher) matches(experiment map[string]any) bool {
	code, ok := experiment["exit_code"].(float64)
	if !ok || code != math.Trunc(code) || corpusString(experiment["state"]) != "PROCESS_STATE_EXITED" ||
		experiment["truncated"] == true || experiment["timed_out"] == true {
		return false
	}
	if (m.Exit == "zero") != (code == 0) {
		return false
	}
	if m.CommandContains != "" {
		var command []string
		for _, argument := range corpusList(experiment["command"]) {
			command = append(command, corpusString(argument))
		}
		if !strings.Contains(strings.ToLower(strings.Join(command, " ")), strings.ToLower(m.CommandContains)) {
			return false
		}
	}
	output := strings.ToLower(corpusString(experiment["stdout"]) + "\n" + corpusString(experiment["stderr"]))
	for _, want := range m.OutputContains {
		if !strings.Contains(output, strings.ToLower(want)) {
			return false
		}
	}
	return true
}

// corpusScoreReport checks a canonical report against one case and returns
// every miss instead of stopping at the first. An empty result is a pass. A run
// that did not complete has no validated analysis, so only the stop itself,
// cleanup and the tool boundary are scored for it.
func corpusScoreReport(expect corpusCase, canonical map[string]any, runErr error) []string {
	misses := []string{}
	status := corpusString(canonical["status"])
	if runErr != nil {
		misses = append(misses, "run error: "+runErr.Error())
	}
	if status != "completed" {
		misses = append(misses, fmt.Sprintf("run status %q, want completed", status))
	}
	if runErr == nil && status == "completed" {
		if outcome := corpusString(canonical["outcome"]); !slices.Contains(expect.Outcomes, outcome) {
			misses = append(misses, fmt.Sprintf("outcome %q, want one of %s", outcome, strings.Join(expect.Outcomes, ", ")))
		}
		analysis := corpusObject(canonical["analysis"])
		if expect.RootCauseEvidence && len(corpusList(corpusObject(analysis["root_cause"])["evidence_process_ids"])) == 0 {
			misses = append(misses, "root cause has no evidence process IDs")
		}
		if expect.MissingInformation && len(corpusList(analysis["missing_information"])) == 0 {
			misses = append(misses, "missing_information is empty")
		}
		experiments := corpusList(canonical["experiments"])
		for _, matcher := range expect.Experiments {
			if !slices.ContainsFunc(experiments, func(e any) bool { return matcher.matches(corpusObject(e)) }) {
				misses = append(misses, fmt.Sprintf("no verified experiment matched %q (exit %s, output %q)", matcher.Label, matcher.Exit, matcher.OutputContains))
			}
		}
	}
	// A completed run always created both runtimes; a stopped run must have
	// deleted whatever it created or may have created.
	cleanup := corpusObject(canonical["cleanup"])
	for _, resource := range []struct{ kind, uncertain string }{{"sandbox", "creation_uncertain"}, {"session", "session_creation_uncertain"}} {
		created := status == "completed" || corpusString(corpusObject(canonical[resource.kind])["id"]) != "" || canonical[resource.uncertain] == true
		if created && cleanup[resource.kind+"_deleted"] != true {
			misses = append(misses, resource.kind+" deletion was not confirmed")
		}
	}
	return append(misses, corpusAuditToolCalls(canonical)...)
}

// corpusAuditToolCalls repeats the tool boundary independently of the run's
// watchdog: the harness configures no additional tools, so every call must be a
// sandbox tool, and any sandbox_id must be this run's sandbox.
func corpusAuditToolCalls(canonical map[string]any) []string {
	sandboxID := corpusString(corpusObject(canonical["sandbox"])["id"])
	foreignTools, foreignTargets := []string{}, 0
	for _, raw := range corpusList(canonical["tool_calls"]) {
		call := corpusObject(raw)
		name := corpusString(call["name"])
		if !agent.IsSandboxTool(name) && !slices.Contains(foreignTools, name) {
			foreignTools = append(foreignTools, name)
		}
		if target, present := corpusObject(call["args"])["sandbox_id"]; present && (sandboxID == "" || target != sandboxID) {
			foreignTargets++
		}
	}
	misses := []string{}
	if len(foreignTools) > 0 {
		misses = append(misses, fmt.Sprintf("investigator called tools outside the sandbox tool set: %q", foreignTools))
	}
	if foreignTargets > 0 {
		misses = append(misses, fmt.Sprintf("%d tool call(s) targeted a sandbox other than this run's", foreignTargets))
	}
	return misses
}

type corpusScore struct {
	Case            string   `json:"case"`
	Expected        []string `json:"expected"`
	Outcome         string   `json:"outcome"`
	Status          string   `json:"status"`
	RunStatus       string   `json:"run_status"`
	Experiments     int      `json:"experiments"`
	DurationSeconds float64  `json:"duration_seconds"`
	Report          string   `json:"report"`
	Misses          []string `json:"misses"`
}

type corpusScorecard struct {
	GeneratedAt string        `json:"generated_at"`
	Agent       string        `json:"agent"`
	Template    string        `json:"template"`
	KAgent      string        `json:"kagent"`
	Passed      int           `json:"passed"`
	Total       int           `json:"total"`
	Cases       []corpusScore `json:"cases"`
}

func corpusTable(scores []corpusScore) string {
	var out strings.Builder
	w := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CASE\tEXPECTED\tOUTCOME\tRUN\tSTATUS\tEXPERIMENTS\tDURATION\tREPORT")
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	for _, s := range scores {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%.1fs\t%s\n", s.Case, strings.Join(s.Expected, "|"), dash(s.Outcome), dash(s.RunStatus), s.Status, s.Experiments, s.DurationSeconds, dash(s.Report))
	}
	w.Flush()
	return out.String()
}

// writeCorpusScorecard saves a private, redacted scorecard next to the reports.
func writeCorpusScorecard(directory string, card corpusScorecard, secrets ...string) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(report.Redact(card, secrets...), "", "  ")
	if err != nil {
		return "", err
	}
	stamp, err := time.Parse(time.RFC3339, card.GeneratedAt)
	if err != nil {
		return "", fmt.Errorf("scorecard time: %w", err)
	}
	name := filepath.Join(directory, "corpus-scorecard-"+stamp.UTC().Format("20060102T150405Z")+".json")
	if err := os.WriteFile(name, append(encoded, '\n'), 0o600); err != nil {
		return "", err
	}
	return name, nil
}
