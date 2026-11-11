package resultchat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContextBoundsAndCitationsMatchIncludedEvidence(t *testing.T) {
	record := savedReport()
	record["round"] = 12
	var prior []any
	for round := 1; round <= 12; round++ {
		experiments := []any{}
		for index := 0; index < 32; index++ {
			experiments = append(experiments, map[string]any{
				"process_id": fmt.Sprintf("process-%d", index), "state": "PROCESS_STATE_EXITED", "exit_code": 1,
				"stdout": strings.Repeat("evidence ", 1024), "stderr": strings.Repeat("failure ", 1024),
				"outputs_complete": true, "truncated": true, "timed_out": false,
			})
		}
		data := map[string]any{"round": round, "experiments": experiments, "analysis": map[string]any{"summary": strings.Repeat("hypothesis ", 1024)}}
		if round == 12 {
			record["experiments"], record["analysis"] = experiments, data["analysis"]
		} else {
			prior = append(prior, data)
		}
	}
	record["rounds"] = prior
	before, _ := json.Marshal(record)
	encoded, allowed, err := buildContext(record, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxContextBytes {
		t.Fatalf("context exceeded limit: %d", len(encoded))
	}
	var projection map[string]any
	if err := json.Unmarshal(encoded, &projection); err != nil {
		t.Fatal(err)
	}
	rounds := projection["rounds"].([]any)
	if len(rounds) != 8 || projection["omitted_rounds"] != float64(4) {
		t.Fatalf("wrong round selection: %d", len(rounds))
	}
	observed := map[Citation]bool{}
	omitted := 0
	for _, raw := range rounds {
		round := raw.(map[string]any)
		if round["model_analysis_context_truncated"] != true {
			t.Fatal("missing analysis truncation notice")
		}
		omitted += int(round["omitted_experiments"].(float64))
		for _, item := range round["experiments"].([]any) {
			experiment := item.(map[string]any)
			citation := Citation{Round: int(round["round"].(float64)), ProcessID: experiment["process_id"].(string)}
			observed[citation] = true
			if !allowed[citation] || experiment["stdout_context_truncated"] != true || experiment["outputs_complete"] != true || experiment["truncated"] != true {
				t.Fatal("lost source attribution or completeness limits")
			}
		}
	}
	if omitted == 0 || len(observed) == 0 || len(observed) != len(allowed) {
		t.Fatal("citation catalog includes omitted evidence")
	}
	if allowed[Citation{Round: 1, ProcessID: "process-0"}] {
		t.Fatal("omitted historical source remained citeable")
	}
	after, _ := json.Marshal(record)
	if !bytes.Equal(before, after) {
		t.Fatal("context preparation mutated canonical report")
	}
}

func TestContextRejectsAmbiguousRoundAndProcessAttribution(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"duplicate round":          func(r map[string]any) { r["rounds"] = append(r["rounds"].([]any), r["rounds"].([]any)[0]) },
		"historical current round": func(r map[string]any) { r["rounds"].([]any)[0].(map[string]any)["round"] = 2 },
		"fractional round":         func(r map[string]any) { r["round"] = 2.5 },
		"null round":               func(r map[string]any) { r["round"] = nil },
		"duplicate process": func(r map[string]any) {
			r["experiments"] = append(r["experiments"].([]any), r["experiments"].([]any)[0])
		},
		"missing process":     func(r map[string]any) { delete(r["experiments"].([]any)[0].(map[string]any), "process_id") },
		"invalid experiments": func(r map[string]any) { r["experiments"] = "not records" },
	} {
		t.Run(name, func(t *testing.T) {
			record := savedReport()
			change(record)
			if _, _, err := buildContext(record, 0, ""); err == nil {
				t.Fatal("accepted ambiguous report attribution")
			}
		})
	}
}

func TestLegacyReportWithoutRoundUsesRoundOneAndOnlyObservedProcesses(t *testing.T) {
	record := savedReport()
	delete(record, "round")
	delete(record, "rounds")
	record["analysis"].(map[string]any)["reproduced_process_ids"] = []any{"invented-model-reference"}
	_, allowed, err := buildContext(record, 0, "")
	if err != nil || !allowed[Citation{Round: 1, ProcessID: "same-process"}] || len(allowed) != 1 {
		t.Fatalf("legacy report attribution failed: %#v %v", allowed, err)
	}
}

func TestExcerptKeepsUTF8Boundaries(t *testing.T) {
	text, truncated := excerpt("ééé", 5)
	if !truncated || text != "éé" || !utf8.ValidString(text) {
		t.Fatalf("invalid excerpt %q", text)
	}
}
