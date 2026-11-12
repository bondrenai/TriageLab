package investigation

import (
	"encoding/json"
	"testing"
)

func validAnalysis() map[string]any {
	return map[string]any{
		"outcome": "reproduced", "summary": "Failure observed; correction verified.", "observations": []any{"Original failed; override passed."},
		"hypotheses": []any{map[string]any{
			"statement": "Wrong configured units", "status": "supported", "evidence_process_ids": []any{"failed", "corrected"},
			"contradicting_process_ids": []any{}, "distinguishing_observation": "An explicit timeout in milliseconds should succeed without changing the endpoint.",
		}},
		"experiment_assessments": []any{map[string]any{
			"process_id": "corrected", "question": "Does an explicit timeout in milliseconds avoid the symptom?",
			"expected_observation": "The client receives a healthy response.", "interpretation": "The override succeeded in the sandbox, supporting a units mismatch.",
		}},
		"reproduced_process_ids": []any{"failed"}, "root_cause": map[string]any{"statement": "Wrong configured units", "evidence_process_ids": []any{"failed", "corrected"}},
		"reproduction_steps": []any{"python3 app.py", "python3 app.py --timeout-ms 5000"}, "next_actions": []any{"Correct configuration"}, "missing_information": []any{},
	}
}
func analysisText() string   { data, _ := json.Marshal(validAnalysis()); return string(data) }
func exitCode(code int) *int { return &code }
func validExperiments() []Experiment {
	return []Experiment{
		{ProcessID: "failed", State: "PROCESS_STATE_EXITED", ExitCode: exitCode(1), OutputsComplete: true, Output: &Output{}},
		{ProcessID: "corrected", State: "PROCESS_STATE_EXITED", ExitCode: exitCode(0), OutputsComplete: true, Output: &Output{}},
	}
}

func TestSupportedAnalysisRetainsReferences(t *testing.T) {
	value, warnings, err := ValidateAnalysis(analysisText(), validExperiments())
	if err != nil || value["outcome"] != "reproduced" || len(warnings) != 0 {
		t.Fatalf("value=%v warnings=%v err=%v", value, warnings, err)
	}
	if assessment := value["experiment_assessments"].([]any)[0].(map[string]any); assessment["process_id"] != "corrected" || assessment["expected_observation"] != "The client receives a healthy response." {
		t.Fatalf("experiment reasoning lost its source: %v", assessment)
	}
}

func TestRetainedInvestigatorCanReturnOriginalAnalysisShape(t *testing.T) {
	input := validAnalysis()
	delete(input, "experiment_assessments")
	for _, raw := range input["hypotheses"].([]any) {
		hypothesis := raw.(map[string]any)
		delete(hypothesis, "contradicting_process_ids")
		delete(hypothesis, "distinguishing_observation")
	}
	encoded, _ := json.Marshal(input)
	value, warnings, err := ValidateAnalysis(string(encoded), validExperiments())
	if err != nil || value["outcome"] != "reproduced" || len(warnings) != 0 {
		t.Fatalf("retained investigator's answer was rejected: value=%v warnings=%v err=%v", value, warnings, err)
	}
	if assessments, ok := value["experiment_assessments"].([]any); !ok || len(assessments) != 0 {
		t.Fatal("older answer was not normalized to empty experiment assessments")
	}
	hypothesis := value["hypotheses"].([]any)[0].(map[string]any)
	if refs, ok := hypothesis["contradicting_process_ids"].([]any); !ok || len(refs) != 0 || hypothesis["distinguishing_observation"] != "" {
		t.Fatal("older answer was not normalized to empty explanatory metadata")
	}
}

func TestIncompleteExperimentsCannotSupportConclusions(t *testing.T) {
	// A non-integral exit code cannot be recorded: observe refuses it (see
	// TestExitedProcessNeedsAnIntegralExitCode), so here it is simply absent.
	for _, change := range []func(*Experiment){
		func(e *Experiment) { e.Output.Truncated = true }, func(e *Experiment) { e.OutputsComplete = false },
		func(e *Experiment) { e.TimedOut = true },
		func(e *Experiment) { e.State = "PROCESS_STATE_RUNNING" }, func(e *Experiment) { e.ExitCode = nil },
	} {
		records := validExperiments()
		change(&records[0])
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

func TestContradictingEvidenceRequiresCompleteCurrentRoundProcesses(t *testing.T) {
	for _, condition := range []string{"complete", "unavailable", "incomplete", "truncated", "timed_out"} {
		t.Run(condition, func(t *testing.T) {
			input := validAnalysis()
			hypothesis := input["hypotheses"].([]any)[0].(map[string]any)
			hypothesis["contradicting_process_ids"] = []any{"contradiction"}
			records := validExperiments()
			record := Experiment{ProcessID: "contradiction", State: "PROCESS_STATE_EXITED", ExitCode: exitCode(0), OutputsComplete: true}
			switch condition {
			case "incomplete":
				record.OutputsComplete = false
			case "truncated":
				record.Output = &Output{Truncated: true}
			case "timed_out":
				record.TimedOut = true
			}
			if condition != "unavailable" {
				records = append(records, record)
			}
			encoded, _ := json.Marshal(input)
			value, warnings, err := ValidateAnalysis(string(encoded), records)
			if err != nil {
				t.Fatal(err)
			}
			hypothesis = value["hypotheses"].([]any)[0].(map[string]any)
			if condition == "complete" {
				if hypothesis["status"] != "supported" || len(hypothesis["contradicting_process_ids"].([]any)) != 1 || len(warnings) != 0 {
					t.Fatalf("complete contradiction reference was not preserved: %v %v", hypothesis, warnings)
				}
			} else if hypothesis["status"] != "untested" || len(hypothesis["contradicting_process_ids"].([]any)) != 0 || len(warnings) == 0 {
				t.Fatalf("invalid contradiction supported a claim: %v %v", hypothesis, warnings)
			}
			if value["outcome"] != "reproduced" {
				t.Fatal("invalid hypothesis reference changed an independently supported reproduction outcome")
			}
		})
	}
}

func TestExperimentAssessmentsExcludeUnavailableIncompleteAndDuplicateEvidence(t *testing.T) {
	input := validAnalysis()
	assessments := input["experiment_assessments"].([]any)
	for _, id := range []string{"prior-round-process", "incomplete", "corrected", ""} {
		assessments = append(assessments, map[string]any{
			"process_id": id, "question": "Did the test distinguish the cause?",
			"expected_observation": "", "interpretation": "This explanation must not be attached to unavailable evidence.",
		})
	}
	input["experiment_assessments"] = assessments
	records := append(validExperiments(), Experiment{ProcessID: "incomplete", State: "PROCESS_STATE_EXITED", ExitCode: exitCode(0), OutputsComplete: false})
	encoded, _ := json.Marshal(input)
	value, warnings, err := ValidateAnalysis(string(encoded), records)
	if err != nil {
		t.Fatal(err)
	}
	kept := value["experiment_assessments"].([]any)
	if len(kept) != 1 || kept[0].(map[string]any)["process_id"] != "corrected" || len(warnings) != 4 {
		t.Fatalf("unsupported or duplicate assessment survived: %v, warnings=%v", kept, warnings)
	}
	// The typed Experiment has no members for model assessments, so analysis
	// cannot write its interpretation into collected process evidence.
	if records[0].ProcessID != "failed" || records[1].ProcessID != "corrected" || records[2].OutputsComplete {
		t.Fatal("collected process evidence changed during analysis")
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
	records[0].ExitCode = exitCode(0)
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
		func(v map[string]any) { v["experiment_assessments"] = nil },
		func(v map[string]any) { v["hypotheses"] = []any{nil} },
		func(v map[string]any) { delete(v["hypotheses"].([]any)[0].(map[string]any), "status") },
		func(v map[string]any) { v["hypotheses"].([]any)[0].(map[string]any)["contradicting_process_ids"] = nil },
		func(v map[string]any) {
			v["hypotheses"].([]any)[0].(map[string]any)["distinguishing_observation"] = nil
		},
		func(v map[string]any) {
			v["hypotheses"].([]any)[0].(map[string]any)["contradicting_process_ids"] = []any{123}
		},
		func(v map[string]any) {
			v["hypotheses"].([]any)[0].(map[string]any)["distinguishing_observation"] = true
		},
		func(v map[string]any) {
			v["experiment_assessments"].([]any)[0].(map[string]any)["stdout"] = "forged output"
		},
		func(v map[string]any) {
			v["experiment_assessments"].([]any)[0].(map[string]any)["interpretation"] = 123
		},
	} {
		value := validAnalysis()
		mutate(value)
		data, _ := json.Marshal(value)
		if _, _, err := ValidateAnalysis(string(data), validExperiments()); err == nil {
			t.Fatal("invalid schema accepted")
		}
	}
}
