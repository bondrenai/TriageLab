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
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/report"
)

const (
	procedure         = "ticket-evidence-v1"
	approvalLifetime  = 15 * time.Minute
	clarificationWait = 7 * 24 * time.Hour
)

var (
	ErrBusy     = errors.New("a ticket action is already in progress; try again shortly")
	ErrNotFound = errors.New("ticket case not found")
	ErrBlocked  = errors.New("ticket is not ready for this action")
	ErrStorage  = errors.New("ticket state could not be saved; restart the service before trying again")
	fileName    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,100}\.(py|json|log|txt)$`)
)

type Fact struct {
	Label     string `json:"label"`
	Value     string `json:"value"`
	CommentID string `json:"comment_id"`
	URL       string `json:"url,omitempty"`
}

type Question struct {
	Body        string    `json:"body"`
	Status      string    `json:"status"`
	ReplyID     int64     `json:"reply_id"`
	RequesterID int64     `json:"requester_id,omitempty"`
	CommentID   int64     `json:"comment_id,string,omitempty"`
	At          time.Time `json:"at"`
}

type Case struct {
	TicketID          int64                 `json:"ticket_id,string"`
	Account           string                `json:"account"`
	Source            string                `json:"source,omitempty"`
	URL               string                `json:"url,omitempty"`
	Title             string                `json:"title"`
	State             string                `json:"state"`
	Reason            string                `json:"reason"`
	Facts             []Fact                `json:"facts,omitempty"`
	Files             []casefile.Attachment `json:"files,omitempty"`
	Draft             string                `json:"draft,omitempty"`
	Questions         []Question            `json:"questions,omitempty"`
	Plan              string                `json:"plan,omitempty"`
	PlanExpires       time.Time             `json:"plan_expires,omitempty"`
	CheckedAt         time.Time             `json:"checked_at"`
	RunID             string                `json:"run_id,omitempty"`
	ApprovedAt        time.Time             `json:"approved_at,omitempty"`
	Paused            bool                  `json:"paused"`
	Revision          string                `json:"revision"`
	Agent             string                `json:"agent,omitempty"`
	Sandbox           string                `json:"sandbox,omitempty"`
	Matches           []RouteRule           `json:"matches,omitempty"`
	AgentChoices      []string              `json:"agent_choices,omitempty"`
	MatchedTag        string                `json:"matched_tag,omitempty"`
	Missing           []string              `json:"missing,omitempty"`
	SelectedAgent     string                `json:"selected_agent,omitempty"`
	SelectedFor       string                `json:"selected_for,omitempty"`
	ProcedureRevision string                `json:"procedure_revision,omitempty"`

	Requester             *Requester    `json:"requester,omitempty"`
	ClarificationApproval string        `json:"clarification_approval,omitempty"`
	Note                  *NoteDelivery `json:"note,omitempty"`

	replyID                int64
	clarificationRecipient string
	target                 RunTarget
}

type Settings struct {
	Policy                Policy `json:"policy"`
	InternalNotes         bool   `json:"internal_notes"`
	InvestigationComments bool   `json:"investigation_comments"`
	PollSeconds           int    `json:"poll_seconds"`
	MaxExperiments        int    `json:"max_experiments"`
	TimeoutSeconds        int    `json:"timeout_seconds"`
	Template              string `json:"template"`
	ClarificationLimit    int    `json:"clarification_limit"`
}

type Flow struct {
	source         source
	provider       string
	oauth          *zendeskOAuth
	store          *store
	settings       Settings
	runtime        investigation.Config
	secrets        []string
	op             sync.Mutex
	broken         bool
	closed         bool
	cancel         context.CancelFunc
	done           chan struct{}
	savedProcedure atomic.Pointer[ProcedureVersion]
	// ResolveTarget is configured before Start and returns the exact run target
	// to put under approval. It performs no investigation or model call.
	ResolveTarget func(context.Context, string, string) (RunTarget, error)
}

func Open(config Config, dir string, runtime investigation.Config, secrets ...string) (*Flow, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if config.provider() == "github" {
		var token string
		if config.TokenEnv != "" {
			token = os.Getenv(config.TokenEnv)
			if token == "" || !validOAuthToken(token) {
				return nil, errors.New("the configured GitHub token environment variable is missing or invalid")
			}
		}
		g := newGitHub(config.Repository, token)
		g.writable = config.Policy.AutoQuestions || config.InvestigationComments
		s, err := openStore(dir, g.account())
		if err != nil {
			g.client.CloseIdleConnections()
			return nil, err
		}
		flow := newFlow(config, g, s, runtime, append(slices.Clone(secrets), token))
		return flow, flow.checkSavedProcedure()
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
	return flow, flow.checkSavedProcedure()
}

func (f *Flow) checkSavedProcedure() error {
	if saved := f.savedProcedure.Load(); saved != nil {
		if err := f.validateProcedureSet(*saved); err != nil {
			_ = f.Close()
			return err
		}
	}
	return nil
}

func (f *Flow) Provider() string { return f.provider }
func (f *Flow) Account() string  { return f.source.account() }
func (f *Flow) OAuthStatus() OAuthStatus {
	if f.oauth == nil {
		return OAuthStatus{}
	}
	return f.oauth.status()
}
func (f *Flow) BeginOAuth(host string) (string, string, error) {
	if f.oauth == nil {
		return "", "", ErrBlocked
	}
	return f.oauth.begin(host)
}
func (f *Flow) FinishOAuth(ctx context.Context, host, state, browser, code string, denied bool) error {
	if f.oauth == nil {
		return ErrBlocked
	}
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
	f := &Flow{source: source, provider: config.provider(), store: store, runtime: runtime, secrets: secrets, settings: Settings{
		Policy: config.Policy, PollSeconds: config.PollSeconds, MaxExperiments: runtime.MaxExperiments,
		InternalNotes:         config.InternalNotes,
		InvestigationComments: config.InvestigationComments,
		TimeoutSeconds:        runtime.InvestigationTimeout, Template: runtime.Template,
	}}
	if config.Policy.AutoQuestions {
		f.settings.ClarificationLimit = config.Policy.MaxQuestions
	}
	f.savedProcedure.Store(store.procedure)
	return f
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
	if g, ok := f.source.(*githubSource); ok {
		g.client.CloseIdleConnections()
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
	return f.checkTicket(ctx, id, trackTicket, revision)
}

func (f *Flow) Refresh(ctx context.Context, id int64) (Case, error) {
	return f.checkTicket(ctx, id, refreshTicket, "")
}

func (f *Flow) Clarify(ctx context.Context, id int64, revision string) (Case, error) {
	return f.checkTicket(ctx, id, clarifyTicket, revision)
}

type ticketAction uint8

const (
	refreshTicket ticketAction = iota
	trackTicket
	clarifyTicket
)

func (f *Flow) checkTicket(ctx context.Context, id int64, action ticketAction, revision string) (Case, error) {
	if err := f.begin(); err != nil {
		return Case{}, err
	}
	defer f.op.Unlock()
	if action == clarifyTicket && !f.settings.Policy.AutoQuestions {
		return Case{}, ErrBlocked
	}
	ctx, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()
	previous, tracked := f.store.get(id)
	enroll := action == trackTicket
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
		if action == clarifyTicket {
			return Case{}, ErrBlocked
		}
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
	if action != refreshTicket && revision != c.Revision {
		if tracked {
			if err := f.save(c); err != nil {
				return Case{}, err
			}
		}
		return Case{}, ErrChanged
	}
	if action == clarifyTicket && c.State != "ask" {
		return Case{}, ErrBlocked
	}
	if c.State != "ask" || enroll || (action == refreshTicket && c.ClarificationApproval == "") {
		return c, f.save(c)
	}
	// Persist a reservation before performing any external write. Unknown
	// outcomes, including a crash after acceptance, are never retried.
	c.State, c.Reason = "review", "Clarification delivery is unconfirmed. Check the conversation at the source."
	c.Questions = append(slices.Clone(c.Questions), Question{Body: c.Draft, Status: "unconfirmed", ReplyID: c.replyID, RequesterID: t.Requester, At: time.Now().UTC()})
	if err := f.save(c); err != nil {
		return Case{}, err
	}
	commentID, err := f.source.postComment(ctx, t, c.Draft, true)
	c.Questions = slices.Clone(c.Questions)
	last := &c.Questions[len(c.Questions)-1]
	switch {
	case err == nil:
		last.Status = "sent"
		last.CommentID = commentID
		c.ClarificationApproval = c.clarificationRecipient
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
type Launch func(casefile.Case, RunTarget, func() error) error

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
	input.Source["ticket_agent"] = c.Agent
	if len(c.Matches) > 0 {
		input.Source["routing_matches"] = c.Matches
	}
	c.RunID, c.ApprovedAt = input.ID, now
	c.State, c.Reason = "launched", "The operator approved this evidence. Open the investigation to see its status."
	err = launch(input, c.target, func() error { return f.save(c) })
	return c, err
}

func (f *Flow) assess(ctx context.Context, id int64, previous Case) (Case, ticket, casefile.Case, error) {
	return f.assessProcedure(ctx, id, previous, f.Procedure())
}

func (f *Flow) assessProcedure(ctx context.Context, id int64, previous Case, version ProcedureVersion) (Case, ticket, casefile.Case, error) {
	s, err := f.source.read(ctx, id)
	if err != nil {
		return Case{}, ticket{}, casefile.Case{}, err
	}
	t := s.Ticket
	p, exclusion := version.match(t)
	c := previous
	c.TicketID, c.Account, c.CheckedAt = id, f.source.account(), time.Now().UTC()
	c.Source, c.URL = f.provider, t.URL
	if c.URL == "" {
		c.URL = c.Account + "/agent/tickets/" + strconv.FormatInt(id, 10)
	}
	secrets := f.currentSecrets()
	c.Title, _ = report.Redacted(bounded(t.Subject, 500), secrets...)
	profile := t.RequesterProfile
	profile.Name = report.Redact(bounded(profile.Name, 200), secrets...).(string)
	profile.Email = report.Redact(bounded(profile.Email, 320), secrets...).(string)
	c.Requester = &profile
	c.clarificationRecipient = digest(struct {
		Requester Requester
		Policy    Policy
		Opening   string
		Evidence  EvidenceRules
	}{t.RequesterProfile, p.Policy, p.Clarification, p.Evidence})
	if exclusion != "" || c.ClarificationApproval != c.clarificationRecipient {
		c.ClarificationApproval = ""
	}
	c.State, c.Reason, c.Draft, c.Plan = "excluded", exclusion, "", ""
	c.PlanExpires = time.Time{}
	c.Facts, c.Files = nil, nil
	c.Matches, c.AgentChoices, c.Missing = nil, nil, nil
	c.Agent, c.Sandbox = "", ""
	c.MatchedTag = ""
	c.ProcedureRevision = version.Revision
	c.Revision = digest(struct {
		Account   string
		Ticket    ticket
		Requester Requester
		Procedure Procedure
		Comments  []comment
	}{c.Account, t, t.RequesterProfile, p, s.Comments})
	if previous.Paused || previous.RunID != "" {
		return previous, t, casefile.Case{}, nil
	}
	if c.Reason != "" {
		c.SelectedAgent, c.SelectedFor = "", ""
		return c, t, casefile.Case{}, nil
	}
	c.MatchedTag = p.Policy.Tag
	c.route(p, t.Tags)
	if c.SelectedAgent != "" {
		c.Revision = digest([]string{c.Revision, c.SelectedAgent})
	}

	facts := make(map[string]Fact)
	var redactions []string
	credentialInFacts := false
	var replies []comment
	fieldPatterns := make(map[string]*regexp.Regexp)
	for _, rule := range p.Evidence {
		if rule.Pattern != "" {
			fieldPatterns[rule.Match] = regexp.MustCompile(rule.Pattern)
		}
	}
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
		// A GitHub issue can be authored by the same account that posts our
		// clarifications. A confirmed outgoing comment is not a requester reply.
		if f.provider == "github" && slices.ContainsFunc(c.Questions, func(q Question) bool {
			return q.Status == "sent" && q.CommentID > 0 && m.ID == q.CommentID &&
				m.URL == c.URL+"#issuecomment-"+strconv.FormatInt(q.CommentID, 10)
		}) {
			continue
		}
		latestRequester = m
		replies = append(replies, m)
		for _, line := range strings.Split(m.Body, "\n") {
			key, value, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			key = strings.TrimSpace(key)
			for _, rule := range p.Evidence {
				if rule.Kind != "field" || !strings.EqualFold(key, rule.Match) {
					continue
				}
				value = strings.TrimSpace(value)
				// A configured credential in requester prose is an incident
				// to review, not text to redact and run.
				if f.hasSecret([]byte(value)) {
					credentialInFacts = true
				}
				pattern := fieldPatterns[rule.Match]
				if value == "" || len(value) > 1000 || (pattern != nil && !pattern.MatchString(value)) {
					delete(facts, rule.Match)
					break
				}
				value, applied := report.Redacted(value, secrets...)
				redactions = append(redactions, applied...)
				facts[rule.Match] = Fact{Label: rule.Match, Value: value, CommentID: strconv.FormatInt(m.ID, 10), URL: m.URL}
				break
			}
		}
	}
	selectedFiles := p.Evidence.selectFiles(replies)
	var missing, lines []string
	for _, rule := range p.Evidence {
		switch rule.Kind {
		case "field":
			if fact, ok := facts[rule.Match]; ok {
				lines = append(lines, fact.Label+": "+fact.Value+" (requester comment "+fact.CommentID+")")
				c.Facts = append(c.Facts, fact)
			} else if rule.Required {
				missing = append(missing, rule.Question)
			}
		case "attachment":
			if rule.Required && !slices.ContainsFunc(selectedFiles, func(f evidenceFile) bool { return rule.matchesFile(f.attachment) }) {
				missing = append(missing, rule.Question)
			}
		}
	}
	c.Missing = missing
	if len(missing) != 0 {
		c.Draft = "- " + strings.Join(missing, "\n- ")
		if p.Clarification != "" {
			c.Draft = p.Clarification + "\n\n" + c.Draft
		}
		c.Revision = digest(struct{ Snapshot, Draft string }{c.Revision, c.Draft})
		c.State, c.Reason = "review", "More information is needed. Review the suggested clarification."
		c.replyID = latestRequester.ID
		for _, q := range c.Questions {
			if q.Status == "unconfirmed" {
				c.Reason = "A previous clarification may have been sent. Check the conversation at the source before contacting the requester."
				return c, t, casefile.Case{}, nil
			}
			if q.RequesterID != 0 && q.RequesterID != t.Requester {
				continue
			}
			if q.Status == "sent" && latestRequester.ID == q.ReplyID {
				if c.CheckedAt.Sub(q.At) >= clarificationWait {
					c.Reason = "The clarification has been unanswered for seven days. Review the conversation at the source."
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
		if p.Policy.AutoQuestions && len(c.Questions) < p.Policy.MaxQuestions &&
			latestRequester.ID > 0 && latestPublic.ID == latestRequester.ID {
			if findings := sanitizeOutput(c.Draft, secrets, outboundTerms(f.store.list(), id, s.Comments)); len(findings) > 0 {
				c.State, c.Reason = "review", "The suggested clarification was held for review because "+strings.Join(findings, " and ")+"."
				return c, t, casefile.Case{}, nil
			}
			c.State, c.Reason = "ask", "Review the requester and draft before approving clarifications."
			if c.ClarificationApproval != "" {
				c.Reason = "The approved policy allows this follow-up clarification."
			}
		}
		return c, t, casefile.Case{}, nil
	}
	if credentialInFacts {
		c.State, c.Reason = "review", "Ticket evidence contains a configured credential."
		return c, t, casefile.Case{}, nil
	}
	var files []attachment
	var fileEvidence []string
	for _, fsrc := range selectedFiles {
		files = append(files, fsrc.attachment)
		name := reproductionName(fsrc.Name)
		fileEvidence = append(fileEvidence, name+" from "+f.commentReference(strconv.FormatInt(fsrc.commentID, 10), fsrc.commentURL))
		if f.provider == "github" {
			fileEvidence = append(fileEvidence, name+" attached at "+fsrc.URL)
		}
	}
	input, err := f.prepare(ctx, c.Title, lines, files)
	if err != nil {
		c.State, c.Reason = "review", err.Error()
		return c, t, casefile.Case{}, nil
	}
	input.Source = map[string]any{"kind": f.provider, "ticket_id": strconv.FormatInt(id, 10),
		"url": c.URL, "procedure": procedure, "ticket_procedure_tag": p.Policy.Tag}
	if f.provider == "github" {
		input.Source["repository"] = strings.TrimPrefix(c.Account, "https://github.com/")
		input.Source["issue_number"] = strconv.FormatInt(id, 10)
	}
	if len(redactions) > 0 {
		slices.Sort(redactions)
		input.Source["input_redactions"] = slices.Compact(redactions)
	}
	input.Environment = map[string]any{}
	for _, fact := range c.Facts {
		if strings.EqualFold(fact.Label, "Python version") {
			input.Environment["python_version"] = fact.Value
		}
		input.Evidence = append(input.Evidence, fact.Label+" from "+f.commentReference(fact.CommentID, fact.URL))
	}
	input.Evidence = append(input.Evidence, fileEvidence...)
	c.Files = slices.Clone(input.Attachments)
	for i := range c.Files {
		c.Files[i].Content = nil
	}
	if c.Agent == "" {
		c.State, c.Reason = "review", "More than one agent matches. Choose which agent should investigate."
		return c, t, casefile.Case{}, nil
	}
	c.target = RunTarget{Agent: c.Agent, Sandbox: c.Sandbox}
	if f.ResolveTarget != nil {
		c.target, err = f.ResolveTarget(ctx, c.Agent, c.Sandbox)
	} else if c.Agent != f.runtime.Agent || c.Sandbox != "triagelab-python" {
		err = ErrBlocked
	}
	if err != nil {
		c.State, c.Reason = "review", "The selected agent or sandbox is unavailable. Check deployment status and refresh the case."
		return c, t, casefile.Case{}, nil
	}
	c.Plan = digest(struct {
		Procedure, Account, Description string
		TicketID, Requester             int64
		Settings                        Procedure
		Runtime                         investigation.Config
		Target                          RunTarget
		Matches                         []RouteRule
		Files                           []casefile.Attachment
		FileEvidence                    []string
	}{procedure, c.Account, input.Description, id, t.Requester, p, f.runtime, c.target, c.Matches, c.Files, fileEvidence})
	if f.provider == "github" {
		// Comment edits/deletions and changed attachment locations invalidate
		// approval even when the selected file bytes remain identical.
		c.Plan = digest([]string{c.Plan, c.Revision})
	}
	c.PlanExpires = c.CheckedAt.Add(approvalLifetime)
	c.State, c.Reason = "approval", "Evidence is ready. Approve the proposed sandbox run."
	return c, t, input, nil
}

func (f *Flow) commentReference(id, url string) string {
	if f.provider == "github" {
		return "GitHub issue author at " + url
	}
	return "Zendesk public requester comment " + id
}

func (f *Flow) prepare(ctx context.Context, title string, lines []string, files []attachment) (casefile.Case, error) {
	if len(files) > 4 {
		return casefile.Case{}, errors.New("Select a reproduction with at most four supported files at the source.")
	}
	dir, err := os.MkdirTemp(f.store.dir, ".evidence-")
	if err != nil {
		return casefile.Case{}, errors.New("Could not prepare ticket evidence.")
	}
	defer os.RemoveAll(dir)
	seen := make(map[string]bool)
	for _, a := range files {
		name := reproductionName(a.Name)
		if f.hasSecret([]byte(a.Name)) || f.hasSecret([]byte(a.URL)) {
			return casefile.Case{}, errors.New("An attachment reference contains a configured credential.")
		}
		unknownSize := f.provider == "github" && a.Size == -1
		if !fileName.MatchString(name) || (!unknownSize && a.Size <= 0) || a.Size > casefile.MaxFileBytes || seen[name] {
			return casefile.Case{}, errors.New("Attachments must be unique .py, .json, .txt or .log files, each at most 1 MiB.")
		}
		seen[name] = true
	}
	for _, a := range files {
		data, err := f.source.file(ctx, a)
		if err != nil || len(data) == 0 || len(data) > casefile.MaxFileBytes || (f.provider == "github" && (!utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0)) {
			return casefile.Case{}, errors.New("Could not verify the attached reproduction files. Review the attachments at the source.")
		}
		if f.hasSecret(data) {
			return casefile.Case{}, errors.New("An attachment contains a configured credential.")
		}
		if err := os.WriteFile(filepath.Join(dir, reproductionName(a.Name)), data, 0600); err != nil {
			return casefile.Case{}, errors.New("Could not prepare ticket evidence.")
		}
	}
	provider := "Zendesk"
	if f.provider == "github" {
		provider = "GitHub"
	}
	desc := "Investigate this case reported in " + provider + "."
	if title != "" {
		desc += "\nIssue: " + title
	}
	if len(lines) != 0 {
		desc += "\n" + strings.Join(lines, "\n")
	}
	if f.hasSecret([]byte(desc)) {
		return casefile.Case{}, errors.New("Ticket evidence contains a configured credential.")
	}
	c, err := casefile.Ingest(desc, dir, nil, nil)
	if errors.Is(err, casefile.ErrLocation) {
		// The directory is ours, so this is an environment problem, not the ticket.
		return casefile.Case{}, errors.New("Could not read the prepared ticket evidence. Check that the ticket state directory is accessible.")
	}
	if err != nil {
		return casefile.Case{}, errors.New("The selected evidence failed ingestion checks. Review the ticket for secrets or unsafe files.")
	}
	if f.provider == "github" {
		for i := range c.Attachments {
			for _, a := range files {
				if c.Attachments[i].Path == reproductionName(a.Name) {
					c.Attachments[i].SourceURL = a.URL
					break
				}
			}
		}
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
