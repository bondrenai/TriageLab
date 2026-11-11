package ticketflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/investigation"
)

func TestJiraConfigRequiresSiteEmailAndExplicitWriteCredentials(t *testing.T) {
	valid := Config{Source: "jira", Origin: "https://test-site.atlassian.net", Email: "triagelab@example.com",
		TokenEnv: "TRIAGELAB_TEST_JIRA_TOKEN", Policy: Policy{Tag: "reproduce"}}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"unknown source":             func(c *Config) { c.Source = "notion" },
		"missing origin":             func(c *Config) { c.Origin = "" },
		"non-Atlassian origin":       func(c *Config) { c.Origin = "https://jira.example.com" },
		"origin with path":           func(c *Config) { c.Origin = "https://test-site.atlassian.net/jira" },
		"origin with query":          func(c *Config) { c.Origin = "https://test-site.atlassian.net?x=1" },
		"plain http off loopback":    func(c *Config) { c.Origin = "http://test-site.atlassian.net" },
		"origin with user":           func(c *Config) { c.Origin = "https://token@test-site.atlassian.net" },
		"missing email":              func(c *Config) { c.Email = "" },
		"malformed email":            func(c *Config) { c.Email = "not-an-address" },
		"missing token":              func(c *Config) { c.TokenEnv = "" },
		"token value instead of env": func(c *Config) { c.TokenEnv = "jira-secret" },
		"missing tag":                func(c *Config) { c.Policy.Tag = "" },
		"repository":                 func(c *Config) { c.Repository = "owner/repo" },
		"workspace":                  func(c *Config) { c.Workspace = "acme" },
		"team":                       func(c *Config) { c.Team = "ENG" },
		"Bondren origin":             func(c *Config) { c.Origin = "https://app.bondren.example" },
		"Zendesk account":            func(c *Config) { c.Subdomain = "example" },
		"Zendesk client":             func(c *Config) { c.ClientID = "triagelab" },
		"Zendesk callback":           func(c *Config) { c.RedirectURI = "http://localhost:8080/oauth/zendesk/callback" },
		"investigation comments":     func(c *Config) { c.InvestigationComments = true },
		"priority":                   func(c *Config) { c.Policy.Priorities = []string{"Highest"} },
		"custom field":               func(c *Config) { c.Policy.FieldID = 12; c.Policy.FieldValue = "python" },
		"question limit":             func(c *Config) { c.Policy.MaxQuestions = 4 },
		"missing question limit":     func(c *Config) { c.Policy.AutoQuestions = true },
		"polling too frequent":       func(c *Config) { c.PollSeconds = 59 },
		"polling too slow":           func(c *Config) { c.PollSeconds = 3601 },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			change(&c)
			if err := c.validate(); err == nil {
				t.Fatal("accepted invalid Jira configuration")
			}
		})
	}
	valid.Policy.AutoQuestions, valid.Policy.MaxQuestions = true, 2
	valid.InternalNotes, valid.PollSeconds = true, 60
	if err := valid.validate(); err != nil {
		t.Fatalf("rejected configured Jira write workflow: %v", err)
	}
	loopback := valid
	loopback.Origin = "http://127.0.0.1:5174"
	if err := loopback.validate(); err != nil {
		t.Fatalf("rejected loopback development origin: %v", err)
	}
}

func TestLoadJiraConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jira.json")
	body := `{"source":"jira","origin":"https://test-site.atlassian.net","email":"triagelab@example.com",` +
		`"token_env":"TRIAGELAB_JIRA_TOKEN","policy":{"tag":"reproduce"}}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil || config.provider() != "jira" || config.Origin != "https://test-site.atlassian.net" ||
		config.Email != "triagelab@example.com" {
		t.Fatalf("wrong Jira configuration: %+v %v", config, err)
	}
}

func TestOpenJiraEnablesOnlyConfiguredWrites(t *testing.T) {
	const variable = "TRIAGELAB_TEST_JIRA_TOKEN"
	t.Setenv(variable, "test-jira-api-token")
	for _, feature := range []string{"none", "clarifications", "internal notes"} {
		t.Run(feature, func(t *testing.T) {
			config := Config{Source: "jira", Origin: "https://test-site.atlassian.net", Email: "triagelab@example.com",
				TokenEnv: variable, Policy: Policy{Tag: "reproduce"}}
			switch feature {
			case "clarifications":
				config.Policy.AutoQuestions, config.Policy.MaxQuestions = true, 2
			case "internal notes":
				config.InternalNotes = true
			}
			flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			if flow.source.(*jiraSource).writable != (feature != "none") || flow.Settings().InternalNotes != config.InternalNotes {
				t.Fatal("connector write capability does not match explicit configuration")
			}
		})
	}
}

func TestOpenJiraKeepsTokenServerSideAndDoesNotUseOAuth(t *testing.T) {
	config := Config{Source: "jira", Origin: "https://test-site.atlassian.net", Email: "triagelab@example.com",
		TokenEnv: "TRIAGELAB_TEST_JIRA_TOKEN", Policy: Policy{Tag: "reproduce"}}
	for _, token := range []string{"", "secret\ninvalid"} {
		t.Setenv(config.TokenEnv, token)
		if _, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig()); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("missing/malformed token accepted or disclosed: %v", err)
		}
	}
	const token = "test-jira-api-token"
	t.Setenv(config.TokenEnv, token)
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if flow.Provider() != "jira" || flow.Account() != "https://test-site.atlassian.net" || !flow.hasSecret([]byte(token)) {
		t.Fatal("Jira source identity or secret filtering is missing")
	}
	if _, _, err := flow.BeginOAuth("localhost:8080"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("Jira started Zendesk OAuth: %v", err)
	}
	if flow.OAuthStatus().Connected {
		t.Fatal("Jira reports a Zendesk grant")
	}
	if _, err := flow.Clarify(t.Context(), 10042, "revision"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("Jira clarification was not blocked: %v", err)
	}
}

func TestJiraProcedureRejectsZendeskOnlyPolicy(t *testing.T) {
	config := Config{Source: "jira", Origin: "https://test-site.atlassian.net", Email: "triagelab@example.com",
		TokenEnv: "TRIAGELAB_TEST_JIRA_TOKEN", Policy: Policy{Tag: "reproduce"}}
	t.Setenv(config.TokenEnv, "test-jira-api-token")
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	version := flow.Procedure()
	version.Procedure.Policy.Priorities = []string{"Highest"}
	if err := flow.validateProcedureSet(version); err == nil {
		t.Fatal("Jira procedure accepted Zendesk priorities")
	}
}

func TestResolveTicketAcceptsJiraReferences(t *testing.T) {
	t.Setenv("TRIAGELAB_TEST_JIRA_TOKEN", "test-jira-api-token")
	jira, err := Open(Config{Source: "jira", Origin: "https://test-site.atlassian.net", Email: "triagelab@example.com",
		TokenEnv: "TRIAGELAB_TEST_JIRA_TOKEN", Policy: Policy{Tag: "reproduce"}},
		filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer jira.Close()
	if _, err := jira.ResolveTicket(t.Context(), "OPS-42x"); !errors.Is(err, ErrJiraReference) {
		t.Fatalf("malformed Jira reference produced %v, want ErrJiraReference", err)
	}
	if id, err := jira.ResolveTicket(t.Context(), " 10042 "); err != nil || id != 10042 {
		t.Fatalf("numeric Jira reference resolved to %d: %v", id, err)
	}
}
