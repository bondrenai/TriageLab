package investigation

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

const maxRounds = 32
const maxReportBytes = 64 << 20

func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// stampTime returns the current UTC instant for a report timestamp member.
func stampTime() *time.Time {
	now := time.Now().UTC()
	return &now
}

// ErrBusy means another process owns this investigation's lifecycle.
var ErrBusy = errors.New("investigation is active in another operation")

// ErrRetentionUnsupported means the connected kagent controller is a release
// build without the TriageLab fork's durable suspended-sandbox retention.
var ErrRetentionUnsupported = errors.New("this kagent controller does not retain suspended sandboxes; pausing requires the TriageLab controller fork")

// State is the private lifecycle journal. It contains no credentials, imported
// case text, process output, or model replies. Reports are evidence views, not
// authority for resource identity or continuation settings.
type State struct {
	Version                int      `json:"version"`
	Config                 Config   `json:"config"`
	AdditionalTools        []string `json:"additional_tools,omitempty"`
	Trigger                string   `json:"trigger,omitempty"`
	Actor                  string   `json:"actor,omitempty"`
	RunID                  string   `json:"run_id"`
	Round                  int      `json:"round"`
	Phase                  string   `json:"phase"`
	SandboxID              string   `json:"sandbox_id,omitempty"`
	SessionID              string   `json:"session_id,omitempty"`
	TaskID                 string   `json:"task_id,omitempty"`
	MessageID              string   `json:"message_id,omitempty"`
	UpdatedAt              string   `json:"updated_at"`
	PauseReady             bool     `json:"pause_ready,omitempty"`
	ResumeFromRound        int      `json:"resume_from_round,omitempty"`
	ContinuationDigest     string   `json:"continuation_digest,omitempty"`
	SandboxCreationPending bool     `json:"sandbox_creation_pending,omitempty"`
	SessionCreationPending bool     `json:"session_creation_pending,omitempty"`
	// Retention records the controller capability probed when the run was
	// created. It gates every pause path and rebuilds the exact sandbox
	// creation request, so resume and close never re-probe.
	Retention bool `json:"retention,omitempty"`
}

func readJSON(name string, limit int64, target any, strict bool) error {
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return fmt.Errorf("invalid or oversized investigation record")
	}
	decoder := json.NewDecoder(io.LimitReader(file, limit+1))
	decoder.UseNumber()
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("investigation record must contain one JSON value")
	}
	return nil
}

// LoadState reads a bounded lifecycle record without contacting upstream.
func LoadState(dir string) (State, error) {
	var state State
	if err := readJSON(filepath.Join(dir, "lifecycle.json"), 128<<10, &state, true); err != nil {
		return state, err
	}
	if state.Version != 1 || state.RunID == "" || state.Round < 1 || state.Round > maxRounds {
		return state, fmt.Errorf("invalid investigation lifecycle record")
	}
	if !slices.Contains([]string{"creating", "running", "pausing", "paused", "resuming", "invoking", "closing", "closed"}, state.Phase) {
		return state, fmt.Errorf("unknown investigation lifecycle phase")
	}
	if err := state.Config.Validate(); err != nil {
		return state, fmt.Errorf("saved investigation settings: %w", err)
	}
	if err := agent.ValidateExtraTools(state.AdditionalTools); err != nil {
		return state, err
	}
	return state, nil
}

func writePrivate(name string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(name), ".lifecycle-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), name); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func saveState(dir string, state *State) error {
	state.UpdatedAt = stamp()
	return writePrivate(filepath.Join(dir, "lifecycle.json"), state)
}

func lockRun(dir string) (func(), error) {
	if info, err := os.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("investigation directory must be a real directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".lifecycle.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}

// ErrLifecycleExists directs saved-report cleanup to the lifecycle journal.
var ErrLifecycleExists = errors.New("this investigation has a lifecycle journal; use close to release its resources")

// LockLegacyCleanup serializes report-based recovery with other operations on
// this run. Any lifecycle journal, even an unreadable one, requires Close.
func LockLegacyCleanup(dir string) (func(), error) {
	unlock, err := lockRun(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(dir, "lifecycle.json")); !errors.Is(err, os.ErrNotExist) {
		unlock()
		if err != nil {
			return nil, err
		}
		return nil, ErrLifecycleExists
	}
	return unlock, nil
}

type pauseRequest struct {
	RunID  string `json:"run_id"`
	Round  int    `json:"round"`
	TaskID string `json:"task_id"`
}

// RequestPause requests a pause at the next poll boundary. Acceptance does not
// mean suspension has completed; LoadState reports the durable phase.
func RequestPause(dir string) error {
	state, err := LoadState(dir)
	if err != nil {
		return err
	}
	if state.Phase != "running" || state.TaskID == "" {
		return fmt.Errorf("only an active investigation can be asked to pause")
	}
	if !state.Retention {
		return ErrRetentionUnsupported
	}
	unlock, err := lockRun(dir)
	if err == nil {
		unlock()
		return fmt.Errorf("investigation is no longer active; close its saved resources")
	}
	if !errors.Is(err, ErrBusy) {
		return err
	}
	return writePrivate(filepath.Join(dir, "pause-request.json"), pauseRequest{RunID: state.RunID, Round: state.Round, TaskID: state.TaskID})
}

func pauseRequested(dir string, state *State) (bool, error) {
	var request pauseRequest
	err := readJSON(filepath.Join(dir, "pause-request.json"), 4096, &request, true)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return request.RunID == state.RunID && request.Round == state.Round && request.TaskID == state.TaskID, nil
}

type steerAttachment struct {
	Path      string `json:"path"`
	SizeBytes int    `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Data      []byte `json:"data,omitempty"`
}

type steerRequest struct {
	RunID       string            `json:"run_id"`
	Round       int               `json:"round"`
	TaskID      string            `json:"task_id"`
	Actor       string            `json:"actor,omitempty"`
	Digest      string            `json:"digest"`
	Description string            `json:"description"`
	Source      map[string]any    `json:"source"`
	Environment map[string]any    `json:"environment"`
	Attachments []steerAttachment `json:"attachments,omitempty"`
	Evidence    []string          `json:"evidence"`
}

func checkContinuationPaths(original []casefile.Attachment, nextRound int) error {
	prefix := fmt.Sprintf("rounds/%04d", nextRound)
	for _, attachment := range original {
		existing := attachment.Path
		if existing == prefix || strings.HasPrefix(existing, prefix+"/") || strings.HasPrefix(prefix, existing+"/") {
			return fmt.Errorf("additional input directory conflicts with an original input; close this investigation before starting a new one")
		}
	}
	return nil
}

// RequestSteer queues operator guidance to interrupt the active round at the
// next poll boundary and start a new round in the same live sandbox and session.
// Unlike RequestPause, it never suspends the sandbox and works on both release
// and fork kagent controllers.
func RequestSteer(dir string, added casefile.Case, actor string) error {
	return RequestSteerWithReserve(dir, added, actor, nil)
}

// RequestSteerWithReserve validates steering eligibility under the active-run
// lock check before invoking reserve and writing the private steer request.
func RequestSteerWithReserve(dir string, added casefile.Case, actor string, reserve func() error) error {
	state, err := LoadState(dir)
	if err != nil {
		return err
	}
	if state.Phase != "running" || state.TaskID == "" || state.SandboxID == "" || state.SessionID == "" {
		return fmt.Errorf("only an active running investigation can be steered")
	}
	if state.Round >= maxRounds {
		return fmt.Errorf("investigation reached the %d round limit; let it finish or stop it", maxRounds)
	}
	if paused, err := pauseRequested(dir, &state); err != nil {
		return err
	} else if paused {
		return fmt.Errorf("a pause is already pending for this round")
	}
	if _, steered, err := steerRequested(dir, &state); err != nil {
		return err
	} else if steered {
		return fmt.Errorf("steering is already pending for this round")
	}
	digest, err := continuationDigest(added)
	if err != nil {
		return err
	}
	current, err := loadReport(filepath.Join(dir, "report.json"), &state, state.Round)
	if err != nil {
		return err
	}
	if err := checkContinuationPaths(current.Case.Attachments, state.Round+1); err != nil {
		return err
	}
	unlock, err := lockRun(dir)
	if err == nil {
		unlock()
		return fmt.Errorf("investigation is no longer active; close its saved resources")
	}
	if !errors.Is(err, ErrBusy) {
		return err
	}
	if reserve != nil {
		if err := reserve(); err != nil {
			return err
		}
	}
	attachments := make([]steerAttachment, len(added.Attachments))
	for index, attachment := range added.Attachments {
		sum := sha256.Sum256(attachment.Content)
		attachments[index] = steerAttachment{
			Path:      attachment.Path,
			SizeBytes: attachment.SizeBytes,
			SHA256:    hex.EncodeToString(sum[:]),
			Data:      append([]byte(nil), attachment.Content...),
		}
	}
	return writePrivate(filepath.Join(dir, "steer-request.json"), steerRequest{
		RunID:       state.RunID,
		Round:       state.Round,
		TaskID:      state.TaskID,
		Actor:       actor,
		Digest:      digest,
		Description: added.Description,
		Source:      added.Source,
		Environment: added.Environment,
		Attachments: attachments,
		Evidence:    added.Evidence,
	})
}

func steerRequested(dir string, state *State) (steerRequest, bool, error) {
	var request steerRequest
	err := readJSON(filepath.Join(dir, "steer-request.json"), 8<<20, &request, true)
	if errors.Is(err, os.ErrNotExist) {
		return steerRequest{}, false, nil
	}
	if err != nil {
		return steerRequest{}, false, err
	}
	if request.RunID != state.RunID || request.Round != state.Round || request.TaskID != state.TaskID {
		return steerRequest{}, false, nil
	}
	return request, true, nil
}

func steerCase(request steerRequest) (casefile.Case, error) {
	attachments := make([]casefile.Attachment, len(request.Attachments))
	for index, item := range request.Attachments {
		attachments[index] = casefile.Attachment{
			Path:      item.Path,
			SizeBytes: item.SizeBytes,
			SHA256:    item.SHA256,
			Content:   item.Data,
		}
	}
	added := casefile.Case{
		Description: request.Description,
		Source:      request.Source,
		Environment: request.Environment,
		Attachments: attachments,
		Evidence:    request.Evidence,
	}
	digest, err := continuationDigest(added)
	if err != nil {
		return casefile.Case{}, err
	}
	if digest != request.Digest {
		return casefile.Case{}, fmt.Errorf("staged steer request digest mismatch")
	}
	for index := range added.Attachments {
		sum := sha256.Sum256(added.Attachments[index].Content)
		added.Attachments[index].SHA256 = hex.EncodeToString(sum[:])
	}
	return added, nil
}

// HasPendingPause reports whether a valid pause request is queued for state.
func HasPendingPause(dir string, state State) bool {
	ok, err := pauseRequested(dir, &state)
	return err == nil && ok
}

// HasPendingSteer reports whether a valid steer request is queued for state.
func HasPendingSteer(dir string, state State) bool {
	_, ok, err := steerRequested(dir, &state)
	return err == nil && ok
}

func sandboxRequest(state State) map[string]any {
	request := map[string]any{"namespace": state.Config.Namespace, "template": state.Config.Template, "request_id": state.RunID, "name": "triagelab-" + state.RunID, "ttl_seconds": state.Config.SandboxTTL}
	if state.Retention {
		request["retain_while_suspended"] = true
	}
	return request
}

func stateFromReport(state *State, r *Report) {
	state.SandboxID = r.sandboxID()
	state.SessionID = r.sessionID()
	state.TaskID = r.taskID()
	state.SandboxCreationPending = isSet(r.CreationUncertain)
	state.SessionCreationPending = isSet(r.SessionCreationUncertain)
}

func cleanupComplete(r *Report) bool {
	sandboxDeleted := r.Cleanup != nil && r.Cleanup.SandboxDeleted
	sessionDeleted := r.Cleanup != nil && r.Cleanup.SessionDeleted
	return !isSet(r.CreationUncertain) && !isSet(r.SessionCreationUncertain) &&
		(r.sandboxID() == "" || sandboxDeleted) &&
		(r.sessionID() == "" || sessionDeleted)
}

func loadReport(name string, state *State, round int) (*Report, error) {
	var data json.RawMessage
	if err := readJSON(name, maxReportBytes, &data, false); err != nil {
		return nil, err
	}
	result, err := DecodeReport(data)
	if err != nil {
		return nil, err
	}
	if result.Round != round || result.RunID != state.RunID {
		return nil, fmt.Errorf("report does not match the saved investigation round")
	}
	if result.Experiments == nil {
		result.Experiments = []Experiment{}
	}
	if result.Errors == nil {
		result.Errors = []string{}
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	return result, nil
}

func anySlice(value any) []any { values, _ := value.([]any); return values }

func roundDir(dir string, round int) string {
	return filepath.Join(dir, "rounds", fmt.Sprintf("%04d", round))
}

// archiveRound never replaces a published round. Archives omit history so that
// latest report history stays flat and process IDs remain scoped to their round.
func (i *Investigator) archiveRound(dir string, state *State, r *Report) error {
	destination := roundDir(dir, state.Round)
	if _, err := os.Lstat(filepath.Join(destination, "report.json")); err == nil {
		_, err = loadReport(filepath.Join(destination, "report.json"), state, state.Round)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	archived := *r
	archived.Rounds = nil
	return i.persist(destination, &archived)
}

func (i *Investigator) runRound(parent context.Context, activeCtx *context.Context, extraCancels *[]context.CancelFunc, dir string, state *State, r *Report, observed map[string]time.Time) error {
	for iteration := 0; ; iteration++ {
		ctx := *activeCtx
		requested, err := pauseRequested(dir, state)
		if err != nil {
			return err
		}
		if requested {
			return i.pauseRound(ctx, dir, state, r)
		}
		steered, hasSteer, err := steerRequested(dir, state)
		if err != nil {
			return err
		}
		if hasSteer {
			if err := i.steerRound(parent, activeCtx, extraCancels, dir, state, r, steered); err != nil {
				return err
			}
			observed = map[string]time.Time{}
			iteration = -1
			continue
		}
		finished, err := i.pollOnce(ctx, dir, state.TaskID, r, observed, iteration)
		if err != nil {
			return err
		}
		// A request can arrive while the final poll captures evidence. Honor it
		// before normal completion would delete the retained workspace.
		requested, err = pauseRequested(dir, state)
		if err != nil {
			return err
		}
		if requested {
			return i.pauseRound(ctx, dir, state, r)
		}
		steered, hasSteer, err = steerRequested(dir, state)
		if err != nil {
			return err
		}
		if hasSteer {
			if err := i.steerRound(parent, activeCtx, extraCancels, dir, state, r, steered); err != nil {
				return err
			}
			observed = map[string]time.Time{}
			iteration = -1
			continue
		}
		if finished {
			// Without durable retention a suspended sandbox still expires at its
			// original deadline, so a missing-information pause is pointless.
			if state.Retention && listLen(r.Analysis["missing_information"]) > 0 {
				return i.pauseRound(ctx, dir, state, r)
			}
			return nil
		}
	}
}

// settleTask observes cancellation before capturing guest-local process handles.
func (i *Investigator) settleTask(ctx context.Context, state *State, r *Report) error {
	if state.TaskID == "" {
		return fmt.Errorf("task identity is uncertain; close the investigation to release its resources")
	}
	canceled := false
	for {
		summary, err := i.call(ctx, "get_session", map[string]any{"session_id": state.SessionID, "last_turns": 1, "max_bytes": 131072})
		if err != nil {
			return err
		}
		turns := anySlice(summary["turns"])
		if len(turns) == 0 || str(obj(turns[len(turns)-1])["task_id"]) != state.TaskID {
			return fmt.Errorf("cannot confirm the current investigator task")
		}
		phase := str(obj(turns[len(turns)-1])["state"])
		r.TaskState = phase
		if terminal(phase) {
			return nil
		}
		if !canceled {
			if err := i.Client.CancelTask(ctx, state.SessionID, state.TaskID); err != nil {
				return err
			}
			canceled = true
		}
		if err := i.pause(ctx); err != nil {
			return err
		}
	}
}

func (i *Investigator) quiesceRound(ctx context.Context, dir string, state *State, r *Report, action string) error {
	if err := i.settleTask(ctx, state, r); err != nil {
		return fmt.Errorf("%s task: %w", action, err)
	}
	observed := map[string]time.Time{}
	killed := map[string]bool{}
	for {
		if _, err := i.observe(ctx, r, observed); err != nil {
			return fmt.Errorf("%s evidence capture: %w", action, err)
		}
		for _, call := range r.ToolCalls {
			if call.Pending {
				return fmt.Errorf("cannot confirm quiescence while a tool call has no settled result; close the investigation")
			}
		}
		settled := true
		for index := range r.Experiments {
			e := &r.Experiments[index]
			if e.exited() && e.OutputsComplete {
				continue
			}
			settled = false
			pid := e.ProcessID
			if !killed[pid] {
				if _, err := i.call(ctx, "kill_sandbox_process", map[string]any{"sandbox_id": state.SandboxID, "process_id": pid}); err != nil {
					return fmt.Errorf("stop experiment for %s: %w", action, err)
				}
				e.StoppedForPause = true
				killed[pid] = true
			}
		}
		if err := i.persist(dir, r); err != nil {
			return err
		}
		if settled {
			return nil
		}
		if err := i.pause(ctx); err != nil {
			return err
		}
	}
}

func (i *Investigator) pauseRound(ctx context.Context, dir string, state *State, r *Report) error {
	state.Phase = "pausing"
	if err := saveState(dir, state); err != nil {
		return err
	}
	r.Status = StatusPausing
	i.progress(ctx, "Settling the investigator and preserving evidence before suspension")
	if !state.PauseReady {
		if err := i.quiesceRound(ctx, dir, state, r, "pause"); err != nil {
			return err
		}
		state.PauseReady = true
		if err := saveState(dir, state); err != nil {
			return err
		}
	}
	if err := i.sandboxTransition(ctx, "suspend_sandbox", "RUNTIME_STATE_SUSPENDED", state, r); err != nil {
		return fmt.Errorf("suspension is not confirmed; retry resume to reconcile or close: %w", err)
	}
	r.Status, r.FinishedAt = StatusPaused, stampTime()
	if err := i.persist(dir, r); err != nil {
		return err
	}
	if err := i.archiveRound(dir, state, r); err != nil {
		return err
	}
	state.Phase = "paused"
	if err := saveState(dir, state); err != nil {
		return err
	}
	i.progress(ctx, "Investigation paused; workspace and session retained")
	return nil
}

func (i *Investigator) steerRound(parent context.Context, activeCtx *context.Context, extraCancels *[]context.CancelFunc, dir string, state *State, r *Report, request steerRequest) error {
	_ = os.Remove(filepath.Join(dir, "steer-request.json"))
	if state.Round >= maxRounds {
		return fmt.Errorf("investigation reached the %d round limit; let it finish or stop it", maxRounds)
	}
	added, err := steerCase(request)
	if err != nil {
		return err
	}
	if err := checkContinuationPaths(r.Case.Attachments, state.Round+1); err != nil {
		return err
	}
	settleCtx := *activeCtx
	i.progress(settleCtx, fmt.Sprintf("Redirecting the investigator: settling round %d", state.Round))
	if err := i.quiesceRound(settleCtx, dir, state, r, "steer"); err != nil {
		return err
	}
	if r.Status != StatusCompleted {
		r.Status = StatusInterrupted
	}
	r.FinishedAt = stampTime()
	if err := i.persist(dir, r); err != nil {
		return err
	}
	if err := i.archiveRound(dir, state, r); err != nil {
		return err
	}
	history := []*Report{}
	for round := 1; round <= state.Round; round++ {
		prior, err := loadReport(filepath.Join(roundDir(dir, round), "report.json"), state, round)
		if err != nil {
			return err
		}
		prior.Rounds = nil
		history = append(history, prior)
	}
	nextCtx, nextCancel := context.WithTimeout(parent, time.Duration(i.Config.InvestigationTimeout)*time.Second)
	*extraCancels = append(*extraCancels, nextCancel)
	*activeCtx = nextCtx

	r.Rounds = history
	r.Round = state.Round + 1
	r.RoundStartedAt, r.FinishedAt = stampTime(), nil
	r.Status, r.Outcome = StatusRunning, OutcomeInconclusive
	r.Experiments, r.ToolCalls = []Experiment{}, []kagent.ToolCall{}
	r.Analysis, r.Warnings, r.Errors = map[string]any{}, []string{}, []string{}
	r.Continuation = &added
	r.ResumedBy = request.Actor
	if r.ResumedBy == "" && i.Actor != "" {
		r.ResumedBy = i.Actor
	}
	r.TaskState = ""
	r.TaskFailure = ""
	r.Session = map[string]any{"id": state.SessionID}

	state.ResumeFromRound = state.Round
	state.Round++
	state.TaskID, state.PauseReady, state.ContinuationDigest = "", false, request.Digest
	state.MessageID = fmt.Sprintf("%s-round-%d", state.RunID, state.Round)
	if err := saveState(dir, state); err != nil {
		return err
	}
	if err := i.persist(dir, r); err != nil {
		return err
	}
	setResourceIDs(r, *state)

	workspace := path.Join("/data/workspace/rounds", fmt.Sprintf("%04d", state.Round))
	for _, attachment := range added.Attachments {
		value, err := i.call(nextCtx, "write_sandbox_file", map[string]any{
			"sandbox_id":  state.SandboxID,
			"path":        path.Join(workspace, attachment.Path),
			"data_base64": base64.StdEncoding.EncodeToString(attachment.Content),
			"mode":        384,
		})
		if err != nil {
			return fmt.Errorf("upload continuation: %w", err)
		}
		n, ok := integer(value["bytes_written"])
		if !ok || n != len(attachment.Content) {
			return fmt.Errorf("incomplete continuation file upload")
		}
	}
	message, err := json.Marshal(map[string]any{
		"run_context": map[string]any{
			"sandbox_id":                 state.SandboxID,
			"workspace":                  "/data/workspace",
			"additional_input_directory": workspace,
			"round":                      state.Round,
			"max_experiments":            i.Config.MaxExperiments,
			"process_timeout_seconds":    i.Config.ProcessTimeout,
		},
		"untrusted_additional_information": added,
	})
	if err != nil {
		return err
	}
	state.Phase = "invoking"
	if err := saveState(dir, state); err != nil {
		return err
	}
	i.progress(nextCtx, fmt.Sprintf("Starting steered round %d", state.Round))
	invoked, err := i.call(nextCtx, "invoke_agent", map[string]any{
		"session_id": state.SessionID,
		"message_id": state.MessageID,
		"message":    "The operator interrupted the previous turn to redirect this investigation in the same standalone sandbox. Incorporate the guidance and additional information below while preserving all sandbox safety and evidence rules. Previous round process IDs are historical: never read their handles or cite them as evidence for this round. Collect new experiments for current claims. Durable files remain under /data/workspace; new attachment paths are relative to additional_input_directory. Return the final structured report, including missing_information if blocked. The following JSON separates trusted run context from untrusted case material.\n\n" + string(message),
	})
	if err != nil {
		return fmt.Errorf("continuation invocation outcome is uncertain; it will not be replayed, close the investigation to release resources: %w", err)
	}
	if str(invoked["session_id"]) != state.SessionID || str(invoked["task_id"]) == "" {
		return fmt.Errorf("continuation omitted its exact session/task identity; invocation will not be replayed")
	}
	state.TaskID, state.Phase = str(invoked["task_id"]), "running"
	r.Session["task_id"] = state.TaskID
	r.Status = StatusRunning
	if err := saveState(dir, state); err != nil {
		return err
	}
	return i.persist(dir, r)
}

func (i *Investigator) sandboxTransition(ctx context.Context, tool, target string, state *State, r *Report) error {
	for {
		value, err := i.call(ctx, tool, map[string]any{"sandbox_id": state.SandboxID})
		if err != nil {
			return err
		}
		if str(value["id"]) != state.SandboxID || value["retain_while_suspended"] != true {
			return fmt.Errorf("sandbox identity or retention policy changed")
		}
		r.Sandbox = value
		if value["failure"] != nil {
			return fmt.Errorf("sandbox lifecycle failed; inspect controller diagnostics")
		}
		if value["state"] == target && value["operation"] == "RUNTIME_OPERATION_NONE" {
			if target == "RUNTIME_STATE_SUSPENDED" {
				if _, present := value["expires_at"]; present {
					return fmt.Errorf("suspended sandbox still reports an expiration deadline; durable retention is not confirmed")
				}
			}
			return nil
		}
		if value["state"] == "RUNTIME_STATE_DELETED" || value["operation"] == "RUNTIME_OPERATION_DELETE" {
			return fmt.Errorf("sandbox was deleted or is being deleted")
		}
		if err := i.pause(ctx); err != nil {
			return err
		}
	}
}

func (i *Investigator) validateSaved(state State) error {
	if err := i.Config.Validate(); err != nil {
		return err
	}
	current, saved := i.Config, state.Config
	current.TokenEnv, saved.TokenEnv = "", ""
	current.Version, saved.Version = "", ""
	current.ManualAgents, saved.ManualAgents = nil, nil
	if !reflect.DeepEqual(current, saved) || !slices.Equal(i.AdditionalTools, state.AdditionalTools) {
		return fmt.Errorf("resume requires the saved connection, identity, agent, template, tools, and execution limits")
	}
	return nil
}

func continuationDigest(c casefile.Case) (string, error) {
	if strings.TrimSpace(c.Description) == "" || len(c.Description) > casefile.MaxDescriptionBytes || len(c.Attachments) > casefile.MaxFiles {
		return "", fmt.Errorf("resume requires bounded additional information")
	}
	total := 0
	seen := map[string]bool{}
	c.Attachments = append([]casefile.Attachment(nil), c.Attachments...)
	for index, attachment := range c.Attachments {
		if !fs.ValidPath(attachment.Path) || attachment.Path == "." || strings.Contains(attachment.Path, `\`) || strings.IndexFunc(attachment.Path, unicode.IsControl) >= 0 || seen[attachment.Path] {
			return "", fmt.Errorf("unsafe or duplicate continuation attachment path")
		}
		seen[attachment.Path] = true
		total += len(attachment.Content)
		if len(attachment.Content) > casefile.MaxFileBytes || total > casefile.MaxTotalBytes || attachment.SizeBytes != len(attachment.Content) {
			return "", fmt.Errorf("continuation attachment exceeds limits or its inventory does not match its content")
		}
		sum := sha256.Sum256(attachment.Content)
		digest := hex.EncodeToString(sum[:])
		if attachment.SHA256 != "" && attachment.SHA256 != digest {
			return "", fmt.Errorf("continuation attachment digest mismatch")
		}
		c.Attachments[index].SHA256 = digest
	}
	c.ID = ""
	encoded, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// Resume explicitly authorizes one new bounded turn in the retained Session.
// Pending lifecycle work may be retried; an uncertain invocation is never replayed.
func (i *Investigator) Resume(parent context.Context, added casefile.Case, dir string) (result *Report, returnErr error) {
	unlock, err := lockRun(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, err := LoadState(dir)
	if err != nil {
		return nil, err
	}
	if i.ExpectedRound != 0 && state.Round != i.ExpectedRound || i.ExpectedPhase != "" && state.Phase != i.ExpectedPhase {
		return nil, fmt.Errorf("investigation round or phase changed; review the current state before continuing")
	}
	if err := i.validateSaved(state); err != nil {
		return nil, err
	}
	digest, err := continuationDigest(added)
	if err != nil {
		return nil, err
	}
	if state.Phase != "paused" && state.Phase != "pausing" && state.Phase != "resuming" {
		return nil, fmt.Errorf("cannot resume phase %s; an uncertain or interrupted invocation must be closed, never replayed", state.Phase)
	}
	if state.SandboxID == "" || state.SessionID == "" {
		return nil, fmt.Errorf("retained resource identities are missing")
	}
	var extraCancels []context.CancelFunc
	defer func() {
		for _, fn := range extraCancels {
			fn()
		}
	}()
	ctx, cancel := context.WithTimeout(parent, time.Duration(i.Config.InvestigationTimeout)*time.Second)
	defer cancel()
	activeCtx := ctx
	if state.Phase == "pausing" {
		result, err = loadReport(filepath.Join(dir, "report.json"), &state, state.Round)
		if err != nil {
			return nil, err
		}
		setResourceIDs(result, state)
		if err := i.pauseRound(ctx, dir, &state, result); err != nil {
			if ctx.Err() != nil && state.Phase != "paused" {
				return result, i.cancelLifecycle(dir, &state, result, errors.Join(err, ctx.Err()))
			}
			return result, err
		}
	}
	if state.Phase == "paused" {
		if state.Round >= maxRounds {
			return nil, fmt.Errorf("investigation reached the %d round limit; close it", maxRounds)
		}
		previous, err := loadReport(filepath.Join(roundDir(dir, state.Round), "report.json"), &state, state.Round)
		if err != nil {
			return nil, err
		}
		if err := checkContinuationPaths(previous.Case.Attachments, state.Round+1); err != nil {
			return nil, err
		}
		result = previous
		history := []*Report{}
		for round := 1; round <= state.Round; round++ {
			prior, err := loadReport(filepath.Join(roundDir(dir, round), "report.json"), &state, round)
			if err != nil {
				return nil, err
			}
			prior.Rounds = nil
			history = append(history, prior)
		}
		result.Rounds = history
		result.Round = state.Round + 1
		result.RoundStartedAt, result.FinishedAt = stampTime(), nil
		result.Status, result.Outcome = StatusResuming, OutcomeInconclusive
		result.Experiments, result.ToolCalls = []Experiment{}, []kagent.ToolCall{}
		result.Analysis, result.Warnings, result.Errors = map[string]any{}, []string{}, []string{}
		result.Continuation = &added
		result.ResumedBy = ""
		if i.Actor != "" {
			result.ResumedBy = i.Actor
		}
		result.TaskState = ""
		result.TaskFailure = ""
		result.Session = map[string]any{"id": state.SessionID}
		if err := i.persist(dir, result); err != nil {
			return result, err
		}
		state.ResumeFromRound = state.Round
		state.Round++
		state.Phase, state.TaskID, state.PauseReady, state.ContinuationDigest = "resuming", "", false, digest
		state.MessageID = fmt.Sprintf("%s-round-%d", state.RunID, state.Round)
		if err := saveState(dir, &state); err != nil {
			return result, err
		}
	} else {
		if state.ContinuationDigest != digest {
			return nil, fmt.Errorf("pending resume must use the same additional information and files")
		}
		result, err = loadReport(filepath.Join(dir, "report.json"), &state, state.Round)
		if err != nil {
			return nil, err
		}
		result.Continuation = &added
	}
	setResourceIDs(result, state)
	defer func() {
		_ = os.Remove(filepath.Join(dir, "steer-request.json"))
		if activeCtx.Err() != nil && state.Phase != "paused" {
			returnErr = errors.Join(returnErr, activeCtx.Err())
		}
		if returnErr != nil {
			result.Errors = append(result.Errors, returnErr.Error())
			result.Status = Status(state.Phase)
		}
		if activeCtx.Err() != nil && state.Phase != "running" && state.Phase != "paused" {
			returnErr = i.cancelLifecycle(dir, &state, result, returnErr)
			return
		}
		if state.Phase == "running" {
			if returnErr != nil {
				result.Status = StatusError
				if errors.Is(returnErr, context.Canceled) {
					result.Status = StatusInterrupted
				}
				if errors.Is(returnErr, context.DeadlineExceeded) {
					result.Status = StatusTimedOut
				}
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
			if returnErr != nil && state.TaskID != "" {
				captureCtx, captureCancel := context.WithTimeout(cleanupCtx, 20*time.Second)
				if _, err := i.observe(captureCtx, result, map[string]time.Time{}); err != nil {
					result.Warnings = append(result.Warnings, "Final evidence capture was incomplete")
				}
				captureCancel()
			}
			i.Cleanup(cleanupCtx, result)
			cleanupCancel()
			state.Phase = "closing"
			if cleanupComplete(result) {
				state.Phase = "closed"
			}
			result.FinishedAt = stampTime()
			returnErr = errors.Join(returnErr, saveState(dir, &state), i.archiveRound(dir, &state, result))
		}
		returnErr = errors.Join(returnErr, i.persist(dir, result))
	}()
	i.progress(ctx, "Restoring the retained investigation workspace")
	summary, err := i.call(ctx, "get_session", map[string]any{"session_id": state.SessionID, "last_turns": 1, "max_bytes": 131072})
	if err != nil {
		return result, fmt.Errorf("verify retained session: %w", err)
	}
	if str(obj(summary["session"])["id"]) != state.SessionID || obj(summary["session"])["state"] == "RUNTIME_STATE_DELETED" {
		return result, fmt.Errorf("retained session identity is missing or deleted")
	}
	turns := anySlice(summary["turns"])
	if len(turns) == 0 || !terminal(str(obj(turns[len(turns)-1])["state"])) {
		return result, fmt.Errorf("retained session has an unsettled turn; close the investigation instead of invoking again")
	}
	if err := i.sandboxTransition(ctx, "resume_sandbox", "RUNTIME_STATE_READY", &state, result); err != nil {
		return result, err
	}
	workspace := path.Join("/data/workspace/rounds", fmt.Sprintf("%04d", state.Round))
	for _, attachment := range added.Attachments {
		value, err := i.call(ctx, "write_sandbox_file", map[string]any{"sandbox_id": state.SandboxID, "path": path.Join(workspace, attachment.Path), "data_base64": base64.StdEncoding.EncodeToString(attachment.Content), "mode": 384})
		if err != nil {
			return result, fmt.Errorf("upload continuation: %w", err)
		}
		n, ok := integer(value["bytes_written"])
		if !ok || n != len(attachment.Content) {
			return result, fmt.Errorf("incomplete continuation file upload")
		}
	}
	message, err := json.Marshal(map[string]any{
		"run_context":                      map[string]any{"sandbox_id": state.SandboxID, "workspace": "/data/workspace", "additional_input_directory": workspace, "round": state.Round, "max_experiments": i.Config.MaxExperiments, "process_timeout_seconds": i.Config.ProcessTimeout},
		"untrusted_additional_information": added,
	})
	if err != nil {
		return result, err
	}
	state.Phase = "invoking"
	if err := saveState(dir, &state); err != nil {
		return result, err
	}
	invoked, err := i.call(ctx, "invoke_agent", map[string]any{"session_id": state.SessionID, "message_id": state.MessageID, "message": "Continue the investigation in the same standalone sandbox using this additional information. Previous round process IDs are historical: never read their handles or cite them as evidence for this round. Collect new experiments for current claims. Durable files remain under /data/workspace; new attachment paths are relative to additional_input_directory. Return the final structured report, including missing_information if blocked. The following JSON separates trusted run context from untrusted case material.\n\n" + string(message)})
	if err != nil {
		return result, fmt.Errorf("continuation invocation outcome is uncertain; it will not be replayed, close the investigation to release resources: %w", err)
	}
	if str(invoked["session_id"]) != state.SessionID || str(invoked["task_id"]) == "" {
		return result, fmt.Errorf("continuation omitted its exact session/task identity; invocation will not be replayed")
	}
	state.TaskID, state.Phase = str(invoked["task_id"]), "running"
	result.Session["task_id"] = state.TaskID
	result.Status = StatusRunning
	if err := saveState(dir, &state); err != nil {
		return result, err
	}
	if err := i.persist(dir, result); err != nil {
		return result, err
	}
	return result, i.runRound(parent, &activeCtx, &extraCancels, dir, &state, result, map[string]time.Time{})
}

func setResourceIDs(r *Report, state State) {
	if r.Sandbox == nil {
		r.Sandbox = map[string]any{}
	}
	if r.Session == nil {
		r.Session = map[string]any{}
	}
	r.Sandbox["id"] = state.SandboxID
	r.Session["id"], r.Session["task_id"] = state.SessionID, state.TaskID
}

// Cancellation or the round budget ending means stop, including during a
// lifecycle transition. Transport failures alone leave the durable intent for
// reconciliation. A confirmed paused state is never discarded by cancellation.
func (i *Investigator) cancelLifecycle(dir string, state *State, r *Report, cause error) error {
	r.Status = StatusInterrupted
	if errors.Is(cause, context.DeadlineExceeded) {
		r.Status = StatusTimedOut
	}
	if cause != nil && !slices.Contains(r.Errors, cause.Error()) {
		r.Errors = append(r.Errors, cause.Error())
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	i.Cleanup(cleanupCtx, r)
	cancel()
	stateFromReport(state, r)
	state.Phase = "closing"
	if cleanupComplete(r) {
		state.Phase = "closed"
	}
	r.FinishedAt = stampTime()
	return errors.Join(cause, saveState(dir, state), i.persist(dir, r), i.archiveRound(dir, state, r))
}

// Close releases retained resources using the private journal, including after
// process restart or an uncertain invocation. It never invokes an investigator.
func (i *Investigator) Close(parent context.Context, dir string) (*Report, error) {
	unlock, err := lockRun(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, err := LoadState(dir)
	if err != nil {
		return nil, err
	}
	if i.ExpectedRound != 0 && state.Round != i.ExpectedRound || i.ExpectedPhase != "" && state.Phase != i.ExpectedPhase {
		return nil, fmt.Errorf("investigation round or phase changed; review the current state before closing")
	}
	if err := i.validateSaved(state); err != nil {
		return nil, err
	}
	_ = os.Remove(filepath.Join(dir, "steer-request.json"))
	r, err := loadReport(filepath.Join(dir, "report.json"), &state, state.Round)
	if err != nil {
		r, err = loadReport(filepath.Join(roundDir(dir, state.Round), "report.json"), &state, state.Round)
	}
	if err != nil {
		// Closing must remain possible if a crash interrupted the report/state
		// boundary. Only journaled identities and creation keys are operational.
		r = &Report{SchemaVersion: SchemaVersion, RunID: state.RunID, Round: state.Round, Case: casefile.Case{Description: "Investigation evidence report unavailable"}, Agent: &AgentRef{Namespace: state.Config.Namespace, Name: state.Config.Agent}, SandboxTemplate: state.Config.Template, Experiments: []Experiment{}, Errors: []string{}, Warnings: []string{"Saved evidence report could not be loaded; existing round archives are unchanged"}}
	}
	if state.Round > 1 && r.Rounds == nil {
		history := []*Report{}
		for round := 1; round < state.Round; round++ {
			previous, err := loadReport(filepath.Join(roundDir(dir, round), "report.json"), &state, round)
			if err != nil {
				r.HistoryIncomplete = true
				r.Warnings = append(r.Warnings, fmt.Sprintf("Round %d evidence archive could not be loaded; its existing files were left unchanged", round))
				continue
			}
			previous.Rounds = nil
			history = append(history, previous)
		}
		r.Rounds = history
	}
	setResourceIDs(r, state)
	if state.Phase == "closed" {
		// Cleanup was committed before its report write. Reconcile that write
		// after a crash without repeating upstream operations or turning a
		// completed investigation into a different outcome.
		if !slices.Contains([]Status{StatusCompleted, StatusError, StatusInterrupted, StatusTimedOut, StatusClosed}, r.Status) {
			r.Status = StatusClosed
		}
		if r.Cleanup == nil {
			r.Cleanup = &CleanupRecord{Errors: []string{}}
		}
		if state.SandboxID != "" {
			r.Cleanup.SandboxDeleted = true
		}
		if state.SessionID != "" {
			r.Cleanup.SessionDeleted = true
		}
		if r.FinishedAt == nil {
			if parsed, err := time.Parse(time.RFC3339Nano, state.UpdatedAt); err == nil {
				r.FinishedAt = &parsed
			}
		}
		return r, i.persist(dir, r)
	}
	state.Phase = "closing"
	if err := saveState(dir, &state); err != nil {
		return r, err
	}
	r.CreationUncertain, r.SessionCreationUncertain = flag(state.SandboxCreationPending), flag(state.SessionCreationPending)
	r.SandboxRequest = sandboxRequest(state)
	r.SessionRequest = map[string]any{"agent": map[string]any{"namespace": state.Config.Namespace, "name": state.Config.Agent}, "request_id": state.RunID, "name": "triagelab-" + state.RunID}
	r.Cleanup = &CleanupRecord{Errors: []string{}}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	i.Cleanup(ctx, r)
	stateFromReport(&state, r)
	if cleanupComplete(r) {
		state.Phase = "closed"
	}
	r.Status, r.FinishedAt = Status(state.Phase), stampTime()
	writeErr := errors.Join(saveState(dir, &state), i.persist(dir, r))
	if state.Phase != "closed" {
		return r, errors.Join(fmt.Errorf("investigation cleanup is incomplete; retry close"), writeErr)
	}
	return r, writeErr
}

// Keep report growth bounded even when many rounds contain large process output.
func checkReportSize(r *Report) error {
	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if len(encoded) >= maxReportBytes {
		return fmt.Errorf("investigation report exceeds the 64 MiB history limit")
	}
	return nil
}
