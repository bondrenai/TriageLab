package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bondrenai/TriageLab/internal/report"
)

type NoteDelivery struct {
	Status    string    `json:"status"`
	CommentID int64     `json:"comment_id,string,omitempty"`
	At        time.Time `json:"at"`
}

type NotePreview struct {
	TicketID  int64  `json:"ticket_id,string"`
	TicketURL string `json:"ticket_url"`
	Body      string `json:"body,omitempty"`
	Plan      string `json:"plan,omitempty"`
	Status    string `json:"status"`
	CommentID int64  `json:"comment_id,string,omitempty"`
	CanPost   bool   `json:"can_post"`
}

func (f *Flow) NotePreview(runID string, saved map[string]any) *NotePreview {
	c, ok := f.noteCase(runID, saved)
	if !ok {
		return nil
	}
	preview := f.notePreview(c, saved)
	return &preview
}

func (f *Flow) PostNote(ctx context.Context, runID string, saved map[string]any, plan string) (NotePreview, error) {
	if err := f.begin(); err != nil {
		return NotePreview{}, err
	}
	defer f.op.Unlock()
	c, ok := f.noteCase(runID, saved)
	if !ok {
		return NotePreview{}, ErrBlocked
	}
	preview := f.notePreview(c, saved)
	if preview.Status == "posted" {
		return preview, nil
	}
	if preview.Status != "ready" {
		return NotePreview{}, ErrBlocked
	}
	if plan == "" || plan != preview.Plan {
		return NotePreview{}, ErrChanged
	}
	if !preview.CanPost {
		return NotePreview{}, ErrOAuthRequired
	}
	ctx, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()
	t, err := f.source.ticket(ctx, c.TicketID)
	if err != nil {
		return NotePreview{}, err
	}
	if t.Status == "closed" {
		return NotePreview{}, ErrBlocked
	}
	if err := ctx.Err(); err != nil {
		return NotePreview{}, err
	}
	// Journal before writing. A crash, timeout, or missing receipt leaves a
	// reservation that blocks another send. The operator can inspect Zendesk.
	c.Note = &NoteDelivery{Status: "unconfirmed", At: time.Now().UTC()}
	if err := f.save(c); err != nil {
		return NotePreview{}, err
	}
	commentID, err := f.source.postComment(ctx, t, preview.Body, false)
	switch {
	case errors.Is(err, ErrChanged), errors.Is(err, ErrOAuthRequired):
		// These responses confirm Zendesk did not apply the write.
		c.Note = nil
		if saveErr := f.save(c); saveErr != nil {
			return NotePreview{}, saveErr
		}
		return NotePreview{}, err
	case err == nil:
		c.Note = &NoteDelivery{Status: "posted", CommentID: commentID, At: c.Note.At}
		if err := f.save(c); err != nil {
			return NotePreview{}, err
		}
	}
	return f.notePreview(c, saved), nil
}

func (f *Flow) noteCase(runID string, saved map[string]any) (Case, bool) {
	if !f.settings.InternalNotes || runID == "" || saved["run_id"] != runID || saved["status"] != "completed" {
		return Case{}, false
	}
	finished, _ := saved["finished_at"].(string)
	if _, err := time.Parse(time.RFC3339Nano, finished); err != nil {
		return Case{}, false
	}
	input, _ := saved["case"].(map[string]any)
	source, _ := input["source"].(map[string]any)
	rawID, _ := source["ticket_id"].(string)
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 || rawID != strconv.FormatInt(id, 10) || source["kind"] != "zendesk" {
		return Case{}, false
	}
	c, found := f.store.get(id)
	return c, found && c.RunID == runID && c.State == "launched" && c.Plan != "" &&
		source["approval_plan"] == c.Plan && source["url"] == c.Account+"/agent/tickets/"+rawID
}

func (f *Flow) notePreview(c Case, saved map[string]any) NotePreview {
	preview := NotePreview{TicketID: c.TicketID, TicketURL: c.Account + "/agent/tickets/" + strconv.FormatInt(c.TicketID, 10)}
	if c.Note != nil {
		preview.Status, preview.CommentID = c.Note.Status, c.Note.CommentID
		return preview
	}
	preview.Status = "ready"
	preview.Body = investigationNote(c.RunID, saved, f.currentSecrets())
	preview.Plan = digest(struct {
		RunID, TicketURL, Body string
	}{c.RunID, preview.TicketURL, preview.Body})
	preview.CanPost = f.oauth != nil && f.oauth.status().Connected
	return preview
}

func investigationNote(runID string, saved map[string]any, secrets []string) string {
	line := func(value any, max int) string {
		text, _ := value.(string)
		text = report.Redact(text, secrets...).(string)
		// Zendesk expands ticket placeholders even in plain comments.
		text = strings.ReplaceAll(strings.Join(strings.Fields(text), " "), "{{", "{ { ")
		if len(text) > max {
			return bounded(text, max) + "…"
		}
		return text
	}
	outcome := "Inconclusive"
	switch saved["outcome"] {
	case "reproduced":
		outcome = "Reproduced"
	case "not_reproduced":
		outcome = "No reproduction observed"
	}
	var body strings.Builder
	fmt.Fprintf(&body, "TriageLab investigation %s\nOutcome — %s\n", runID, outcome)
	analysis, _ := saved["analysis"].(map[string]any)
	if summary := line(analysis["summary"], 1200); summary != "" {
		fmt.Fprintf(&body, "Agent summary — %s\n", summary)
	}
	if actions, ok := analysis["next_actions"].([]any); ok && len(actions) > 0 {
		if action := line(actions[0], 350); action != "" {
			fmt.Fprintf(&body, "Suggested next step — %s\n", action)
		}
	}
	body.WriteString("Recorded process evidence\n")
	experiments, _ := saved["experiments"].([]any)
	count := 0
	for _, raw := range experiments {
		experiment, _ := raw.(map[string]any)
		id := line(experiment["process_id"], 100)
		exit, ok := experiment["exit_code"].(json.Number)
		code, err := exit.Int64()
		if !ok || err != nil || id == "" {
			continue
		}
		command, _ := json.Marshal(experiment["command"])
		fmt.Fprintf(&body, "- %s · exit %d · %s\n", id, code, line(string(command), 240))
		count++
		if count == 6 {
			break
		}
	}
	if count == 0 {
		body.WriteString("No process exit codes were recorded.\n")
	}
	return strings.TrimSpace(body.String())
}
