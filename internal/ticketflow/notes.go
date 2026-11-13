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

// MaxNoteBodyBytes bounds an operator-edited investigation comment body.
const MaxNoteBodyBytes = 16 << 10

type NoteDelivery struct {
	Status    string `json:"status"`
	CommentID int64  `json:"comment_id,string,omitempty"`
	// Body is the exact text this delivery committed to send. It is recorded
	// with the durable reservation before any write, so an uncertain delivery
	// can be compared with the conversation at the source.
	Body string    `json:"body,omitempty"`
	At   time.Time `json:"at"`
}

type NotePreview struct {
	Source     string `json:"source"`
	Visibility string `json:"visibility"`
	TicketID   int64  `json:"ticket_id,string"`
	TicketURL  string `json:"ticket_url"`
	Body       string `json:"body,omitempty"`
	Plan       string `json:"plan,omitempty"`
	Status     string `json:"status"`
	CommentID  int64  `json:"comment_id,string,omitempty"`
	CanPost    bool   `json:"can_post"`
	// Automatic reports that the procedure policy for this case posts the
	// note without operator review once the run completes. A preview that is
	// still ready or blocked was left for a human to review, edit, and post.
	Automatic bool     `json:"automatic,omitempty"`
	Findings  []string `json:"findings,omitempty"`
}

// notesEnabled reports whether this connection may post investigation notes:
// investigation comments on GitHub, GitLab, and Linear issues, and internal
// notes on Zendesk tickets and Bondren and Jira issues.
func (f *Flow) notesEnabled() bool {
	return (f.provider == "github" || f.provider == "gitlab" || f.provider == "linear") && f.settings.InvestigationComments ||
		(f.provider == "zendesk" || f.provider == "bondren" || f.provider == "jira") && f.settings.InternalNotes
}

func (f *Flow) NotePreview(runID string, saved map[string]any) *NotePreview {
	c, ok := f.noteCase(runID, saved)
	if !ok {
		return nil
	}
	preview := f.notePreview(c, saved)
	return &preview
}

func (f *Flow) PostNote(ctx context.Context, runID string, saved map[string]any, plan, body string) (NotePreview, error) {
	ctx, finish := traced(ctx, "post_note")
	preview, err := f.postNote(ctx, runID, saved, plan, body)
	finish(err)
	return preview, err
}

func (f *Flow) postNote(ctx context.Context, runID string, saved map[string]any, plan, body string) (NotePreview, error) {
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
	// A blocked preview keeps its plan: the operator may edit the held text
	// so it no longer discloses and post the corrected body.
	if preview.Status != "ready" && preview.Status != "blocked" {
		return NotePreview{}, ErrBlocked
	}
	if plan == "" || plan != preview.Plan {
		return NotePreview{}, ErrChanged
	}
	if !preview.CanPost {
		return NotePreview{}, ErrOAuthRequired
	}
	return f.deliverNote(ctx, c, preview, body, saved)
}

// PostAutomaticNote delivers the investigation note for a completed run when
// the case's approved procedure policy allows posting without operator
// review. The workspace calls it once a ticket-launched run settles. Skips
// are not errors: the ready or blocked preview keeps a human in the loop.
func (f *Flow) PostAutomaticNote(ctx context.Context, runID string, saved map[string]any) {
	ctx, finish := traced(ctx, "automatic_note")
	_, err := f.postAutomaticNote(ctx, runID, saved)
	finish(err)
}

func (f *Flow) postAutomaticNote(ctx context.Context, runID string, saved map[string]any) (NotePreview, error) {
	if err := f.begin(); err != nil {
		// Busy or broken state leaves the note for manual review.
		return NotePreview{}, nil
	}
	defer f.op.Unlock()
	c, ok := f.noteCase(runID, saved)
	if !ok || !c.AutoComment {
		return NotePreview{}, nil
	}
	preview := f.notePreview(c, saved)
	if preview.Status != "ready" || !preview.CanPost {
		return NotePreview{}, nil
	}
	return f.deliverNote(ctx, c, preview, "", saved)
}

// deliverNote runs under op. The plan already binds the generated note to the
// completed report; body carries an operator edit, empty for the generated
// text. The sanitizer holds an edit for review instead of rewriting it.
func (f *Flow) deliverNote(ctx context.Context, c Case, preview NotePreview, body string, saved map[string]any) (NotePreview, error) {
	text := preview.Body
	if body != "" && body != preview.Body {
		if strings.TrimSpace(body) == "" || len(body) > MaxNoteBodyBytes {
			return NotePreview{}, ErrBlocked
		}
		// An edit is posted exactly as reviewed. It must clear the same
		// output checks as the generated note.
		if findings := sanitizeOutput(body, f.currentSecrets(), outboundTerms(f.store.list(), c.TicketID, nil)); len(findings) > 0 {
			held := preview
			held.Body, held.Status, held.Findings = body, "blocked", findings
			return held, nil
		}
		text = body
	}
	if preview.Status == "blocked" && text == preview.Body {
		// The generated note is held; only a changed operator edit may post.
		return NotePreview{}, ErrBlocked
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
	c.Note = &NoteDelivery{Status: "unconfirmed", Body: text, At: time.Now().UTC()}
	if err := f.save(c); err != nil {
		return NotePreview{}, err
	}
	commentID, err := f.source.postComment(ctx, t, text, f.provider == "github" || f.provider == "gitlab" || f.provider == "linear")
	switch {
	case errors.Is(err, ErrChanged), errors.Is(err, ErrOAuthRequired):
		// These responses confirm the connector did not apply the write.
		c.Note = nil
		if saveErr := f.save(c); saveErr != nil {
			return NotePreview{}, saveErr
		}
		return NotePreview{}, err
	case err == nil:
		c.Note = &NoteDelivery{Status: "posted", CommentID: commentID, Body: text, At: c.Note.At}
		if err := f.save(c); err != nil {
			return NotePreview{}, err
		}
	}
	return f.notePreview(c, saved), nil
}

func (f *Flow) noteCase(runID string, saved map[string]any) (Case, bool) {
	if !f.notesEnabled() || runID == "" || saved["run_id"] != runID || saved["status"] != "completed" {
		return Case{}, false
	}
	finished, _ := saved["finished_at"].(string)
	if _, err := time.Parse(time.RFC3339Nano, finished); err != nil {
		return Case{}, false
	}
	return f.approvedCase(runID, saved)
}

// approvedCase binds local work to the original durable ticket approval. It
// does not fetch new ticket material or grant permission to send a message.
func (f *Flow) approvedCase(runID string, saved map[string]any) (Case, bool) {
	if runID == "" || saved["run_id"] != runID {
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
	if f.provider == "gitlab" && (c.Source != "gitlab" || c.URL != f.ticketURL(id) ||
		source["gitlab_project"] != gitlabProjectFromAccount(f.Account()) || source["issue_iid"] != rawID) {
		return Case{}, false
	}
	if f.provider == "linear" && (c.Source != "linear" || c.URL != f.ticketURL(id) ||
		source["linear_team"] != strings.TrimPrefix(f.Account(), "https://linear.app/") ||
		source["linear_identifier"] != strings.TrimPrefix(f.ticketURL(id), f.Account()+"/issue/")) {
		return Case{}, false
	}
	if f.provider == "bondren" && (c.Source != "bondren" || source["bondren_origin"] != f.Account() ||
		source["url"] != c.URL || source["bondren_issue"] != bondrenIssueUUID(c.URL, f.Account())) {
		return Case{}, false
	}
	if f.provider == "jira" && (c.Source != "jira" || source["jira_site"] != f.Account() ||
		source["url"] != c.URL || source["jira_issue"] != strconv.FormatInt(c.TicketID, 10) ||
		jiraIssueKeyFromURL(c.URL, f.Account()) == "") {
		return Case{}, false
	}
	// Bondren issue URLs carry the issue UUID rather than the number, so the
	// stored case URL is the durable locator to bind against.
	boundURL := f.ticketURL(id)
	if f.provider == "bondren" || f.provider == "jira" {
		boundURL = c.URL
	}
	return c, found && c.RunID == runID && c.State == "launched" && c.Plan != "" &&
		c.Account == f.Account() && source["approval_plan"] == c.Plan && source["url"] == boundURL
}

func (f *Flow) ticketURL(id int64) string {
	path := "/agent/tickets/"
	switch f.provider {
	case "github":
		path = "/issues/"
	case "gitlab":
		path = "/-/issues/"
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
	// Bondren issue URLs carry the issue UUID, not the number, so the stored
	// case URL is the ticket's durable locator.
	url := f.ticketURL(c.TicketID)
	if f.provider == "bondren" || f.provider == "jira" {
		url = c.URL
	}
	preview := NotePreview{Source: f.provider, Visibility: "internal", TicketID: c.TicketID, TicketURL: url, Automatic: c.AutoComment}
	if f.provider == "github" {
		preview.Visibility = "repository"
	}
	if f.provider == "gitlab" {
		preview.Visibility = "project"
	}
	if f.provider == "linear" {
		preview.Visibility = "workspace"
	}
	if c.Note != nil {
		preview.Status, preview.CommentID, preview.Body = c.Note.Status, c.Note.CommentID, c.Note.Body
		return preview
	}
	preview.Status = "ready"
	preview.Body = investigationNote(c.RunID, saved, f.currentSecrets())
	if f.provider == "github" || f.provider == "gitlab" || f.provider == "linear" || f.provider == "bondren" {
		// Render imported summaries and commands literally. In GitHub, GitLab,
		// Linear, or Bondren Markdown, prose could otherwise create mentions,
		// links or embedded images.
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
	if f.provider == "github" || f.provider == "gitlab" || f.provider == "linear" {
		preview.CanPost = f.settings.InvestigationComments
	} else if f.provider == "bondren" || f.provider == "jira" {
		preview.CanPost = f.settings.InternalNotes
	} else {
		preview.CanPost = f.oauth != nil && f.oauth.status().Connected
	}
	preview.Plan = digest(struct {
		RunID, TicketURL, Body, Source, Visibility string
	}{c.RunID, preview.TicketURL, preview.Body, preview.Source, preview.Visibility})
	// The output sanitizer holds the note instead of rewriting it; the
	// operator reviews the findings with the exact blocked body and may post
	// a corrected edit under the same plan.
	if findings := sanitizeOutput(preview.Body, f.currentSecrets(), outboundTerms(f.store.list(), c.TicketID, nil)); len(findings) > 0 {
		preview.Status = "blocked"
		preview.Findings = findings
		return preview
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
