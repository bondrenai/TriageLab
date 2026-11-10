package investigation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

// SchemaVersion identifies the report.json layout this build writes and is
// willing to rewrite. Every TriageLab build so far has written version 1.
const SchemaVersion = 1

// ErrUnsupportedSchema marks a saved report this build will not rewrite. Its
// wrapped message is an operator-facing sentence.
var ErrUnsupportedSchema = errors.New("Unsupported report schema")

// Status is a run's lifecycle state.
type Status string

const (
	// StatusQueued is saved by the workspace before the investigator starts.
	StatusQueued      Status = "queued"
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusError       Status = "error"
	StatusInterrupted Status = "interrupted"
	StatusTimedOut    Status = "timed_out"
)

// Outcome is a run's validated result: the outcome enum of the investigator's
// output schema. Anything short of a completed, validated run is inconclusive.
type Outcome string

const (
	OutcomeReproduced    Outcome = "reproduced"
	OutcomeNotReproduced Outcome = "not_reproduced"
	OutcomeInconclusive  Outcome = "inconclusive"
)

// substrateVersion is the pinned Agent Substrate release recorded in reports.
const substrateVersion = "v1.0.0"

// Report is the canonical investigation record saved as report.json. It types
// the members TriageLab defines. Members whose shape kagent defines stay
// generic: the sandbox and session resources, the saved creation requests, and
// the tool-call arguments and responses inside kagent's own ToolCall type. The
// model's schema-validated analysis stays generic too.
//
// A Report decoded from JSON remembers the object it was read from. Encoding
// it writes back every member this build does not model, and every modelled
// member this build did not change, exactly as read, so rewriting an older or
// newer report drops nothing. A nested type that a rewrite changes must do the
// same for its own members, as CleanupRecord does.
type Report struct {
	SchemaVersion   int           `json:"schema_version"`
	RunID           string        `json:"run_id"`
	Case            casefile.Case `json:"case"`
	Status          Status        `json:"status"`
	Outcome         Outcome       `json:"outcome"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      *time.Time    `json:"finished_at"`
	Agent           *AgentRef     `json:"agent,omitempty"`
	SandboxTemplate string        `json:"sandbox_template"`
	// StartedBy is the signed-in workspace account that started the run. It is
	// absent for CLI runs.
	StartedBy string `json:"started_by,omitempty"`

	// Sandbox is kagent's sandbox resource as last returned. Session starts as
	// TriageLab's journal of the session ID and task ID; recovery can replace it
	// with kagent's session resource, which then also carries the task ID.
	Sandbox map[string]any `json:"sandbox,omitzero"`
	Session map[string]any `json:"session,omitzero"`
	// The creation journal: the exact kagent tool arguments, replayed verbatim
	// to recover a resource whose creation outcome is uncertain, and whether it is.
	SandboxRequest           map[string]any `json:"sandbox_request,omitzero"`
	SessionRequest           map[string]any `json:"session_request,omitzero"`
	CreationUncertain        *bool          `json:"creation_uncertain,omitempty"`
	SessionCreationUncertain *bool          `json:"session_creation_uncertain,omitempty"`
	// TaskState is the last observed kagent task state.
	TaskState string `json:"task_state,omitzero"`
	// TaskFailure is kagent's bounded failure text for a turn that stopped
	// without completing. It is untrusted upstream text.
	TaskFailure string `json:"task_failure,omitempty"`

	Experiments []Experiment      `json:"experiments"`
	ToolCalls   []kagent.ToolCall `json:"tool_calls,omitzero"`
	// Analysis is the investigator's answer after ValidateAnalysis.
	Analysis map[string]any `json:"analysis,omitzero"`
	Warnings []string       `json:"warnings"`
	Errors   []string       `json:"errors"`

	Connection *Connection       `json:"connection,omitempty"`
	Cleanup    *CleanupRecord    `json:"cleanup,omitempty"`
	Upstream   *UpstreamVersions `json:"upstream,omitempty"`
	TriageLab  *BuildProvenance  `json:"triagelab,omitempty"`

	loaded preserved
}

// AgentRef names the kagent Agent a run invoked.
type AgentRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Connection is the kagent endpoint and identity that own a run's resources.
// Cleanup of a saved report requires the same configuration.
type Connection struct {
	Endpoint  string `json:"endpoint"`
	UserID    string `json:"user_id"`
	Namespace string `json:"namespace"`
}

// CleanupRecord is the outcome of the most recent runtime cleanup attempt.
type CleanupRecord struct {
	SandboxDeleted bool     `json:"sandbox_deleted"`
	SessionDeleted bool     `json:"session_deleted"`
	Errors         []string `json:"errors"`

	loaded preserved
}

// UpstreamVersions records the pinned upstream releases this build targets.
type UpstreamVersions struct {
	KAgent    string `json:"kagent"`
	Substrate string `json:"substrate"`
}

// BuildProvenance identifies this binary and the definitions embedded in it.
// The deployed Agent's prompt and model are not read back.
type BuildProvenance struct {
	Version                    string `json:"version"`
	EmbeddedPromptSHA256       string `json:"embedded_prompt_sha256"`
	EmbeddedOutputSchemaSHA256 string `json:"embedded_output_schema_sha256"`
	DeployedAgentVerified      bool   `json:"deployed_agent_verified"`
}

// Experiment is one process the investigator started in the sandbox, with the
// state, exit code and output TriageLab read directly from the sandbox service.
type Experiment struct {
	CallID    string            `json:"call_id"`
	ProcessID string            `json:"process_id"`
	Command   []string          `json:"command"`
	CWD       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	// State is the sandbox service's process state; absent until first read.
	State    string `json:"state,omitzero"`
	ExitCode *int   `json:"exit_code,omitempty"`
	// Output is present once the process exited and its output was read. Its
	// members are stored inline, beside the other experiment members.
	*Output
	OutputsComplete bool `json:"outputs_complete,omitempty"`
	TimedOut        bool `json:"timed_out,omitempty"`
}

// Output is a process's bounded standard output and error.
type Output struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}

// exited reports whether the sandbox service last reported the process exited.
func (e *Experiment) exited() bool { return e.State == "PROCESS_STATE_EXITED" }

func (r *Report) sandboxID() string { return str(r.Sandbox["id"]) }
func (r *Report) sessionID() string { return str(r.Session["id"]) }
func (r *Report) taskID() string    { return str(r.Session["task_id"]) }

func flag(value bool) *bool  { return &value }
func isSet(value *bool) bool { return value != nil && *value }

// newReport is the record Run journals before any kagent call.
func newReport(c casefile.Case, config Config, sandboxRequest map[string]any) *Report {
	return &Report{
		SchemaVersion: SchemaVersion, RunID: c.ID, Case: c, StartedAt: time.Now().UTC(),
		Status: StatusRunning, Outcome: OutcomeInconclusive, Sandbox: map[string]any{}, Session: map[string]any{},
		Agent:           &AgentRef{Namespace: config.Namespace, Name: config.Agent},
		SandboxTemplate: config.Template,
		SandboxRequest:  sandboxRequest, Experiments: []Experiment{}, ToolCalls: []kagent.ToolCall{}, Analysis: map[string]any{},
		Warnings: []string{}, Errors: []string{},
		Connection: &Connection{Endpoint: config.Endpoint, UserID: config.UserID, Namespace: config.Namespace},
		Cleanup:    &CleanupRecord{Errors: []string{}},
		Upstream:   &UpstreamVersions{KAgent: kagent.Version, Substrate: substrateVersion},
		// Only this binary's own definitions: the deployed Agent may have been
		// rendered by another build, and its prompt and model are not read back.
		TriageLab: &BuildProvenance{
			Version: config.Version, EmbeddedPromptSHA256: agent.PromptSHA256(),
			EmbeddedOutputSchemaSHA256: agent.SchemaSHA256(),
		},
	}
}

// QueuedReport is the record the workspace saves before handing a case to the
// investigator. It has no runtime references, so it is not eligible for cleanup.
func QueuedReport(c casefile.Case, config Config) *Report {
	return &Report{
		SchemaVersion: SchemaVersion, RunID: c.ID, Case: c, Status: StatusQueued,
		Agent:           &AgentRef{Namespace: config.Namespace, Name: config.Agent},
		SandboxTemplate: config.Template, Outcome: OutcomeInconclusive, StartedAt: time.Now().UTC(),
		Experiments: []Experiment{}, Errors: []string{}, Warnings: []string{},
	}
}

// DecodeReport reads one saved report.json for mutation. It refuses a report
// whose schema_version this build does not write, and one whose TriageLab
// members have the wrong JSON types, rather than rewriting either.
func DecodeReport(data []byte) (*Report, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var decoded Report
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("a report must contain exactly one JSON object")
	}
	return &decoded, nil
}

// checkSchema refuses anything but this build's schema version. A missing
// version is refused too: every TriageLab build has written one, and cleanup
// replays saved creation requests against a live cluster, so an unidentified
// record is not trusted to have this layout.
func checkSchema(members map[string]json.RawMessage) error {
	raw, present := members["schema_version"]
	if !present {
		return fmt.Errorf("%w: the report records no schema_version, so it was not written by TriageLab or was edited. This build only rewrites version %d reports.", ErrUnsupportedSchema, SchemaVersion)
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil || version != SchemaVersion {
		shown := string(raw)
		if len(shown) > 24 {
			shown = shown[:24] + "…"
		}
		return fmt.Errorf("%w: the report has schema_version %s, and this build only rewrites version %d reports. Use the TriageLab build that wrote it.", ErrUnsupportedSchema, shown, SchemaVersion)
	}
	return nil
}

func (r Report) MarshalJSON() ([]byte, error) {
	type fields Report
	return r.loaded.encode(fields(r))
}

func (r *Report) UnmarshalJSON(data []byte) error {
	type fields Report
	var typed fields
	loaded, err := decodeObject(data, &typed, checkSchema)
	if err != nil {
		return err
	}
	*r = Report(typed)
	r.loaded = loaded
	return nil
}

func (c CleanupRecord) MarshalJSON() ([]byte, error) {
	type fields CleanupRecord
	return c.loaded.encode(fields(c))
}

func (c *CleanupRecord) UnmarshalJSON(data []byte) error {
	type fields CleanupRecord
	var typed fields
	loaded, err := decodeObject(data, &typed, nil)
	if err != nil {
		return err
	}
	*c = CleanupRecord(typed)
	c.loaded = loaded
	return nil
}

// preserved remembers the JSON object a value was decoded from.
type preserved struct {
	members  map[string]json.RawMessage // every member as read
	baseline map[string]json.RawMessage // the typed fields re-encoded right after reading
}

// decodeObject decodes a JSON object into typed, a pointer to a struct without
// JSON methods, and returns what encode needs to write it back faithfully.
// Numbers in generic members stay exact json.Number values.
func decodeObject(data []byte, typed any, check func(map[string]json.RawMessage) error) (preserved, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil || members == nil {
		return preserved{}, errors.New("expected a JSON object")
	}
	if check != nil {
		if err := check(members); err != nil {
			return preserved{}, err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(typed); err != nil {
		return preserved{}, err
	}
	baseline, err := encodedMembers(typed)
	if err != nil {
		return preserved{}, err
	}
	return preserved{members: members, baseline: baseline}, nil
}

// encode writes typed, a struct without JSON methods, as a JSON object. For a
// decoded value, a modelled member whose encoding is unchanged since decoding
// is written back as read (or stays absent), and unmodelled members are kept.
func (p preserved) encode(typed any) ([]byte, error) {
	if p.members == nil {
		return json.Marshal(typed)
	}
	current, err := encodedMembers(typed)
	if err != nil {
		return nil, err
	}
	modelled := jsonNames(reflect.TypeOf(typed))
	result := make(map[string]json.RawMessage, len(p.members)+len(current))
	for name, value := range p.members {
		if !modelled[name] {
			result[name] = value
		}
	}
	for name := range modelled {
		now, present := current[name]
		before, wasPresent := p.baseline[name]
		switch {
		case present == wasPresent && bytes.Equal(now, before):
			if original, ok := p.members[name]; ok {
				result[name] = original
			}
		case present:
			result[name] = now
		}
	}
	return json.Marshal(result)
}

func encodedMembers(value any) (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	err = json.Unmarshal(encoded, &members)
	return members, err
}

// jsonNames returns the object member names encoding/json uses for a struct
// type's exported fields. The preserving types embed no structs.
func jsonNames(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	for index := range t.NumField() {
		field := t.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		names[name] = true
	}
	return names
}
