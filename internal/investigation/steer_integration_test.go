package investigation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

// TestLiveInvestigationSteer exercises opt-in live in-flight steering against
// a real kagent/Substrate deployment and model provider without suspending the
// sandbox. It works against both the release and fork kagent controllers.
func TestLiveInvestigationSteer(t *testing.T) {
	if os.Getenv("TRIAGELAB_STEER_INTEGRATION") != "1" {
		t.Skip("set TRIAGELAB_STEER_INTEGRATION=1 for real in-flight steer/model integration")
	}
	config, token := livePauseConfig(t)
	root := t.TempDir()
	if output := os.Getenv("TRIAGELAB_REPORT_DIR"); output != "" {
		if !filepath.IsAbs(output) {
			output = filepath.Join("..", "..", output)
		}
		var err error
		if root, err = filepath.Abs(output); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if root, err = os.MkdirTemp(root, "steer-"); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "investigation")
	for _, name := range []string{"original.txt", "steer-guidance.txt"} {
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
				t.Errorf("live steer cleanup failed; retain %s: %v", dir, err)
			}
		}
	})

	fixture, err := filepath.Abs(filepath.Join("..", "..", "examples", "config-bug"))
	if err != nil {
		t.Fatal(err)
	}
	description, err := os.ReadFile(filepath.Join(fixture, "issue.md"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := casefile.Ingest(string(description), fixture, []string{filepath.Join(root, "original.txt")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	added, err := casefile.Ingest(
		"Redirect the investigation: read steer-guidance.txt in the additional input directory, verify the corrected python3 app.py --timeout-ms 5000 run if not already verified in this round, and complete the report.",
		"",
		[]string{filepath.Join(root, "steer-guidance.txt")},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	backend := &liveSteerBackend{Client: client, root: root, dir: dir}
	var round1Sandbox, round1Session, round1Task string
	requested := false
	investigator := Investigator{
		Client:  backend,
		Config:  config,
		Secrets: []string{token},
		Progress: func(message string) {
			t.Log(message)
			if !requested && strings.HasPrefix(message, "Experiment ") && strings.HasSuffix(message, " started") {
				state, loadErr := LoadState(dir)
				if loadErr != nil {
					t.Errorf("load state before steer: %v", loadErr)
					return
				}
				round1Sandbox, round1Session, round1Task = state.SandboxID, state.SessionID, state.TaskID
				if steerErr := RequestSteer(dir, added, "live-steer-test"); steerErr != nil {
					t.Errorf("request in-flight steer after observed experiment: %v", steerErr)
					return
				}
				requested = true
			}
		},
	}

	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		stop := deadline.Add(-corpusCleanupReserve)
		if time.Until(stop) < corpusMinimumCaseTime {
			t.Fatal("go test deadline too close to start; raise -timeout")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, stop)
		defer cancel()
	}

	result, err := investigator.Run(ctx, c, dir)
	if err != nil || result == nil {
		t.Fatalf("live steered investigation failed: report=%+v err=%v", result, err)
	}
	if !requested {
		t.Fatal("never observed a running experiment during round 1 to queue steer")
	}
	if backend.sawSuspendOrResume {
		t.Fatal("in-flight steer unexpectedly suspended or resumed the sandbox")
	}
	if !backend.filesChecked {
		t.Fatal("steered round 2 did not verify preserved original.txt and uploaded steer-guidance.txt")
	}
	if result.Status != StatusCompleted || result.Round != 2 || len(result.Rounds) != 1 {
		t.Fatalf("expected completed round 2 with 1 archived round, got status=%s round=%d rounds=%d", result.Status, result.Round, len(result.Rounds))
	}
	if round1Sandbox == "" || result.sandboxID() != round1Sandbox {
		t.Fatalf("steer changed sandbox ID: round1=%q round2=%q", round1Sandbox, result.sandboxID())
	}
	if round1Session == "" || result.sessionID() != round1Session || result.taskID() == "" || result.taskID() == round1Task {
		t.Fatalf("steer did not preserve session (%q vs %q) with new task (%q vs %q)", round1Session, result.sessionID(), round1Task, result.taskID())
	}
	archivePath := filepath.Join(dir, "rounds", "0001", "report.json")
	archiveData, err := os.ReadFile(archivePath)
	if err != nil || !json.Valid(archiveData) {
		t.Fatalf("missing or invalid round 1 archive %s: %v", archivePath, err)
	}
	if !cleanupComplete(result) {
		t.Fatalf("steered run left live resources: %+v", result.Cleanup)
	}

	checkCtx, cancel := context.WithTimeout(context.Background(), time.Duration(config.RequestTimeout)*time.Second)
	defer cancel()
	sandbox, err := client.CallTool(checkCtx, "get_sandbox", map[string]any{"sandbox_id": round1Sandbox})
	if err != nil || sandbox["state"] != "RUNTIME_STATE_DELETED" {
		t.Fatalf("sandbox deletion not confirmed: %v", err)
	}
	_, err = client.CallTool(checkCtx, "get_session", map[string]any{"session_id": round1Session, "last_turns": 1})
	var upstream *kagent.Error
	if !errors.As(err, &upstream) || upstream.Kind != "tool" {
		t.Fatalf("deleted session remains readable or its deletion could not be checked: %v", err)
	}
	t.Logf("Live in-flight steer verified: same Sandbox/Session, round 2 files preserved/uploaded, clean deletion (%s)", dir)
}

// liveSteerBackend wraps real kagent calls to verify file preservation and new
// round attachment upload before the steered turn starts, without faking responses.
type liveSteerBackend struct {
	*kagent.Client
	root, dir          string
	filesChecked       bool
	sawSuspendOrResume bool
}

func (b *liveSteerBackend) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	if name == "suspend_sandbox" || name == "resume_sandbox" {
		b.sawSuspendOrResume = true
	}
	if name == "invoke_agent" && str(args["session_id"]) != "" {
		state, err := LoadState(b.dir)
		if err != nil {
			return nil, err
		}
		if state.Round == 2 {
			for filename, remotePath := range map[string]string{
				"original.txt":       "/data/workspace/files/original.txt",
				"steer-guidance.txt": "/data/workspace/rounds/0002/files/steer-guidance.txt",
			} {
				want, err := os.ReadFile(filepath.Join(b.root, filename))
				if err != nil {
					return nil, err
				}
				read, err := b.Client.CallTool(ctx, "read_sandbox_file", map[string]any{"sandbox_id": state.SandboxID, "path": remotePath})
				if err != nil {
					return nil, err
				}
				var text strings.Builder
				for _, content := range anySlice(read["content"]) {
					text.WriteString(str(obj(content)["text"]))
				}
				if text.String() != "1\t"+string(want) {
					return nil, fmt.Errorf("steered sandbox file %s (%s) does not match its original bytes", filename, remotePath)
				}
			}
			b.filesChecked = true
		}
	}
	return b.Client.CallTool(ctx, name, args)
}
