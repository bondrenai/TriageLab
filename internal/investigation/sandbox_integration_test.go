package investigation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/kagent"
)

// TestLiveSandboxBoundary checks the reproduction sandbox itself against a real
// kagent/Substrate deployment: nested upload, execution, default-deny DNS and
// HTTP egress, process termination, and deletion. It invokes no agent or model,
// so it needs only a caller token and incurs no provider charges. The egress
// probes repeat the commands recorded in docs/verification/sandbox-smoke.json.
func TestLiveSandboxBoundary(t *testing.T) {
	if os.Getenv("TRIAGELAB_SANDBOX_INTEGRATION") != "1" {
		t.Skip("set TRIAGELAB_SANDBOX_INTEGRATION=1 for real kagent/Substrate sandbox checks")
	}
	configPath := os.Getenv("TRIAGELAB_CONFIG")
	if configPath != "" && !filepath.IsAbs(configPath) {
		configPath = filepath.Join("..", "..", configPath)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	token := os.Getenv(config.TokenEnv)
	if token == "" {
		t.Fatal("live sandbox checks require a caller token")
	}
	live := &Investigator{Client: livePauseClient(t, config, token), Config: config}
	id := liveSandboxRequestID(t)
	record := &liveSandboxRecord{
		Kind: "real-upstream-sandbox-smoke", RequestID: id, StartedAt: stamp(),
		Upstream: map[string]string{"kagent": kagent.Version}, Experiments: []liveSandboxProcess{},
	}
	// Registered first so it runs last and keeps whatever was observed, including after a failure.
	t.Cleanup(func() { saveLiveSandboxRecord(t, record, token) })

	// The sandbox expires at its TTL regardless, so nothing here outlives it.
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(config.SandboxTTL)*time.Second)
	defer cancel()
	sandboxID, deleted := "", false
	t.Cleanup(func() {
		if sandboxID == "" || deleted {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cleanupCancel()
		final, err := deleteLiveSandbox(cleanupCtx, live, sandboxID)
		if err != nil {
			t.Errorf("sandbox %s was not deleted; its TTL remains the backstop: %v", sandboxID, err)
			return
		}
		record.Cleanup = final
	})

	// No retention flag: this is an ordinary sandbox on a released or forked controller.
	request := map[string]any{"namespace": config.Namespace, "template": config.Template, "request_id": id, "name": "triagelab-" + id, "ttl_seconds": config.SandboxTTL}
	var sandbox map[string]any
	for {
		// Reads do not advance a pending lifecycle operation; the same creation request does.
		sandbox, err = live.call(ctx, "create_sandbox", request)
		if err != nil {
			t.Fatalf("create sandbox: %v", err)
		}
		if sandboxID = str(sandbox["id"]); sandboxID == "" {
			t.Fatal("create_sandbox omitted its resource ID")
		}
		if sandbox["failure"] != nil {
			t.Fatal("sandbox preparation failed; inspect controller diagnostics")
		}
		if sandbox["state"] == "RUNTIME_STATE_READY" && sandbox["operation"] == "RUNTIME_OPERATION_NONE" {
			break
		}
		if err = live.pause(ctx); err != nil {
			t.Fatalf("sandbox did not become ready: %v", err)
		}
	}
	record.Sandbox = sandbox

	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	content := "boundary " + hex.EncodeToString(nonce[:]) + "\n"
	uploaded, err := live.call(ctx, "write_sandbox_file", map[string]any{"sandbox_id": sandboxID, "path": "/data/workspace/nested/input/probe.txt", "data_base64": base64.StdEncoding.EncodeToString([]byte(content)), "mode": 384})
	if err != nil {
		t.Fatalf("upload nested file: %v", err)
	}
	if written, ok := integer(uploaded["bytes_written"]); !ok || written != len(content) {
		t.Fatal("incomplete sandbox file upload")
	}

	run := func(label string, command ...string) liveSandboxProcess {
		t.Helper()
		process := startLiveSandboxProcess(t, ctx, live, sandboxID, label, command)
		awaitLiveSandboxExit(t, ctx, live, sandboxID, &process)
		outputs, err := live.readOutputs(ctx, sandboxID, process.ProcessID)
		if err != nil {
			t.Fatalf("read %s output: %v", label, err)
		}
		process.Stdout, process.Stderr = outputs.Stdout, outputs.Stderr
		record.Experiments = append(record.Experiments, process)
		return process
	}

	// The denial probes below mean nothing unless ordinary execution works.
	control := run("nested-upload-readback", "python3", "-c", "import sys; sys.stdout.write(open('/data/workspace/nested/input/probe.txt').read())")
	if control.ExitCode != 0 || control.Stdout != content {
		t.Fatalf("the sandbox did not run a process against the uploaded file: exit %d", control.ExitCode)
	}

	// A probe that fails in an unrecorded way is reported for review, not
	// counted as a denial. The 403 is the egress mediator's recorded refusal;
	// this probe cannot distinguish it from an origin's own 403.
	dns := run("default-deny-egress", "python3", "-c", "import socket; socket.getaddrinfo('example.com',443)")
	switch {
	case dns.ExitCode == 0:
		t.Error("the sandbox resolved an external name; default-deny DNS egress is not in effect")
	case !strings.Contains(dns.Stderr, "socket.gaierror"):
		t.Errorf("the DNS probe failed differently from the recorded denial: %s", liveLastLine(dns.Stderr))
	}
	direct := run("direct-ip-http-denial", "python3", "-c", "import urllib.request; urllib.request.urlopen('http://1.1.1.1',timeout=3)")
	switch {
	case direct.ExitCode == 0:
		t.Error("the sandbox reached an external address over HTTP; default-deny egress is not in effect")
	case !strings.Contains(direct.Stderr, "HTTP Error 403"):
		t.Errorf("the direct-IP HTTP probe failed differently from the recorded 403 denial: %s", liveLastLine(direct.Stderr))
	}

	victim := startLiveSandboxProcess(t, ctx, live, sandboxID, "kill-verification", []string{"python3", "-c", "import time; time.sleep(600)"})
	before, err := live.call(ctx, "get_sandbox_process", map[string]any{"sandbox_id": sandboxID, "process_id": victim.ProcessID})
	if err != nil || before["state"] == "PROCESS_STATE_EXITED" {
		t.Fatalf("the long-running process was not observed before termination: %v", err)
	}
	// The acknowledgment only confirms signal delivery; the exit is observed separately.
	if _, err = live.call(ctx, "kill_sandbox_process", map[string]any{"sandbox_id": sandboxID, "process_id": victim.ProcessID}); err != nil {
		t.Fatalf("kill process: %v", err)
	}
	awaitLiveSandboxExit(t, ctx, live, sandboxID, &victim)
	record.Kill = map[string]any{"process_id": victim.ProcessID, "state": victim.State, "exit_code": victim.ExitCode}
	if victim.ExitCode != 137 {
		t.Errorf("the killed process exited %d, not 137 (SIGKILL)", victim.ExitCode)
	}

	final, err := deleteLiveSandbox(ctx, live, sandboxID)
	if err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
	deleted = true
	record.Cleanup = final
	observed, err := live.call(ctx, "get_sandbox", map[string]any{"sandbox_id": sandboxID})
	if err != nil || observed["state"] != "RUNTIME_STATE_DELETED" {
		t.Fatalf("sandbox deletion was not confirmed by a separate read: %v", err)
	}
	if !t.Failed() {
		t.Log("Real sandbox upload, execution, DNS and HTTP egress denial, termination, and deletion verified without an agent")
	}
}

// liveSandboxRecord keeps the shape of docs/verification/sandbox-smoke.json so a
// new run can be compared with the recorded one.
type liveSandboxRecord struct {
	Kind         string               `json:"kind"`
	AgentInvoked bool                 `json:"agent_invoked"`
	RequestID    string               `json:"request_id"`
	StartedAt    string               `json:"started_at"`
	FinishedAt   string               `json:"finished_at"`
	Upstream     map[string]string    `json:"upstream"`
	Experiments  []liveSandboxProcess `json:"experiments"`
	Sandbox      map[string]any       `json:"sandbox,omitempty"`
	Kill         map[string]any       `json:"kill_verification,omitempty"`
	Cleanup      map[string]any       `json:"cleanup,omitempty"`
}

type liveSandboxProcess struct {
	Label     string   `json:"label"`
	Command   []string `json:"command"`
	ExitCode  int      `json:"exit_code"`
	ProcessID string   `json:"process_id"`
	State     string   `json:"state"`
	Stdout    string   `json:"stdout"`
	Stderr    string   `json:"stderr"`
}

// liveSandboxRequestID has the UUID shape of a case ID, which an investigation
// uses as its creation idempotency key.
func liveSandboxRequestID(t *testing.T) string {
	t.Helper()
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

// A process start has no idempotency receipt, so it is never retried.
func startLiveSandboxProcess(t *testing.T, ctx context.Context, live *Investigator, sandboxID, label string, command []string) liveSandboxProcess {
	t.Helper()
	started, err := live.call(ctx, "start_sandbox_process", map[string]any{"sandbox_id": sandboxID, "command": command})
	if err != nil {
		t.Fatalf("start %s: %v", label, err)
	}
	process := liveSandboxProcess{Label: label, Command: command, ProcessID: str(started["process_id"])}
	if process.ProcessID == "" {
		t.Fatalf("start %s omitted its process ID", label)
	}
	return process
}

func awaitLiveSandboxExit(t *testing.T, ctx context.Context, live *Investigator, sandboxID string, process *liveSandboxProcess) {
	t.Helper()
	deadline := time.Now().Add(time.Duration(live.Config.ProcessTimeout) * time.Second)
	for {
		state, err := live.call(ctx, "get_sandbox_process", map[string]any{"sandbox_id": sandboxID, "process_id": process.ProcessID})
		if err != nil {
			t.Fatalf("observe %s: %v", process.Label, err)
		}
		if str(state["process_id"]) != process.ProcessID {
			t.Fatalf("%s process identity mismatch", process.Label)
		}
		process.State = str(state["state"])
		if process.State == "PROCESS_STATE_EXITED" {
			code, ok := integer(state["exit_code"])
			if !ok {
				t.Fatalf("exited %s process has no valid exit code", process.Label)
			}
			process.ExitCode = code
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not exit within the process time limit", process.Label)
		}
		if err = live.pause(ctx); err != nil {
			t.Fatalf("observe %s: %v", process.Label, err)
		}
	}
}

// deleteLiveSandbox repeats the deletion until the service reports it complete.
func deleteLiveSandbox(ctx context.Context, live *Investigator, sandboxID string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	last := errors.New("deletion still pending")
	for {
		value, err := live.call(ctx, "delete_sandbox", map[string]any{"sandbox_id": sandboxID})
		if err == nil && value["state"] == "RUNTIME_STATE_DELETED" {
			return value, nil
		}
		if err != nil && ctx.Err() == nil {
			last = err
		}
		if live.pause(ctx) != nil {
			return nil, last
		}
	}
}

func saveLiveSandboxRecord(t *testing.T, record *liveSandboxRecord, token string) {
	t.Helper()
	dir := os.Getenv("TRIAGELAB_REPORT_DIR")
	if dir == "" {
		return
	}
	record.FinishedAt = stamp()
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		t.Errorf("encode sandbox record: %v", err)
		return
	}
	if bytes.Contains(encoded.Bytes(), []byte(token)) {
		t.Error("the sandbox record contains the caller token and was not saved")
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Errorf("save sandbox record: %v", err)
		return
	}
	name := filepath.Join(dir, "sandbox-boundary-"+record.RequestID+".json")
	if err := os.WriteFile(name, encoded.Bytes(), 0o600); err != nil {
		t.Errorf("save sandbox record: %v", err)
		return
	}
	t.Logf("sandbox record: %s", name)
}

func liveLastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return lines[len(lines)-1]
}
