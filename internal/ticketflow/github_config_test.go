package ticketflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/investigation"
)

func TestGitHubConfigRequiresRepositoryScopeAndExplicitWriteCredentials(t *testing.T) {
	valid := Config{Source: "github", Repository: "owner/repo", Policy: Policy{Tag: "reproduce"}}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"unknown source":                    func(c *Config) { c.Source = "sourcehut" },
		"URL instead of repository":         func(c *Config) { c.Repository = "https://github.com/owner/repo" },
		"traversal":                         func(c *Config) { c.Repository = "owner/.." },
		"query":                             func(c *Config) { c.Repository = "owner/repo?access_token=secret" },
		"empty repository":                  func(c *Config) { c.Repository = "" },
		"missing label":                     func(c *Config) { c.Policy.Tag = "" },
		"token value instead of env":        func(c *Config) { c.TokenEnv = "Bearer secret" },
		"Zendesk account":                   func(c *Config) { c.Subdomain = "example" },
		"Zendesk client":                    func(c *Config) { c.ClientID = "triagelab" },
		"Zendesk callback":                  func(c *Config) { c.RedirectURI = "http://localhost:8080/oauth/zendesk/callback" },
		"automatic questions without token": func(c *Config) { c.Policy.AutoQuestions = true; c.Policy.MaxQuestions = 2 },
		"comments without token":            func(c *Config) { c.InvestigationComments = true },
		"question limit":                    func(c *Config) { c.Policy.MaxQuestions = 4 },
		"negative question limit":           func(c *Config) { c.Policy.MaxQuestions = -1 },
		"missing question limit":            func(c *Config) { c.Policy.AutoQuestions = true; c.TokenEnv = "GITHUB_TOKEN" },
		"internal notes":                    func(c *Config) { c.InternalNotes = true },
		"priority":                          func(c *Config) { c.Policy.Priorities = []string{"high"} },
		"custom field":                      func(c *Config) { c.Policy.FieldID = 12; c.Policy.FieldValue = "python" },
		"polling too frequent":              func(c *Config) { c.PollSeconds = 59 },
		"polling too slow":                  func(c *Config) { c.PollSeconds = 3601 },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			change(&c)
			if err := c.validate(); err == nil {
				t.Fatal("accepted invalid GitHub configuration")
			}
		})
	}
	valid.TokenEnv = "TRIAGELAB_TEST_GITHUB_TOKEN"
	valid.Policy.AutoQuestions, valid.Policy.MaxQuestions = true, 2
	valid.InvestigationComments, valid.PollSeconds = true, 60
	if err := valid.validate(); err != nil {
		t.Fatalf("rejected configured GitHub write workflow: %v", err)
	}
	zendesk := oauthConfig(false)
	zendesk.InvestigationComments = true
	if err := zendesk.validate(); err == nil {
		t.Fatal("GitHub public comment setting was accepted for Zendesk")
	}
}

func TestLoadTicketConfigDefaultsOnlyZendeskOAuth(t *testing.T) {
	for _, tc := range []struct{ name, body, provider, redirect string }{
		{"github", `{"source":"github","repository":"owner/repo","policy":{"tag":"reproduce"}}`, "github", ""},
		{"zendesk", `{"subdomain":"example","client_id":"triagelab","policy":{"tag":"reproduce"}}`, "zendesk", "http://127.0.0.1:8080" + OAuthCallbackPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			config, err := LoadConfig(path)
			if err != nil || config.provider() != tc.provider || config.RedirectURI != tc.redirect {
				t.Fatalf("wrong source defaults: %+v %v", config, err)
			}
		})
	}
}

func TestOpenGitHubEnablesOnlyConfiguredWrites(t *testing.T) {
	const variable = "TRIAGELAB_TEST_GITHUB_TOKEN"
	t.Setenv(variable, "test-private-github-token")
	for _, feature := range []string{"none", "clarifications", "investigation comments"} {
		t.Run(feature, func(t *testing.T) {
			config := Config{Source: "github", Repository: "owner/repo", TokenEnv: variable, Policy: Policy{Tag: "reproduce"}}
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
			if flow.source.(*githubSource).writable != (feature != "none") || flow.Settings().InvestigationComments != config.InvestigationComments {
				t.Fatal("connector write capability does not match explicit configuration")
			}
		})
	}
}

func TestOpenGitHubKeepsTokenServerSideAndDoesNotUseOAuth(t *testing.T) {
	config := Config{Source: "github", Repository: "owner/repo", TokenEnv: "TRIAGELAB_TEST_GITHUB_TOKEN", Policy: Policy{Tag: "reproduce"}}
	for _, token := range []string{"", "secret\ninvalid"} {
		t.Setenv(config.TokenEnv, token)
		if _, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig()); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("missing/malformed token accepted or disclosed: %v", err)
		}
	}
	const token = "test-private-github-token"
	t.Setenv(config.TokenEnv, token)
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if flow.Provider() != "github" || flow.Account() != "https://github.com/owner/repo" || !flow.hasSecret([]byte(token)) {
		t.Fatal("GitHub source identity or secret filtering is missing")
	}
	if _, _, err := flow.BeginOAuth("localhost:8080"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("GitHub started Zendesk OAuth: %v", err)
	}
	if err := flow.FinishOAuth(t.Context(), "localhost:8080", "", "", "", false); !errors.Is(err, ErrBlocked) {
		t.Fatalf("GitHub finished Zendesk OAuth: %v", err)
	}
	if flow.OAuthStatus().Connected {
		t.Fatal("GitHub reports a Zendesk grant")
	}
	if _, err := flow.Clarify(t.Context(), 42, "revision"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("GitHub clarification was not blocked: %v", err)
	}
}
