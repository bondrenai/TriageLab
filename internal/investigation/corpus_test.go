package investigation

// These corpus checks need no deployment and never execute fixture code.
// Scoring tests use report fixtures and the preserved live report; they check
// the harness, not a new investigation.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

func TestCorpusManifestCasesIngest(t *testing.T) {
	corpus, err := loadCorpus(corpusManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(corpus.Cases, func(c corpusCase) bool { return c.Name == "config-bug" }) {
		t.Fatal("the corpus must keep the config-bug baseline")
	}
	manifest, err := filepath.Abs(corpusManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range corpus.Cases {
		dir, err := filepath.Abs(filepath.Join(corpusExamplesDir, entry.Name))
		if err != nil {
			t.Fatal(err)
		}
		// The whole case directory is uploaded; expectations must stay outside it.
		if relative, err := filepath.Rel(dir, manifest); err != nil || !strings.HasPrefix(relative, "..") {
			t.Errorf("%s: manifest would be uploaded with the case", entry.Name)
		}
		description, err := os.ReadFile(filepath.Join(dir, "issue.md"))
		if err != nil {
			t.Errorf("%s: %v", entry.Name, err)
			continue
		}
		c, err := casefile.Ingest(string(description), dir, nil, nil)
		if err != nil {
			t.Errorf("%s: ingestion refused the case: %v", entry.Name, err)
			continue
		}
		if len(c.Attachments) < 2 {
			t.Errorf("%s: expected the issue and at least one reproduction file, got %d files", entry.Name, len(c.Attachments))
		}
		for _, attachment := range c.Attachments {
			if filepath.Base(attachment.Path) == filepath.Base(corpusManifestPath) {
				t.Errorf("%s: uploads a file named like the manifest: %s", entry.Name, attachment.Path)
			}
		}
	}
}

func TestCorpusManifestIsStrict(t *testing.T) {
	valid := `{"version":1,"cases":[{"name":"case-one","purpose":"p","outcomes":["reproduced"],"rationale":"r",` +
		`"experiments":[{"label":"l","exit":"zero","output_contains":["ok"]}]}]}`
	if _, err := decodeCorpus(strings.NewReader(valid)); err != nil {
		t.Fatalf("valid manifest refused: %v", err)
	}
	duplicate := `{"version":1,"cases":[{"name":"case-one","purpose":"p","outcomes":["reproduced"],"rationale":"r"},` +
		`{"name":"case-one","purpose":"p","outcomes":["inconclusive"],"rationale":"r"}]}`
	for _, test := range []struct{ name, manifest, want string }{
		{"unknown field", strings.Replace(valid, `"rationale":"r"`, `"rationale":"r","expected":"x"`, 1), "unknown field"},
		{"trailing data", valid + `{}`, "one JSON object"},
		{"string boolean", strings.Replace(valid, `"rationale":"r"`, `"rationale":"r","missing_information":"yes"`, 1), "cannot unmarshal"},
		{"wrong version", strings.Replace(valid, `"version":1`, `"version":2`, 1), "version"},
		{"no cases", `{"version":1,"cases":[]}`, "no cases"},
		{"unknown outcome", strings.Replace(valid, `["reproduced"]`, `["fixed"]`, 1), "not an investigation outcome"},
		{"no outcomes", strings.Replace(valid, `["reproduced"]`, `[]`, 1), "accepts no outcome"},
		{"repeated outcome", strings.Replace(valid, `["reproduced"]`, `["reproduced","reproduced"]`, 1), "repeats outcome"},
		{"duplicate case", duplicate, "duplicate corpus case"},
		{"traversal name", strings.Replace(valid, `"case-one"`, `"../case-one"`, 1), "one lowercase directory name"},
		{"nested name", strings.Replace(valid, `"case-one"`, `"examples/case-one"`, 1), "one lowercase directory name"},
		{"outcome in name", strings.Replace(valid, `"case-one"`, `"not-reproduced-case"`, 1), "must not name an outcome"},
		{"missing purpose", strings.Replace(valid, `"purpose":"p"`, `"purpose":" "`, 1), "purpose and a rationale"},
		{"missing rationale", strings.Replace(valid, `"rationale":"r"`, `"rationale":""`, 1), "purpose and a rationale"},
		{"bad exit", strings.Replace(valid, `"exit":"zero"`, `"exit":"1"`, 1), "exit must be zero or nonzero"},
		{"empty output match", strings.Replace(valid, `["ok"]`, `[]`, 1), "nonblank output_contains"},
		{"blank output match", strings.Replace(valid, `["ok"]`, `[" "]`, 1), "nonblank output_contains"},
		{"unlabelled matcher", strings.Replace(valid, `"label":"l"`, `"label":""`, 1), "unlabelled"},
	} {
		if _, err := decodeCorpus(strings.NewReader(test.manifest)); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: want error containing %q, got %v", test.name, test.want, err)
		}
	}
}

func TestCorpusExpectedOutcomesComeFromSchema(t *testing.T) {
	outcomes := corpusOutcomes()
	for _, want := range []string{"reproduced", "not_reproduced", "inconclusive"} {
		if !slices.Contains(outcomes, want) {
			t.Fatalf("schema outcomes %v lack %q", outcomes, want)
		}
	}
	corpus, err := loadCorpus(corpusManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range corpus.Cases {
		for _, outcome := range entry.Outcomes {
			if !slices.Contains(outcomes, outcome) {
				t.Errorf("%s expects %q, which the output schema does not allow", entry.Name, outcome)
			}
		}
	}
}

func TestCorpusSelection(t *testing.T) {
	corpus := corpusManifest{Version: 1, Cases: []corpusCase{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}}
	names := func(cases []corpusCase) []string {
		result := []string{}
		for _, c := range cases {
			result = append(result, c.Name)
		}
		return result
	}
	all, err := corpus.selectCases("all")
	if err != nil || !slices.Equal(names(all), []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("empty selection: %v %v", names(all), err)
	}
	smoke := corpusManifest{Cases: []corpusCase{{Name: "config-bug"}, {Name: "other-case"}}}
	selected, err := smoke.selectCases("  ")
	if err != nil || len(selected) != 1 || selected[0].Name != "config-bug" {
		t.Fatalf("default selection: %v, %v", selected, err)
	}
	some, err := corpus.selectCases(" gamma, alpha,gamma ")
	if err != nil || !slices.Equal(names(some), []string{"alpha", "gamma"}) {
		t.Fatalf("subset selection: %v %v", names(some), err)
	}
	if _, err := corpus.selectCases("alpha,delta"); err == nil || !strings.Contains(err.Error(), "delta") {
		t.Fatalf("unknown case accepted: %v", err)
	}
	if _, err := corpus.selectCases(" , "); err == nil {
		t.Fatal("blank selection accepted")
	}
}

func corpusCaseNamed(t *testing.T, name string) corpusCase {
	t.Helper()
	corpus, err := loadCorpus(corpusManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(corpus.Cases, func(c corpusCase) bool { return c.Name == name })
	if index < 0 {
		t.Fatalf("corpus case %s missing", name)
	}
	return corpus.Cases[index]
}

// The preserved November 8 live run must still satisfy the baseline's expectations.
func TestCorpusScoresPreservedLiveReport(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "verification", "agent-demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string]any
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatal(err)
	}
	if misses := corpusScoreReport(corpusCaseNamed(t, "config-bug"), recorded, nil); len(misses) != 0 {
		t.Fatalf("preserved live report missed the baseline: %q", misses)
	}
}

func corpusFixtureReport() map[string]any {
	return map[string]any{
		"status": "completed", "outcome": "reproduced",
		"sandbox": map[string]any{"id": "sandbox-1"},
		"analysis": map[string]any{
			"root_cause":          map[string]any{"statement": "s", "evidence_process_ids": []any{"proc-1"}},
			"missing_information": []any{},
		},
		"experiments": []map[string]any{
			{"process_id": "proc-1", "command": []string{"python3", "deliver.py"}, "state": "PROCESS_STATE_EXITED", "exit_code": 1, "stdout": "attempt 1: HTTP 503\n", "stderr": "delivery failed after 1 attempt(s)\n", "truncated": false},
			{"process_id": "proc-2", "command": []string{"python3", "deliver.py", "--config", "fixed.json"}, "state": "PROCESS_STATE_EXITED", "exit_code": 0, "stdout": "Delivered after 3 attempt(s)\n", "stderr": "", "truncated": false},
		},
		"tool_calls": []map[string]any{
			{"id": "call-1", "name": "read_sandbox_file", "args": map[string]any{"sandbox_id": "sandbox-1", "path": "/data/workspace/README.md"}},
			{"id": "call-2", "name": "start_sandbox_process", "args": map[string]any{"sandbox_id": "sandbox-1", "command": []string{"python3", "deliver.py"}}},
		},
		"cleanup": map[string]any{"sandbox_deleted": true, "session_deleted": true},
	}
}

func TestCorpusScoringCollectsEveryMiss(t *testing.T) {
	expect := corpusCase{
		Name: "fixture", Outcomes: []string{"reproduced"}, RootCauseEvidence: true,
		Experiments: []corpusMatcher{
			{Label: "failure", CommandContains: "DELIVER.PY", Exit: "nonzero", OutputContains: []string{"failed after 1 attempt"}},
			{Label: "correction", Exit: "zero", OutputContains: []string{"delivered after 3"}},
		},
	}
	score := func(mutate func(map[string]any), runErr error) []string {
		t.Helper()
		result := corpusFixtureReport()
		if mutate != nil {
			mutate(result)
		}
		canonical, err := corpusCanonical(result)
		if err != nil {
			t.Fatal(err)
		}
		return corpusScoreReport(expect, canonical, runErr)
	}
	if misses := score(nil, nil); len(misses) != 0 {
		t.Fatalf("matching report missed: %q", misses)
	}
	contains := func(misses []string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !slices.ContainsFunc(misses, func(m string) bool { return strings.Contains(m, w) }) {
				t.Errorf("misses %q lack %q", misses, w)
			}
		}
	}
	// A wrong outcome, an unverified correction, failed cleanup and both tool
	// boundary violations are all reported together.
	misses := score(func(r map[string]any) {
		r["outcome"] = "inconclusive"
		r["experiments"].([]map[string]any)[1]["truncated"] = true
		r["cleanup"] = map[string]any{"sandbox_deleted": true, "session_deleted": false}
		r["tool_calls"] = append(r["tool_calls"].([]map[string]any),
			map[string]any{"id": "call-3", "name": "read_file", "args": map[string]any{"path": "README.md"}},
			map[string]any{"id": "call-4", "name": "start_sandbox_process", "args": map[string]any{"sandbox_id": "01a1c0de-5ba7-7e11-8b17-00000000c0de"}})
	}, nil)
	contains(misses, `outcome "inconclusive"`, `"correction"`, "session deletion was not confirmed", `"read_file"`, "1 tool call(s) targeted a sandbox")
	if len(misses) != 5 {
		t.Errorf("want exactly 5 misses, got %q", misses)
	}
	// A watchdog stop is a miss even when the partial report looks plausible;
	// its tool audit and cleanup are still scored.
	misses = score(func(r map[string]any) {
		r["status"] = "error"
		r["cleanup"] = map[string]any{"sandbox_deleted": false, "session_deleted": true}
		r["tool_calls"] = append(r["tool_calls"].([]map[string]any), map[string]any{"id": "call-3", "name": "write_file", "args": map[string]any{}})
	}, errors.New("investigator used a tool outside the configured tool set"))
	contains(misses, "run error: investigator used a tool outside", `run status "error"`, "sandbox deletion was not confirmed", `"write_file"`)
	if len(misses) != 4 {
		t.Errorf("a stopped run should not also score its unvalidated analysis: %q", misses)
	}
	contains(score(func(r map[string]any) {
		r["analysis"] = map[string]any{"root_cause": map[string]any{"evidence_process_ids": []any{}}}
	}, nil), "root cause has no evidence")
	contains(score(func(r map[string]any) {
		r["experiments"].([]map[string]any)[0]["command"] = []string{"python3", "-c", "print('failed after 1 attempt')"}
	}, nil), `"failure"`)
	contains(score(func(r map[string]any) { r["sandbox"] = map[string]any{}; r["cleanup"] = map[string]any{} }, nil),
		"sandbox deletion was not confirmed", "session deletion was not confirmed")
	expect.MissingInformation = true
	contains(score(nil, nil), "missing_information is empty")
	// Nothing was created before an infrastructure refusal, so there is nothing to clean up;
	// an uncertain creation still requires confirmed deletion.
	refused := errors.New("create sandbox: refused")
	if misses := corpusScoreReport(expect, map[string]any{"status": "error"}, refused); len(misses) != 2 {
		t.Errorf("refused run: %q", misses)
	}
	contains(corpusScoreReport(expect, map[string]any{"status": "error", "creation_uncertain": true}, refused), "sandbox deletion was not confirmed")
}

func TestCorpusScoresPausedRunAfterClosingResources(t *testing.T) {
	expect := corpusCase{Outcomes: []string{"inconclusive"}, MissingInformation: true}
	saved := map[string]any{
		"status": "paused", "outcome": "inconclusive",
		"analysis": map[string]any{"missing_information": []any{"customer logs"}},
		"cleanup":  map[string]any{"sandbox_deleted": true, "session_deleted": true},
	}
	if misses := corpusScoreReport(expect, saved, nil); len(misses) != 0 {
		t.Fatalf("cleaned-up pause missed: %q", misses)
	}
	saved["cleanup"] = nil
	if misses := corpusScoreReport(expect, saved, nil); len(misses) != 2 {
		t.Fatalf("expected both cleanup misses, got %q", misses)
	}
}

func TestCorpusScorecardIsPrivateAndRedacted(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "reports")
	card := corpusScorecard{
		GeneratedAt: "2026-11-09T12:00:00Z", Agent: "triagelab-investigator", Total: 1,
		Cases: []corpusScore{{Case: "config-bug", Expected: []string{"reproduced"}, Status: "miss", Misses: []string{"run error: rejected credential example-secret-value"}}},
	}
	path, err := writeCorpusScorecard(directory, card, "example-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "corpus-scorecard-20261109T120000Z.json" {
		t.Fatalf("scorecard name %s", path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("scorecard permissions: %v %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "example-secret-value") {
		t.Fatal("scorecard retained a configured credential")
	}
	var decoded corpusScorecard
	if err := json.Unmarshal(data, &decoded); err != nil || len(decoded.Cases) != 1 || decoded.Cases[0].Case != "config-bug" {
		t.Fatalf("scorecard did not round-trip: %v %+v", err, decoded)
	}
	if table := corpusTable(decoded.Cases); !strings.Contains(table, "config-bug") || !strings.Contains(table, "miss") {
		t.Fatalf("table omitted the case: %s", table)
	}
}
