package ticketflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
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
	Subdomain     string `json:"subdomain"`
	ClientID      string `json:"client_id"`
	RedirectURI   string `json:"redirect_uri"`
	PollSeconds   int    `json:"poll_seconds"`
	InternalNotes bool   `json:"internal_notes"`
	Policy        Policy `json:"policy"`
}

func LoadConfig(path string) (Config, error) {
	config := Config{RedirectURI: "http://127.0.0.1:8080" + OAuthCallbackPath}
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
	return config, config.validate()
}

func (config Config) validate() error {
	p := config.Policy
	if !subdomain.MatchString(config.Subdomain) || !oauthIdentifier.MatchString(config.ClientID) ||
		(config.PollSeconds != 0 && (config.PollSeconds < 60 || config.PollSeconds > 3600)) ||
		p.Tag == "" || len(p.Tag) > 100 || p.MaxQuestions < 0 || p.MaxQuestions > 3 || (p.AutoQuestions && p.MaxQuestions == 0) ||
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
