package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/bondrenai/TriageLab/internal/agent"
)

const procedureFile = ".procedure"
const defaultClarification = "To help us reproduce the timeout, please reply with the following:"
const MaxProcedureSetBytes = 128 << 10
const maxProcedures = 8

var ErrInvalidProcedure = errors.New("invalid ticket procedure")

type RouteRule struct {
	Tag   string `json:"tag"`
	Agent string `json:"agent"`
}

type Procedure struct {
	Policy        Policy        `json:"policy"`
	Evidence      EvidenceRules `json:"evidence"`
	Clarification string        `json:"clarification"`
	DefaultAgent  string        `json:"default_agent"`
	Sandbox       string        `json:"sandbox"`
	Routes        []RouteRule   `json:"routes"`
}

type ProcedureVersion struct {
	Procedure  Procedure   `json:"procedure"`
	Additional []Procedure `json:"additional_procedures,omitempty"`
	Revision   string      `json:"revision"`
}

type RunTarget struct {
	Agent, Sandbox                 string
	AgentRevision, SandboxRevision string
	Tools                          []string
}

func (f *Flow) Procedure() ProcedureVersion {
	if saved := f.savedProcedure.Load(); saved != nil {
		version := *saved
		version.Procedure = cloneProcedure(version.Procedure)
		version.Additional = slices.Clone(version.Additional)
		for i, p := range version.Additional {
			version.Additional[i] = cloneProcedure(p)
		}
		return version
	}
	p := Procedure{
		Policy: f.settings.Policy, Evidence: defaultEvidence(),
		Clarification: defaultClarification,
		DefaultAgent:  f.runtime.Agent, Sandbox: f.runtime.Template, Routes: []RouteRule{},
	}
	return ProcedureVersion{Procedure: p, Revision: digest(p)}
}

func cloneProcedure(p Procedure) Procedure {
	p.Evidence = slices.Clone(p.Evidence)
	p.Policy.Priorities = slices.Clone(p.Policy.Priorities)
	p.Routes = slices.Clone(p.Routes)
	return p
}

// Additional procedures are checked in priority order. The final procedure
// still needs to match its own admission criteria.
func (v ProcedureVersion) match(provider string, t ticket) (Procedure, string) {
	for _, p := range v.Additional {
		if p.Policy.exclusion(provider, t) == "" {
			return p, ""
		}
	}
	reason := v.Procedure.Policy.exclusion(provider, t)
	if reason != "" && len(v.Additional) > 0 {
		reason = "This case does not match any saved procedure."
	}
	return v.Procedure, reason
}

func (v ProcedureVersion) byTag(tag string) (Procedure, bool) {
	for _, p := range v.Additional {
		if p.Policy.Tag == tag {
			return p, true
		}
	}
	return v.Procedure, v.Procedure.Policy.Tag == tag
}

func (f *Flow) validateProcedureSet(v ProcedureVersion) error {
	if len(v.Additional) >= maxProcedures {
		return ErrInvalidProcedure
	}
	if err := f.validateProcedure(v.Procedure); err != nil {
		return err
	}
	seen := map[string]bool{v.Procedure.Policy.Tag: true}
	for _, p := range v.Additional {
		if seen[p.Policy.Tag] {
			return ErrInvalidProcedure
		}
		if err := f.validateProcedure(p); err != nil {
			return err
		}
		seen[p.Policy.Tag] = true
	}
	return nil
}

func (f *Flow) validateProcedure(p Procedure) error {
	policy := p.Policy
	validText := func(value string, max int) bool {
		return value != "" && len(value) <= max && strings.TrimSpace(value) == value &&
			!strings.ContainsFunc(value, unicode.IsControl) && !f.hasSecret([]byte(value))
	}
	if !validText(policy.Tag, 100) || len(policy.Priorities) > 4 ||
		policy.FieldID < 0 || policy.FieldID > (1<<53)-1 || (policy.FieldID > 0) != (policy.FieldValue != "") ||
		(policy.FieldValue != "" && !validText(policy.FieldValue, 200)) ||
		policy.MaxQuestions < 0 || policy.MaxQuestions > 3 ||
		(policy.AutoQuestions && (!f.settings.Policy.AutoQuestions || policy.MaxQuestions == 0 || policy.MaxQuestions > f.settings.Policy.MaxQuestions)) ||
		(p.Clarification != "" && (!validText(strings.ReplaceAll(p.Clarification, "\n", " "), 500) || f.hasSecret([]byte(p.Clarification)))) ||
		!p.Evidence.valid(validText) || !agent.ValidResourceName(p.DefaultAgent) ||
		!agent.ValidResourceName(p.Sandbox) || f.hasSecret([]byte(p.DefaultAgent)) || f.hasSecret([]byte(p.Sandbox)) ||
		len(p.Routes) > 20 {
		return ErrInvalidProcedure
	}
	if (f.provider == "github" || f.provider == "linear") && (len(policy.Priorities) != 0 || policy.FieldID != 0) {
		return ErrInvalidProcedure
	}
	for _, priority := range policy.Priorities {
		if !slices.Contains([]string{"low", "normal", "high", "urgent"}, priority) {
			return ErrInvalidProcedure
		}
	}
	tags := make(map[string]bool, len(p.Routes))
	for _, route := range p.Routes {
		if !validText(route.Tag, 100) || !agent.ValidResourceName(route.Agent) ||
			f.hasSecret([]byte(route.Agent)) || tags[route.Tag] {
			return ErrInvalidProcedure
		}
		tags[route.Tag] = true
	}
	return nil
}

func (f *Flow) SaveProcedure(p Procedure, revision string) (ProcedureVersion, error) {
	return f.SaveProcedures(ProcedureVersion{Procedure: p, Revision: revision})
}

func (f *Flow) SaveProcedures(version ProcedureVersion) (ProcedureVersion, error) {
	if err := f.begin(); err != nil {
		return ProcedureVersion{}, err
	}
	defer f.op.Unlock()
	current := f.Procedure()
	if version.Revision == "" || version.Revision != current.Revision {
		return ProcedureVersion{}, ErrChanged
	}
	// Legacy single-procedure requests leave the rest of the list intact.
	if version.Additional == nil {
		version.Additional = current.Additional
	}
	if err := f.validateProcedureSet(version); err != nil {
		return ProcedureVersion{}, err
	}
	record := procedureRecord{Account: f.Account(), Procedure: cloneProcedure(version.Procedure),
		Additional: slices.Clone(version.Additional)}
	if record.Procedure.Routes == nil {
		record.Procedure.Routes = []RouteRule{}
	}
	for i, p := range record.Additional {
		record.Additional[i] = cloneProcedure(p)
		if p.Routes == nil {
			record.Additional[i].Routes = []RouteRule{}
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return ProcedureVersion{}, err
	}
	if len(data) > MaxProcedureSetBytes {
		return ProcedureVersion{}, ErrInvalidProcedure
	}
	committed, err := writePrivate(f.store.dir, procedureFile, data)
	if committed {
		saved := record.version()
		f.savedProcedure.Store(&saved)
	}
	if err != nil {
		f.broken = true
		return ProcedureVersion{}, ErrStorage
	}
	return f.Procedure(), nil
}

type procedureRecord struct {
	Account    string      `json:"account"`
	Procedure  Procedure   `json:"procedure"`
	Additional []Procedure `json:"additional_procedures,omitempty"`
}

func (r procedureRecord) version() ProcedureVersion {
	revision := digest(r.Procedure)
	if len(r.Additional) > 0 {
		revision = digest(r)
	}
	return ProcedureVersion{Procedure: r.Procedure, Additional: r.Additional, Revision: revision}
}

func (s *store) loadProcedure(account string) error {
	file, err := os.OpenFile(filepath.Join(s.dir, procedureFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > MaxProcedureSetBytes {
		return errors.New("saved ticket procedures must be in a private regular file of at most 128 KiB")
	}
	var record procedureRecord
	d := json.NewDecoder(io.LimitReader(file, MaxProcedureSetBytes+1))
	d.DisallowUnknownFields()
	if d.Decode(&record) != nil || d.Decode(new(any)) != io.EOF || record.Account != account {
		return errors.New("saved ticket procedure is invalid or belongs to another source")
	}
	version := record.version()
	s.procedure = &version
	return nil
}

func (f *Flow) PreviewProcedure(ctx context.Context, id int64, p Procedure) (Case, error) {
	ctx, finish := traced(ctx, "procedure_preview")
	c, err := f.previewProcedure(ctx, id, p)
	finish(err)
	return c, err
}

func (f *Flow) previewProcedure(ctx context.Context, id int64, p Procedure) (Case, error) {
	if err := f.begin(); err != nil {
		return Case{}, err
	}
	defer f.op.Unlock()
	if err := f.validateProcedure(p); err != nil {
		return Case{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()
	c, _, _, err := f.assessProcedure(ctx, id, Case{}, ProcedureVersion{Procedure: p, Revision: digest(p)})
	// This preview may use an unpublished procedure and cannot enroll or approve.
	c.Plan, c.Revision = "", ""
	return c, err
}

func (c *Case) route(p Procedure, tags []string) {
	for _, rule := range p.Routes {
		if slices.Contains(tags, rule.Tag) {
			c.Matches = append(c.Matches, rule)
			if !slices.Contains(c.AgentChoices, rule.Agent) {
				c.AgentChoices = append(c.AgentChoices, rule.Agent)
			}
		}
	}
	key := routeKey(p, c.Matches)
	if c.SelectedFor != key || !slices.Contains(c.AgentChoices, c.SelectedAgent) {
		c.SelectedAgent, c.SelectedFor = "", ""
	}
	switch len(c.AgentChoices) {
	case 0:
		c.Agent = p.DefaultAgent
	case 1:
		c.Agent = c.AgentChoices[0]
	default:
		c.Agent = c.SelectedAgent
	}
	c.Sandbox = p.Sandbox
}

func routeKey(p Procedure, matches []RouteRule) string {
	return digest(struct {
		Procedure Procedure
		Matches   []RouteRule
	}{p, matches})
}

func (f *Flow) ChooseRoute(ctx context.Context, id int64, revision, agent string) (Case, error) {
	ctx, finish := traced(ctx, "choose_route")
	c, err := f.chooseRoute(ctx, id, revision, agent)
	finish(err)
	return c, err
}

func (f *Flow) chooseRoute(ctx context.Context, id int64, revision, agent string) (Case, error) {
	if err := f.begin(); err != nil {
		return Case{}, err
	}
	defer f.op.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()
	previous, ok := f.store.get(id)
	if !ok || previous.RunID != "" || previous.Paused {
		return Case{}, ErrBlocked
	}
	if revision == "" || previous.Revision != revision {
		return Case{}, ErrChanged
	}
	if len(previous.AgentChoices) < 2 || !slices.Contains(previous.AgentChoices, agent) {
		return Case{}, ErrBlocked
	}
	version := f.Procedure()
	p, ok := version.byTag(previous.MatchedTag)
	if !ok || previous.ProcedureRevision != version.Revision {
		return Case{}, ErrChanged
	}
	previous.SelectedAgent = agent
	previous.SelectedFor = routeKey(p, previous.Matches)
	c, _, _, err := f.assessProcedure(ctx, id, previous, version)
	if err != nil {
		return Case{}, err
	}
	if c.MatchedTag != p.Policy.Tag || c.SelectedAgent != agent || len(c.AgentChoices) < 2 {
		return c, errors.Join(ErrChanged, f.save(c))
	}
	return c, f.save(c)
}
