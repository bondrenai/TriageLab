package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

type lifecycleFixture struct {
	*fixtureBackend
	missing           bool
	retentionMissing  bool
	suspended         bool
	resumed           bool
	suspendError      error
	resumeError       error
	invokeError       error
	invocations       int
	operations        []string
	onFirstPoll       func() error
	pendingTool       bool
	suspendedDeadline bool
	cancelOn          string
	cancel            context.CancelFunc
}

func newLifecycleFixture() *lifecycleFixture {
	return &lifecycleFixture{fixtureBackend: newFixture(), missing: true}
}

func (f *lifecycleFixture) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	f.operations = append(f.operations, name)
	if name == f.cancelOn {
		if name == "suspend_sandbox" {
			f.suspended = true
		}
		if name == "resume_sandbox" {
			f.resumed = true
		}
		if name == "invoke_agent" {
			f.invocations++
			f.state = "TASK_STATE_WORKING"
		}
		f.cancel()
		return nil, ctx.Err()
	}
	switch name {
	case "suspend_sandbox":
		f.suspended = true
		if f.suspendError != nil {
			err := f.suspendError
			f.suspendError = nil
			return nil, err
		}
		value := map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_SUSPENDED", "operation": "RUNTIME_OPERATION_NONE", "retain_while_suspended": true}
		if f.suspendedDeadline {
			value["expires_at"] = "2026-11-11T00:00:00Z"
		}
		return value, nil
	case "resume_sandbox":
		f.suspended, f.resumed = false, true
		if f.resumeError != nil {
			err := f.resumeError
			f.resumeError = nil
			return nil, err
		}
		return map[string]any{"id": "sandbox", "state": "RUNTIME_STATE_READY", "operation": "RUNTIME_OPERATION_NONE", "retain_while_suspended": true}, nil
	case "invoke_agent":
		f.invocations++
		if args["session_id"] != "session" || args["message_id"] == "" {
			return nil, errors.New("missing continuation identity")
		}
		if f.invokeError != nil {
			return nil, f.invokeError
		}
		f.state = "TASK_STATE_COMPLETED"
		return map[string]any{"session_id": "session", "task_id": "task"}, nil
	case "get_session":
		if f.onFirstPoll != nil {
			callback := f.onFirstPoll
			f.onFirstPoll = nil
			if err := callback(); err != nil {
				return nil, err
			}
		}
	case "get_sandbox_process", "read_sandbox_outputs":
		if f.resumed && f.noExperiments {
			return nil, errors.New("read a historical guest handle after resume")
		}
	}
	value, err := f.fixtureBackend.CallTool(ctx, name, args)
	if name == "create_sandbox" && f.retentionMissing {
		delete(value, "retain_while_suspended")
	}
	return value, err
}

func (f *lifecycleFixture) TaskItems(ctx context.Context, session, task string) ([]map[string]any, error) {
	items, err := f.fixtureBackend.TaskItems(ctx, session, task)
	if err != nil {
		return nil, err
	}
	if f.missing {
		for _, item := range items {
			for _, raw := range anySlice(obj(item["artifact"])["parts"]) {
				data := obj(obj(raw)["data"])
				if data["missing_information"] != nil {
					data["missing_information"] = []any{"Supply the customer configuration"}
				}
			}
		}
	}
	if f.pendingTool {
		items = append(items, map[string]any{"artifact": map[string]any{"parts": []any{map[string]any{
			"metadata": map[string]any{"kagent.dev/a2a/part-type": "function_call"},
			"data":     map[string]any{"id": "pending-write", "name": "write_sandbox_file", "args": map[string]any{"sandbox_id": "sandbox", "path": "/data/workspace/unsettled"}},
		}}}})
	}
	return items, nil
}

func (f *lifecycleFixture) CancelTask(ctx context.Context, session, task string) error {
	f.operations = append(f.operations, "cancel_task")
	return f.fixtureBackend.CancelTask(ctx, session, task)
}

func startPaused(t *testing.T, f *lifecycleFixture) (*Investigator, string, *Report) {
	t.Helper()
	i := &Investigator{Client: f, Config: fixtureConfig(), Secrets: []string{"secret-marker"}}
	dir := t.TempDir()
	r, err := i.Run(context.Background(), casefile.Case{ID: "durable-case", Description: "Original customer problem secret-marker", Source: map[string]any{"kind": "local"}}, dir)
	if err != nil || r.Status != StatusPaused {
		t.Fatalf("pause failed: report=%v error=%v", r, err)
	}
	return i, dir, r
}

func TestMissingInformationPausesAndPreservesPrivateJournal(t *testing.T) {
	f := newLifecycleFixture()
	_, dir, r := startPaused(t, f)
	state, err := LoadState(dir)
	if err != nil || state.Phase != "paused" || state.Round != 1 || !state.PauseReady || state.SandboxID != "sandbox" || state.SessionID != "session" || state.TaskID != "task" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if r.Cleanup.SandboxDeleted || r.Cleanup.SessionDeleted {
		t.Fatal("pause deleted retained resources")
	}
	for _, call := range f.operations {
		if strings.HasPrefix(call, "delete_") {
			t.Fatal("pause called deletion")
		}
	}
	for _, name := range []string{"lifecycle.json", "report.json", "rounds/0001/report.json", "rounds/0001/report.md"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private record %s: info=%v error=%v", name, info, err)
		}
	}
	journal, err := os.ReadFile(filepath.Join(dir, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(journal), "Original customer") || strings.Contains(string(journal), "secret-marker") || strings.Contains(string(journal), "verification passed") {
		t.Fatal("lifecycle journal retained case data, output, or credentials")
	}
}

func TestResumeAfterRestartPreservesRoundsAndNeverReadsOldHandles(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	archive, err := os.ReadFile(filepath.Join(roundDir(dir, 1), "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.noExperiments, f.missing = true, false
	f.operations = nil
	restarted := &Investigator{Client: f, Config: i.Config, Secrets: i.Secrets}
	added := casefile.Case{ID: "fresh-ingestion-id", Description: "Customer supplied the configuration", Attachments: []casefile.Attachment{{Path: "config.json", Content: []byte("{}"), SizeBytes: 2}}}
	r, err := restarted.Resume(context.Background(), added, dir)
	if err != nil || r.Status != StatusCompleted || f.invocations != 1 {
		t.Fatalf("resume result=%v error=%v invocations=%d", r, err, f.invocations)
	}
	if r.Case.Description != "Original customer problem [REDACTED]" || r.Continuation.ID != added.ID {
		t.Fatal("resume replaced the original case or lost additional information")
	}
	if len(r.Experiments) != 0 || len(r.Rounds) != 1 {
		t.Fatal("historical experiments leaked into the current round")
	}
	old := r.Rounds[0]
	if len(old.Experiments) != 2 || old.Rounds != nil {
		t.Fatal("history lost evidence or recursively embedded reports")
	}
	after, err := os.ReadFile(filepath.Join(roundDir(dir, 1), "report.json"))
	if err != nil || string(archive) != string(after) {
		t.Fatal("resume rewrote the previous round")
	}
	for _, call := range f.calls {
		if call.name == "write_sandbox_file" && call.args["path"] != "/data/workspace/rounds/0002/config.json" {
			t.Fatalf("overwrote original input path: %v", call.args)
		}
	}
	state, err := LoadState(dir)
	if err != nil || state.Phase != "closed" || state.Round != 2 || state.ResumeFromRound != 1 {
		t.Fatalf("state=%+v error=%v", state, err)
	}
}

func TestUpstreamRunCompletesWhenInformationIsMissing(t *testing.T) {
	f := newLifecycleFixture()
	f.retention = false
	i := &Investigator{Client: f, Config: fixtureConfig()}
	dir := t.TempDir()
	r, err := i.Run(context.Background(), casefile.Case{ID: "release-controller", Description: "Release controller blocks"}, dir)
	if err != nil || r.Status != StatusCompleted {
		t.Fatalf("result=%v error=%v", r, err)
	}
	analysis := r.Analysis
	if listLen(analysis["missing_information"]) == 0 {
		t.Fatal("missing information was dropped from the report")
	}
	state, err := LoadState(dir)
	if err != nil || state.Phase != "closed" || state.Retention {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if !r.Cleanup.SandboxDeleted || !r.Cleanup.SessionDeleted {
		t.Fatalf("release-controller run retained resources: %v", r.Cleanup)
	}
	for _, operation := range f.operations {
		if operation == "suspend_sandbox" {
			t.Fatal("release-controller run suspended its sandbox")
		}
	}
}

func TestPauseRequiresDurableRetention(t *testing.T) {
	base := State{Version: 1, Config: fixtureConfig(), RunID: "11111111-1111-4111-8111-111111111111", Round: 1, Phase: "running", TaskID: "task"}
	for _, retention := range []bool{false, true} {
		state := base
		state.Retention = retention
		dir := t.TempDir()
		if err := saveState(dir, &state); err != nil {
			t.Fatal(err)
		}
		lock, err := os.OpenFile(filepath.Join(dir, ".lifecycle.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		err = RequestPause(dir)
		lock.Close()
		if !retention {
			if !errors.Is(err, ErrRetentionUnsupported) {
				t.Fatalf("release controller pause err=%v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "pause-request.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("pause request survived a retention refusal")
			}
			continue
		}
		if err != nil {
			t.Fatalf("fork pause err=%v", err)
		}
	}
}

func TestManualPauseSettlesTaskAndProcessesBeforeSuspending(t *testing.T) {
	f := newLifecycleFixture()
	f.state, f.running = "TASK_STATE_WORKING", true
	i := &Investigator{Client: f, Config: fixtureConfig()}
	dir := t.TempDir()
	f.onFirstPoll = func() error { return RequestPause(dir) }
	r, err := i.Run(context.Background(), casefile.Case{ID: "manual-pause", Description: "Pause running experiments"}, dir)
	if err != nil || r.Status != StatusPaused || !f.canceled {
		t.Fatalf("result=%v error=%v canceled=%v", r, err, f.canceled)
	}
	for _, e := range r.Experiments {
		if !e.exited() || !e.OutputsComplete || !e.StoppedForPause {
			t.Fatalf("experiment not settled before pause: %v", e)
		}
	}
	positions := map[string]int{}
	for index, operation := range f.operations {
		positions[operation] = index
	}
	if positions["cancel_task"] >= positions["kill_sandbox_process"] || positions["read_sandbox_outputs"] >= positions["suspend_sandbox"] {
		t.Fatalf("unsafe pause ordering: %v", f.operations)
	}
}

func TestPauseRequestedDuringFinalPollIsHonored(t *testing.T) {
	f := newLifecycleFixture()
	f.missing = false
	i := &Investigator{Client: f, Config: fixtureConfig()}
	dir := t.TempDir()
	f.onFirstPoll = func() error { return RequestPause(dir) }
	r, err := i.Run(context.Background(), casefile.Case{ID: "final-poll-pause", Description: "Pause at completion"}, dir)
	if err != nil || r.Status != StatusPaused || !f.suspended {
		t.Fatalf("accepted pause was lost: result=%v error=%v", r, err)
	}
	for _, operation := range f.operations {
		if strings.HasPrefix(operation, "delete_") {
			t.Fatal("completion raced the accepted pause")
		}
	}
}

func TestUncertainSuspendReconcilesWithoutReadingLostHandles(t *testing.T) {
	f := newLifecycleFixture()
	f.suspendError = &kagent.Error{Kind: "transport", Message: "suspension response lost"}
	i := &Investigator{Client: f, Config: fixtureConfig()}
	dir := t.TempDir()
	r, err := i.Run(context.Background(), casefile.Case{ID: "suspend-uncertain", Description: "Blocked investigation"}, dir)
	if err == nil || r.Status != StatusPausing {
		t.Fatalf("result=%v error=%v", r, err)
	}
	state, err := LoadState(dir)
	if err != nil || state.Phase != "pausing" || !state.PauseReady {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	f.noExperiments, f.missing = true, false
	f.resumed = true // The old registry is already gone after the lost suspension response.
	r, err = i.Resume(context.Background(), casefile.Case{Description: "Received requested information"}, dir)
	if err != nil || r.Status != StatusCompleted || f.invocations != 1 {
		t.Fatalf("reconciliation failed: result=%v error=%v", r, err)
	}
}

func TestUncertainResumeRequiresSameInputAndDoesNotInvokeBeforeRecovery(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	f.resumeError = &kagent.Error{Kind: "transport", Message: "restoration response lost"}
	added := casefile.Case{ID: "first", Description: "The requested information"}
	_, err := i.Resume(context.Background(), added, dir)
	if err == nil || f.invocations != 0 {
		t.Fatalf("error=%v invocations=%d", err, f.invocations)
	}
	state, err := LoadState(dir)
	if err != nil || state.Phase != "resuming" || state.Round != 2 || state.ResumeFromRound != 1 || state.ContinuationDigest == "" {
		t.Fatalf("state=%+v error=%v", state, err)
	}
	operations := len(f.operations)
	if _, err := i.Resume(context.Background(), casefile.Case{Description: "Changed input"}, dir); err == nil || len(f.operations) != operations {
		t.Fatal("changed input resumed a reserved continuation")
	}
	f.noExperiments, f.missing = true, false
	added.ID = "new-ingestion-id"
	r, err := i.Resume(context.Background(), added, dir)
	if err != nil || r.Status != StatusCompleted || f.invocations != 1 {
		t.Fatalf("retry result=%v error=%v", r, err)
	}
}

func TestUncertainInvocationNeverReplaysAndCloseUsesJournalIDs(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	f.invokeError = &kagent.Error{Kind: "transport", Message: "invocation response lost"}
	added := casefile.Case{Description: "The requested information"}
	_, err := i.Resume(context.Background(), added, dir)
	if err == nil || f.invocations != 1 {
		t.Fatalf("error=%v invocations=%d", err, f.invocations)
	}
	state, err := LoadState(dir)
	if err != nil || state.Phase != "invoking" || state.MessageID == "" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err := i.Resume(context.Background(), added, dir); err == nil || f.invocations != 1 {
		t.Fatal("uncertain invocation was replayed")
	}
	var raw json.RawMessage
	if err := readJSON(filepath.Join(dir, "report.json"), maxReportBytes, &raw, false); err != nil {
		t.Fatal(err)
	}
	report, err := DecodeReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	report.Sandbox, report.Session = map[string]any{"id": "wrong-sandbox"}, map[string]any{"id": "wrong-session", "task_id": "wrong-task"}
	if err := i.persist(dir, report); err != nil {
		t.Fatal(err)
	}
	r, err := i.Close(context.Background(), dir)
	if err != nil || r.Status != StatusClosed || !r.Cleanup.SandboxDeleted {
		t.Fatalf("close result=%v error=%v", r, err)
	}
	for _, call := range f.calls {
		if call.name == "delete_sandbox" && call.args["sandbox_id"] != "sandbox" {
			t.Fatal("close trusted a report sandbox ID")
		}
		if call.name == "delete_session" && call.args["session_id"] != "session" {
			t.Fatal("close trusted a report session ID")
		}
	}
}

func TestLifecycleLockRejectsConcurrentResumeAndClose(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	unlock, err := lockRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := i.Resume(context.Background(), casefile.Case{Description: "Additional information"}, dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("resume lock error=%v", err)
	}
	if _, err := i.Close(context.Background(), dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("close lock error=%v", err)
	}
	if f.invocations != 0 {
		t.Fatal("concurrent resume invoked the investigator")
	}
}

func TestLegacyCleanupSharesTheLifecycleLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockLegacyCleanup(dir); !errors.Is(err, ErrBusy) {
		unlock()
		t.Fatalf("expected lifecycle lock conflict, got %v", err)
	}
	unlock()
	release, err := LockLegacyCleanup(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := lockRun(dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected cleanup lock conflict, got %v", err)
	}
}

func TestStalePauseMarkerDoesNotPauseAnotherRound(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	if err := writePrivate(filepath.Join(dir, "pause-request.json"), pauseRequest{RunID: "durable-case", Round: 1, TaskID: "task"}); err != nil {
		t.Fatal(err)
	}
	f.noExperiments, f.missing = true, false
	r, err := i.Resume(context.Background(), casefile.Case{Description: "Additional information"}, dir)
	if err != nil || r.Status != StatusCompleted {
		t.Fatalf("stale request paused new turn: result=%v error=%v", r, err)
	}
}

func TestRetentionCapabilityAndSavedConnectionFailClosed(t *testing.T) {
	f := newLifecycleFixture()
	f.retentionMissing = true
	i := &Investigator{Client: f, Config: fixtureConfig()}
	r, err := i.Run(context.Background(), casefile.Case{ID: "unsupported", Description: "Old backend"}, t.TempDir())
	if err == nil || !r.Cleanup.SandboxDeleted {
		t.Fatalf("unsupported retention result=%v error=%v", r, err)
	}
	for _, operation := range f.operations {
		if operation == "create_session" {
			t.Fatal("unsupported backend invoked an investigator")
		}
	}
	f = newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	i.Config.UserID = "another-owner"
	operations := len(f.operations)
	if _, err := i.Resume(context.Background(), casefile.Case{Description: "Additional information"}, dir); err == nil || len(f.operations) != operations {
		t.Fatal("resume accepted a different upstream owner")
	}
}

func TestContinuationDigestChecksBytesAndIgnoresOnlyIngestionID(t *testing.T) {
	first := casefile.Case{ID: "first", Description: "New information", Attachments: []casefile.Attachment{{Path: "input.txt", Content: []byte("one"), SizeBytes: 3}}}
	digest, err := continuationDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	first.ID = "second"
	again, err := continuationDigest(first)
	if err != nil || digest != again {
		t.Fatal("ephemeral ingestion ID changed continuation identity")
	}
	first.Attachments[0].Content = []byte("two")
	changed, err := continuationDigest(first)
	if err != nil || changed == digest {
		t.Fatal("actual attachment bytes were not bound")
	}
	first.Attachments[0].SHA256 = strings.Repeat("0", 64)
	if _, err := continuationDigest(first); err == nil {
		t.Fatal("incorrect supplied content hash accepted")
	}
}

func TestResumeRejectsMismatchedPendingReport(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	f.resumeError = errors.New("restore interrupted")
	added := casefile.Case{Description: "New information"}
	if _, err := i.Resume(context.Background(), added, dir); err == nil {
		t.Fatal("expected pending resume")
	}
	encoded, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := DecodeReport(encoded)
	if err != nil {
		t.Fatal(err)
	}
	r.Round = 1
	if err := i.persist(dir, r); err != nil {
		t.Fatal(err)
	}
	before := len(f.operations)
	if _, err := i.Resume(context.Background(), added, dir); err == nil || len(f.operations) != before {
		t.Fatal("mismatched evidence round admitted a continuation")
	}
}

func TestPauseRefusesUnsettledToolCall(t *testing.T) {
	f := newLifecycleFixture()
	f.pendingTool = true
	i := &Investigator{Client: f, Config: fixtureConfig()}
	dir := t.TempDir()
	r, err := i.Run(context.Background(), casefile.Case{ID: "pending-tool", Description: "Pause requires quiescence"}, dir)
	if err == nil || r.Status != StatusPausing || f.suspended {
		t.Fatalf("unsettled tool was suspended: result=%v error=%v", r, err)
	}
	state, err := LoadState(dir)
	if err != nil || state.PauseReady {
		t.Fatalf("unsafe pause receipt: state=%+v error=%v", state, err)
	}
	closed, err := i.Close(context.Background(), dir)
	if err != nil || closed.Status != StatusClosed {
		t.Fatalf("failed pause could not be closed: result=%v error=%v", closed, err)
	}
}

func TestRepeatedPausesKeepFlatImmutableHistory(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	f.noExperiments = true
	for round := 2; round <= 3; round++ {
		i.Actor = ""
		if round == 2 {
			i.Actor = "workspace-user"
		}
		r, err := i.Resume(context.Background(), casefile.Case{Description: "Additional information still leaves a question"}, dir)
		if err != nil || r.Status != StatusPaused {
			t.Fatalf("round=%d result=%v error=%v", round, r, err)
		}
		if round == 3 && r.ResumedBy != "" {
			t.Fatal("CLI continuation inherited the preceding user's attribution")
		}
		history := r.Rounds
		if len(history) != round-1 {
			t.Fatalf("round %d history count %d", round, len(history))
		}
		for _, previous := range history {
			if previous.Rounds != nil {
				t.Fatal("recursive report history")
			}
		}
	}
	state, err := LoadState(dir)
	if err != nil || state.Round != 3 || state.Phase != "paused" || state.SandboxID != "sandbox" || state.SessionID != "session" || f.invocations != 2 {
		t.Fatalf("state=%+v error=%v invocations=%d", state, err, f.invocations)
	}
	if _, err := i.Close(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}

func TestPauseRequiresExpirationExemptionReceipt(t *testing.T) {
	f := newLifecycleFixture()
	f.suspendedDeadline = true
	i := &Investigator{Client: f, Config: fixtureConfig()}
	dir := t.TempDir()
	r, err := i.Run(context.Background(), casefile.Case{ID: "retention-receipt", Description: "Durable retention must be confirmed"}, dir)
	if err == nil || r.Status != StatusPausing {
		t.Fatalf("expiring suspension was accepted: result=%v error=%v", r, err)
	}
	state, err := LoadState(dir)
	if err != nil || state.Phase != "pausing" {
		t.Fatalf("state=%+v error=%v", state, err)
	}
}

func TestCloseReconcilesReportAfterCleanupCommit(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	r, err := i.Close(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the prior report surviving a crash after the closed state was
	// synced. The state proves deletion; the stale report is only a view.
	r.Status, r.FinishedAt = StatusPausing, nil
	r.Cleanup = &CleanupRecord{Errors: []string{}}
	if err := i.persist(dir, r); err != nil {
		t.Fatal(err)
	}
	before := len(f.operations)
	r, err = i.Close(context.Background(), dir)
	if err != nil || r.Status != StatusClosed || !r.Cleanup.SandboxDeleted || !r.Cleanup.SessionDeleted || r.FinishedAt == nil || len(f.operations) != before {
		t.Fatalf("close reconciliation result=%v error=%v", r, err)
	}
}

func TestReviewedRoundCannotResumeOrCloseAfterAnotherContinuation(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	reviewed, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.noExperiments = true
	if _, err := i.Resume(context.Background(), casefile.Case{Description: "A different process continued the investigation"}, dir); err != nil {
		t.Fatal(err)
	}
	current, err := LoadState(dir)
	if err != nil || current.Round != reviewed.Round+1 || current.Phase != "paused" {
		t.Fatalf("state=%+v error=%v", current, err)
	}
	stale := &Investigator{Client: f, Config: i.Config, ExpectedRound: reviewed.Round}
	before := len(f.operations)
	if _, err := stale.Resume(context.Background(), casefile.Case{Description: "Input reviewed for the old round"}, dir); err == nil {
		t.Fatal("stale review authorized another invocation")
	}
	if _, err := stale.Close(context.Background(), dir); err == nil {
		t.Fatal("stale review closed a newer round")
	}
	if len(f.operations) != before {
		t.Fatal("stale lifecycle request reached upstream")
	}
	after, err := LoadState(dir)
	if err != nil || after.Round != current.Round || after.Phase != current.Phase {
		t.Fatalf("stale request changed lifecycle: state=%+v error=%v", after, err)
	}
}

func TestReviewedPendingResumeCannotReplayAfterSameRoundFinishes(t *testing.T) {
	f := newLifecycleFixture()
	i, dir, _ := startPaused(t, f)
	input := casefile.Case{Description: "New customer information"}
	f.resumeError = errors.New("restoration response lost")
	if _, err := i.Resume(context.Background(), input, dir); err == nil {
		t.Fatal("expected pending restoration")
	}
	reviewed, err := LoadState(dir)
	if err != nil || reviewed.Phase != "resuming" {
		t.Fatalf("state=%+v error=%v", reviewed, err)
	}
	f.noExperiments = true
	if _, err := i.Resume(context.Background(), input, dir); err != nil {
		t.Fatal(err)
	}
	current, err := LoadState(dir)
	if err != nil || current.Round != reviewed.Round || current.Phase != "paused" {
		t.Fatalf("state=%+v error=%v", current, err)
	}
	stale := &Investigator{Client: f, Config: i.Config, ExpectedRound: reviewed.Round, ExpectedPhase: reviewed.Phase}
	before := len(f.operations)
	if _, err := stale.Resume(context.Background(), input, dir); err == nil {
		t.Fatal("stale pending-resume review invoked another round")
	}
	if _, err := stale.Close(context.Background(), dir); err == nil {
		t.Fatal("stale pending-resume review closed a completed round")
	}
	if len(f.operations) != before {
		t.Fatal("stale lifecycle phase reached upstream")
	}
}

func TestCloseFallbackRebuildsHistoryAndPreservesExactNumbers(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(strBool(missing), func(t *testing.T) {
			f := newLifecycleFixture()
			i, dir, _ := startPaused(t, f)
			// An imported exact integer must survive JSON recovery and the next
			// canonical report write without passing through float64.
			state, err := LoadState(dir)
			if err != nil {
				t.Fatal(err)
			}
			first, err := loadReport(filepath.Join(roundDir(dir, 1), "report.json"), &state, 1)
			if err != nil {
				t.Fatal(err)
			}
			first.Case.Environment = map[string]any{"large_record_id": json.Number("9007199254740993")}
			if err := i.persist(roundDir(dir, 1), first); err != nil {
				t.Fatal(err)
			}
			f.noExperiments = true
			r, err := i.Resume(context.Background(), casefile.Case{Description: "More information"}, dir)
			if err != nil || r.Status != StatusPaused {
				t.Fatalf("result=%v error=%v", r, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte("interrupted report write"), 0o600); err != nil {
				t.Fatal(err)
			}
			if missing {
				if err := os.Remove(filepath.Join(roundDir(dir, 1), "report.json")); err != nil {
					t.Fatal(err)
				}
			}
			r, err = i.Close(context.Background(), dir)
			if err != nil || r.Status != StatusClosed {
				t.Fatalf("fallback close result=%v error=%v", r, err)
			}
			history := r.Rounds
			if missing {
				if len(history) != 0 || !r.HistoryIncomplete || len(r.Warnings) == 0 {
					t.Fatal("unreadable earlier history silently disappeared")
				}
			} else {
				if len(history) != 1 || history[0].Rounds != nil {
					t.Fatal("fallback discarded or nested earlier history")
				}
				value := history[0].Case.Environment["large_record_id"]
				if value != json.Number("9007199254740993") {
					t.Fatalf("historical integer changed: %v", value)
				}
				encoded, err := os.ReadFile(filepath.Join(dir, "report.json"))
				if err != nil || !strings.Contains(string(encoded), "9007199254740993") {
					t.Fatal("canonical report rounded exact evidence")
				}
			}
		})
	}
}

func TestCanceledLifecycleTransitionsCleanUpInsteadOfRetaining(t *testing.T) {
	for _, transition := range []string{"initial-suspend", "resume", "invoke", "reconcile-suspend"} {
		t.Run(transition, func(t *testing.T) {
			f := newLifecycleFixture()
			i := &Investigator{Client: f, Config: fixtureConfig()}
			dir := t.TempDir()
			if transition != "initial-suspend" {
				if transition == "reconcile-suspend" {
					f.suspendError = errors.New("suspension response lost")
				}
				r, err := i.Run(context.Background(), casefile.Case{ID: "cancel-transition", Description: "Blocked investigation"}, dir)
				if transition == "reconcile-suspend" {
					if err == nil || r.Status != StatusPausing {
						t.Fatalf("expected uncertain pause: result=%v error=%v", r, err)
					}
				} else if err != nil || r.Status != StatusPaused {
					t.Fatalf("pause result=%v error=%v", r, err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.cancel = cancel
			f.cancelOn = "suspend_sandbox"
			if transition == "resume" {
				f.cancelOn = "resume_sandbox"
			}
			if transition == "invoke" {
				f.cancelOn = "invoke_agent"
			}
			var r *Report
			var err error
			if transition == "initial-suspend" {
				r, err = i.Run(ctx, casefile.Case{ID: "cancel-transition", Description: "Blocked investigation"}, dir)
			} else {
				r, err = i.Resume(ctx, casefile.Case{Description: "Additional information"}, dir)
			}
			if !errors.Is(err, context.Canceled) || r.Status != StatusInterrupted || !r.Cleanup.SandboxDeleted || !r.Cleanup.SessionDeleted {
				t.Fatalf("canceled transition retained resources: result=%v error=%v", r, err)
			}
			state, err := LoadState(dir)
			if err != nil || state.Phase != "closed" {
				t.Fatalf("canceled transition state=%+v error=%v", state, err)
			}
			if transition == "invoke" && (!f.canceled || f.invocations != 1) {
				t.Fatal("canceled uncertain invocation was not recovered for cleanup")
			}
		})
	}
}
