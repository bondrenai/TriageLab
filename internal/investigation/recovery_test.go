package investigation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

func savedRecoveryRecord(t *testing.T, sessionUncertain, sandboxUncertain bool) map[string]any {
	t.Helper()
	value := map[string]any{
		"session": map[string]any{}, "sandbox": map[string]any{},
		"session_creation_uncertain": sessionUncertain, "creation_uncertain": sandboxUncertain,
		"session_request": map[string]any{
			"agent":      map[string]any{"namespace": "kagent", "name": "triagelab-investigator"},
			"request_id": "original-session-request", "name": "triagelab-recovery",
		},
		"sandbox_request": map[string]any{
			"namespace": "kagent", "template": "triagelab-python", "ttl_seconds": 900,
			"request_id": "original-sandbox-request", "name": "triagelab-recovery",
		},
		"cleanup": map[string]any{"sandbox_deleted": false, "session_deleted": false, "errors": []string{}},
	}
	// Cleanup consumes a persisted JSON report, so test the types produced on reload.
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestCleanupRecoversBothUncertainCreationsFromSavedRequests(t *testing.T) {
	backend := newFixture()
	investigator := Investigator{Client: backend, Config: fixtureConfig()}
	saved := savedRecoveryRecord(t, true, true)
	sessionRequest, sandboxRequest := saved["session_request"], saved["sandbox_request"]
	investigator.Cleanup(context.Background(), saved)
	if saved["session_creation_uncertain"] != false || saved["creation_uncertain"] != false {
		t.Fatalf("creation uncertainty not resolved: %v", saved)
	}
	cleanup := obj(saved["cleanup"])
	if cleanup["session_deleted"] != true || cleanup["sandbox_deleted"] != true || len(cleanup["errors"].([]string)) != 0 {
		t.Fatalf("recovered resources not cleaned: %v", cleanup)
	}
	seen := map[string]bool{}
	for _, call := range backend.calls {
		seen[call.name] = true
		switch call.name {
		case "create_session":
			if !reflect.DeepEqual(call.args, sessionRequest) {
				t.Fatalf("session recovery changed its idempotency request: %v", call)
			}
			if _, present := call.args["message"]; present {
				t.Fatal("cleanup invoked the investigator")
			}
		case "create_sandbox":
			if !reflect.DeepEqual(call.args, sandboxRequest) {
				t.Fatalf("sandbox recovery changed its idempotency request: %v", call)
			}
		case "get_session", "delete_session":
			if call.args["session_id"] != "session" {
				t.Fatalf("accessed a different session: %v", call)
			}
		case "delete_sandbox":
			if call.args["sandbox_id"] != "sandbox" {
				t.Fatalf("deleted a different sandbox: %v", call)
			}
		default:
			t.Fatalf("unexpected cleanup operation: %v", call)
		}
	}
	for _, required := range []string{"create_session", "create_sandbox", "delete_session", "delete_sandbox"} {
		if !seen[required] {
			t.Fatalf("missing recovery operation %s", required)
		}
	}
	if backend.canceled {
		t.Fatal("cleanup canceled an already completed task")
	}
}

type orderedCleanupBackend struct {
	*fixtureBackend
	operations []string
}

func (backend *orderedCleanupBackend) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	backend.operations = append(backend.operations, name)
	return backend.fixtureBackend.CallTool(ctx, name, args)
}

func (backend *orderedCleanupBackend) CancelTask(ctx context.Context, session, task string) error {
	backend.operations = append(backend.operations, "cancel_task")
	return backend.fixtureBackend.CancelTask(ctx, session, task)
}

func TestCleanupRecoversWorkingTaskAndCancelsBeforeDeletion(t *testing.T) {
	for _, uncertainCreation := range []bool{false, true} {
		t.Run(strBool(uncertainCreation), func(t *testing.T) {
			backend := &orderedCleanupBackend{fixtureBackend: newFixture()}
			backend.state = "TASK_STATE_WORKING"
			investigator := Investigator{Client: backend, Config: fixtureConfig()}
			saved := savedRecoveryRecord(t, uncertainCreation, uncertainCreation)
			if !uncertainCreation {
				// Simulate a process killed after journaling runtime IDs, before its task ID.
				saved["session"] = map[string]any{"id": "session"}
				saved["sandbox"] = map[string]any{"id": "sandbox"}
			}
			investigator.Cleanup(context.Background(), saved)
			if obj(saved["session"])["task_id"] != "task" || !backend.canceled {
				t.Fatalf("working task not recovered and canceled: session=%v canceled=%v", saved["session"], backend.canceled)
			}
			positions := map[string]int{}
			for index, name := range backend.operations {
				positions[name] = index
			}
			lookup, foundLookup := positions["get_session"]
			cancel, foundCancel := positions["cancel_task"]
			deleteSession, foundSession := positions["delete_session"]
			deleteSandbox, foundSandbox := positions["delete_sandbox"]
			if !foundLookup || !foundCancel || !foundSession || !foundSandbox ||
				lookup >= cancel || cancel >= deleteSession || cancel >= deleteSandbox {
				t.Fatalf("working task was not canceled before runtime deletion: %v", backend.operations)
			}
			cleanup := obj(saved["cleanup"])
			if cleanup["session_deleted"] != true || cleanup["sandbox_deleted"] != true || len(cleanup["errors"].([]string)) != 0 {
				t.Fatalf("recovered working task cleanup incomplete: %v", cleanup)
			}
		})
	}
}

func TestCleanupRetainsUncertaintyWhenRecoveryFails(t *testing.T) {
	for _, resource := range []string{"session", "sandbox"} {
		t.Run(resource, func(t *testing.T) {
			backend := newFixture()
			uncertain := &kagent.Error{Kind: "transport", Message: "response lost"}
			if resource == "session" {
				backend.sessionError = uncertain
			} else {
				backend.createErrors = []error{uncertain}
			}
			investigator := Investigator{Client: backend, Config: fixtureConfig()}
			saved := savedRecoveryRecord(t, true, true)
			investigator.Cleanup(context.Background(), saved)
			marker := "creation_uncertain"
			if resource == "session" {
				marker = "session_creation_uncertain"
			}
			if saved[marker] != true {
				t.Fatal("lost unresolved creation marker")
			}
			cleanup := obj(saved["cleanup"])
			if cleanup[resource+"_deleted"] == true {
				t.Fatal("claimed deletion of an unidentified resource")
			}
			issues := strings.Join(cleanup["errors"].([]string), "\n")
			if !strings.Contains(issues, resource+" creation remains uncertain") {
				t.Fatalf("lost recovery diagnostic: %s", issues)
			}
			for _, call := range backend.calls {
				if call.name == "delete_"+resource {
					t.Fatalf("deleted unresolved %s", resource)
				}
			}
		})
	}
}

func TestCleanupDoesNotCreateWithoutUncertaintyOrOriginalRequest(t *testing.T) {
	for _, missingRequest := range []bool{false, true} {
		t.Run(strBool(missingRequest), func(t *testing.T) {
			backend := newFixture()
			investigator := Investigator{Client: backend, Config: fixtureConfig()}
			saved := savedRecoveryRecord(t, missingRequest, missingRequest)
			if missingRequest {
				delete(saved, "session_request")
				delete(saved, "sandbox_request")
			}
			investigator.Cleanup(context.Background(), saved)
			if len(backend.calls) != 0 {
				t.Fatalf("allocated or deleted unrelated resources: %v", backend.calls)
			}
			issues := obj(saved["cleanup"])["errors"].([]string)
			if missingRequest && (len(issues) != 2 || !strings.Contains(strings.Join(issues, "\n"), "original request is unavailable")) {
				t.Fatalf("missing original request not diagnosed: %v", issues)
			}
			if !missingRequest && len(issues) != 0 {
				t.Fatalf("invented uncertainty: %v", issues)
			}
		})
	}
}

type twiceUncertainSessionBackend struct {
	*fixtureBackend
	remaining        int
	journalPath      string
	requestPersisted bool
}

func (backend *twiceUncertainSessionBackend) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	if name == "create_session" && args["message"] == nil && backend.remaining > 0 {
		if backend.remaining == 2 {
			encoded, err := os.ReadFile(backend.journalPath)
			var saved map[string]any
			if err == nil && json.Unmarshal(encoded, &saved) == nil {
				backend.requestPersisted = reflect.DeepEqual(saved["session_request"], args)
			}
		}
		copyArgs := map[string]any{}
		for key, value := range args {
			copyArgs[key] = value
		}
		backend.calls = append(backend.calls, recordedCall{name: name, args: copyArgs})
		backend.remaining--
		return nil, &kagent.Error{Kind: "timeout", Message: "creation response lost"}
	}
	return backend.fixtureBackend.CallTool(ctx, name, args)
}

func TestRunJournalsSessionRequestBeforeAmbiguousCreationAndRecoversForCleanup(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "report")
	backend := &twiceUncertainSessionBackend{
		fixtureBackend: newFixture(), remaining: 2,
		journalPath: filepath.Join(directory, "report.json"),
	}
	investigator := Investigator{Client: backend, Config: fixtureConfig()}
	result, err := investigator.Run(context.Background(), casefile.Case{ID: "case-session-recovery", Description: "A failure"}, directory)
	if err == nil || result["status"] != "error" || result["outcome"] != "inconclusive" {
		t.Fatalf("result=%v error=%v", result, err)
	}
	if !backend.requestPersisted {
		t.Fatal("session creation began before its recovery request was persisted")
	}
	var requests []map[string]any
	for _, call := range backend.calls {
		if call.name == "create_session" {
			requests = append(requests, call.args)
		}
	}
	if len(requests) != 3 {
		t.Fatalf("expected two uncertain attempts and cleanup recovery: %v", requests)
	}
	for _, request := range requests {
		if !reflect.DeepEqual(request, result["session_request"]) || request["request_id"] != "case-session-recovery" {
			t.Fatalf("unstable session recovery key: %v", requests)
		}
		if _, present := request["message"]; present {
			t.Fatal("recovery started a model invocation")
		}
	}
	cleanup := obj(result["cleanup"])
	if result["session_creation_uncertain"] != false || cleanup["session_deleted"] != true || cleanup["sandbox_deleted"] != true {
		t.Fatalf("incomplete recovery: %v", result)
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	if obj(saved["session_request"])["request_id"] != "case-session-recovery" || obj(saved["session"])["id"] != "session" {
		t.Fatalf("recovery journal missing from final report: %v", saved)
	}
}

func TestRunRecoversTwiceAmbiguousSandboxCreationForCleanup(t *testing.T) {
	backend := newFixture()
	backend.createErrors = []error{
		&kagent.Error{Kind: "timeout", Message: "creation response lost"},
		&kagent.Error{Kind: "transport", Message: "creation response lost again"},
	}
	result, err, _ := runFixture(t, backend)
	if err == nil || result["status"] != "error" {
		t.Fatalf("result=%v error=%v", result, err)
	}
	var requests []map[string]any
	for _, call := range backend.calls {
		if call.name == "create_sandbox" {
			requests = append(requests, call.args)
		}
	}
	if len(requests) != 3 {
		t.Fatalf("expected cleanup recovery after ambiguous creates: %v", requests)
	}
	for _, request := range requests {
		if !reflect.DeepEqual(request, result["sandbox_request"]) {
			t.Fatalf("sandbox request changed: %v", requests)
		}
	}
	if result["creation_uncertain"] != false || obj(result["cleanup"])["sandbox_deleted"] != true {
		t.Fatalf("sandbox recovery not recorded: %v", result)
	}
}

func TestFinalObservationRetriesOutputAfterTransientReadFailure(t *testing.T) {
	backend := newFixture()
	backend.readError = &kagent.Error{Kind: "transport", Message: "output response interrupted"}
	result, err, directory := runFixture(t, backend)
	if err == nil || result["status"] != "error" || result["outcome"] != "inconclusive" {
		t.Fatalf("transient execution error concealed: result=%v error=%v", result, err)
	}
	experiments := result["experiments"].([]map[string]any)
	if len(experiments) != 2 {
		t.Fatalf("missing experiments: %v", experiments)
	}
	if experiments[0]["state"] != "PROCESS_STATE_EXITED" || experiments[0]["outputs_complete"] != true ||
		!strings.Contains(str(experiments[0]["stderr"]), "too small") {
		t.Fatalf("final observation skipped exited process with incomplete output: %v", experiments[0])
	}
	if experiments[1]["outputs_complete"] != true || !strings.Contains(str(experiments[1]["stdout"]), "passed") {
		t.Fatalf("lost corrective experiment output: %v", experiments[1])
	}
	failedReads := 0
	for _, call := range backend.calls {
		if call.name == "read_sandbox_outputs" && call.args["process_id"] == "failed" {
			failedReads++
		}
	}
	if failedReads != 2 {
		t.Fatalf("expected initial read and final capture retry, got %d", failedReads)
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"outputs_complete": true`) || !strings.Contains(string(encoded), "too small") {
		t.Fatal("recovered output absent from persisted error report")
	}
	if obj(result["cleanup"])["sandbox_deleted"] != true || obj(result["cleanup"])["session_deleted"] != true {
		t.Fatalf("output retry prevented cleanup: %v", result["cleanup"])
	}
}
