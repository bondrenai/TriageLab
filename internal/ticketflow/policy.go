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
	Tag           string   `json:"tag"`
	Priorities    []string `json:"priorities,omitempty"`
	FieldID       int64    `json:"field_id,omitempty"`
	FieldValue    string   `json:"field_value,omitempty"`
	AutoQuestions bool     `json:"auto_questions"`
	MaxQuestions  int      `json:"max_questions"`
}

type Config struct {
	Source                string `json:"source,omitempty"`
	Repository            string `json:"repository,omitempty"`
	TokenEnv              string `json:"token_env,omitempty"`
	Subdomain             string `json:"subdomain"`
	ClientID              string `json:"client_id"`
	RedirectURI           string `json:"redirect_uri"`
	PollSeconds           int    `json:"poll_seconds"`
	InternalNotes         bool   `json:"internal_notes"`
	InvestigationComments bool   `json:"investigation_comments"`
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

func (config Config) validate() error {
	p := config.Policy
	if (config.PollSeconds != 0 && (config.PollSeconds < 60 || config.PollSeconds > 3600)) ||
		p.MaxQuestions < 0 || p.MaxQuestions > 3 || (p.AutoQuestions && p.MaxQuestions == 0) {
		return errors.New("invalid polling interval or clarification attempt limit")
	}
	if config.provider() == "github" {
		if !githubRepository.MatchString(config.Repository) || strings.HasSuffix(config.Repository, "/.") || strings.HasSuffix(config.Repository, "/..") ||
			(config.TokenEnv != "" && !tokenEnvironment.MatchString(config.TokenEnv)) ||
			config.Subdomain != "" || config.ClientID != "" || config.RedirectURI != "" ||
			config.InternalNotes || len(p.Priorities) != 0 || p.FieldID != 0 || p.FieldValue != "" ||
			strings.TrimSpace(p.Tag) == "" || len(p.Tag) > 100 {
			return errors.New("GitHub requires an owner/repository and label policy; Zendesk connection, priority, field and internal-note settings do not apply")
		}
		if (p.AutoQuestions || config.InvestigationComments) && config.TokenEnv == "" {
			return errors.New("GitHub clarification and investigation comments require token_env with Issues write permission")
		}
		return nil
	}
	if config.provider() != "zendesk" || config.Repository != "" || config.TokenEnv != "" || config.InvestigationComments {
		return errors.New("ticket source must be zendesk or github with its own connection settings")
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

func (p Policy) exclusion(t ticket) string {
	if !slices.Contains([]string{"new", "open", "pending", "hold"}, t.Status) {
		return "The ticket is not open for investigation."
	}
	if !slices.Contains(t.Tags, p.Tag) {
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
