package ticketflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/investigation"
)

func TestLinearConfigRequiresWorkspaceTeamAndExplicitWriteCredentials(t *testing.T) {
	valid := Config{Source: "linear", Workspace: "acme", Team: "ENG", TokenEnv: "TRIAGELAB_TEST_LINEAR_TOKEN", Policy: Policy{Tag: "reproduce"}}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"unknown source":             func(c *Config) { c.Source = "notion" },
		"empty workspace":            func(c *Config) { c.Workspace = "" },
		"uppercase workspace":        func(c *Config) { c.Workspace = "Acme" },
		"workspace with slash":       func(c *Config) { c.Workspace = "acme/eng" },
		"workspace URL":              func(c *Config) { c.Workspace = "https://linear.app/acme" },
		"empty team":                 func(c *Config) { c.Team = "" },
		"lowercase team":             func(c *Config) { c.Team = "eng" },
		"team from URL":              func(c *Config) { c.Team = "ENG-42" },
		"long team":                  func(c *Config) { c.Team = "ENGINEERING" },
		"missing token":              func(c *Config) { c.TokenEnv = "" },
		"token value instead of env": func(c *Config) { c.TokenEnv = "lin-api-secret" },
		"missing label":              func(c *Config) { c.Policy.Tag = "" },
		"repository":                 func(c *Config) { c.Repository = "owner/repo" },
		"Zendesk account":            func(c *Config) { c.Subdomain = "example" },
		"Zendesk client":             func(c *Config) { c.ClientID = "triagelab" },
		"Zendesk callback":           func(c *Config) { c.RedirectURI = "http://localhost:8080/oauth/zendesk/callback" },
		"internal notes":             func(c *Config) { c.InternalNotes = true },
		"priority":                   func(c *Config) { c.Policy.Priorities = []string{"high"} },
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
				t.Fatal("accepted invalid Linear configuration")
			}
		})
	}
	valid.Policy.AutoQuestions, valid.Policy.MaxQuestions = true, 2
	valid.InvestigationComments, valid.PollSeconds = true, 60
	if err := valid.validate(); err != nil {
		t.Fatalf("rejected configured Linear write workflow: %v", err)
	}
}

func TestLoadLinearConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linear.json")
	body := `{"source":"linear","workspace":"acme","team":"ENG","token_env":"TRIAGELAB_LINEAR_TOKEN","policy":{"tag":"reproduce"}}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil || config.provider() != "linear" || config.Workspace != "acme" || config.Team != "ENG" {
		t.Fatalf("wrong Linear configuration: %+v %v", config, err)
	}
}

func TestOpenLinearEnablesOnlyConfiguredWrites(t *testing.T) {
	const variable = "TRIAGELAB_TEST_LINEAR_TOKEN"
	t.Setenv(variable, "test-linear-api-key")
	for _, feature := range []string{"none", "clarifications", "investigation comments"} {
		t.Run(feature, func(t *testing.T) {
			config := Config{Source: "linear", Workspace: "acme", Team: "ENG", TokenEnv: variable, Policy: Policy{Tag: "reproduce"}}
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
			if flow.source.(*linearSource).writable != (feature != "none") || flow.Settings().InvestigationComments != config.InvestigationComments {
				t.Fatal("connector write capability does not match explicit configuration")
			}
		})
	}
}

func TestOpenLinearKeepsTokenServerSideAndDoesNotUseOAuth(t *testing.T) {
	config := Config{Source: "linear", Workspace: "acme", Team: "ENG", TokenEnv: "TRIAGELAB_TEST_LINEAR_TOKEN", Policy: Policy{Tag: "reproduce"}}
	for _, token := range []string{"", "secret\ninvalid"} {
		t.Setenv(config.TokenEnv, token)
		if _, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig()); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("missing/malformed token accepted or disclosed: %v", err)
		}
	}
	const token = "test-linear-api-key"
	t.Setenv(config.TokenEnv, token)
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if flow.Provider() != "linear" || flow.Account() != "https://linear.app/acme/ENG" || !flow.hasSecret([]byte(token)) {
		t.Fatal("Linear source identity or secret filtering is missing")
	}
	if _, _, err := flow.BeginOAuth("localhost:8080"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("Linear started Zendesk OAuth: %v", err)
	}
	if flow.OAuthStatus().Connected {
		t.Fatal("Linear reports a Zendesk grant")
	}
	if _, err := flow.Clarify(t.Context(), 42, "revision"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("Linear clarification was not blocked: %v", err)
	}
}

func TestLinearProcedureRejectsZendeskOnlyPolicy(t *testing.T) {
	config := Config{Source: "linear", Workspace: "acme", Team: "ENG", TokenEnv: "TRIAGELAB_TEST_LINEAR_TOKEN",
		Policy: Policy{Tag: "reproduce"}}
	t.Setenv(config.TokenEnv, "test-linear-api-key")
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	version := flow.Procedure()
	version.Procedure.Policy.Priorities = []string{"high"}
	if err := flow.validateProcedureSet(version); err == nil {
		t.Fatal("Linear procedure accepted Zendesk priorities")
	}
}
