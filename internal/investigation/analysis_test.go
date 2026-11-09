package investigation

import (
	"encoding/json"
	"testing"
)

func validAnalysis() map[string]any {
	return map[string]any{
		"outcome": "reproduced", "summary": "Failure observed; correction verified.", "observations": []any{"Original failed; override passed."},
		"hypotheses":             []any{map[string]any{"statement": "Wrong configured units", "status": "supported", "evidence_process_ids": []any{"failed", "corrected"}}},
		"reproduced_process_ids": []any{"failed"}, "root_cause": map[string]any{"statement": "Wrong configured units", "evidence_process_ids": []any{"failed", "corrected"}},
		"reproduction_steps": []any{"python3 app.py", "python3 app.py --timeout-ms 5000"}, "next_actions": []any{"Correct configuration"}, "missing_information": []any{},
	}
}
func analysisText() string { data, _ := json.Marshal(validAnalysis()); return string(data) }
func validExperiments() []map[string]any {
	return []map[string]any{
		{"process_id": "failed", "state": "PROCESS_STATE_EXITED", "exit_code": 1, "truncated": false},
		{"process_id": "corrected", "state": "PROCESS_STATE_EXITED", "exit_code": 0, "truncated": false},
	}
}

func TestSupportedAnalysisRetainsReferences(t *testing.T) {
	value, warnings, err := ValidateAnalysis(analysisText(), validExperiments())
	if err != nil || value["outcome"] != "reproduced" || len(warnings) != 0 {
		t.Fatalf("value=%v warnings=%v err=%v", value, warnings, err)
	}
}

func TestIncompleteExperimentsCannotSupportConclusions(t *testing.T) {
	for _, change := range []map[string]any{{"truncated": true}, {"timed_out": true}, {"state": "PROCESS_STATE_RUNNING"}, {"exit_code": true}, {"exit_code": 1.5}} {
		records := validExperiments()
		for key, value := range change {
			records[0][key] = value
		}
		value, warnings, err := ValidateAnalysis(analysisText(), records)
		if err != nil || value["outcome"] != "inconclusive" || len(warnings) == 0 {
			t.Fatalf("invalid evidence accepted: value=%v warnings=%v err=%v", value, warnings, err)
		}
		if value["hypotheses"].([]any)[0].(map[string]any)["status"] != "untested" {
			t.Fatal("hypothesis not downgraded")
		}
		if len(value["root_cause"].(map[string]any)["evidence_process_ids"].([]any)) != 0 {
			t.Fatal("unsupported root cause retained")
		}
	}
}

func TestMissingEvidenceDowngradesReproductionAndNonReproduction(t *testing.T) {
	for _, outcome := range []string{"reproduced", "not_reproduced"} {
		input := validAnalysis()
		input["outcome"] = outcome
		data, _ := json.Marshal(input)
		value, warnings, err := ValidateAnalysis(string(data), nil)
		if err != nil || value["outcome"] != "inconclusive" || len(warnings) == 0 {
			t.Fatalf("value=%v warnings=%v err=%v", value, warnings, err)
		}
	}
}

func TestZeroExitCanBeEvidenceForLogicalDefect(t *testing.T) {
	records := validExperiments()
	records[0]["exit_code"] = 0
	value, _, err := ValidateAnalysis(analysisText(), records)
	if err != nil || value["outcome"] != "reproduced" {
		t.Fatalf("logical failures can exit 0: value=%v err=%v", value, err)
	}
}

func TestInvalidSchemaRejected(t *testing.T) {
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { delete(v, "summary") }, func(v map[string]any) { v["extra"] = "unsupported" },
		func(v map[string]any) { v["outcome"] = "confirmed" }, func(v map[string]any) { v["summary"] = 123 },
		func(v map[string]any) { v["observations"] = make([]any, 129) },
	} {
		value := validAnalysis()
		mutate(value)
		data, _ := json.Marshal(value)
		if _, _, err := ValidateAnalysis(string(data), validExperiments()); err == nil {
			t.Fatal("invalid schema accepted")
		}
	}
}
