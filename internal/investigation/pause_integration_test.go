package investigation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

// This opt-in test invokes a real model and real sandbox. Each lifecycle action
// runs in a separate process so resume cannot depend on in-memory run state.
func TestLiveInvestigationPauseResume(t *testing.T) {
	if os.Getenv("TRIAGELAB_PAUSE_INTEGRATION") != "1" {
		t.Skip("set TRIAGELAB_PAUSE_INTEGRATION=1 for real pause/resume/model integration")
	}
	config, token := livePauseConfig(t)
	if stage := os.Getenv("TRIAGELAB_PAUSE_TEST_STAGE"); stage != "" {
		livePauseStage(t, stage, config, token)
		return
	}
	root := t.TempDir()
	if output := os.Getenv("TRIAGELAB_REPORT_DIR"); output != "" {
		if err := os.MkdirAll(output, 0o700); err != nil {
			t.Fatal(err)
		}
		var err error
		root, err = os.MkdirTemp(output, "pause-resume-")
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "investigation")
	for _, name := range []string{"original.txt", "additional.txt"} {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(name+" "+hex.EncodeToString(nonce[:])+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client := livePauseClient(t, config, token)
	t.Cleanup(func() {
		if state, err := LoadState(dir); err == nil && state.Phase != "closed" {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			defer cancel()
			investigator := Investigator{Client: client, Config: config, Secrets: []string{token}}
			if _, err := investigator.Close(ctx, dir); err != nil {
				t.Errorf("live resource cleanup failed; retain %s: %v", dir, err)
			}
		}
	})
	stage := func(name string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Duration(config.InvestigationTimeout+120)*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveInvestigationPauseResume$", "-test.v")
		command.Env = append(os.Environ(), "TRIAGELAB_PAUSE_TEST_STAGE="+name, "TRIAGELAB_PAUSE_TEST_DIR="+root)
		if refreshed := os.Getenv("TRIAGELAB_PAUSE_RESUME_TOKEN"); name != "run" && refreshed != "" {
			command.Env = append(command.Env, config.TokenEnv+"="+refreshed)
		}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("live %s subprocess failed: %v\n%s\nreport: %s", name, err, output, dir)
		}
		t.Logf("%s subprocess exited successfully", name)
	}

	stage("run")
	first := livePausedState(t, client, dir, 1)
	archivePath := filepath.Join(dir, "rounds", "0001", "report.json")
	archive, err := os.ReadFile(archivePath)
	if err != nil || !json.Valid(archive) {
		t.Fatalf("read archived report: %v", err)
	}
	stage("resume")
	second := livePausedState(t, client, dir, 2)
	if first.SandboxID != second.SandboxID || first.SessionID != second.SessionID || first.TaskID == second.TaskID {
		t.Fatal("resume did not preserve the sandbox/session and create a new task")
	}
	currentArchive, err := os.ReadFile(archivePath)
	if err != nil || !bytes.Equal(currentArchive, archive) {
		t.Fatal("resuming changed the previous round's evidence archive")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(config.RequestTimeout)*time.Second)
	items, err := client.TaskItems(ctx, first.SessionID, first.TaskID)
	cancel()
	if err != nil || len(items) == 0 {
		t.Fatalf("the original investigator task is no longer readable: %v", err)
	}
	stage("close")
	closed, err := LoadState(dir)
	if err != nil || closed.Phase != "closed" {
		t.Fatalf("close did not persist cleanup: state=%s error=%v", closed.Phase, err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), time.Duration(config.RequestTimeout)*time.Second)
	defer cancel()
	sandbox, err := client.CallTool(ctx, "get_sandbox", map[string]any{"sandbox_id": first.SandboxID})
	if err != nil || sandbox["state"] != "RUNTIME_STATE_DELETED" {
		t.Fatalf("sandbox deletion not confirmed: %v", err)
	}
	_, err = client.CallTool(ctx, "get_session", map[string]any{"session_id": first.SessionID, "last_turns": 1})
	var upstream *kagent.Error
	if !errors.As(err, &upstream) || upstream.Kind != "tool" {
		t.Fatalf("deleted session remains readable or its deletion could not be checked: %v", err)
	}
	t.Logf("Real model, two pause boundaries, process restart, retained/new files, same Session/Sandbox, and close verified: %s", dir)
}

func livePauseConfig(t *testing.T) (Config, string) {
	t.Helper()
	name := os.Getenv("TRIAGELAB_CONFIG")
	if name != "" && !filepath.IsAbs(name) {
		name = filepath.Join("..", "..", name)
	}
	config, err := LoadConfig(name)
	if err != nil {
		t.Fatal(err)
	}
	token := os.Getenv(config.TokenEnv)
	if token == "" {
		t.Fatal("live pause integration requires a caller token and a real model-backed Agent")
	}
	return config, token
}

func livePauseClient(t *testing.T, config Config, token string) *kagent.Client {
	t.Helper()
	var options []kagent.Option
	if config.AllowClusterHTTP {
		options = append(options, kagent.AllowClusterHTTP())
	}
	client, err := kagent.New(config.Endpoint, config.UserID, token, time.Duration(config.RequestTimeout)*time.Second, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func livePausedState(t *testing.T, client *kagent.Client, dir string, round int) State {
	t.Helper()
	state, err := LoadState(dir)
	if err != nil || state.Phase != "paused" || state.Round != round || state.SandboxID == "" || state.SessionID == "" || state.TaskID == "" {
		t.Fatalf("expected paused round %d with retained resources: state=%s error=%v", round, state.Phase, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(state.Config.RequestTimeout)*time.Second)
	defer cancel()
	sandbox, err := client.CallTool(ctx, "get_sandbox", map[string]any{"sandbox_id": state.SandboxID})
	if err != nil || sandbox["state"] != "RUNTIME_STATE_SUSPENDED" || sandbox["operation"] != "RUNTIME_OPERATION_NONE" || sandbox["retain_while_suspended"] != true || str(sandbox["expires_at"]) != "" {
		t.Fatalf("retained suspension not confirmed: %v", err)
	}
	session, err := client.CallTool(ctx, "get_session", map[string]any{"session_id": state.SessionID, "last_turns": 1})
	turns, ok := integer(session["total_turns"])
	if err != nil || str(obj(session["session"])["id"]) != state.SessionID || !ok || turns != round {
		t.Fatalf("same investigator conversation did not retain %d turns: %v", round, err)
	}
	return state
}

func livePauseStage(t *testing.T, stage string, config Config, token string) {
	t.Helper()
	root := os.Getenv("TRIAGELAB_PAUSE_TEST_DIR")
	if !filepath.IsAbs(root) {
		t.Fatal("live subprocess requires an absolute fixture directory")
	}
	dir := filepath.Join(root, "investigation")
	client := livePauseClient(t, config, token)
	backend := &livePauseBackend{Client: client, root: root, dir: dir}
	requested := false
	investigator := Investigator{Client: backend, Config: config, Secrets: []string{token}, Progress: func(message string) {
		if !requested && strings.HasPrefix(message, "Experiment ") && strings.HasSuffix(message, " started") {
			if err := RequestPause(dir); err != nil {
				t.Errorf("request pause after observed experiment: %v", err)
			}
			requested = true
		}
	}}
	var result *Report
	var err error
	switch stage {
	case "run":
		fixture, pathErr := filepath.Abs(filepath.Join("..", "..", "examples", "config-bug"))
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		description, readErr := os.ReadFile(filepath.Join(fixture, "issue.md"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		c, ingestErr := casefile.Ingest(string(description), fixture, []string{filepath.Join(root, "original.txt")}, nil)
		if ingestErr != nil {
			t.Fatal(ingestErr)
		}
		result, err = investigator.Run(t.Context(), c, dir)
	case "resume":
		added, ingestErr := casefile.Ingest("Continue the original configuration investigation with this additional customer evidence. Inspect the new attachment and run a fresh verification experiment in the retained workspace.", "", []string{filepath.Join(root, "additional.txt")}, nil)
		if ingestErr != nil {
			t.Fatal(ingestErr)
		}
		result, err = investigator.Resume(t.Context(), added, dir)
		if err == nil && !backend.filesChecked {
			t.Fatal("continuation did not verify restored and newly uploaded files")
		}
	case "close":
		result, err = investigator.Close(t.Context(), dir)
	default:
		t.Fatal("invalid live subprocess stage")
	}
	if err != nil {
		t.Fatalf("live %s failed: %v", stage, err)
	}
	if stage == "close" {
		if !cleanupComplete(result) {
			t.Fatal("close left live resources")
		}
		return
	}
	if result.Status != StatusPaused || !requested || len(result.Experiments) == 0 {
		t.Fatalf("real model did not execute an experiment and honor pause: status=%v", result.Status)
	}
}

// This wraps only real API calls, checking filesystem contents after restoration
// and input transfer but before starting the next model turn. It supplies no
// simulated tool, task, process, or model responses.
type livePauseBackend struct {
	*kagent.Client
	root, dir    string
	filesChecked bool
}

func (b *livePauseBackend) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	if name == "invoke_agent" {
		state, err := LoadState(b.dir)
		if err != nil {
			return nil, err
		}
		for filename, path := range map[string]string{
			"original.txt":   "/data/workspace/files/original.txt",
			"additional.txt": "/data/workspace/rounds/0002/files/additional.txt",
		} {
			want, err := os.ReadFile(filepath.Join(b.root, filename))
			if err != nil {
				return nil, err
			}
			read, err := b.Client.CallTool(ctx, "read_sandbox_file", map[string]any{"sandbox_id": state.SandboxID, "path": path})
			if err != nil {
				return nil, err
			}
			var text strings.Builder
			for _, content := range anySlice(read["content"]) {
				text.WriteString(str(obj(content)["text"]))
			}
			if text.String() != "1\t"+string(want) {
				return nil, fmt.Errorf("restored or added file %s does not match its original bytes", filename)
			}
		}
		b.filesChecked = true
	}
	return b.Client.CallTool(ctx, name, args)
}
