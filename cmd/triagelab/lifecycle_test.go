package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/report"
)

type lifecycleBackend struct {
	call func(context.Context, string, map[string]any) (map[string]any, error)
}

func (b lifecycleBackend) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	return b.call(ctx, name, args)
}
func (lifecycleBackend) TaskItems(context.Context, string, string) ([]map[string]any, error) {
	return nil, errors.New("unexpected transcript request")
}
func (lifecycleBackend) CancelTask(context.Context, string, string) error {
	return errors.New("unexpected task cancellation")
}

func savedCLIState(t *testing.T, state investigation.State) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), state.RunID)
	// Deliberately stale public IDs ensure cleanup uses operational state.
	saved := map[string]any{"schema_version": 1, "run_id": state.RunID, "round": state.Round,
		"case": map[string]any{"description": "Waiting for a customer trace"}, "status": "paused", "outcome": "inconclusive",
		"started_at": "2026-01-01T00:00:00Z", "experiments": []any{}, "warnings": []string{}, "errors": []string{},
		"sandbox": map[string]any{"id": "stale-public-sandbox"}, "session": map[string]any{"id": "stale-public-session"}}
	if err := report.Write(dir, saved); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lifecycle.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "report.json")
}

func cliState() investigation.State {
	return investigation.State{Version: 1, Config: investigation.DefaultConfig(), RunID: "11111111-1111-4111-8111-111111111111",
		Round: 1, Phase: "paused", Trigger: "cli", SandboxID: "saved-sandbox", SessionID: "saved-session"}
}

func noLifecycleCalls(t *testing.T) investigation.Backend {
	t.Helper()
	return lifecycleBackend{call: func(_ context.Context, name string, _ map[string]any) (map[string]any, error) {
		t.Errorf("rejected CLI action contacted upstream: %s", name)
		return nil, errors.New("unexpected upstream call")
	}}
}

func TestSavedCommandsRequirePrivateLifecycleRecord(t *testing.T) {
	t.Setenv("TRIAGELAB_TOKEN", "")
	for _, command := range []string{"pause", "resume", "close"} {
		t.Run(command, func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := run(context.Background(), []string{command}, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "requires --report") {
				t.Fatalf("missing report: code=%d stdout=%s stderr=%s", code, &out, &stderr)
			}
			out.Reset()
			stderr.Reset()
			path := filepath.Join(t.TempDir(), "report.json")
			if err := os.WriteFile(path, []byte(`{"run_id":"historical"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if code := run(context.Background(), []string{command, "--report", path}, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "private investigation lifecycle") {
				t.Fatalf("legacy report: code=%d stdout=%s stderr=%s", code, &out, &stderr)
			}
		})
	}
}

func TestSavedCommandRejectsChangedConnectionBeforeAnyMutation(t *testing.T) {
	for _, field := range []string{"endpoint", "namespace", "user", "cluster-http"} {
		t.Run(field, func(t *testing.T) {
			state := cliState()
			path := savedCLIState(t, state)
			config := state.Config
			switch field {
			case "endpoint":
				config.Endpoint = "http://127.0.0.1:9999/mcp"
			case "namespace":
				config.Namespace = "another-namespace"
			case "user":
				config.UserID = "another-owner"
			case "cluster-http":
				config.AllowClusterHTTP = true
			}
			for _, command := range []string{"pause", "resume", "close"} {
				var out, stderr bytes.Buffer
				runner := investigation.Investigator{Client: noLifecycleCalls(t), Config: config}
				code := runSavedInvestigation(context.Background(), command, &runOptions{reportPath: path, description: "New information"}, &runner, &out, &stderr)
				if code != 2 || !strings.Contains(stderr.String(), "Configuration must match") {
					t.Fatalf("%s with changed %s: code=%d stdout=%s stderr=%s", command, field, code, &out, &stderr)
				}
			}
			current, err := investigation.LoadState(filepath.Dir(path))
			if err != nil || current.Phase != state.Phase || current.Round != state.Round {
				t.Fatalf("rejected request changed state: %+v %v", current, err)
			}
		})
	}
}

func TestCLIResumeCannotBypassTicketContinuationApproval(t *testing.T) {
	state := cliState()
	state.Trigger = "ticket"
	path := savedCLIState(t, state)
	runner := investigation.Investigator{Client: noLifecycleCalls(t), Config: state.Config}
	var out, stderr bytes.Buffer
	code := runSavedInvestigation(context.Background(), "resume", &runOptions{reportPath: path, description: "Customer supplied logs"}, &runner, &out, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "signed-in workspace") {
		t.Fatalf("ticket resume: code=%d stdout=%s stderr=%s", code, &out, &stderr)
	}
	current, err := investigation.LoadState(filepath.Dir(path))
	if err != nil || current.Round != 1 || current.Phase != "paused" {
		t.Fatalf("ticket resume changed lifecycle: %+v %v", current, err)
	}
}

func TestCLIPauseWritesTaskBoundRequestWithoutClaimingSuspension(t *testing.T) {
	state := cliState()
	state.Phase, state.TaskID = "running", "current-task"
	path := savedCLIState(t, state)
	runner := investigation.Investigator{Client: noLifecycleCalls(t), Config: state.Config}
	var out, stderr bytes.Buffer
	if code := runSavedInvestigation(context.Background(), "pause", &runOptions{reportPath: path}, &runner, &out, &stderr); code != 4 {
		t.Fatalf("inactive process accepted pause: code=%d stdout=%s stderr=%s", code, &out, &stderr)
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".lifecycle.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := runSavedInvestigation(context.Background(), "pause", &runOptions{reportPath: path}, &runner, &out, &stderr); code != 0 || !strings.Contains(out.String(), "Pause requested") || !strings.Contains(out.String(), "after evidence collection") {
		t.Fatalf("pause request: code=%d stdout=%s stderr=%s", code, &out, &stderr)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "pause-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		RunID  string `json:"run_id"`
		Round  int    `json:"round"`
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if request.RunID != state.RunID || request.Round != state.Round || request.TaskID != state.TaskID {
		t.Fatalf("pause targeted wrong task: %+v", request)
	}
	current, err := investigation.LoadState(filepath.Dir(path))
	if err != nil || current.Phase != "running" {
		t.Fatalf("request falsely declared paused: %+v %v", current, err)
	}
}

func TestCLICloseUsesSavedIDsAndImmutableExecutionSettings(t *testing.T) {
	state := cliState()
	state.Config.Agent, state.Config.Template = "saved-investigator", "saved-template"
	path := savedCLIState(t, state)
	var calls []string
	backend := lifecycleBackend{call: func(_ context.Context, name string, args map[string]any) (map[string]any, error) {
		calls = append(calls, name)
		switch name {
		case "get_session":
			if args["session_id"] != state.SessionID {
				t.Errorf("wrong session read: %v", args)
			}
			return map[string]any{"turns": []any{}}, nil
		case "delete_session":
			if args["session_id"] != state.SessionID {
				t.Errorf("wrong session deleted: %v", args)
			}
			return map[string]any{"session": map[string]any{"state": "RUNTIME_STATE_DELETED"}}, nil
		case "delete_sandbox":
			if args["sandbox_id"] != state.SandboxID {
				t.Errorf("wrong sandbox deleted: %v", args)
			}
			return map[string]any{"state": "RUNTIME_STATE_DELETED"}, nil
		default:
			t.Errorf("close unexpectedly called %s", name)
			return nil, fmt.Errorf("unexpected %s", name)
		}
	}}
	current := investigation.DefaultConfig()
	current.TokenEnv = "REFRESHED_CREDENTIAL_LOOKUP"
	runner := investigation.Investigator{Client: backend, Config: current}
	var out, stderr bytes.Buffer
	code := runSavedInvestigation(context.Background(), "close", &runOptions{reportPath: path}, &runner, &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), "Investigation closed") || stderr.Len() != 0 {
		t.Fatalf("close: code=%d stdout=%s stderr=%s", code, &out, &stderr)
	}
	if strings.Join(calls, ",") != "get_session,delete_session,delete_sandbox" {
		t.Fatalf("wrong cleanup calls: %v", calls)
	}
	if runner.Config.Agent != state.Config.Agent || runner.Config.Template != state.Config.Template {
		t.Fatal("saved execution settings were replaced by current defaults")
	}
	closed, err := investigation.LoadState(filepath.Dir(path))
	if err != nil || closed.Phase != "closed" {
		t.Fatalf("closure not persisted: %+v %v", closed, err)
	}
}

func TestCLICloseAlreadyClosedDoesNotReturnInvestigationOutcomeExitCode(t *testing.T) {
	state := cliState()
	state.Phase = "closed"
	path := savedCLIState(t, state)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved["status"], saved["outcome"] = "completed", "inconclusive"
	if err := report.Write(filepath.Dir(path), saved); err != nil {
		t.Fatal(err)
	}
	runner := investigation.Investigator{Client: noLifecycleCalls(t), Config: state.Config}
	var out, stderr bytes.Buffer
	if code := runSavedInvestigation(context.Background(), "close", &runOptions{reportPath: path}, &runner, &out, &stderr); code != 0 {
		t.Fatalf("idempotent close returned prior investigation outcome: code=%d stderr=%s", code, &stderr)
	}
}

func TestCLIResumeRejectsInvalidAdditionalInputsBeforeRestoring(t *testing.T) {
	for _, name := range []string{"empty", "conflicting-description", "oversized-description-file"} {
		t.Run(name, func(t *testing.T) {
			state := cliState()
			path := savedCLIState(t, state)
			opts := &runOptions{reportPath: path}
			if name != "empty" {
				opts.descriptionFile = filepath.Join(t.TempDir(), "additional.txt")
				text := "New customer information"
				if name == "conflicting-description" {
					opts.description = "A second description"
				} else {
					text = strings.Repeat("x", casefile.MaxDescriptionBytes+1)
				}
				if err := os.WriteFile(opts.descriptionFile, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runner := investigation.Investigator{Client: noLifecycleCalls(t), Config: state.Config}
			var out, stderr bytes.Buffer
			if code := runSavedInvestigation(context.Background(), "resume", opts, &runner, &out, &stderr); code != 2 || stderr.Len() == 0 {
				t.Fatalf("invalid continuation: code=%d stdout=%s stderr=%s", code, &out, &stderr)
			}
			current, err := investigation.LoadState(filepath.Dir(path))
			if err != nil || current.Phase != "paused" || current.Round != 1 {
				t.Fatalf("invalid input advanced lifecycle: %+v %v", current, err)
			}
		})
	}
}

func TestInvestigationExitCodesDistinguishPauseCloseFailureAndFindings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("Saved evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, status, outcome string
		runErr                error
		cleanupErrors         []string
		want                  int
	}{
		{name: "paused", status: "paused", outcome: "inconclusive", want: 0},
		{name: "closed", status: "closed", outcome: "inconclusive", want: 0},
		{name: "reproduced", status: "completed", outcome: "reproduced", want: 0},
		{name: "not-reproduced", status: "completed", outcome: "not_reproduced", want: 1},
		{name: "inconclusive", status: "completed", outcome: "inconclusive", want: 3},
		{name: "execution-error", status: "error", want: 4},
		{name: "interrupted", status: "interrupted", want: 130},
		{name: "timed-out", status: "timed_out", want: 124},
		{name: "pause-failed", status: "paused", runErr: errors.New("failed with private-test-secret"), want: 4},
		{name: "cleanup-failed", status: "closing", cleanupErrors: []string{"sandbox still exists"}, runErr: errors.New("cleanup failed"), want: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			result := &investigation.Report{Status: investigation.Status(test.status), Outcome: investigation.Outcome(test.outcome), Cleanup: &investigation.CleanupRecord{Errors: test.cleanupErrors}}
			code := printInvestigationResult(result, test.runErr, dir, &out, &stderr, "private-test-secret")
			if code != test.want {
				t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", code, test.want, &out, &stderr)
			}
			if strings.Contains(stderr.String(), "private-test-secret") {
				t.Fatal("runtime error exposed a configured credential")
			}
			if test.want == 5 && !strings.Contains(stderr.String(), "triagelab close") {
				t.Fatalf("missing cleanup recovery instruction: %s", &stderr)
			}
		})
	}
	var out, stderr bytes.Buffer
	if code := printInvestigationResult(nil, errors.New("no report"), dir, &out, &stderr); code != 4 {
		t.Fatalf("missing result exit=%d", code)
	}
}
