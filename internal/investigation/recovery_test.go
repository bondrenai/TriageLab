package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

func savedRecoveryRecord(t *testing.T, sessionUncertain, sandboxUncertain bool, edits ...func(map[string]any)) *Report {
	t.Helper()
	value := map[string]any{
		"schema_version": 1, "run_id": "recovery",
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
	for _, edit := range edits {
		edit(value)
	}
	// Cleanup consumes a persisted JSON report, so test the types produced on reload.
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := DecodeReport(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// resolved reports a creation marker that is present and false.
func resolved(marker *bool) bool { return marker != nil && !*marker }

func TestCleanupRecoversBothUncertainCreationsFromSavedRequests(t *testing.T) {
	backend := newFixture()
	investigator := Investigator{Client: backend, Config: fixtureConfig()}
	saved := savedRecoveryRecord(t, true, true)
	sessionRequest, sandboxRequest := saved.SessionRequest, saved.SandboxRequest
	investigator.Cleanup(context.Background(), saved)
	if !resolved(saved.SessionCreationUncertain) || !resolved(saved.CreationUncertain) {
		t.Fatalf("creation uncertainty not resolved: %+v", saved)
	}
	if !saved.Cleanup.SessionDeleted || !saved.Cleanup.SandboxDeleted || len(saved.Cleanup.Errors) != 0 {
		t.Fatalf("recovered resources not cleaned: %+v", saved.Cleanup)
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
				saved.Session = map[string]any{"id": "session"}
				saved.Sandbox = map[string]any{"id": "sandbox"}
			}
			investigator.Cleanup(context.Background(), saved)
			if saved.taskID() != "task" || !backend.canceled {
				t.Fatalf("working task not recovered and canceled: session=%v canceled=%v", saved.Session, backend.canceled)
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
			if !saved.Cleanup.SessionDeleted || !saved.Cleanup.SandboxDeleted || len(saved.Cleanup.Errors) != 0 {
				t.Fatalf("recovered working task cleanup incomplete: %+v", saved.Cleanup)
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
			marker, deleted := saved.CreationUncertain, saved.Cleanup.SandboxDeleted
			if resource == "session" {
				marker, deleted = saved.SessionCreationUncertain, saved.Cleanup.SessionDeleted
			}
			if !isSet(marker) {
				t.Fatal("lost unresolved creation marker")
			}
			if deleted {
				t.Fatal("claimed deletion of an unidentified resource")
			}
			issues := strings.Join(saved.Cleanup.Errors, "\n")
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
			saved := savedRecoveryRecord(t, missingRequest, missingRequest, func(record map[string]any) {
				if missingRequest {
					delete(record, "session_request")
					delete(record, "sandbox_request")
				}
			})
			investigator.Cleanup(context.Background(), saved)
			if len(backend.calls) != 0 {
				t.Fatalf("allocated or deleted unrelated resources: %v", backend.calls)
			}
			issues := saved.Cleanup.Errors
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
	if err == nil || result.Status != StatusError || result.Outcome != OutcomeInconclusive {
		t.Fatalf("result=%+v error=%v", result, err)
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
		if !reflect.DeepEqual(request, result.SessionRequest) || request["request_id"] != "case-session-recovery" {
			t.Fatalf("unstable session recovery key: %v", requests)
		}
		if _, present := request["message"]; present {
			t.Fatal("recovery started a model invocation")
		}
	}
	if !resolved(result.SessionCreationUncertain) || !result.Cleanup.SessionDeleted || !result.Cleanup.SandboxDeleted {
		t.Fatalf("incomplete recovery: %+v", result)
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
	result, _, err := runFixture(t, backend)
	if err == nil || result.Status != StatusError {
		t.Fatalf("result=%+v error=%v", result, err)
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
		if !reflect.DeepEqual(request, result.SandboxRequest) {
			t.Fatalf("sandbox request changed: %v", requests)
		}
	}
	if !resolved(result.CreationUncertain) || !result.Cleanup.SandboxDeleted {
		t.Fatalf("sandbox recovery not recorded: %+v", result)
	}
}

func repeated(err error, count int) []error {
	errs := make([]error, count)
	for index := range errs {
		errs[index] = err
	}
	return errs
}

// Transient read failures beyond the bound still end the run. The final
// observation then re-enters with the partly updated experiment record.
func TestFinalObservationRetriesOutputAfterTransientReadFailure(t *testing.T) {
	backend := newFixture()
	backend.failures["read_sandbox_outputs"] = repeated(&kagent.Error{Kind: "transport", Message: "output response interrupted"}, readAttempts)
	result, directory, err := runFixture(t, backend)
	if err == nil || result.Status != StatusError || result.Outcome != OutcomeInconclusive {
		t.Fatalf("transient execution error concealed: result=%+v error=%v", result, err)
	}
	experiments := result.Experiments
	if len(experiments) != 2 {
		t.Fatalf("missing experiments: %+v", experiments)
	}
	if experiments[0].State != "PROCESS_STATE_EXITED" || !experiments[0].OutputsComplete ||
		!strings.Contains(stderr(experiments[0]), "too small") {
		t.Fatalf("final observation skipped exited process with incomplete output: %+v", experiments[0])
	}
	if !experiments[1].OutputsComplete || !strings.Contains(stdout(experiments[1]), "passed") {
		t.Fatalf("lost corrective experiment output: %+v", experiments[1])
	}
	failedReads := 0
	for _, call := range backend.calls {
		if call.name == "read_sandbox_outputs" && call.args["process_id"] == "failed" {
			failedReads++
		}
	}
	if failedReads != readAttempts+1 {
		t.Fatalf("expected %d bounded reads and a final capture retry, got %d", readAttempts, failedReads)
	}
	if !strings.Contains(strings.Join(result.Errors, "\n"), "read_sandbox_outputs failed 3 consecutive times") {
		t.Fatalf("exhausted retry bound not reported: %v", result.Errors)
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"outputs_complete": true`) || !strings.Contains(string(encoded), "too small") {
		t.Fatal("recovered output absent from persisted error report")
	}
	if !result.Cleanup.SandboxDeleted || !result.Cleanup.SessionDeleted {
		t.Fatalf("output retry prevented cleanup: %+v", result.Cleanup)
	}
}

func TestTransientClassification(t *testing.T) {
	for _, test := range []struct {
		err  error
		want bool
	}{
		{&kagent.Error{Kind: "transport"}, true}, {&kagent.Error{Kind: "timeout"}, true},
		{&kagent.Error{Kind: "http", Code: 502}, true}, {&kagent.Error{Kind: "http", Code: 503}, true},
		{&kagent.Error{Kind: "http", Code: 504}, true}, {fmt.Errorf("wrapped: %w", &kagent.Error{Kind: "transport"}), true},
		{&kagent.Error{Kind: "http", Code: 500}, false}, {&kagent.Error{Kind: "http", Code: 401}, false},
		{&kagent.Error{Kind: "http", Code: 429}, false}, {&kagent.Error{Kind: "rpc", Code: -32603}, false},
		{&kagent.Error{Kind: "tool"}, false}, {&kagent.Error{Kind: "protocol"}, false},
		{context.DeadlineExceeded, false}, {context.Canceled, false}, {errors.New("evidence invalid"), false},
	} {
		if got := transient(test.err); got != test.want {
			t.Errorf("transient(%#v)=%v", test.err, got)
		}
	}
}

func TestTransientReadFailuresWithinBoundContinueRun(t *testing.T) {
	for _, test := range []struct {
		read  string
		err   error
		calls int // per-experiment reads also succeed once for the second experiment
	}{
		{"get_session", &kagent.Error{Kind: "transport", Message: "port-forward dropped"}, readAttempts},
		{"get_task", &kagent.Error{Kind: "timeout", Message: "transcript read timed out"}, readAttempts},
		{"get_sandbox_process", &kagent.Error{Kind: "http", Code: 503, Message: "gateway unavailable"}, readAttempts + 1},
		{"read_sandbox_outputs", &kagent.Error{Kind: "http", Code: 504, Message: "gateway timeout"}, readAttempts + 1},
	} {
		t.Run(test.read, func(t *testing.T) {
			t.Parallel()
			backend := newFixture()
			backend.failures[test.read] = repeated(test.err, readAttempts-1)
			result, _, err := runFixture(t, backend)
			if err != nil || result.Status != StatusCompleted || result.Outcome != OutcomeReproduced {
				t.Fatalf("tolerable transient failures stopped the run: status=%v err=%v", result.Status, err)
			}
			if got := backend.callCount(test.read); got != test.calls {
				t.Fatalf("%s called %d times, want %d", test.read, got, test.calls)
			}
			experiments := result.Experiments
			if len(experiments) != 2 || !experiments[0].OutputsComplete || !experiments[1].OutputsComplete ||
				!strings.Contains(stderr(experiments[0]), "too small") {
				t.Fatalf("evidence incomplete after retry: %+v", experiments)
			}
			if cleanup := result.Cleanup; !cleanup.SessionDeleted || !cleanup.SandboxDeleted {
				t.Fatalf("cleanup=%+v", cleanup)
			}
		})
	}
}

func TestTransientReadFailuresBeyondBoundAbortWithReportAndCleanup(t *testing.T) {
	backend := newFixture()
	backend.failures["get_session"] = repeated(&kagent.Error{Kind: "transport", Message: "port-forward dropped"}, readAttempts)
	result, directory, err := runFixture(t, backend)
	var upstream *kagent.Error
	if !errors.As(err, &upstream) || upstream.Kind != "transport" || result.Status != StatusError || result.Outcome != OutcomeInconclusive {
		t.Fatalf("status=%v err=%v", result.Status, err)
	}
	if got := backend.callCount("get_session"); got != readAttempts {
		t.Fatalf("get_session called %d times, want %d", got, readAttempts)
	}
	if !backend.canceled {
		t.Fatal("task with unobserved state was not canceled")
	}
	if cleanup := result.Cleanup; !cleanup.SessionDeleted || !cleanup.SandboxDeleted {
		t.Fatalf("cleanup=%+v", cleanup)
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["status"] != "error" || len(saved["experiments"].([]any)) != 2 || obj(saved["cleanup"])["sandbox_deleted"] != true {
		t.Fatalf("persisted report incomplete: status=%v experiments=%v cleanup=%v", saved["status"], saved["experiments"], saved["cleanup"])
	}
}

func TestNonTransientReadFailureIsNotRepeated(t *testing.T) {
	for name, failure := range map[string]error{
		"rpc": &kagent.Error{Kind: "rpc", Code: -32603}, "tool": &kagent.Error{Kind: "tool"},
		"protocol": &kagent.Error{Kind: "protocol"}, "http 500": &kagent.Error{Kind: "http", Code: 500},
	} {
		t.Run(name, func(t *testing.T) {
			backend := newFixture()
			backend.failures["get_session"] = []error{failure}
			result, _, err := runFixture(t, backend)
			// The journalled task ID means neither final capture nor cleanup reads the session again.
			if err == nil || result.Status != StatusError || backend.callCount("get_session") != 1 {
				t.Fatalf("status=%v err=%v reads=%d", result.Status, err, backend.callCount("get_session"))
			}
		})
	}
}

func TestTransientMutationFailuresAreNotRepeated(t *testing.T) {
	uncertain := &kagent.Error{Kind: "transport", Message: "response lost"}
	t.Run("upload", func(t *testing.T) {
		backend := newFixture()
		backend.failures["write_sandbox_file"] = []error{uncertain}
		result, _, err := runFixture(t, backend)
		if err == nil || result.Status != StatusError || backend.callCount("write_sandbox_file") != 1 {
			t.Fatalf("status=%v err=%v uploads=%d", result.Status, err, backend.callCount("write_sandbox_file"))
		}
		if !result.Cleanup.SandboxDeleted {
			t.Fatalf("cleanup=%+v", result.Cleanup)
		}
	})
	t.Run("invocation", func(t *testing.T) {
		backend := newFixture()
		backend.sessionError, backend.errorOnMessage = uncertain, true
		result, _, err := runFixture(t, backend)
		invocations := 0
		for _, call := range backend.calls {
			if _, invoked := call.args["message"]; call.name == "create_session" && invoked {
				invocations++
			}
		}
		if err == nil || result.Status != StatusError || invocations != 1 {
			t.Fatalf("status=%v err=%v invocations=%d", result.Status, err, invocations)
		}
	})
	t.Run("kill", func(t *testing.T) {
		backend := newFixture()
		backend.state, backend.running = "TASK_STATE_WORKING", true
		backend.failures["kill_sandbox_process"] = []error{uncertain}
		investigator := Investigator{Client: backend, Config: fixtureConfig()}
		record := &Report{Session: map[string]any{"id": "session", "task_id": "task"}, Sandbox: map[string]any{"id": "sandbox"}, Experiments: []Experiment{}}
		_, err := investigator.observe(context.Background(), record, map[string]time.Time{"failed": time.Now().Add(-2 * time.Second)})
		if err == nil || !strings.Contains(err.Error(), "kill timed-out process") || backend.callCount("kill_sandbox_process") != 1 {
			t.Fatalf("err=%v kills=%d", err, backend.callCount("kill_sandbox_process"))
		}
		if !record.Experiments[0].TimedOut {
			t.Fatal("timeout not preserved")
		}
	})
}

func TestProcessWatchdogFiresAfterTransientStateRead(t *testing.T) {
	backend := newFixture()
	backend.state, backend.running = "TASK_STATE_WORKING", true
	backend.failures["get_sandbox_process"] = []error{&kagent.Error{Kind: "timeout", Message: "state read timed out"}}
	investigator := Investigator{Client: backend, Config: fixtureConfig()}
	record := &Report{Session: map[string]any{"id": "session", "task_id": "task"}, Sandbox: map[string]any{"id": "sandbox"}, Experiments: []Experiment{}}
	_, err := investigator.observe(context.Background(), record, map[string]time.Time{"failed": time.Now().Add(-2 * time.Second)})
	if !errors.Is(err, context.DeadlineExceeded) || !backend.killed["failed"] || backend.callCount("get_sandbox_process") != 2 {
		t.Fatalf("err=%v killed=%v reads=%d", err, backend.killed, backend.callCount("get_sandbox_process"))
	}
}

func TestDeadlineAndCancellationDuringReadRetryAreClassified(t *testing.T) {
	transportFailure := &kagent.Error{Kind: "transport", Message: "port-forward dropped"}
	t.Run("interrupted", func(t *testing.T) {
		t.Parallel()
		backend := newFixture()
		backend.failures["get_session"] = repeated(transportFailure, readAttempts)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		investigator := Investigator{Client: backend, Config: fixtureConfig(), Progress: func(message string) {
			if strings.Contains(message, "retrying") {
				cancel()
			}
		}}
		result, err := investigator.Run(ctx, casefile.Case{ID: "case", Description: "case"}, t.TempDir())
		if !errors.Is(err, context.Canceled) || result.Status != StatusInterrupted || backend.callCount("get_session") != 1 {
			t.Fatalf("status=%v err=%v reads=%d", result.Status, err, backend.callCount("get_session"))
		}
		if cleanup := result.Cleanup; !cleanup.SessionDeleted || !cleanup.SandboxDeleted {
			t.Fatalf("cleanup=%+v", cleanup)
		}
	})
	t.Run("timed out", func(t *testing.T) {
		t.Parallel()
		backend := newFixture()
		backend.failures["get_session"] = repeated(transportFailure, 100)
		config := fixtureConfig()
		config.InvestigationTimeout = 1
		investigator := Investigator{Client: backend, Config: config}
		result, err := investigator.Run(context.Background(), casefile.Case{ID: "case", Description: "case"}, t.TempDir())
		if !errors.Is(err, context.DeadlineExceeded) || result.Status != StatusTimedOut || backend.callCount("get_session") >= readAttempts {
			t.Fatalf("status=%v err=%v reads=%d", result.Status, err, backend.callCount("get_session"))
		}
		if cleanup := result.Cleanup; !cleanup.SessionDeleted || !cleanup.SandboxDeleted {
			t.Fatalf("cleanup=%+v", cleanup)
		}
	})
}
