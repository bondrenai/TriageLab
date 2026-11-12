package ticketflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
)

// ContinuationReservation binds an explicitly approved continuation to the
// evidence, settings, and paused round. It grants no helpdesk write permission.
type ContinuationReservation struct {
	Round  int       `json:"round"`
	Digest string    `json:"digest"`
	Actor  string    `json:"actor"`
	At     time.Time `json:"at"`
}

type ContinuationApproval struct {
	RunID  string
	Round  int
	Report map[string]any
	Input  casefile.Case
	Config investigation.Config
	Tools  []string
	Actor  string
}

// ApproveContinuation keeps the original ticket approval and note receipts.
// The caller supplies only operator-reviewed new inputs and immutable saved run
// settings; no ticket is fetched and no message is sent. As with Approve, launch
// acquires the workspace slot and calls reserve before dispatching work.
func (f *Flow) ApproveContinuation(a ContinuationApproval, launch func(reserve func() error) error) error {
	if err := f.begin(); err != nil {
		return err
	}
	defer f.op.Unlock()
	c, ok := f.approvedCase(a.RunID, a.Report)
	if !ok || c.Paused || a.Round < 1 || a.Round >= 32 || a.Actor == "" || len(a.Actor) > 256 || launch == nil {
		return ErrBlocked
	}
	if a.Report["status"] != "paused" && a.Report["status"] != "pausing" && a.Report["status"] != "resuming" {
		return ErrBlocked
	}
	if err := a.Config.Validate(); err != nil {
		return ErrBlocked
	}
	if a.Config.Endpoint != f.runtime.Endpoint || !f.runtime.AllowsNamespace(a.Config.Namespace) ||
		a.Config.UserID != f.runtime.UserID || a.Config.Agent != c.Agent || a.Config.Template != c.Sandbox {
		return ErrChanged
	}
	if err := agent.ValidateExtraTools(a.Tools); err != nil {
		return ErrBlocked
	}
	if err := f.validateContinuationInput(a.Input); err != nil {
		return err
	}
	// Ingestion allocates a new input ID each time. Content identity, not this
	// incidental ID, governs a retry of the same approved continuation.
	input := a.Input
	input.ID = ""
	config := a.Config
	// Credential lookup and the manual-agent menu may change between attempts;
	// neither changes the retained investigation's identity or tool allowance.
	config.TokenEnv, config.ManualAgents = "", nil
	fingerprint := digest(struct {
		RunID, OriginalPlan string
		Round               int
		Input               casefile.Case
		Config              investigation.Config
		Tools               []string
	}{a.RunID, c.Plan, a.Round, input, config, a.Tools})
	for _, prior := range c.Continuations {
		if prior.Round == a.Round {
			if prior.Digest != fingerprint {
				return ErrChanged
			}
			return launch(func() error { return nil })
		}
	}
	if a.Round != len(c.Continuations)+1 {
		return ErrChanged
	}
	c.Continuations = append(slices.Clone(c.Continuations), ContinuationReservation{
		Round: a.Round, Digest: fingerprint, Actor: a.Actor, At: time.Now().UTC(),
	})
	return launch(func() error { return f.save(c) })
}

func (f *Flow) validateContinuationInput(input casefile.Case) error {
	if strings.TrimSpace(input.Description) == "" || len(input.Description) > casefile.MaxDescriptionBytes ||
		len(input.Attachments) > casefile.MaxFiles || f.hasSecret([]byte(input.Description)) {
		return fmt.Errorf("continuation needs bounded information without credentials")
	}
	seen := make(map[string]bool)
	total := 0
	for _, file := range input.Attachments {
		actual := sha256.Sum256(file.Content)
		if !fs.ValidPath(file.Path) || strings.Contains(file.Path, `\`) || seen[file.Path] ||
			len(file.Content) > casefile.MaxFileBytes || file.SizeBytes != len(file.Content) ||
			file.SHA256 != hex.EncodeToString(actual[:]) || f.hasSecret(file.Content) {
			return fmt.Errorf("continuation attachment does not match reviewed evidence")
		}
		seen[file.Path] = true
		total += len(file.Content)
		if total > casefile.MaxTotalBytes {
			return fmt.Errorf("continuation exceeds attachment limits")
		}
	}
	return nil
}
