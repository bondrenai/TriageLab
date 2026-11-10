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
	Source     string   `json:"source"`
	Visibility string   `json:"visibility"`
	TicketID   int64    `json:"ticket_id,string"`
	TicketURL  string   `json:"ticket_url"`
	Body       string   `json:"body,omitempty"`
	Plan       string   `json:"plan,omitempty"`
	Status     string   `json:"status"`
	CommentID  int64    `json:"comment_id,string,omitempty"`
	CanPost    bool     `json:"can_post"`
	Findings   []string `json:"findings,omitempty"`
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
	ctx, finish := traced(ctx, "post_note")
	preview, err := f.postNote(ctx, runID, saved, plan)
	finish(err)
	return preview, err
}

func (f *Flow) postNote(ctx context.Context, runID string, saved map[string]any, plan string) (NotePreview, error) {
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
	if !openForInvestigation(f.provider, t.Status) {
		return NotePreview{}, ErrBlocked
	}
	if err := ctx.Err(); err != nil {
		return NotePreview{}, err
	}
	// Journal before writing. A crash, timeout, or missing receipt leaves a
	// reservation that blocks another send. The operator can inspect the source.
	c.Note = &NoteDelivery{Status: "unconfirmed", At: time.Now().UTC()}
	if err := f.save(c); err != nil {
		return NotePreview{}, err
	}
	commentID, err := f.source.postComment(ctx, t, preview.Body, f.provider == "github")
	switch {
	case errors.Is(err, ErrChanged), errors.Is(err, ErrOAuthRequired):
		// These responses confirm the connector did not apply the write.
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
	enabled := f.provider == "zendesk" && f.settings.InternalNotes ||
		(f.provider == "github" || f.provider == "linear") && f.settings.InvestigationComments
	if !enabled || runID == "" || saved["run_id"] != runID || saved["status"] != "completed" {
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
	if err != nil || id <= 0 || rawID != strconv.FormatInt(id, 10) || source["kind"] != f.provider {
		return Case{}, false
	}
	c, found := f.store.get(id)
	if f.provider == "github" && (c.Source != "github" || c.URL != f.ticketURL(id) ||
		source["repository"] != strings.TrimPrefix(f.Account(), "https://github.com/") || source["issue_number"] != rawID) {
		return Case{}, false
	}
	if f.provider == "linear" && (c.Source != "linear" || c.URL != f.ticketURL(id) ||
		source["linear_team"] != strings.TrimPrefix(f.Account(), "https://linear.app/") ||
		source["linear_identifier"] != strings.TrimPrefix(f.ticketURL(id), f.Account()+"/issue/")) {
		return Case{}, false
	}
	return c, found && c.RunID == runID && c.State == "launched" && c.Plan != "" &&
		c.Account == f.Account() && source["approval_plan"] == c.Plan && source["url"] == f.ticketURL(id)
}

func (f *Flow) ticketURL(id int64) string {
	path := "/agent/tickets/"
	switch f.provider {
	case "github":
		path = "/issues/"
	case "linear":
		// The identifier is the configured team key plus the issue number.
		teamKey := strings.TrimPrefix(f.Account(), "https://linear.app/")
		if _, key, found := strings.Cut(teamKey, "/"); found {
			teamKey = key
		}
		return f.Account() + "/issue/" + teamKey + "-" + strconv.FormatInt(id, 10)
	}
	return f.Account() + path + strconv.FormatInt(id, 10)
}

func (f *Flow) notePreview(c Case, saved map[string]any) NotePreview {
	preview := NotePreview{Source: f.provider, Visibility: "internal", TicketID: c.TicketID, TicketURL: f.ticketURL(c.TicketID)}
	if f.provider == "github" {
		preview.Visibility = "repository"
	}
	if f.provider == "linear" {
		preview.Visibility = "workspace"
	}
	if c.Note != nil {
		preview.Status, preview.CommentID = c.Note.Status, c.Note.CommentID
		return preview
	}
	preview.Status = "ready"
	preview.Body = investigationNote(c.RunID, saved, f.currentSecrets())
	if f.provider == "github" || f.provider == "linear" {
		// Render imported summaries and commands literally. In GitHub or Linear
		// Markdown, prose could otherwise create mentions, links or embedded
		// images.
		width, run := 3, 0
		for _, char := range preview.Body {
			if char == '`' {
				run++
				if run >= width {
					width = run + 1
				}
			} else {
				run = 0
			}
		}
		fence := strings.Repeat("`", width)
		preview.Body = fence + "text\n" + preview.Body + "\n" + fence
	}
	// The output sanitizer holds the note instead of rewriting it; the
	// operator reviews the findings with the exact blocked body.
	if findings := sanitizeOutput(preview.Body, f.currentSecrets(), outboundTerms(f.store.list(), c.TicketID, nil)); len(findings) > 0 {
		preview.Status = "blocked"
		preview.Findings = findings
		return preview
	}
	preview.Plan = digest(struct {
		RunID, TicketURL, Body, Source, Visibility string
	}{c.RunID, preview.TicketURL, preview.Body, preview.Source, preview.Visibility})
	if f.provider == "github" || f.provider == "linear" {
		preview.CanPost = f.settings.InvestigationComments
	} else {
		preview.CanPost = f.oauth != nil && f.oauth.status().Connected
	}
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
