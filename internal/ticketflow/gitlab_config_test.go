package ticketflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/investigation"
)

func TestGitLabConfigRequiresProjectAndCredential(t *testing.T) {
	valid := Config{Source: "gitlab", Repository: "group/project", TokenEnv: "TRIAGELAB_TEST_GITLAB_TOKEN",
		Policy: Policy{Tag: "reproduce"}}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"unknown source":             func(c *Config) { c.Source = "sourcehut" },
		"missing project":            func(c *Config) { c.Repository = "" },
		"URL instead of project":     func(c *Config) { c.Repository = "https://gitlab.com/group/project" },
		"single segment":             func(c *Config) { c.Repository = "project" },
		"traversal":                  func(c *Config) { c.Repository = "group/../project" },
		"query":                      func(c *Config) { c.Repository = "group/project?access_token=secret" },
		"missing credential":         func(c *Config) { c.TokenEnv = "" },
		"token value instead of env": func(c *Config) { c.TokenEnv = "Bearer secret" },
		"plain http origin":          func(c *Config) { c.Origin = "http://gitlab.example.com" },
		"origin with path":           func(c *Config) { c.Origin = "https://gitlab.example.com/gitlab" },
		"origin with query":          func(c *Config) { c.Origin = "https://gitlab.example.com/?token=secret" },
		"missing label":              func(c *Config) { c.Policy.Tag = "" },
		"Zendesk account":            func(c *Config) { c.Subdomain = "example" },
		"Zendesk client":             func(c *Config) { c.ClientID = "triagelab" },
		"Zendesk callback":           func(c *Config) { c.RedirectURI = "http://localhost:8080/oauth/zendesk/callback" },
		"Jira email":                 func(c *Config) { c.Email = "triagelab@example.com" },
		"Linear workspace":           func(c *Config) { c.Workspace = "acme" },
		"internal notes":             func(c *Config) { c.InternalNotes = true },
		"priority":                   func(c *Config) { c.Policy.Priorities = []string{"high"} },
		"custom field":               func(c *Config) { c.Policy.FieldID = 12; c.Policy.FieldValue = "python" },
		"polling too frequent":       func(c *Config) { c.PollSeconds = 59 },
		"polling too slow":           func(c *Config) { c.PollSeconds = 3601 },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			change(&c)
			if err := c.validate(); err == nil {
				t.Fatal("accepted invalid GitLab configuration")
			}
		})
	}
	selfManaged := valid
	selfManaged.Origin = "https://gitlab.example.com"
	selfManaged.Policy.AutoQuestions, selfManaged.Policy.MaxQuestions = true, 2
	selfManaged.InvestigationComments, selfManaged.PollSeconds = true, 60
	if err := selfManaged.validate(); err != nil {
		t.Fatalf("rejected configured GitLab write workflow: %v", err)
	}
	loopback := valid
	loopback.Origin = "http://127.0.0.1:8080"
	if err := loopback.validate(); err != nil {
		t.Fatalf("rejected loopback GitLab fixture origin: %v", err)
	}
}

func TestLoadGitLabConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitlab.json")
	body := `{"source":"gitlab","repository":"group/project","origin":"https://gitlab.example.com",` +
		`"token_env":"TRIAGELAB_GITLAB_TOKEN","policy":{"tag":"reproduce"}}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil || config.provider() != "gitlab" || config.Repository != "group/project" ||
		config.Origin != "https://gitlab.example.com" || config.TokenEnv != "TRIAGELAB_GITLAB_TOKEN" {
		t.Fatalf("wrong GitLab configuration: %+v %v", config, err)
	}
	defaulted, err := LoadConfig(writeTemp(t, `{"source":"gitlab","repository":"group/project",`+
		`"token_env":"TRIAGELAB_GITLAB_TOKEN","policy":{"tag":"reproduce"}}`))
	if err != nil || SourceAccount(defaulted) != "https://gitlab.com/group/project" {
		t.Fatalf("GitLab origin default not applied: %+v %v", defaulted, err)
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenGitLabEnablesOnlyConfiguredWrites(t *testing.T) {
	const variable = "TRIAGELAB_TEST_GITLAB_TOKEN"
	t.Setenv(variable, "test-gitlab-api-token")
	for _, feature := range []string{"none", "clarifications", "investigation comments"} {
		t.Run(feature, func(t *testing.T) {
			config := Config{Source: "gitlab", Repository: "group/project", TokenEnv: variable, Policy: Policy{Tag: "reproduce"}}
			switch feature {
			case "clarifications":
				config.Policy.AutoQuestions, config.Policy.MaxQuestions = true, 2
			case "investigation comments":
				config.InvestigationComments = true
			}
			flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			if flow.source.(*gitlabSource).writable != (feature != "none") || flow.Settings().InvestigationComments != config.InvestigationComments {
				t.Fatal("connector write capability does not match explicit configuration")
			}
		})
	}
}

func TestOpenGitLabKeepsTokenServerSideAndDoesNotUseOAuth(t *testing.T) {
	config := Config{Source: "gitlab", Repository: "group/project", TokenEnv: "TRIAGELAB_TEST_GITLAB_TOKEN", Policy: Policy{Tag: "reproduce"}}
	for _, token := range []string{"", "secret\ninvalid"} {
		t.Setenv(config.TokenEnv, token)
		if _, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig()); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("missing/malformed token accepted or disclosed: %v", err)
		}
	}
	const token = "test-gitlab-api-token"
	t.Setenv(config.TokenEnv, token)
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if flow.Provider() != "gitlab" || flow.Account() != "https://gitlab.com/group/project" || !flow.hasSecret([]byte(token)) {
		t.Fatal("GitLab source identity or secret filtering is missing")
	}
	if _, _, err := flow.BeginOAuth("localhost:8080"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("GitLab started Zendesk OAuth: %v", err)
	}
	if flow.OAuthStatus().Connected {
		t.Fatal("GitLab reports a Zendesk grant")
	}
	if _, err := flow.Clarify(t.Context(), 42, "revision"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("GitLab clarification was not blocked: %v", err)
	}
}

func TestGitLabProcedureRejectsZendeskOnlyPolicy(t *testing.T) {
	config := Config{Source: "gitlab", Repository: "group/project", TokenEnv: "TRIAGELAB_TEST_GITLAB_TOKEN",
		Policy: Policy{Tag: "reproduce"}}
	t.Setenv(config.TokenEnv, "test-gitlab-api-token")
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	version := flow.Procedure()
	version.Procedure.Policy.Priorities = []string{"high"}
	if err := flow.validateProcedureSet(version); err == nil {
		t.Fatal("GitLab procedure accepted Zendesk priorities")
	}
}

func TestResolveTicketAcceptsGitLabReferences(t *testing.T) {
	t.Setenv("TRIAGELAB_TEST_GITLAB_TOKEN", "test-gitlab-api-token")
	flow, err := Open(Config{Source: "gitlab", Repository: "group/project", TokenEnv: "TRIAGELAB_TEST_GITLAB_TOKEN",
		Policy: Policy{Tag: "reproduce"}}, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if _, err := flow.ResolveTicket(t.Context(), "OPS-42"); !errors.Is(err, ErrGitLabReference) {
		t.Fatalf("malformed GitLab reference produced %v, want ErrGitLabReference", err)
	}
	for _, reference := range []string{" 42 ", "https://gitlab.com/group/project/-/issues/42#note_5"} {
		if id, err := flow.ResolveTicket(t.Context(), reference); err != nil || id != 42 {
			t.Fatalf("GitLab reference %q resolved to %d: %v", reference, id, err)
		}
	}
}
