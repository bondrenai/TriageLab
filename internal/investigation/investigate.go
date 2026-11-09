// Package investigation bounds a real KAgent investigation and independently collects evidence.
package investigation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
	"github.com/bondrenai/TriageLab/internal/report"
)

// Backend is the small upstream seam. Production always supplies a real KAgent client.
// Tests may use protocol fixtures; there is deliberately no fake CLI backend.
type Backend interface {
	CallTool(context.Context, string, map[string]any) (map[string]any, error)
	TaskItems(context.Context, string, string) ([]map[string]any, error)
	CancelTask(context.Context, string, string) error
}

type Investigator struct {
	Client          Backend
	Config          Config
	Progress        func(string)
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
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
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
func (i *Investigator) progress(s string) {
	if i.Progress != nil {
		i.Progress(s)
	}
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
func (i *Investigator) persist(dir string, r map[string]any) error {
	return report.Write(dir, r, i.Secrets...)
}

// Run always attempts cleanup and persists an error report, including interrupted or failed runs.
func (i *Investigator) Run(parent context.Context, c casefile.Case, dir string) (result map[string]any, returnErr error) {
	if err := i.Config.Validate(); err != nil {
		return nil, err
	}
	if err := agent.ValidateExtraTools(i.AdditionalTools); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(i.Config.InvestigationTimeout)*time.Second)
	defer cancel()
	request := map[string]any{"namespace": i.Config.Namespace, "template": i.Config.Template, "request_id": c.ID, "name": "triagelab-" + c.ID, "ttl_seconds": i.Config.SandboxTTL}
	result = map[string]any{
		"schema_version": 1, "run_id": c.ID, "case": c, "started_at": stamp(), "finished_at": nil,
		"status": "running", "outcome": "inconclusive", "sandbox": map[string]any{}, "session": map[string]any{},
		"agent":            map[string]string{"namespace": i.Config.Namespace, "name": i.Config.Agent},
		"sandbox_template": i.Config.Template,
		"sandbox_request":  request, "experiments": []map[string]any{}, "tool_calls": []kagent.ToolCall{}, "analysis": map[string]any{},
		"warnings": []string{}, "errors": []string{},
		"connection": map[string]any{"endpoint": i.Config.Endpoint, "user_id": i.Config.UserID, "namespace": i.Config.Namespace},
		"cleanup":    map[string]any{"sandbox_deleted": false, "session_deleted": false, "errors": []string{}},
		"upstream":   map[string]any{"kagent": kagent.Version, "substrate": "v1.0.0"},
	}
	if err := i.persist(dir, result); err != nil {
		return result, err
	}
	observed := map[string]time.Time{}
	defer func() {
		if returnErr != nil {
			if ctx.Err() != nil {
				returnErr = errors.Join(returnErr, ctx.Err())
			}
			status := "error"
			if errors.Is(returnErr, context.Canceled) {
				status = "interrupted"
			} else if errors.Is(returnErr, context.DeadlineExceeded) {
				status = "timed_out"
			}
			result["status"] = status
			result["errors"] = append(result["errors"].([]string), returnErr.Error())
		}
		i.progress("Collecting final evidence and cleaning up")
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()
		// If invocation timed out, recover its task through the already-journalled Session.
		session := obj(result["session"])
		if str(session["id"]) != "" && str(session["task_id"]) == "" {
			summary, err := i.call(cleanupCtx, "get_session", map[string]any{"session_id": session["id"], "last_turns": 1})
			if err == nil {
				turns, _ := summary["turns"].([]any)
				if len(turns) > 0 {
					session["task_id"] = obj(turns[len(turns)-1])["task_id"]
				}
			}
		}
		if returnErr != nil && str(session["task_id"]) != "" {
			captureCtx, cc := context.WithTimeout(cleanupCtx, 20*time.Second)
			if _, err := i.observe(captureCtx, result, observed); err != nil {
				result["warnings"] = append(result["warnings"].([]string), "Final evidence capture was incomplete")
			}
			cc()
		}
		i.Cleanup(cleanupCtx, result)
		result["finished_at"] = stamp()
		if err := i.persist(dir, result); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("save report: %w", err))
		}
	}()
	i.progress("Creating an isolated sandbox")
	var sandbox map[string]any
	for attempt := 0; ; attempt++ {
		var err error
		sandbox, err = i.call(ctx, "create_sandbox", request)
		if err != nil {
			var upstream *kagent.Error
			uncertain := errors.As(err, &upstream) && (upstream.Kind == "timeout" || upstream.Kind == "transport")
			if uncertain && str(obj(result["sandbox"])["id"]) == "" {
				result["creation_uncertain"] = true
			}
			if attempt == 0 && uncertain {
				continue
			}
			return result, fmt.Errorf("create sandbox: %w", err)
		}
		if str(sandbox["id"]) == "" {
			return result, fmt.Errorf("create_sandbox omitted resource ID")
		}
		result["sandbox"] = sandbox
		result["creation_uncertain"] = false
		if err = i.persist(dir, result); err != nil {
			return result, err
		}
		if sandbox["failure"] != nil {
			return result, fmt.Errorf("sandbox preparation failed; inspect controller diagnostics")
		}
		if sandbox["state"] == "RUNTIME_STATE_READY" && sandbox["operation"] == "RUNTIME_OPERATION_NONE" {
			break
		}
		if err = i.pause(ctx); err != nil {
			return result, err
		}
	}
	sid := str(sandbox["id"])
	i.progress(fmt.Sprintf("Uploading %d files", len(c.Attachments)))
	for _, attachment := range c.Attachments {
		uploaded, err := i.call(ctx, "write_sandbox_file", map[string]any{"sandbox_id": sid, "path": path.Join("/data/workspace", attachment.Path), "data_base64": base64.StdEncoding.EncodeToString(attachment.Content), "mode": 384})
		if err != nil {
			return result, fmt.Errorf("upload file: %w", err)
		}
		n, ok := integer(uploaded["bytes_written"])
		if !ok || n != len(attachment.Content) {
			return result, fmt.Errorf("incomplete sandbox file upload")
		}
	}
	message, err := json.Marshal(map[string]any{
		"run_context":    map[string]any{"sandbox_id": sid, "workspace": "/data/workspace", "max_experiments": i.Config.MaxExperiments, "process_timeout_seconds": i.Config.ProcessTimeout},
		"untrusted_case": c,
	})
	if err != nil {
		return result, err
	}
	i.progress("Starting the KAgent investigator")
	sessionRequest := map[string]any{"agent": map[string]any{"namespace": i.Config.Namespace, "name": i.Config.Agent}, "request_id": c.ID, "name": "triagelab-" + c.ID}
	result["session_request"] = sessionRequest
	if err = i.persist(dir, result); err != nil {
		return result, err
	}
	// Journal a Session ID before its first model invocation. Retry only the stable creation key.
	var created map[string]any
	for attempt := 0; attempt < 2; attempt++ {
		created, err = i.call(ctx, "create_session", sessionRequest)
		if err == nil {
			break
		}
		var upstream *kagent.Error
		if !errors.As(err, &upstream) || (upstream.Kind != "timeout" && upstream.Kind != "transport") {
			return result, err
		}
		result["session_creation_uncertain"] = true
	}
	if err != nil {
		return result, fmt.Errorf("create session (request ID retained in report): %w", err)
	}
	sessionID := str(obj(created["session"])["id"])
	if sessionID == "" {
		return result, fmt.Errorf("create_session omitted resource ID")
	}
	result["session"] = map[string]any{"id": sessionID}
	result["session_creation_uncertain"] = false
	if err = i.persist(dir, result); err != nil {
		return result, err
	}
	invocation := map[string]any{}
	for key, value := range sessionRequest {
		invocation[key] = value
	}
	invocation["message"] = "Perform this investigation now using the supplied standalone sandbox. Inspect the files, execute experiments, and test a safe correction when feasible. Do not substitute a plan or invented process IDs for tool execution. Return the final structured report only after finishing the investigation or reaching its limits. The following JSON separates trusted run context from untrusted case data.\n\n" + string(message)
	created, err = i.call(ctx, "create_session", invocation)
	if err != nil {
		return result, fmt.Errorf("invoke investigator: %w", err)
	}
	taskID := str(obj(created["turn"])["task_id"])
	if taskID == "" {
		return result, fmt.Errorf("investigator invocation omitted task ID")
	}
	obj(result["session"])["task_id"] = taskID
	if err = i.persist(dir, result); err != nil {
		return result, err
	}
	for {
		summary, err := i.call(ctx, "get_session", map[string]any{"session_id": sessionID, "last_turns": 1, "max_bytes": 131072})
		if err != nil {
			return result, err
		}
		turns, ok := summary["turns"].([]any)
		if !ok {
			return result, fmt.Errorf("invalid session turn summary")
		}
		if len(turns) == 0 {
			if err = i.pause(ctx); err != nil {
				return result, err
			}
			continue
		}
		turn := obj(turns[len(turns)-1])
		if str(turn["task_id"]) != taskID {
			return result, fmt.Errorf("unexpected task identity")
		}
		result["task_state"] = turn["state"]
		reply, err := i.observe(ctx, result, observed)
		if err != nil {
			return result, err
		}
		if err = i.persist(dir, result); err != nil {
			return result, err
		}
		if stopped(str(turn["state"])) {
			if turn["state"] != "TASK_STATE_COMPLETED" {
				return result, fmt.Errorf("investigator stopped with %s", str(turn["state"]))
			}
			experiments := result["experiments"].([]map[string]any)
			for _, e := range experiments {
				if e["state"] != "PROCESS_STATE_EXITED" {
					return result, fmt.Errorf("investigator finished with experiments still running")
				}
			}
			if reply == "" {
				if turn["clipped"] == true {
					return result, fmt.Errorf("agent reply was truncated")
				}
				reply = str(turn["reply"])
			}
			analysis, warnings, err := ValidateAnalysis(reply, experiments)
			if err != nil {
				return result, err
			}
			result["analysis"] = analysis
			result["warnings"] = warnings
			result["outcome"] = analysis["outcome"]
			result["status"] = "completed"
			return result, nil
		}
		if err = i.pause(ctx); err != nil {
			return result, err
		}
	}
}

func (i *Investigator) observe(ctx context.Context, r map[string]any, observed map[string]time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	session := obj(r["session"])
	sid := str(obj(r["sandbox"])["id"])
	items, err := i.Client.TaskItems(ctx, str(session["id"]), str(session["task_id"]))
	if err != nil {
		return "", err
	}
	calls, err := kagent.ExtractToolCalls(items)
	if err != nil {
		return "", err
	}
	r["tool_calls"] = calls
	starts, err := kagent.ProcessStarts(calls, sid)
	if err != nil {
		return "", err
	}
	experiments := r["experiments"].([]map[string]any)
	known := map[string]bool{}
	for _, e := range experiments {
		known[str(e["process_id"])] = true
	}
	for _, start := range starts {
		if !known[start.ProcessID] {
			experiments = append(experiments, map[string]any{"call_id": start.CallID, "process_id": start.ProcessID, "command": start.Command, "cwd": start.CWD, "env": start.Env})
			known[start.ProcessID] = true
			i.progress(fmt.Sprintf("Experiment %d started", len(experiments)))
		}
	}
	r["experiments"] = experiments
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
	for _, e := range experiments {
		if e["state"] == "PROCESS_STATE_EXITED" && e["outputs_complete"] == true {
			continue
		}
		pid := str(e["process_id"])
		if _, ok := observed[pid]; !ok {
			observed[pid] = time.Now()
		}
		state, err := i.call(ctx, "get_sandbox_process", map[string]any{"sandbox_id": sid, "process_id": pid})
		if err != nil {
			return "", err
		}
		if str(state["process_id"]) != pid {
			return "", fmt.Errorf("process identity mismatch")
		}
		e["state"] = state["state"]
		if state["state"] == "PROCESS_STATE_EXITED" {
			code, ok := integer(state["exit_code"])
			if !ok {
				return "", fmt.Errorf("exited process has no valid exit code")
			}
			e["exit_code"] = code
			outputs, err := i.readOutputs(ctx, sid, pid)
			if err != nil {
				return "", err
			}
			for key, value := range outputs {
				e[key] = value
			}
			e["outputs_complete"] = true
			i.progress(fmt.Sprintf("Experiment finished: exit %d", code))
		} else if time.Since(observed[pid]) >= time.Duration(i.Config.ProcessTimeout)*time.Second {
			_, err := i.call(ctx, "kill_sandbox_process", map[string]any{"sandbox_id": sid, "process_id": pid})
			e["timed_out"] = true
			if err != nil {
				return "", fmt.Errorf("kill timed-out process: %w", err)
			}
			return "", fmt.Errorf("process execution time limit reached: %w", context.DeadlineExceeded)
		}
	}
	return kagent.AgentReply(items), nil
}

func (i *Investigator) readOutputs(ctx context.Context, sid, pid string) (map[string]any, error) {
	streams := map[string][]byte{"stdout": {}, "stderr": {}}
	offsets := map[string]int{"stdout": 0, "stderr": 0}
	truncated := false
	for {
		output, err := i.call(ctx, "read_sandbox_outputs", map[string]any{"sandbox_id": sid, "process_id": pid, "stdout_offset": offsets["stdout"], "stderr_offset": offsets["stderr"]})
		if err != nil {
			return nil, err
		}
		before := offsets["stdout"] + offsets["stderr"]
		for _, name := range []string{"stdout", "stderr"} {
			text, ok := output[name+"_base64"].(string)
			if !ok {
				return nil, fmt.Errorf("missing process output")
			}
			data, err := base64.StdEncoding.Strict().DecodeString(text)
			if err != nil {
				return nil, fmt.Errorf("invalid process output base64")
			}
			next, ok := integer(output[name+"_offset"])
			if !ok || next != offsets[name]+len(data) {
				return nil, fmt.Errorf("inconsistent process output offsets")
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
			return nil, fmt.Errorf("process output pagination did not advance")
		}
		if len(streams["stdout"])+len(streams["stderr"]) >= i.Config.MaxOutputBytes {
			truncated = true
			break
		}
	}
	return map[string]any{"stdout": strings.ToValidUTF8(string(streams["stdout"]), "�"), "stderr": strings.ToValidUTF8(string(streams["stderr"]), "�"), "truncated": truncated}, nil
}

// Cleanup uses an independent caller-supplied context; get alone does not advance lifecycle work.
func (i *Investigator) Cleanup(ctx context.Context, r map[string]any) {
	cleanup := obj(r["cleanup"])
	issues := []string{}
	for _, resource := range []struct{ kind, marker, request string }{{"session", "session_creation_uncertain", "session_request"}, {"sandbox", "creation_uncertain", "sandbox_request"}} {
		if r[resource.marker] != true {
			continue
		}
		request := obj(r[resource.request])
		if len(request) == 0 {
			issues = append(issues, resource.kind+" creation uncertain; original request is unavailable")
			continue
		}
		recovered, err := i.call(ctx, "create_"+resource.kind, request)
		if resource.kind == "session" {
			recovered = obj(recovered["session"])
		}
		if err != nil || str(recovered["id"]) == "" {
			issues = append(issues, resource.kind+" creation remains uncertain; recover using saved idempotency request")
			continue
		}
		r[resource.kind] = recovered
		r[resource.marker] = false
	}
	session := obj(r["session"])
	sid := str(session["id"])
	tid := str(session["task_id"])
	if sid != "" && tid == "" {
		summary, err := i.call(ctx, "get_session", map[string]any{"session_id": sid, "last_turns": 1})
		if err == nil {
			turns, _ := summary["turns"].([]any)
			if len(turns) > 0 {
				turn := obj(turns[len(turns)-1])
				tid = str(turn["task_id"])
				session["task_id"] = tid
				r["task_state"] = turn["state"]
			}
		}
	}
	if sid != "" && tid != "" && !terminal(str(r["task_state"])) {
		if err := i.Client.CancelTask(ctx, sid, tid); err != nil {
			issues = append(issues, fmt.Sprintf("Cancel task: %v", err))
		}
	}
	for _, resource := range []struct{ kind, id string }{{"session", sid}, {"sandbox", str(obj(r["sandbox"])["id"])}} {
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
		cleanup[resource.kind+"_deleted"] = deleted
		if !deleted {
			issues = append(issues, resource.kind+" cleanup: "+lastError+"; retry with saved report IDs")
		}
	}
	cleanup["errors"] = issues
}
