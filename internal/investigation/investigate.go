// Package investigation bounds a real KAgent investigation and independently collects evidence.
package investigation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Backend is the small upstream seam. Production always supplies a real KAgent client.
// Tests may use protocol fixtures; there is deliberately no fake CLI backend.
type Backend interface {
	CallTool(context.Context, string, map[string]any) (map[string]any, error)
	TaskItems(context.Context, string, string) ([]map[string]any, error)
	CancelTask(context.Context, string, string) error
}

type Investigator struct {
	Client Backend
	Config Config
	// Progress receives TriageLab-authored phase messages for the CLI or UI.
	Progress func(string)
	// Trigger labels the launch origin for telemetry ("cli", "ui", "ticket").
	Trigger string
	// Actor records who started the run in the report provenance; it is
	// empty for CLI runs.
	Actor           string
	Secrets         []string
	AdditionalTools []string
}

func str(v any) string         { s, _ := v.(string); return s }
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func integer(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) && n == math.Trunc(n) && n >= -2147483648 && n <= 2147483647 {
			return int(n), true
		}
	}
	return 0, false
}
func terminal(s string) bool {
	return s == "TASK_STATE_COMPLETED" || s == "TASK_STATE_FAILED" || s == "TASK_STATE_CANCELED" || s == "TASK_STATE_REJECTED"
}
func stopped(s string) bool {
	return terminal(s) || s == "TASK_STATE_INPUT_REQUIRED" || s == "TASK_STATE_AUTH_REQUIRED"
}
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (i *Investigator) progress(ctx context.Context, s string) {
	if i.Progress != nil {
		i.Progress(s)
	}
	// Messages are TriageLab-authored, but redact anyway so telemetry can
	// never echo a credential that reached a phase message.
	message, _ := report.Redacted(s, i.Secrets...)
	telemetry.Logger().InfoContext(ctx, message)
}
func (i *Investigator) call(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request, cancel := context.WithTimeout(ctx, time.Duration(i.Config.RequestTimeout)*time.Second)
	defer cancel()
	return i.Client.CallTool(request, name, args)
}
func (i *Investigator) pause(ctx context.Context) error {
	return wait(ctx, time.Duration(i.Config.PollInterval)*time.Second)
}

// readAttempts bounds one read-only KAgent call: it tolerates two consecutive
// transient failures, pausing one poll interval before each repeat. Uploads,
// process starts and kills, and the model invocation are never repeated here.
const readAttempts = 3

// transient reports failures that a read-only call may safely repeat: a lost
// or timed-out connection, such as a dropped port-forward, or a gateway that
// reports its upstream temporarily unavailable. Protocol, JSON-RPC, tool, and
// other HTTP failures are deterministic or unexplained and fail the run.
func transient(err error) bool {
	var upstream *kagent.Error
	if !errors.As(err, &upstream) {
		return false
	}
	switch upstream.Kind {
	case "timeout", "transport":
		return true
	case "http":
		return upstream.Code == http.StatusBadGateway || upstream.Code == http.StatusServiceUnavailable || upstream.Code == http.StatusGatewayTimeout
	}
	return false
}

// retryRead runs a read-only operation and repeats it after transient failures.
// The caller's context still bounds every attempt and pause; an interrupted
// pause returns the last failure for the caller to classify with ctx.
func retryRead[T any](ctx context.Context, i *Investigator, name string, read func() (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		value, err := read()
		if err == nil || !transient(err) {
			return value, err
		}
		if attempt == readAttempts {
			return value, fmt.Errorf("%s failed %d consecutive times: %w", name, attempt, err)
		}
		i.progress(ctx, fmt.Sprintf("KAgent %s read failed; retrying (%d of %d)", name, attempt+1, readAttempts))
		if i.pause(ctx) != nil {
			return value, err
		}
	}
}

// read calls a read-only KAgent tool with bounded transient-failure tolerance.
func (i *Investigator) read(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	return retryRead(ctx, i, name, func() (map[string]any, error) { return i.call(ctx, name, args) })
}
func (i *Investigator) persist(dir string, r *Report) error {
	return report.Write(dir, r, i.Secrets...)
}

// Run always attempts cleanup and persists an error report, including interrupted or failed runs.
func (i *Investigator) Run(parent context.Context, c casefile.Case, dir string) (result *Report, returnErr error) {
	if err := i.Config.Validate(); err != nil {
		return nil, err
	}
	if err := agent.ValidateExtraTools(i.AdditionalTools); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(i.Config.InvestigationTimeout)*time.Second)
	defer cancel()
	trigger := i.Trigger
	if trigger == "" {
		trigger = "unlabeled"
	}
	ctx, runSpan := tracer().Start(ctx, "investigation.run", trace.WithAttributes(
		attribute.String("run_id", c.ID),
		attribute.String("trigger", trigger),
		attribute.String("agent", i.Config.Agent),
		attribute.String("sandbox_template", i.Config.Template),
	))
	runStart := time.Now()
	request := map[string]any{"namespace": i.Config.Namespace, "template": i.Config.Template, "request_id": c.ID, "name": "triagelab-" + c.ID, "ttl_seconds": i.Config.SandboxTTL}
	result = newReport(c, i.Config, request)
	result.StartedBy = i.Actor
	if err := i.persist(dir, result); err != nil {
		return result, err
	}
	observed := map[string]time.Time{}
	defer func() {
		if returnErr != nil {
			if ctx.Err() != nil {
				returnErr = errors.Join(returnErr, ctx.Err())
			}
			result.Status = StatusError
			if errors.Is(returnErr, context.Canceled) {
				result.Status = StatusInterrupted
			} else if errors.Is(returnErr, context.DeadlineExceeded) {
				result.Status = StatusTimedOut
			}
			result.Errors = append(result.Errors, returnErr.Error())
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()
		i.progress(cleanupCtx, "Collecting final evidence and cleaning up")
		// If invocation timed out, recover its task through the already-journalled Session.
		if result.sessionID() != "" && result.taskID() == "" {
			summary, err := i.call(cleanupCtx, "get_session", map[string]any{"session_id": result.Session["id"], "last_turns": 1})
			if err == nil {
				turns, _ := summary["turns"].([]any)
				if len(turns) > 0 {
					result.Session["task_id"] = obj(turns[len(turns)-1])["task_id"]
				}
			}
		}
		if returnErr != nil && result.taskID() != "" {
			captureCtx, cc := context.WithTimeout(cleanupCtx, 20*time.Second)
			if _, err := i.observe(captureCtx, result, observed); err != nil {
				result.Warnings = append(result.Warnings, "Final evidence capture was incomplete")
			}
			cc()
		}
		i.Cleanup(cleanupCtx, result)
		finished := time.Now().UTC()
		result.FinishedAt = &finished
		if err := i.persist(dir, result); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("save report: %w", err))
		}
		status, outcome := string(result.Status), string(result.Outcome)
		runSpan.SetAttributes(attribute.String("status", status), attribute.String("outcome", outcome))
		if result.Status != StatusCompleted {
			runSpan.SetStatus(codes.Error, "investigation ended with "+status)
		}
		runSpan.End()
		loadInstruments().runEnded(status, outcome, trigger, time.Since(runStart).Seconds(), len(result.Experiments))
	}()
	i.progress(ctx, "Creating an isolated sandbox")
	sandboxCtx, sandboxSpan := tracer().Start(ctx, "investigation.sandbox.create")
	sandboxAttempts := 0
	defer func() {
		sandboxSpan.SetAttributes(attribute.Int("attempts", sandboxAttempts))
		sandboxSpan.End()
	}()
	var sandbox map[string]any
	for attempt := 0; ; attempt++ {
		sandboxAttempts = attempt + 1
		var err error
		sandbox, err = i.call(sandboxCtx, "create_sandbox", request)
		if err != nil {
			var upstream *kagent.Error
			uncertain := errors.As(err, &upstream) && (upstream.Kind == "timeout" || upstream.Kind == "transport")
			if uncertain && result.sandboxID() == "" {
				result.CreationUncertain = flag(true)
			}
			if attempt == 0 && uncertain {
				continue
			}
			return result, fmt.Errorf("create sandbox: %w", err)
		}
		if str(sandbox["id"]) == "" {
			return result, fmt.Errorf("create_sandbox omitted resource ID")
		}
		result.Sandbox = sandbox
		result.CreationUncertain = flag(false)
		if err = i.persist(dir, result); err != nil {
			return result, err
		}
		if sandbox["failure"] != nil {
			return result, fmt.Errorf("sandbox preparation failed; inspect controller diagnostics")
		}
		if sandbox["state"] == "RUNTIME_STATE_READY" && sandbox["operation"] == "RUNTIME_OPERATION_NONE" {
			break
		}
		if err = i.pause(sandboxCtx); err != nil {
			return result, err
		}
	}
	sandboxSpan.End()
	sid := str(sandbox["id"])
	i.progress(ctx, fmt.Sprintf("Uploading %d files", len(c.Attachments)))
	uploadCtx, uploadSpan := tracer().Start(ctx, "investigation.inputs.upload")
	defer uploadSpan.End()
	uploadedBytes := 0
	for _, attachment := range c.Attachments {
		uploadedBytes += len(attachment.Content)
		uploaded, err := i.call(uploadCtx, "write_sandbox_file", map[string]any{"sandbox_id": sid, "path": path.Join("/data/workspace", attachment.Path), "data_base64": base64.StdEncoding.EncodeToString(attachment.Content), "mode": 384})
		if err != nil {
			return result, fmt.Errorf("upload file: %w", err)
		}
		n, ok := integer(uploaded["bytes_written"])
		if !ok || n != len(attachment.Content) {
			return result, fmt.Errorf("incomplete sandbox file upload")
		}
	}
	uploadSpan.SetAttributes(attribute.Int("files", len(c.Attachments)), attribute.Int("bytes", uploadedBytes))
	uploadSpan.End()
	message, err := json.Marshal(map[string]any{
		"run_context":    map[string]any{"sandbox_id": sid, "workspace": "/data/workspace", "max_experiments": i.Config.MaxExperiments, "process_timeout_seconds": i.Config.ProcessTimeout},
		"untrusted_case": c,
	})
	if err != nil {
		return result, err
	}
	i.progress(ctx, "Starting the KAgent investigator")
	sessionRequest := map[string]any{"agent": map[string]any{"namespace": i.Config.Namespace, "name": i.Config.Agent}, "request_id": c.ID, "name": "triagelab-" + c.ID}
	result.SessionRequest = sessionRequest
	if err = i.persist(dir, result); err != nil {
		return result, err
	}
	// Journal a Session ID before its first model invocation. Retry only the stable creation key.
	sessionCtx, sessionSpan := tracer().Start(ctx, "investigation.session.create")
	defer sessionSpan.End()
	var created map[string]any
	for attempt := 0; attempt < 2; attempt++ {
		created, err = i.call(sessionCtx, "create_session", sessionRequest)
		if err == nil {
			break
		}
		var upstream *kagent.Error
		if !errors.As(err, &upstream) || (upstream.Kind != "timeout" && upstream.Kind != "transport") {
			return result, err
		}
		result.SessionCreationUncertain = flag(true)
	}
	if err != nil {
		return result, fmt.Errorf("create session (request ID retained in report): %w", err)
	}
	sessionID := str(obj(created["session"])["id"])
	if sessionID == "" {
		return result, fmt.Errorf("create_session omitted resource ID")
	}
	result.Session = map[string]any{"id": sessionID}
	result.SessionCreationUncertain = flag(false)
	if err = i.persist(dir, result); err != nil {
		return result, err
	}
	sessionSpan.End()
	invocation := map[string]any{}
	for key, value := range sessionRequest {
		invocation[key] = value
	}
	invocation["message"] = "Perform this investigation now using the supplied standalone sandbox. Inspect the files, execute experiments, and test a safe correction when feasible. Do not substitute a plan or invented process IDs for tool execution. Return the final structured report only after finishing the investigation or reaching its limits. The following JSON separates trusted run context from untrusted case data.\n\n" + string(message)
	invocationCtx, invocationSpan := tracer().Start(ctx, "investigation.session.invoke")
	defer invocationSpan.End()
	created, err = i.call(invocationCtx, "create_session", invocation)
	if err != nil {
		return result, fmt.Errorf("invoke investigator: %w", err)
	}
	invocationSpan.End()
	taskID := str(obj(created["turn"])["task_id"])
	if taskID == "" {
		return result, fmt.Errorf("investigator invocation omitted task ID")
	}
	result.Session["task_id"] = taskID
	if err = i.persist(dir, result); err != nil {
		return result, err
	}
	for iteration := 0; ; iteration++ {
		finished, pollErr := i.pollOnce(ctx, dir, taskID, result, observed, iteration)
		if finished || pollErr != nil {
			return result, pollErr
		}
	}
}

// pollOnce performs one session poll: read the latest turn, collect evidence,
// and finalize the report when the task reaches a stopping state.
func (i *Investigator) pollOnce(ctx context.Context, dir, taskID string, r *Report, observed map[string]time.Time, iteration int) (finished bool, err error) {
	ctx, span := tracer().Start(ctx, "investigation.session.poll", trace.WithAttributes(
		attribute.Int("iteration", iteration),
		attribute.String("run_id", r.RunID),
	))
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, "poll failed")
		}
		span.End()
	}()
	summary, err := i.read(ctx, "get_session", map[string]any{"session_id": r.sessionID(), "last_turns": 1, "max_bytes": 131072})
	if err != nil {
		return false, err
	}
	turns, ok := summary["turns"].([]any)
	if !ok {
		return false, fmt.Errorf("invalid session turn summary")
	}
	if len(turns) == 0 {
		if err = i.pause(ctx); err != nil {
			return false, err
		}
		return false, nil
	}
	turn := obj(turns[len(turns)-1])
	if str(turn["task_id"]) != taskID {
		return false, fmt.Errorf("unexpected task identity")
	}
	r.TaskState = str(turn["state"])
	reply, err := i.observe(ctx, r, observed)
	if err != nil {
		return false, err
	}
	if err = i.persist(dir, r); err != nil {
		return false, err
	}
	if stopped(str(turn["state"])) {
		if turn["state"] != "TASK_STATE_COMPLETED" {
			return true, fmt.Errorf("investigator stopped with %s", str(turn["state"]))
		}
		for _, e := range r.Experiments {
			if !e.exited() {
				return true, fmt.Errorf("investigator finished with experiments still running")
			}
		}
		if reply == "" {
			if turn["clipped"] == true {
				return true, fmt.Errorf("agent reply was truncated")
			}
			reply = str(turn["reply"])
		}
		analysis, warnings, validateErr := ValidateAnalysis(reply, r.Experiments)
		if validateErr != nil {
			return true, validateErr
		}
		r.Analysis = analysis
		r.Warnings = warnings
		r.Outcome = Outcome(str(analysis["outcome"]))
		r.Status = StatusCompleted
		return true, nil
	}
	if err = i.pause(ctx); err != nil {
		return false, err
	}
	return false, nil
}

func (i *Investigator) observe(ctx context.Context, r *Report, observed map[string]time.Time) (string, error) {
	ctx, span := tracer().Start(ctx, "investigation.evidence.observe", trace.WithAttributes(attribute.String("run_id", r.RunID)))
	reply, err := i.collectEvidence(ctx, r, observed)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
	return reply, err
}

func (i *Investigator) collectEvidence(ctx context.Context, r *Report, observed map[string]time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sid := r.sandboxID()
	items, err := retryRead(ctx, i, "get_task", func() ([]map[string]any, error) {
		return i.Client.TaskItems(ctx, r.sessionID(), r.taskID())
	})
	if err != nil {
		return "", err
	}
	calls, err := kagent.ExtractToolCalls(items)
	if err != nil {
		return "", err
	}
	r.ToolCalls = calls
	starts, err := kagent.ProcessStarts(calls, sid)
	if err != nil {
		return "", err
	}
	known := map[string]bool{}
	for _, e := range r.Experiments {
		known[e.ProcessID] = true
	}
	for _, start := range starts {
		if !known[start.ProcessID] {
			r.Experiments = append(r.Experiments, Experiment{CallID: start.CallID, ProcessID: start.ProcessID, Command: start.Command, CWD: start.CWD, Env: start.Env})
			known[start.ProcessID] = true
			i.progress(ctx, fmt.Sprintf("Experiment %d started", len(r.Experiments)))
		}
	}
	for _, call := range calls {
		if !agent.IsSandboxTool(call.Name) && !slices.Contains(i.AdditionalTools, call.Name) {
			return "", fmt.Errorf("investigator used a tool outside the configured tool set")
		}
	}
	startAttempts := 0
	for _, call := range calls {
		if call.Name == "start_sandbox_process" {
			startAttempts++
		}
	}
	if startAttempts > i.Config.MaxExperiments {
		return "", fmt.Errorf("investigator exceeded the experiment limit")
	}
	for index := range r.Experiments {
		e := &r.Experiments[index]
		if e.exited() && e.OutputsComplete {
			continue
		}
		pid := e.ProcessID
		if _, ok := observed[pid]; !ok {
			observed[pid] = time.Now()
		}
		state, err := i.read(ctx, "get_sandbox_process", map[string]any{"sandbox_id": sid, "process_id": pid})
		if err != nil {
			return "", err
		}
		if str(state["process_id"]) != pid {
			return "", fmt.Errorf("process identity mismatch")
		}
		e.State = str(state["state"])
		if e.exited() {
			code, ok := integer(state["exit_code"])
			if !ok {
				return "", fmt.Errorf("exited process has no valid exit code")
			}
			e.ExitCode = &code
			output, err := i.readOutputs(ctx, sid, pid)
			if err != nil {
				return "", err
			}
			e.Output = &output
			e.OutputsComplete = true
			i.progress(ctx, fmt.Sprintf("Experiment finished: exit %d", code))
		} else if time.Since(observed[pid]) >= time.Duration(i.Config.ProcessTimeout)*time.Second {
			loadInstruments().processKilled()
			_, err := i.call(ctx, "kill_sandbox_process", map[string]any{"sandbox_id": sid, "process_id": pid})
			e.TimedOut = true
			if err != nil {
				return "", fmt.Errorf("kill timed-out process: %w", err)
			}
			return "", fmt.Errorf("process execution time limit reached: %w", context.DeadlineExceeded)
		}
	}
	return kagent.AgentReply(items), nil
}

func (i *Investigator) readOutputs(ctx context.Context, sid, pid string) (Output, error) {
	streams := map[string][]byte{"stdout": {}, "stderr": {}}
	offsets := map[string]int{"stdout": 0, "stderr": 0}
	truncated := false
	for {
		// Offsets advance only after a page is accepted, so a repeated read is identical.
		output, err := i.read(ctx, "read_sandbox_outputs", map[string]any{"sandbox_id": sid, "process_id": pid, "stdout_offset": offsets["stdout"], "stderr_offset": offsets["stderr"]})
		if err != nil {
			return Output{}, err
		}
		before := offsets["stdout"] + offsets["stderr"]
		for _, name := range []string{"stdout", "stderr"} {
			text, ok := output[name+"_base64"].(string)
			if !ok {
				return Output{}, fmt.Errorf("missing process output")
			}
			data, err := base64.StdEncoding.Strict().DecodeString(text)
			if err != nil {
				return Output{}, fmt.Errorf("invalid process output base64")
			}
			next, ok := integer(output[name+"_offset"])
			if !ok || next != offsets[name]+len(data) {
				return Output{}, fmt.Errorf("inconsistent process output offsets")
			}
			offsets[name] = next
			remaining := i.Config.MaxOutputBytes - len(streams["stdout"]) - len(streams["stderr"])
			if len(data) > remaining {
				data = data[:remaining]
				truncated = true
			}
			streams[name] = append(streams[name], data...)
		}
		if output["truncated"] != true || truncated {
			break
		}
		if before == offsets["stdout"]+offsets["stderr"] {
			return Output{}, fmt.Errorf("process output pagination did not advance")
		}
		if len(streams["stdout"])+len(streams["stderr"]) >= i.Config.MaxOutputBytes {
			truncated = true
			break
		}
	}
	return Output{Stdout: strings.ToValidUTF8(string(streams["stdout"]), "�"), Stderr: strings.ToValidUTF8(string(streams["stderr"]), "�"), Truncated: truncated}, nil
}

// Cleanup uses an independent caller-supplied context; get alone does not advance lifecycle work.
func (i *Investigator) Cleanup(ctx context.Context, r *Report) {
	if r.Cleanup == nil {
		r.Cleanup = &CleanupRecord{}
	}
	ctx, span := tracer().Start(ctx, "investigation.cleanup", trace.WithAttributes(attribute.String("run_id", r.RunID)))
	defer func() {
		cleanup := r.Cleanup
		span.SetAttributes(
			attribute.Bool("sandbox_deleted", cleanup.SandboxDeleted),
			attribute.Bool("session_deleted", cleanup.SessionDeleted),
			attribute.Int("errors", len(cleanup.Errors)),
		)
		complete := cleanup.SandboxDeleted && cleanup.SessionDeleted && len(cleanup.Errors) == 0
		if !complete {
			span.SetStatus(codes.Error, "cleanup incomplete")
		}
		span.End()
		loadInstruments().cleanupEnded(complete)
	}()
	issues := []string{}
	for _, resource := range []struct {
		kind      string
		uncertain **bool
		request   map[string]any
		object    *map[string]any
	}{
		{"session", &r.SessionCreationUncertain, r.SessionRequest, &r.Session},
		{"sandbox", &r.CreationUncertain, r.SandboxRequest, &r.Sandbox},
	} {
		if !isSet(*resource.uncertain) {
			continue
		}
		if len(resource.request) == 0 {
			issues = append(issues, resource.kind+" creation uncertain; original request is unavailable")
			continue
		}
		recovered, err := i.call(ctx, "create_"+resource.kind, resource.request)
		if resource.kind == "session" {
			recovered = obj(recovered["session"])
		}
		if err != nil || str(recovered["id"]) == "" {
			issues = append(issues, resource.kind+" creation remains uncertain; recover using saved idempotency request")
			continue
		}
		*resource.object = recovered
		*resource.uncertain = flag(false)
	}
	sid := r.sessionID()
	tid := r.taskID()
	if sid != "" && tid == "" {
		summary, err := i.call(ctx, "get_session", map[string]any{"session_id": sid, "last_turns": 1})
		if err == nil {
			turns, _ := summary["turns"].([]any)
			if len(turns) > 0 {
				turn := obj(turns[len(turns)-1])
				tid = str(turn["task_id"])
				r.Session["task_id"] = tid
				r.TaskState = str(turn["state"])
			}
		}
	}
	if sid != "" && tid != "" && !terminal(r.TaskState) {
		if err := i.Client.CancelTask(ctx, sid, tid); err != nil {
			issues = append(issues, fmt.Sprintf("Cancel task: %v", err))
		}
	}
	for _, resource := range []struct {
		kind    string
		id      string
		deleted *bool
	}{{"session", sid, &r.Cleanup.SessionDeleted}, {"sandbox", r.sandboxID(), &r.Cleanup.SandboxDeleted}} {
		if resource.id == "" {
			continue
		}
		deleted := false
		lastError := "deletion still pending"
		for attempt := 0; attempt < 3; attempt++ {
			value, err := i.call(ctx, "delete_"+resource.kind, map[string]any{resource.kind + "_id": resource.id})
			if err == nil {
				if resource.kind == "session" {
					value = obj(value["session"])
				}
				if value["state"] == "RUNTIME_STATE_DELETED" {
					deleted = true
					break
				}
			} else {
				lastError = err.Error()
			}
			if attempt < 2 {
				if err = i.pause(ctx); err != nil {
					break
				}
			}
		}
		*resource.deleted = deleted
		if !deleted {
			issues = append(issues, resource.kind+" cleanup: "+lastError+"; retry with saved report IDs")
		}
	}
	r.Cleanup.Errors = issues
}
