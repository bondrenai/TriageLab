package ticketflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/report"
)

const (
	procedure         = "python-timeout-v1"
	approvalLifetime  = 15 * time.Minute
	clarificationWait = 7 * 24 * time.Hour
)

var (
	ErrBusy       = errors.New("a ticket action is already in progress; try again shortly")
	ErrNotFound   = errors.New("ticket case not found")
	ErrBlocked    = errors.New("ticket is not ready for this action")
	ErrStorage    = errors.New("ticket state could not be saved; restart the service before trying again")
	fileName      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,100}\.(py|json|log|txt)$`)
	pythonVersion = regexp.MustCompile(`^3\.12(?:\.[0-9]{1,3})?$`)
)

type Fact struct {
	Label     string `json:"label"`
	Value     string `json:"value"`
	CommentID string `json:"comment_id"`
}

type Question struct {
	Body    string    `json:"body"`
	Status  string    `json:"status"`
	ReplyID int64     `json:"reply_id"`
	At      time.Time `json:"at"`
}

type Case struct {
	TicketID    int64                 `json:"ticket_id,string"`
	Account     string                `json:"account"`
	Title       string                `json:"title"`
	State       string                `json:"state"`
	Reason      string                `json:"reason"`
	Facts       []Fact                `json:"facts,omitempty"`
	Files       []casefile.Attachment `json:"files,omitempty"`
	Draft       string                `json:"draft,omitempty"`
	Questions   []Question            `json:"questions,omitempty"`
	Plan        string                `json:"plan,omitempty"`
	PlanExpires time.Time             `json:"plan_expires,omitempty"`
	CheckedAt   time.Time             `json:"checked_at"`
	RunID       string                `json:"run_id,omitempty"`
	ApprovedAt  time.Time             `json:"approved_at,omitempty"`
	Paused      bool                  `json:"paused"`
	Revision    string                `json:"revision"`
	replyID     int64
}

type Settings struct {
	Policy         Policy `json:"policy"`
	PollSeconds    int    `json:"poll_seconds"`
	MaxExperiments int    `json:"max_experiments"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Template       string `json:"template"`
}

type Flow struct {
	source   source
	oauth    *zendeskOAuth
	store    *store
	settings Settings
	runtime  investigation.Config
	secrets  []string
	op       sync.Mutex
	broken   bool
	closed   bool
	cancel   context.CancelFunc
	done     chan struct{}
}

func Open(config Config, dir string, runtime investigation.Config, secrets ...string) (*Flow, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	z := newZendesk(config.Subdomain)
	s, err := openStore(dir, z.account())
	if err != nil {
		return nil, err
	}
	z.oauth, err = openOAuth(config, z.origin, dir, z.client)
	if err != nil {
		s.lock.Close()
		return nil, err
	}
	flow := newFlow(config, z, s, runtime, secrets)
	flow.oauth = z.oauth
	return flow, nil
}

func (f *Flow) OAuthStatus() OAuthStatus                       { return f.oauth.status() }
func (f *Flow) BeginOAuth(host string) (string, string, error) { return f.oauth.begin(host) }
func (f *Flow) FinishOAuth(ctx context.Context, host, state, browser, code string, denied bool) error {
	if err := f.begin(); err != nil {
		return err
	}
	defer f.op.Unlock()
	return f.oauth.finish(ctx, host, state, browser, code, denied)
}

func (f *Flow) currentSecrets() []string {
	if f.oauth == nil {
		return f.secrets
	}
	return append(slices.Clone(f.secrets), f.oauth.secrets()...)
}

func newFlow(config Config, source source, store *store, runtime investigation.Config, secrets []string) *Flow {
	return &Flow{source: source, store: store, runtime: runtime, secrets: secrets, settings: Settings{
		Policy: config.Policy, PollSeconds: config.PollSeconds, MaxExperiments: runtime.MaxExperiments,
		TimeoutSeconds: runtime.InvestigationTimeout, Template: runtime.Template,
	}}
}

func (f *Flow) Settings() Settings { return f.settings }
func (f *Flow) Cases() []Case      { return f.store.list() }

func (f *Flow) Start(ctx context.Context) {
	if f.settings.PollSeconds == 0 {
		return
	}
	ctx, f.cancel = context.WithCancel(ctx)
	f.done = make(chan struct{})
	go func() {
		defer close(f.done)
		ticker := time.NewTicker(time.Duration(f.settings.PollSeconds) * time.Second)
		defer ticker.Stop()
		var last int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if f.oauth != nil && !f.oauth.status().Connected {
					continue
				}
				cases := f.Cases()
				waiting := slices.DeleteFunc(cases, func(c Case) bool { return c.Paused || c.State != "waiting" })
				if len(waiting) == 0 {
					continue
				}
				index := slices.IndexFunc(waiting, func(c Case) bool { return c.TicketID > last })
				if index < 0 {
					index = 0
				}
				last = waiting[index].TicketID
				_, _ = f.Refresh(ctx, last)
			}
		}
	}()
}

func (f *Flow) Close() error {
	if f.cancel != nil {
		f.cancel()
		<-f.done
	}
	f.op.Lock()
	defer f.op.Unlock()
	f.closed = true
	if z, ok := f.source.(*zendesk); ok {
		z.client.CloseIdleConnections()
	}
	return f.store.lock.Close()
}

func (f *Flow) begin() error {
	if !f.op.TryLock() {
		return ErrBusy
	}
	if f.broken || f.closed {
		f.op.Unlock()
		return ErrStorage
	}
	return nil
}

func (f *Flow) save(c Case) error {
	if err := f.store.save(c); err != nil {
		f.broken = true
		return ErrStorage
	}
	return nil
}

func (f *Flow) Preview(ctx context.Context, id int64) (Case, error) {
	if err := f.begin(); err != nil {
		return Case{}, err
	}
	defer f.op.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()
	current, _ := f.store.get(id)
	c, _, _, err := f.assess(ctx, id, current)
	return c, err
}

func (f *Flow) Track(ctx context.Context, id int64, revision string) (Case, error) {
	return f.refreshTicket(ctx, id, true, revision)
}

func (f *Flow) Refresh(ctx context.Context, id int64) (Case, error) {
	return f.refreshTicket(ctx, id, false, "")
}

func (f *Flow) refreshTicket(ctx context.Context, id int64, enroll bool, revision string) (Case, error) {
	if err := f.begin(); err != nil {
		return Case{}, err
	}
	defer f.op.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()
	previous, tracked := f.store.get(id)
	if !tracked && !enroll {
		return Case{}, ErrNotFound
	}
	if !tracked && len(f.store.list()) >= maxCases {
		return Case{}, ErrBlocked
	}
	if tracked && enroll {
		return previous, nil
	}
	if previous.Paused || previous.RunID != "" {
		return previous, nil
	}
	c, t, _, err := f.assess(ctx, id, previous)
	if err != nil {
		if tracked {
			previous.Reason = "Could not refresh the ticket. " + err.Error()
			previous.Plan = ""
			if saveErr := f.save(previous); saveErr != nil {
				return Case{}, saveErr
			}
		}
		return Case{}, err
	}
	if enroll && revision != c.Revision {
		return Case{}, ErrChanged
	}
	if c.State != "ask" {
		return c, f.save(c)
	}
	// Persist a reservation before performing any external write. Unknown
	// outcomes, including a crash after acceptance, are never retried.
	c.State, c.Reason = "review", "Clarification delivery is unconfirmed. Check the conversation in Zendesk."
	c.Questions = append(slices.Clone(c.Questions), Question{Body: c.Draft, Status: "unconfirmed", ReplyID: c.replyID, At: time.Now().UTC()})
	if err := f.save(c); err != nil {
		return Case{}, err
	}
	err = f.source.ask(ctx, t, c.Draft)
	c.Questions = slices.Clone(c.Questions)
	last := &c.Questions[len(c.Questions)-1]
	switch {
	case err == nil:
		last.Status = "sent"
		c.State, c.Reason = "waiting", "Clarification sent. Waiting for the requester."
	case errors.Is(err, ErrChanged):
		last.Status = "conflict"
		c.Reason = "The ticket changed before the question could be sent. Refresh to reassess."
	}
	return c, f.save(c)
}

func (f *Flow) Pause(id int64, paused bool) (Case, error) {
	if err := f.begin(); err != nil {
		return Case{}, err
	}
	defer f.op.Unlock()
	c, ok := f.store.get(id)
	if !ok {
		return Case{}, ErrNotFound
	}
	if c.RunID != "" {
		return Case{}, ErrBlocked
	}
	c.Paused, c.Plan = paused, ""
	c.State, c.Reason = "review", "Refresh the ticket to reassess the next action."
	return c, f.save(c)
}

// Launch reserves the shared workspace slot and calls reserve before any
// external investigation starts. The reservation consumes this case's one run.
type Launch func(casefile.Case, func() error) error

func (f *Flow) Approve(ctx context.Context, id int64, plan string, launch Launch) (Case, error) {
	if err := f.begin(); err != nil {
		return Case{}, err
	}
	defer f.op.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()
	previous, ok := f.store.get(id)
	if !ok {
		return Case{}, ErrNotFound
	}
	if previous.Paused || previous.RunID != "" || plan == "" || previous.Plan != plan || time.Now().After(previous.PlanExpires) {
		return Case{}, ErrBlocked
	}
	c, t, input, err := f.assess(ctx, id, previous)
	if err != nil {
		return Case{}, err
	}
	if c.State != "approval" || c.Plan != plan {
		if c.State == "ask" {
			c.State, c.Reason = "review", "The ticket changed. Refresh to apply the clarification policy."
		}
		if err := f.save(c); err != nil {
			return Case{}, err
		}
		return Case{}, ErrChanged
	}
	if err := f.source.confirm(ctx, t); err != nil {
		previous.Plan, previous.State, previous.Reason = "", "review", "Ticket freshness could not be verified. Refresh before approving a run."
		if saveErr := f.save(previous); saveErr != nil {
			return Case{}, saveErr
		}
		return Case{}, err
	}
	if err := ctx.Err(); err != nil {
		return Case{}, err
	}
	now := time.Now().UTC()
	if !now.Before(previous.PlanExpires) {
		return Case{}, ErrBlocked
	}
	input.Source["approval_plan"] = plan
	input.Source["approved_at"] = now.Format(time.RFC3339Nano)
	c.RunID, c.ApprovedAt = input.ID, now
	c.State, c.Reason = "launched", "The operator approved this evidence. Open the investigation to see its status."
	err = launch(input, func() error { return f.save(c) })
	return c, err
}

func (f *Flow) assess(ctx context.Context, id int64, previous Case) (Case, ticket, casefile.Case, error) {
	s, err := f.source.read(ctx, id)
	if err != nil {
		return Case{}, ticket{}, casefile.Case{}, err
	}
	t := s.Ticket
	c := previous
	c.TicketID, c.Account, c.CheckedAt = id, f.source.account(), time.Now().UTC()
	c.Title = report.Redact(bounded(t.Subject, 500), f.currentSecrets()...).(string)
	c.State, c.Reason, c.Draft, c.Plan = "excluded", f.settings.Policy.exclusion(t), "", ""
	c.PlanExpires = time.Time{}
	c.Facts, c.Files = nil, nil
	c.Revision = digest(struct {
		Account  string
		Ticket   ticket
		Policy   Policy
		Comments []comment
	}{c.Account, t, f.settings.Policy, s.Comments})
	if previous.Paused || previous.RunID != "" {
		return previous, t, casefile.Case{}, nil
	}
	if c.Reason != "" {
		return c, t, casefile.Case{}, nil
	}
	if f.runtime.Template != "triagelab-python" {
		c.State, c.Reason = "review", "This procedure requires the triagelab-python sandbox."
		return c, t, casefile.Case{}, nil
	}

	fields := []struct{ name, question string }{
		{"Python version", "Python version: Which Python version runs the affected script?"},
		{"Expected", "Expected: What timeout or behavior did you expect?"},
		{"Actual", "Actual: What happened instead, including the exact error if available?"},
	}
	facts := make(map[string]Fact)
	var files []attachment
	var fileCommentID int64
	var latestPublic comment
	var latestRequester comment
	for _, m := range s.Comments {
		if !m.Public || m.ID <= 0 {
			continue
		}
		latestPublic = m
		if m.Author != t.Requester {
			continue
		}
		latestRequester = m
		for _, line := range strings.Split(m.Body, "\n") {
			key, value, found := strings.Cut(strings.TrimSpace(line), ":")
			if !found {
				continue
			}
			for _, field := range fields {
				if strings.EqualFold(key, field.name) {
					value = strings.TrimSpace(value)
					if value == "" || len(value) > 1000 {
						delete(facts, field.name)
						break
					}
					facts[field.name] = Fact{Label: field.name, Value: value, CommentID: strconv.FormatInt(m.ID, 10)}
					break
				}
			}
		}
		// Use one attachment set from one message. Do not mix incompatible
		// versions of a script and its configuration across replies.
		if slices.ContainsFunc(m.Files, func(a attachment) bool { return strings.HasSuffix(reproductionName(a.Name), ".py") }) {
			files, fileCommentID = m.Files, m.ID
		}
	}
	var missing []string
	var lines []string
	for _, field := range fields {
		if fact, ok := facts[field.name]; ok {
			lines = append(lines, fact.Label+": "+fact.Value+" (requester comment "+fact.CommentID+")")
			fact.Value = report.Redact(fact.Value, f.currentSecrets()...).(string)
			c.Facts = append(c.Facts, fact)
		} else {
			missing = append(missing, field.question)
		}
	}
	if fact, ok := facts["Python version"]; ok && !pythonVersion.MatchString(fact.Value) {
		c.State, c.Reason = "review", "This procedure supports Python 3.12. Review the reported version in Zendesk."
		return c, t, casefile.Case{}, nil
	}
	if len(files) == 0 {
		missing = append(missing, "Please attach the smallest Python script (.py) that shows the timeout, along with any required .json configuration. Remove secrets and customer data.")
	}
	if len(missing) != 0 {
		c.Draft = "To help us reproduce the timeout, please reply with the following:\n\n- " + strings.Join(missing, "\n- ")
		c.State, c.Reason = "review", "More information is needed. Review the suggested clarification."
		c.replyID = latestRequester.ID
		for _, q := range c.Questions {
			if q.Status == "unconfirmed" {
				c.Reason = "A previous clarification may have been sent. Check the conversation in Zendesk before contacting the requester."
				return c, t, casefile.Case{}, nil
			}
			if q.Status == "sent" && latestRequester.ID == q.ReplyID {
				if c.CheckedAt.Sub(q.At) >= clarificationWait {
					c.Reason = "The clarification has been unanswered for seven days. Review the ticket in Zendesk."
				} else {
					c.State, c.Reason = "waiting", "Clarification sent. Waiting for the requester."
				}
				return c, t, casefile.Case{}, nil
			}
			if q.Status == "sent" && q.Body == c.Draft {
				c.Reason = "The requester replied, but the same information is still missing. Review the reply before asking again."
				return c, t, casefile.Case{}, nil
			}
		}
		if f.settings.Policy.AutoQuestions && len(c.Questions) < f.settings.Policy.MaxQuestions &&
			latestRequester.ID > 0 && latestPublic.ID == latestRequester.ID {
			c.State, c.Reason = "ask", "The policy allows sending this clarification to the requester."
		}
		return c, t, casefile.Case{}, nil
	}
	input, err := f.prepare(ctx, lines, files)
	if err != nil {
		c.State, c.Reason = "review", err.Error()
		return c, t, casefile.Case{}, nil
	}
	input.Source = map[string]any{"kind": "zendesk", "ticket_id": strconv.FormatInt(id, 10),
		"url": c.Account + "/agent/tickets/" + strconv.FormatInt(id, 10), "procedure": procedure}
	input.Environment = map[string]any{"python_version": facts["Python version"].Value}
	for _, fact := range c.Facts {
		input.Evidence = append(input.Evidence, fact.Label+" from Zendesk public requester comment "+fact.CommentID)
	}
	input.Evidence = append(input.Evidence, "Reproduction files from Zendesk public requester comment "+strconv.FormatInt(fileCommentID, 10))
	c.Files = slices.Clone(input.Attachments)
	for i := range c.Files {
		c.Files[i].Content = nil
	}
	c.Plan = digest(struct {
		Procedure, Account, Description    string
		TicketID, Requester, FileCommentID int64
		Policy                             Policy
		Runtime                            investigation.Config
		Files                              []casefile.Attachment
	}{procedure, c.Account, input.Description, id, t.Requester, fileCommentID, f.settings.Policy, f.runtime, c.Files})
	c.PlanExpires = c.CheckedAt.Add(approvalLifetime)
	c.State, c.Reason = "approval", "Evidence is ready. Approve the proposed sandbox run."
	return c, t, input, nil
}

func (f *Flow) prepare(ctx context.Context, lines []string, files []attachment) (casefile.Case, error) {
	if len(files) > 4 {
		return casefile.Case{}, errors.New("Select a reproduction with at most four supported files in Zendesk.")
	}
	dir, err := os.MkdirTemp(f.store.dir, ".evidence-")
	if err != nil {
		return casefile.Case{}, errors.New("Could not prepare ticket evidence.")
	}
	defer os.RemoveAll(dir)
	seen := make(map[string]bool)
	for _, a := range files {
		name := reproductionName(a.Name)
		if !fileName.MatchString(name) || a.Size <= 0 || a.Size > casefile.MaxFileBytes || seen[name] {
			return casefile.Case{}, errors.New("Attachments must be unique .py, .json, .txt or .log files, each at most 1 MiB.")
		}
		seen[name] = true
	}
	for _, a := range files {
		data, err := f.source.file(ctx, a)
		if err != nil {
			return casefile.Case{}, errors.New("Could not verify the attached reproduction files. Review the attachments in Zendesk.")
		}
		if f.hasSecret(data) {
			return casefile.Case{}, errors.New("An attachment contains a configured credential.")
		}
		if err := os.WriteFile(filepath.Join(dir, reproductionName(a.Name)), data, 0600); err != nil {
			return casefile.Case{}, errors.New("Could not prepare ticket evidence.")
		}
	}
	desc := "Investigate a Python timeout reported in Zendesk. Use the supplied reproduction files.\n" + strings.Join(lines, "\n")
	if f.hasSecret([]byte(desc)) {
		return casefile.Case{}, errors.New("Ticket evidence contains a configured credential.")
	}
	c, err := casefile.Ingest(desc, dir, nil, nil)
	if err != nil {
		return casefile.Case{}, errors.New("The selected evidence failed ingestion checks. Review the ticket for secrets or unsafe files.")
	}
	return c, nil
}

func reproductionName(name string) string {
	if strings.HasSuffix(name, ".py.txt") || strings.HasSuffix(name, ".json.txt") {
		return strings.TrimSuffix(name, ".txt")
	}
	return name
}

func (f *Flow) hasSecret(data []byte) bool {
	for _, secret := range f.currentSecrets() {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			return true
		}
	}
	return false
}

func bounded(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], string(utf8.RuneError))
}

func digest(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("unserializable ticket policy state: %T", value))
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
