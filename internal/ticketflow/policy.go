package ticketflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type Policy struct {
	Tag              string   `json:"tag"`
	Priorities       []string `json:"priorities,omitempty"`
	FieldID          int64    `json:"field_id,omitempty"`
	FieldValue       string   `json:"field_value,omitempty"`
	AutoQuestions    bool     `json:"auto_questions"`
	MaxQuestions     int      `json:"max_questions"`
	MatchCondition   string   `json:"match_condition,omitempty"`
	AutoRunCondition string   `json:"auto_run_condition,omitempty"`
	AutoRuns         bool     `json:"auto_runs,omitempty"`
}

type Config struct {
	Source                string `json:"source,omitempty"`
	Repository            string `json:"repository,omitempty"`
	TokenEnv              string `json:"token_env,omitempty"`
	Workspace             string `json:"workspace,omitempty"`
	Team                  string `json:"team,omitempty"`
	Origin                string `json:"origin,omitempty"`
	Email                 string `json:"email,omitempty"`
	Subdomain             string `json:"subdomain"`
	ClientID              string `json:"client_id"`
	RedirectURI           string `json:"redirect_uri"`
	PollSeconds           int    `json:"poll_seconds"`
	InternalNotes         bool   `json:"internal_notes"`
	InvestigationComments bool   `json:"investigation_comments"`
	MaxAutoRunsPerDay     int    `json:"max_auto_runs_per_day,omitempty"`
	Policy                Policy `json:"policy"`
}

func LoadConfig(path string) (Config, error) {
	var config Config
	f, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return config, errors.New("could not read ticket configuration or it exceeds 16 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&config) != nil || d.Decode(new(any)) != io.EOF {
		return config, errors.New("invalid ticket configuration JSON")
	}
	if config.provider() == "zendesk" && config.RedirectURI == "" {
		config.RedirectURI = "http://127.0.0.1:8080" + OAuthCallbackPath
	}
	return config, config.validate()
}

func (config Config) provider() string {
	if config.Source == "" {
		return "zendesk"
	}
	return config.Source
}

var githubRepository = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)
var tokenEnvironment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var linearWorkspace = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,56}[a-z0-9]$`)
var linearTeamKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,9}$`)
var jiraSiteHost = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}\.atlassian\.net$`)
var jiraAccountEmail = regexp.MustCompile(`^[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9]([A-Za-z0-9-]{0,251}[A-Za-z0-9])?(\.[A-Za-z]{2,24})+$`)

// validBondrenOrigin accepts a bare HTTPS origin for the Bondren API and UI.
// Plain HTTP is allowed only on loopback hosts for local development.
func validBondrenOrigin(origin string) bool {
	if len(origin) > 200 {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.RawFragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	host := strings.TrimSuffix(u.Host, ":"+u.Port())
	if u.Scheme == "https" && host != "" {
		return true
	}
	return u.Scheme == "http" && slices.Contains([]string{"127.0.0.1", "[::1]", "localhost"}, host)
}

// validJiraOrigin accepts one Jira Cloud site at its atlassian.net HTTPS
// origin. Plain HTTP is allowed only on loopback hosts for local development
// fixtures. Self-managed Jira Data Center deployments are a different API
// vintage and are not supported.
func validJiraOrigin(origin string) bool {
	if len(origin) > 200 {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.RawFragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	host := strings.TrimSuffix(u.Host, ":"+u.Port())
	if u.Scheme == "https" && jiraSiteHost.MatchString(host) {
		return true
	}
	return u.Scheme == "http" && slices.Contains([]string{"127.0.0.1", "[::1]", "localhost"}, host)
}

func (config Config) validate() error {
	p := config.Policy
	for _, expression := range []string{p.MatchCondition, p.AutoRunCondition} {
		if _, err := compileTicketCondition(expression); err != nil {
			return err
		}
	}
	if (config.PollSeconds != 0 && (config.PollSeconds < 60 || config.PollSeconds > 3600)) ||
		p.MaxQuestions < 0 || p.MaxQuestions > 3 || (p.AutoQuestions && p.MaxQuestions == 0) {
		return errors.New("invalid polling interval or clarification attempt limit")
	}
	if config.MaxAutoRunsPerDay < 0 {
		return errors.New("max_auto_runs_per_day must be nonnegative")
	}
	if config.provider() == "github" {
		if !githubRepository.MatchString(config.Repository) || strings.HasSuffix(config.Repository, "/.") || strings.HasSuffix(config.Repository, "/..") ||
			(config.TokenEnv != "" && !tokenEnvironment.MatchString(config.TokenEnv)) ||
			config.Workspace != "" || config.Team != "" ||
			config.Subdomain != "" || config.ClientID != "" || config.RedirectURI != "" || config.Origin != "" || config.Email != "" ||
			config.InternalNotes || len(p.Priorities) != 0 || p.FieldID != 0 || p.FieldValue != "" ||
			strings.TrimSpace(p.Tag) == "" || len(p.Tag) > 100 {
			return errors.New("GitHub requires an owner/repository and label policy; Zendesk connection, priority, field and internal-note settings do not apply")
		}
		if (p.AutoQuestions || config.InvestigationComments) && config.TokenEnv == "" {
			return errors.New("GitHub clarification and investigation comments require token_env with Issues write permission")
		}
		return nil
	}
	if config.provider() == "linear" {
		if !linearWorkspace.MatchString(config.Workspace) || !linearTeamKey.MatchString(config.Team) ||
			!tokenEnvironment.MatchString(config.TokenEnv) ||
			config.Repository != "" || config.Subdomain != "" || config.ClientID != "" || config.RedirectURI != "" ||
			config.Origin != "" || config.Email != "" ||
			config.InternalNotes || len(p.Priorities) != 0 || p.FieldID != 0 || p.FieldValue != "" ||
			strings.TrimSpace(p.Tag) == "" || len(p.Tag) > 100 {
			return errors.New("Linear requires a workspace key, an uppercase team key, and token_env with a label policy; Zendesk connection, priority, field and internal-note settings do not apply")
		}
		return nil
	}
	if config.provider() == "bondren" {
		if !validBondrenOrigin(config.Origin) || !tokenEnvironment.MatchString(config.TokenEnv) ||
			config.Repository != "" || config.Workspace != "" || config.Team != "" ||
			config.Subdomain != "" || config.ClientID != "" || config.RedirectURI != "" || config.Email != "" ||
			config.InvestigationComments || len(p.Priorities) != 0 || p.FieldID != 0 || p.FieldValue != "" ||
			strings.TrimSpace(p.Tag) == "" || len(p.Tag) > 100 {
			return errors.New("Bondren requires an HTTPS origin and token_env with a tag policy; Zendesk, GitHub, Linear, priority, field, and public-comment settings do not apply")
		}
		return nil
	}
	if config.provider() == "jira" {
		if !validJiraOrigin(config.Origin) || !tokenEnvironment.MatchString(config.TokenEnv) ||
			!jiraAccountEmail.MatchString(config.Email) ||
			config.Repository != "" || config.Workspace != "" || config.Team != "" ||
			config.Subdomain != "" || config.ClientID != "" || config.RedirectURI != "" ||
			config.InvestigationComments || len(p.Priorities) != 0 || p.FieldID != 0 || p.FieldValue != "" ||
			strings.TrimSpace(p.Tag) == "" || len(p.Tag) > 100 {
			return errors.New("Jira requires an atlassian.net site origin, the Atlassian account email, and token_env with a label policy; Zendesk, GitHub, Linear, and Bondren connection, priority, field, and public-comment settings do not apply")
		}
		return nil
	}
	if config.provider() != "zendesk" || config.Repository != "" || config.TokenEnv != "" || config.Origin != "" ||
		config.Email != "" || config.InvestigationComments {
		return errors.New("ticket source must be zendesk, github, linear, bondren, or jira with its own connection settings")
	}
	if !subdomain.MatchString(config.Subdomain) || !oauthIdentifier.MatchString(config.ClientID) ||
		p.Tag == "" || len(p.Tag) > 100 ||
		p.FieldID < 0 || (p.FieldID > 0) != (p.FieldValue != "") {
		return errors.New("invalid Zendesk OAuth connection, polling interval, or ticket policy")
	}
	for _, priority := range p.Priorities {
		if !slices.Contains([]string{"low", "normal", "high", "urgent"}, priority) {
			return errors.New("unknown Zendesk priority in policy")
		}
	}
	u, err := url.Parse(config.RedirectURI)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Path != OAuthCallbackPath || u.RawPath != "" || !slices.Contains([]string{"127.0.0.1", "[::1]", "localhost"}, strings.TrimSuffix(u.Host, ":"+u.Port())) {
		return errors.New("Zendesk redirect_uri must use an HTTP loopback origin and " + OAuthCallbackPath)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || config.RedirectURI != "http://"+u.Host+OAuthCallbackPath {
		return errors.New("Zendesk redirect_uri must include an explicit loopback port")
	}
	return nil
}

func (p Policy) exclusion(provider string, t ticket) string {
	if !openForInvestigation(provider, t.Status) {
		return "The ticket is not open for investigation."
	}
	if p.MatchCondition == "" && !slices.Contains(t.Tags, p.Tag) {
		return "The ticket does not have the selected tag."
	}
	if len(p.Priorities) != 0 && !slices.Contains(p.Priorities, t.Priority) {
		return "The ticket priority does not match the procedure."
	}
	if p.FieldID != 0 {
		for _, field := range t.Fields {
			if value, ok := field.Value.(string); field.ID == p.FieldID && ok && value == p.FieldValue {
				return ""
			}
		}
		return "The selected custom field does not match the procedure."
	}
	return ""
}

// openForInvestigation reports whether a source status admits investigation.
// Linear workflow states carry a type: backlog, unstarted and started issues
// are open for work; completed and canceled issues are not. Bondren mailbox
// states keep an issue workable until it is closed. Jira statuses map through
// their status category: to-do and in-progress categories are open for work.
func openForInvestigation(provider, status string) bool {
	switch provider {
	case "github":
		return status == "open"
	case "linear":
		return status == "backlog" || status == "unstarted" || status == "started"
	case "bondren":
		return status == "new" || status == "on_you" || status == "on_customer" || status == "on_hold"
	case "jira":
		return status == "new" || status == "indeterminate"
	default:
		return slices.Contains([]string{"new", "open", "pending", "hold"}, status)
	}
}
