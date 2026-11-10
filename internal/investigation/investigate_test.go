package investigation

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	additionalTool string
	// failures queues errors returned, in order, by the named tool's next calls
	// before it behaves normally. "get_task" addresses transcript reads.
	failures  map[string][]error
	taskReads int
	// exitCode, when set, replaces every reported exit code; absentExitCode omits it.
	exitCode       any
	absentExitCode bool
}

func newFixture() *fixtureBackend {
	return &fixtureBackend{state: "TASK_STATE_COMPLETED", deleteFailures: map[string]int{}, killed: map[string]bool{}, failures: map[string][]error{}}
}

func (f *fixtureBackend) injectedFailure(name string) error {
	queue := f.failures[name]
	if len(queue) == 0 {
		return nil
	}
	f.failures[name] = queue[1:]
	return queue[0]
}

// callCount counts recorded tool calls, including failed attempts.
func (f *fixtureBackend) callCount(name string) int {
	if name == "get_task" {
		return f.taskReads
	}
	count := 0
	for _, call := range f.calls {
		if call.name == name {
			count++
		}
	}
	return count
}

func (f *fixtureBackend) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	copyArgs := map[string]any{}
	for k, v := range args {
		copyArgs[k] = v
	}
	f.calls = append(f.calls, recordedCall{name, copyArgs})
	if err := f.injectedFailure(name); err != nil {
		return nil, err
	}
	switch name {
	case "create_sandbox":
		if len(f.createErrors) > 0 {
			err := f.createErrors[0]
			f.createErrors = f.createErrors[1:]
			if err != nil {
				return nil, err
			}
		}
		return map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE"}, nil
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
		state := map[string]any{"process_id": pid, "state": "PROCESS_STATE_EXITED", "exit_code": float64(code)}
		if f.exitCode != nil {
			state["exit_code"] = f.exitCode
		}
		if f.absentExitCode {
			delete(state, "exit_code")
		}
		return state, nil
	case "read_sandbox_outputs":
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
	f.taskReads++
	if err := f.injectedFailure("get_task"); err != nil {
		return nil, err
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

func runFixture(t *testing.T, f *fixtureBackend) (*Report, string, error) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "report")
	i := Investigator{Client: f, Config: fixtureConfig()}
	c := casefile.Case{ID: "case", Description: "Configured timeout fails", Attachments: []casefile.Attachment{{Path: "app.py", Content: []byte("example"), SizeBytes: 7}}}
	r, err := i.Run(context.Background(), c, dir)
	return r, dir, err
}

func TestRunKeepsFailureAndCorrectionAndCleansBothResources(t *testing.T) {
	f := newFixture()
	r, dir, err := runFixture(t, f)
	if err != nil || r.Status != StatusCompleted || r.Outcome != OutcomeReproduced {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	experiments := r.Experiments
	if len(experiments) != 2 || !exitedWith(experiments[0], 1) || !exitedWith(experiments[1], 0) {
		t.Fatalf("lost evidence: %+v", experiments)
	}
	if !strings.Contains(stderr(experiments[0]), "too small") || !strings.Contains(stdout(experiments[1]), "passed") {
		t.Fatal("outputs not independently collected")
	}
	if !r.Cleanup.SandboxDeleted || !r.Cleanup.SessionDeleted {
		t.Fatalf("cleanup=%+v", r.Cleanup)
	}
	for _, name := range []string{"report.json", "report.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

// Test helpers read evidence that may be missing without panicking.
func exitedWith(e Experiment, code int) bool { return e.ExitCode != nil && *e.ExitCode == code }
func stdout(e Experiment) string {
	if e.Output == nil {
		return ""
	}
	return e.Output.Stdout
}
func stderr(e Experiment) string {
	if e.Output == nil {
		return ""
	}
	return e.Output.Stderr
}

func TestReportRecordsBuildVersionAndEmbeddedDefinitionDigests(t *testing.T) {
	config := fixtureConfig()
	config.Version = "v9.9.9-test"
	directory := filepath.Join(t.TempDir(), "report")
	investigator := Investigator{Client: newFixture(), Config: config}
	result, err := investigator.Run(context.Background(), casefile.Case{ID: "case", Description: "Timeout"}, directory)
	if err != nil {
		t.Fatal(err)
	}
	if result.TriageLab == nil || result.TriageLab.Version != "v9.9.9-test" || result.TriageLab.DeployedAgentVerified {
		t.Fatalf("build provenance=%+v", result.TriageLab)
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	build := obj(saved["triagelab"])
	if build["version"] != "v9.9.9-test" || build["deployed_agent_verified"] != false {
		t.Fatalf("saved build provenance=%v", build)
	}
	markdown, err := os.ReadFile(filepath.Join(directory, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Digest the source files independently of the embedding accessors.
	for file, key := range map[string]string{"prompt.txt": "embedded_prompt_sha256", "schema.json": "embedded_output_schema_sha256"} {
		data, err := os.ReadFile(filepath.Join("..", "agent", file))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if build[key] != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s=%v does not identify %s", key, build[key], file)
		}
		if !strings.Contains(string(markdown), hex.EncodeToString(sum[:])) {
			t.Fatalf("Markdown omits %s", key)
		}
	}
	if !strings.Contains(string(markdown), "the deployed Agent is not read back") || !strings.Contains(string(markdown), "v9.9.9-test") {
		t.Fatal("Markdown omits labeled build provenance")
	}
}

func TestAmbiguousCreateRetriesIdenticalStableKey(t *testing.T) {
	f := newFixture()
	f.createErrors = []error{&kagent.Error{Kind: "timeout", Message: "uncertain"}}
	r, _, err := runFixture(t, f)
	if err != nil {
		t.Fatal(err)
	}
	var requests []map[string]any
	for _, call := range f.calls {
		if call.name == "create_sandbox" {
			requests = append(requests, call.args)
		}
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) || requests[0]["request_id"] != r.RunID {
		t.Fatalf("unstable retry: %v", requests)
	}
}

func TestUnavailableSandboxPreservesInconclusiveReport(t *testing.T) {
	f := newFixture()
	f.createErrors = []error{&kagent.Error{Kind: "tool", Message: "template unavailable"}}
	r, dir, err := runFixture(t, f)
	if err == nil || r.Status != StatusError || r.Outcome != OutcomeInconclusive {
		t.Fatalf("result=%+v err=%v", r, err)
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
			r, _, err := runFixture(t, f)
			if onMessage {
				if err == nil || !f.canceled {
					t.Fatalf("uncertain invocation not canceled: err=%v canceled=%v", err, f.canceled)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !r.Cleanup.SessionDeleted || !r.Cleanup.SandboxDeleted {
				t.Fatalf("leaked resources: %+v", r.Cleanup)
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
			r, _, err := runFixture(t, f)
			if err == nil || !f.canceled || !r.Cleanup.SessionDeleted {
				t.Fatalf("err=%v canceled=%v cleanup=%+v", err, f.canceled, r.Cleanup)
			}
		})
	}
}

func TestModelFailureStillDeletesSandbox(t *testing.T) {
	f := newFixture()
	f.state = "TASK_STATE_FAILED"
	f.noExperiments = true
	r, _, err := runFixture(t, f)
	if err == nil || r.Outcome != OutcomeInconclusive || !r.Cleanup.SandboxDeleted {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

func TestProcessTimeoutRequestsKill(t *testing.T) {
	f := newFixture()
	f.state = "TASK_STATE_WORKING"
	f.running = true
	i := Investigator{Client: f, Config: fixtureConfig()}
	r := &Report{Session: map[string]any{"id": "session", "task_id": "task"}, Sandbox: map[string]any{"id": "sandbox"}, Experiments: []Experiment{}}
	observed := map[string]time.Time{"failed": time.Now().Add(-2 * time.Second)}
	_, err := i.observe(context.Background(), r, observed)
	if !errors.Is(err, context.DeadlineExceeded) || !f.killed["failed"] {
		t.Fatalf("err=%v killed=%v", err, f.killed)
	}
	if !r.Experiments[0].TimedOut {
		t.Fatal("timeout not preserved")
	}
}

func TestParentCancellationClassifiedAndReportPreserved(t *testing.T) {
	f := newFixture()
	i := Investigator{Client: f, Config: fixtureConfig()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := i.Run(ctx, casefile.Case{ID: "case", Description: "case"}, t.TempDir())
	if !errors.Is(err, context.Canceled) || r.Status != StatusInterrupted {
		t.Fatalf("result=%+v err=%v", r, err)
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
			if err != nil || got.Stdout != test.want || got.Truncated != test.truncated {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func TestCleanupExhaustionIsVisible(t *testing.T) {
	f := newFixture()
	f.deleteFailures["delete_sandbox"] = 3
	r, _, err := runFixture(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if r.Cleanup.SandboxDeleted || !r.Cleanup.SessionDeleted || len(r.Cleanup.Errors) == 0 {
		t.Fatalf("cleanup=%+v", r.Cleanup)
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
				if err == nil || !strings.Contains(err.Error(), "outside the configured tool set") || r.Status == StatusCompleted {
					t.Fatalf("unlisted call did not stop the run: %v", err)
				}
				return
			}
			if err != nil || r.Status != StatusCompleted {
				t.Fatalf("configured call did not complete: %v", err)
			}
			if experiments := r.Experiments; len(experiments) != 2 || !exitedWith(experiments[0], 1) {
				t.Fatalf("MCP activity displaced sandbox evidence: %+v", experiments)
			}
		})
	}
}

// An exit code is evidence only when it is an integer. Observation refuses
// anything else, so a malformed code never reaches the report or the analysis.
func TestExitedProcessNeedsAnIntegralExitCode(t *testing.T) {
	for name, setup := range map[string]func(*fixtureBackend){
		"boolean": func(f *fixtureBackend) { f.exitCode = true }, "fraction": func(f *fixtureBackend) { f.exitCode = 1.5 },
		"text": func(f *fixtureBackend) { f.exitCode = "1" }, "absent": func(f *fixtureBackend) { f.absentExitCode = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			setup(f)
			r, _, err := runFixture(t, f)
			if err == nil || !strings.Contains(err.Error(), "no valid exit code") || r.Status != StatusError || r.Outcome != OutcomeInconclusive {
				t.Fatalf("status=%v outcome=%v err=%v", r.Status, r.Outcome, err)
			}
			for _, e := range r.Experiments {
				if e.ExitCode != nil || e.OutputsComplete {
					t.Fatalf("recorded unverified evidence: %+v", e)
				}
			}
			if !r.Cleanup.SandboxDeleted || !r.Cleanup.SessionDeleted {
				t.Fatalf("cleanup=%+v", r.Cleanup)
			}
		})
	}
}
