package investigation

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

// fixtureBackend is used only by unit tests. The executable has no fake mode.
type recordedCall struct {
	name string
	args map[string]any
}
type fixtureBackend struct {
	calls          []recordedCall
	state          string
	createErrors   []error
	sessionError   error
	errorOnMessage bool
	deleteFailures map[string]int
	canceled       bool
	running        bool
	killed         map[string]bool
	noExperiments  bool
	summaryReply   bool
	failureReply   string
	readError      error
	additionalTool string
	retention      bool
	probeError     error
}

func newFixture() *fixtureBackend {
	return &fixtureBackend{state: "TASK_STATE_COMPLETED", deleteFailures: map[string]int{}, killed: map[string]bool{}, retention: true}
}

func (f *fixtureBackend) SandboxRetention(context.Context) (bool, error) {
	if f.probeError != nil {
		return false, f.probeError
	}
	return f.retention, nil
}

func (f *fixtureBackend) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	copyArgs := map[string]any{}
	for k, v := range args {
		copyArgs[k] = v
	}
	f.calls = append(f.calls, recordedCall{name, copyArgs})
	switch name {
	case "create_sandbox":
		if len(f.createErrors) > 0 {
			err := f.createErrors[0]
			f.createErrors = f.createErrors[1:]
			if err != nil {
				return nil, err
			}
		}
		created := map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE"}
		if f.retention {
			created["retain_while_suspended"] = true
		}
		return created, nil
	case "write_sandbox_file":
		data, err := base64.StdEncoding.DecodeString(args["data_base64"].(string))
		return map[string]any{"bytes_written": len(data)}, err
	case "create_session":
		_, hasMessage := args["message"]
		if f.sessionError != nil && hasMessage == f.errorOnMessage {
			err := f.sessionError
			f.sessionError = nil
			return nil, err
		}
		result := map[string]any{"session": map[string]any{"id": "session", "state": "RUNTIME_STATE_READY"}}
		if hasMessage {
			result["turn"] = map[string]any{"task_id": "task", "state": "TASK_STATE_WORKING"}
		}
		return result, nil
	case "get_session":
		turn := map[string]any{"task_id": "task", "state": f.state}
		if f.summaryReply {
			turn["reply"] = analysisText()
		}
		if f.failureReply != "" {
			turn["reply"] = f.failureReply
		}
		return map[string]any{"session": map[string]any{"id": "session"}, "turns": []any{turn}}, nil
	case "get_sandbox_process":
		pid := args["process_id"].(string)
		if f.running && !f.killed[pid] {
			return map[string]any{"process_id": pid, "state": "PROCESS_STATE_RUNNING"}, nil
		}
		code := 0
		if pid == "failed" {
			code = 1
		}
		if f.killed[pid] {
			code = -9
		}
		return map[string]any{"process_id": pid, "state": "PROCESS_STATE_EXITED", "exit_code": float64(code)}, nil
	case "read_sandbox_outputs":
		if f.readError != nil {
			err := f.readError
			f.readError = nil
			return nil, err
		}
		stdout, stderr := "verification passed\n", ""
		if args["process_id"] == "failed" {
			stdout, stderr = "", "timeout configuration too small\n"
		}
		return map[string]any{"stdout_base64": base64.StdEncoding.EncodeToString([]byte(stdout)), "stderr_base64": base64.StdEncoding.EncodeToString([]byte(stderr)), "stdout_offset": len(stdout), "stderr_offset": len(stderr), "truncated": false}, nil
	case "kill_sandbox_process":
		f.killed[args["process_id"].(string)] = true
		return map[string]any{"state": "PROCESS_STATE_RUNNING"}, nil
	case "delete_session", "delete_sandbox":
		if f.deleteFailures[name] > 0 {
			f.deleteFailures[name]--
			return nil, &kagent.Error{Kind: "tool", Message: "temporary deletion failure"}
		}
		resource := map[string]any{"state": "RUNTIME_STATE_DELETED"}
		if name == "delete_session" {
			return map[string]any{"session": resource}, nil
		}
		return resource, nil
	}
	return nil, errors.New("unexpected unit fixture call: " + name)
}

func (f *fixtureBackend) TaskItems(_ context.Context, session, task string) ([]map[string]any, error) {
	if session != "session" || task != "task" {
		return nil, errors.New("unexpected fixture identity")
	}
	items := []map[string]any{}
	if !f.noExperiments {
		for _, pid := range []string{"failed", "corrected"} {
			command := []any{"python3", "app.py"}
			if pid == "corrected" {
				command = append(command, "--timeout-ms", "5000")
			}
			for _, part := range []map[string]any{
				{"metadata": map[string]any{"kagent.dev/a2a/part-type": "function_call"}, "data": map[string]any{"id": "call-" + pid, "name": "start_sandbox_process", "args": map[string]any{"sandbox_id": "sandbox", "command": command}}},
				{"metadata": map[string]any{"kagent.dev/a2a/part-type": "function_response"}, "data": map[string]any{"id": "call-" + pid, "name": "start_sandbox_process", "response": map[string]any{"output": map[string]any{"process_id": pid}}}},
			} {
				items = append(items, map[string]any{"index": len(items), "artifact": map[string]any{"parts": []any{part}}})
			}
		}
	}
	if f.additionalTool != "" {
		items = append(items, map[string]any{"artifact": map[string]any{"parts": []any{
			map[string]any{
				"metadata": map[string]any{"kagent.dev/a2a/part-type": "function_call"},
				"data":     map[string]any{"id": "docs", "name": f.additionalTool, "args": map[string]any{"topic": "timeout"}},
			},
			map[string]any{
				"metadata": map[string]any{"kagent.dev/a2a/part-type": "function_response"},
				"data":     map[string]any{"id": "docs", "name": f.additionalTool, "response": map[string]any{"output": "documentation"}},
			},
		}}})
	}
	if f.state == "TASK_STATE_COMPLETED" {
		items = append(items, map[string]any{"index": len(items), "artifact": map[string]any{"parts": []any{map[string]any{"metadata": map[string]any{"kagent.dev/a2a/output-schema-sha256": strings.Repeat("a", 64)}, "mediaType": "application/json", "data": validAnalysis()}}}})
	}
	return items, nil
}

func (f *fixtureBackend) CancelTask(_ context.Context, session, task string) error {
	if session != "session" || task != "task" {
		return errors.New("unexpected cancel identity")
	}
	f.canceled = true
	f.state = "TASK_STATE_CANCELED"
	return nil
}

func fixtureConfig() Config {
	c := DefaultConfig()
	c.InvestigationTimeout = 6
	c.ProcessTimeout = 1
	c.RequestTimeout = 1
	c.SandboxTTL = 8
	c.PollInterval = 1
	return c
}

func runFixture(t *testing.T, f *fixtureBackend) (map[string]any, error, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "report")
	i := Investigator{Client: f, Config: fixtureConfig()}
	c := casefile.Case{ID: "case", Description: "Configured timeout fails", Attachments: []casefile.Attachment{{Path: "app.py", Content: []byte("example"), SizeBytes: 7}}}
	r, err := i.Run(context.Background(), c, dir)
	return r, err, dir
}

func TestRunKeepsFailureAndCorrectionAndCleansBothResources(t *testing.T) {
	f := newFixture()
	r, err, dir := runFixture(t, f)
	if err != nil || r["status"] != "completed" || r["outcome"] != "reproduced" {
		t.Fatalf("result=%v err=%v", r, err)
	}
	experiments := r["experiments"].([]map[string]any)
	if len(experiments) != 2 || experiments[0]["exit_code"] != 1 || experiments[1]["exit_code"] != 0 {
		t.Fatalf("lost evidence: %v", experiments)
	}
	if !strings.Contains(str(experiments[0]["stderr"]), "too small") || !strings.Contains(str(experiments[1]["stdout"]), "passed") {
		t.Fatal("outputs not independently collected")
	}
	cleanup := obj(r["cleanup"])
	if cleanup["sandbox_deleted"] != true || cleanup["session_deleted"] != true {
		t.Fatalf("cleanup=%v", cleanup)
	}
	for _, name := range []string{"report.json", "report.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAmbiguousCreateRetriesIdenticalStableKey(t *testing.T) {
	f := newFixture()
	f.createErrors = []error{&kagent.Error{Kind: "timeout", Message: "uncertain"}}
	r, err, _ := runFixture(t, f)
	if err != nil {
		t.Fatal(err)
	}
	var requests []map[string]any
	for _, call := range f.calls {
		if call.name == "create_sandbox" {
			requests = append(requests, call.args)
		}
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) || requests[0]["request_id"] != r["run_id"] {
		t.Fatalf("unstable retry: %v", requests)
	}
}

func TestUnavailableSandboxPreservesInconclusiveReport(t *testing.T) {
	f := newFixture()
	f.createErrors = []error{&kagent.Error{Kind: "tool", Message: "template unavailable"}}
	r, err, dir := runFixture(t, f)
	if err == nil || r["status"] != "error" || r["outcome"] != "inconclusive" {
		t.Fatalf("result=%v err=%v", r, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call.name, "delete_") {
			t.Fatal("deleted unrelated resource")
		}
	}
}

func TestEmptySessionCreationRetriesAndInvocationTimeoutRecoversTask(t *testing.T) {
	for _, onMessage := range []bool{false, true} {
		t.Run(strBool(onMessage), func(t *testing.T) {
			f := newFixture()
			f.sessionError = &kagent.Error{Kind: "timeout", Message: "uncertain"}
			f.errorOnMessage = onMessage
			r, err, _ := runFixture(t, f)
			if onMessage {
				if err == nil || !f.canceled {
					t.Fatalf("uncertain invocation not canceled: err=%v canceled=%v", err, f.canceled)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if obj(r["cleanup"])["session_deleted"] != true || obj(r["cleanup"])["sandbox_deleted"] != true {
				t.Fatalf("leaked resources: %v", r["cleanup"])
			}
		})
	}
}

func strBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func TestParkedTasksCanceledBeforeDeletion(t *testing.T) {
	for _, state := range []string{"TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture()
			f.state = state
			r, err, _ := runFixture(t, f)
			if err == nil || !f.canceled || obj(r["cleanup"])["session_deleted"] != true {
				t.Fatalf("err=%v canceled=%v cleanup=%v", err, f.canceled, r["cleanup"])
			}
		})
	}
}

func TestModelFailureStillDeletesSandbox(t *testing.T) {
	f := newFixture()
	f.state = "TASK_STATE_FAILED"
	f.noExperiments = true
	r, err, _ := runFixture(t, f)
	if err == nil || r["outcome"] != "inconclusive" || obj(r["cleanup"])["sandbox_deleted"] != true {
		t.Fatalf("result=%v err=%v", r, err)
	}
}

func TestFailedTaskKeepsBoundedUpstreamReasonInReport(t *testing.T) {
	f := newFixture()
	f.state = "TASK_STATE_FAILED"
	f.noExperiments = true
	f.failureReply = "model request rejected: " + strings.Repeat("x", 3*maxTaskFailureBytes)
	r, err, dir := runFixture(t, f)
	if err == nil || strings.Contains(err.Error(), "model request rejected") {
		t.Fatalf("upstream text belongs in the report, not the returned error: %v", err)
	}
	reason := str(r["task_failure"])
	if !strings.HasPrefix(reason, "model request rejected: ") || len(reason) != maxTaskFailureBytes {
		t.Fatalf("task_failure has %d bytes: %.60q", len(reason), reason)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil || !strings.Contains(string(saved), `"task_failure": "model request rejected: `) {
		t.Fatalf("saved report lacks the upstream reason: %v", err)
	}
}

func TestProcessTimeoutRequestsKill(t *testing.T) {
	f := newFixture()
	f.state = "TASK_STATE_WORKING"
	f.running = true
	i := Investigator{Client: f, Config: fixtureConfig()}
	r := map[string]any{"session": map[string]any{"id": "session", "task_id": "task"}, "sandbox": map[string]any{"id": "sandbox"}, "experiments": []map[string]any{}}
	observed := map[string]time.Time{"failed": time.Now().Add(-2 * time.Second)}
	_, err := i.observe(context.Background(), r, observed)
	if !errors.Is(err, context.DeadlineExceeded) || !f.killed["failed"] {
		t.Fatalf("err=%v killed=%v", err, f.killed)
	}
	if r["experiments"].([]map[string]any)[0]["timed_out"] != true {
		t.Fatal("timeout not preserved")
	}
}

func TestUpstreamControllerRunsInvestigationWithoutRetention(t *testing.T) {
	f := newFixture()
	f.retention = false
	r, err, dir := runFixture(t, f)
	if err != nil || r["status"] != "completed" || r["outcome"] != "reproduced" {
		t.Fatalf("result=%v err=%v", r, err)
	}
	if obj(r["upstream"])["sandbox_retention"] != false {
		t.Fatalf("report did not record the probed capability: %v", r["upstream"])
	}
	for _, call := range f.calls {
		if call.name != "create_sandbox" {
			continue
		}
		if _, present := call.args["retain_while_suspended"]; present {
			t.Fatal("release controller received the fork-only retention input")
		}
		if call.args["request_id"] != r["run_id"] || call.args["ttl_seconds"] == nil {
			t.Fatalf("lost baseline creation inputs: %v", call.args)
		}
	}
	if obj(r["cleanup"])["sandbox_deleted"] != true || obj(r["cleanup"])["session_deleted"] != true {
		t.Fatalf("cleanup=%v", r["cleanup"])
	}
	state, err := LoadState(dir)
	if err != nil || state.Retention {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestUpstreamProbeFailureFailsBeforeCreatingResources(t *testing.T) {
	f := newFixture()
	f.probeError = errors.New("controller unreachable")
	i := Investigator{Client: f, Config: fixtureConfig()}
	dir := t.TempDir()
	_, err := i.Run(context.Background(), casefile.Case{ID: "case", Description: "case"}, dir)
	if err == nil || !strings.Contains(err.Error(), "retention support") {
		t.Fatalf("err=%v", err)
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call.name, "create_") {
			t.Fatalf("probe failure still created resources: %v", call.name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "lifecycle.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed probe left lifecycle state behind")
	}
}

func TestParentCancellationClassifiedAndReportPreserved(t *testing.T) {
	f := newFixture()
	i := Investigator{Client: f, Config: fixtureConfig()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := i.Run(ctx, casefile.Case{ID: "case", Description: "case"}, t.TempDir())
	if !errors.Is(err, context.Canceled) || r["status"] != "interrupted" {
		t.Fatalf("result=%v err=%v", r, err)
	}
}

type outputFixture struct {
	fixtureBackend
	pages []map[string]any
}

func (f *outputFixture) CallTool(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
	if len(f.pages) == 0 {
		return nil, errors.New("unexpected output pagination")
	}
	value := f.pages[0]
	f.pages = f.pages[1:]
	return value, nil
}
func outputPage(text string, offset int, truncated bool) map[string]any {
	return map[string]any{"stdout_base64": base64.StdEncoding.EncodeToString([]byte(text)), "stderr_base64": "", "stdout_offset": offset, "stderr_offset": 0, "truncated": truncated}
}

func TestOutputPaginationLimitsAndIntegrity(t *testing.T) {
	for _, test := range []struct {
		name      string
		pages     []map[string]any
		limit     int
		want      string
		truncated bool
		fail      bool
	}{
		{"pages", []map[string]any{outputPage("ab", 2, true), outputPage("cd", 4, false)}, 10, "abcd", false, false},
		{"limit", []map[string]any{outputPage("abcdef", 6, false)}, 3, "abc", true, false},
		{"offset", []map[string]any{outputPage("a", 9, false)}, 10, "", false, true},
		{"stalled", []map[string]any{outputPage("", 0, true)}, 10, "", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := &outputFixture{pages: test.pages}
			c := fixtureConfig()
			c.MaxOutputBytes = test.limit
			i := Investigator{Client: f, Config: c}
			got, err := i.readOutputs(context.Background(), "sandbox", "process")
			if test.fail {
				if err == nil {
					t.Fatal("invalid output accepted")
				}
				return
			}
			if err != nil || got["stdout"] != test.want || got["truncated"] != test.truncated {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func TestCleanupExhaustionIsVisible(t *testing.T) {
	f := newFixture()
	f.deleteFailures["delete_sandbox"] = 3
	r, err, _ := runFixture(t, f)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := obj(r["cleanup"])
	if cleanup["sandbox_deleted"] != false || cleanup["session_deleted"] != true || len(cleanup["errors"].([]string)) == 0 {
		t.Fatalf("cleanup=%v", cleanup)
	}
}

func TestAdditionalMCPToolRequiresManualRunAllowance(t *testing.T) {
	for _, name := range []string{"permitted", "unlisted"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			f.additionalTool = "search_docs"
			i := Investigator{Client: f, Config: fixtureConfig()}
			if name == "permitted" {
				i.AdditionalTools = []string{"search_docs"}
			}
			r, err := i.Run(context.Background(), casefile.Case{ID: "case", Description: "Timeout"}, t.TempDir())
			if name == "unlisted" {
				if err == nil || !strings.Contains(err.Error(), "outside the configured tool set") || r["status"] == "completed" {
					t.Fatalf("unlisted call did not stop the run: %v", err)
				}
				return
			}
			if err != nil || r["status"] != "completed" {
				t.Fatalf("configured call did not complete: %v", err)
			}
			if experiments := r["experiments"].([]map[string]any); len(experiments) != 2 || experiments[0]["exit_code"] != 1 {
				t.Fatalf("MCP activity displaced sandbox evidence: %v", experiments)
			}
		})
	}
}
