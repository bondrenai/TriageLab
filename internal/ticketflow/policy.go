package ticketflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
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
	Subdomain   string `json:"subdomain"`
	Email       string `json:"email"`
	TokenEnv    string `json:"token_env"`
	PollSeconds int    `json:"poll_seconds"`
	Policy      Policy `json:"policy"`
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
	p := config.Policy
	if !subdomain.MatchString(config.Subdomain) || strings.TrimSpace(config.Email) == "" || config.TokenEnv == "" ||
		(config.PollSeconds != 0 && (config.PollSeconds < 60 || config.PollSeconds > 3600)) ||
		p.Tag == "" || len(p.Tag) > 100 || p.MaxQuestions < 0 || p.MaxQuestions > 3 || (p.AutoQuestions && p.MaxQuestions == 0) ||
		p.FieldID < 0 || (p.FieldID > 0) != (p.FieldValue != "") {
		return config, errors.New("invalid Zendesk connection, polling interval, or ticket policy")
	}
	for _, priority := range p.Priorities {
		if !slices.Contains([]string{"low", "normal", "high", "urgent"}, priority) {
			return config, errors.New("unknown Zendesk priority in policy")
		}
	}
	return config, nil
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
