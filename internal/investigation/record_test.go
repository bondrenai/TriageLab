package investigation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/kagent"
	"github.com/bondrenai/TriageLab/internal/report"
)

// jsonValue decodes JSON for comparison, keeping numbers exact.
func jsonValue(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func encodeJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func demoReport(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "verification", "agent-demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	return jsonValue(t, data).(map[string]any)
}

// withUnmodelledMembers adds members this build does not model at the top
// level and inside every TriageLab object, as a newer build might write.
func withUnmodelledMembers(record map[string]any) {
	record["reviewer_notes"] = map[string]any{"text": "from a newer build", "count": json.Number("9007199254740993")}
	obj(record["cleanup"])["task_canceled"] = true
	obj(record["case"])["priority"] = "high"
	obj(record["connection"])["cluster"] = "dev"
	obj(record["upstream"])["gateway"] = "v2"
	obj(record["sandbox"])["labels"] = map[string]any{"team": "triage"}
	experiment := obj(record["experiments"].([]any)[0])
	experiment["duration_ms"] = json.Number("1234")
	experiment["signal"] = nil
}

// Every member Run writes is modelled: decoding a saved report and encoding
// the typed fields alone, without what decoding preserved, reproduces it.
func TestRunReportsAreFullyModelled(t *testing.T) {
	uncertain := &kagent.Error{Kind: "timeout", Message: "uncertain"}
	for name, setup := range map[string]func(*fixtureBackend){
		"completed":           func(*fixtureBackend) {},
		"uncertain creations": func(f *fixtureBackend) { f.createErrors = []error{uncertain, uncertain}; f.sessionError = uncertain },
		"incomplete output": func(f *fixtureBackend) {
			f.failures["read_sandbox_outputs"] = repeated(&kagent.Error{Kind: "transport"}, readAttempts)
		},
		"killed process": func(f *fixtureBackend) { f.state, f.running = "TASK_STATE_WORKING", true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			setup(f)
			_, dir, _ := runFixture(t, f)
			saved, err := os.ReadFile(filepath.Join(dir, "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeReport(saved)
			if err != nil {
				t.Fatal(err)
			}
			for member := range decoded.loaded.members {
				if !jsonNames(reflect.TypeOf(Report{}))[member] {
					t.Errorf("Run wrote unmodelled member %q", member)
				}
			}
			decoded.loaded = preserved{}
			decoded.Cleanup.loaded = preserved{}
			if got, want := jsonValue(t, encodeJSON(t, decoded)), jsonValue(t, saved); !reflect.DeepEqual(got, want) {
				t.Fatalf("typed round trip changed the report:\n got %s\nwant %s", encodeJSON(t, got), saved)
			}
		})
	}
}

// Rewriting a saved report keeps what this build does not model and what it
// did not change, including nulls and absent members, and changes only what
// cleanup recorded.
func TestCleanupRewriteKeepsEveryMemberItDidNotChange(t *testing.T) {
	original := demoReport(t)
	withUnmodelledMembers(original)
	delete(original, "upstream")
	obj(original["experiments"].([]any)[1])["state"] = nil
	obj(original["cleanup"])["sandbox_deleted"] = false
	obj(original["cleanup"])["session_deleted"] = false
	obj(original["cleanup"])["errors"] = []any{"sandbox cleanup: deletion still pending; retry with saved report IDs"}
	input := encodeJSON(t, original)

	decoded, err := DecodeReport(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonValue(t, encodeJSON(t, decoded)); !reflect.DeepEqual(got, jsonValue(t, input)) {
		t.Fatalf("an unchanged report did not encode as read:\n got %s\nwant %s", encodeJSON(t, got), input)
	}

	investigator := Investigator{Client: newFixture(), Config: fixtureConfig()}
	investigator.Cleanup(context.Background(), decoded)
	directory := t.TempDir()
	if err := report.Write(directory, decoded); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := jsonValue(t, input).(map[string]any)
	cleanup := obj(want["cleanup"])
	cleanup["sandbox_deleted"], cleanup["session_deleted"], cleanup["errors"] = true, true, []any{}
	if got := jsonValue(t, written); !reflect.DeepEqual(got, any(want)) {
		t.Fatalf("rewrite changed more than the cleanup record:\n got %s\nwant %s", written, encodeJSON(t, want))
	}
}

func TestDecodeReportRefusesSchemaVersionsItDoesNotWrite(t *testing.T) {
	valid := encodeJSON(t, demoReport(t))
	if _, err := DecodeReport(valid); err != nil {
		t.Fatalf("version %d refused: %v", SchemaVersion, err)
	}
	for name, version := range map[string]any{"newer": 2, "zero": 0, "string": "1", "fraction": 1.5, "null": nil, "missing": "<missing>"} {
		t.Run(name, func(t *testing.T) {
			record := demoReport(t)
			record["schema_version"] = version
			if version == "<missing>" {
				delete(record, "schema_version")
			}
			// A newer layout may also change the types of known members.
			record["status"] = map[string]any{"phase": "completed"}
			_, err := DecodeReport(encodeJSON(t, record))
			if !errors.Is(err, ErrUnsupportedSchema) || !strings.Contains(err.Error(), "schema_version") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	// A version 1 report whose TriageLab members have the wrong types is not
	// rewritten either, but it is invalid rather than of another version.
	mistyped := demoReport(t)
	mistyped["status"] = 5
	for name, data := range map[string][]byte{
		"not an object": []byte(`[]`), "null": []byte(`null`), "trailing data": append(valid, []byte(`{}`)...),
		"wrong member type": encodeJSON(t, mistyped),
	} {
		if _, err := DecodeReport(data); err == nil || errors.Is(err, ErrUnsupportedSchema) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func TestOutcomesAreTheOutputSchemaEnum(t *testing.T) {
	properties, _ := agent.Schema()["properties"].(map[string]any)
	outcome, _ := properties["outcome"].(map[string]any)
	var schema []string
	for _, value := range outcome["enum"].([]any) {
		schema = append(schema, value.(string))
	}
	ours := []string{string(OutcomeReproduced), string(OutcomeNotReproduced), string(OutcomeInconclusive)}
	slices.Sort(schema)
	slices.Sort(ours)
	if !slices.Equal(schema, ours) {
		t.Fatalf("schema outcomes %v, Outcome constants %v", schema, ours)
	}
}
