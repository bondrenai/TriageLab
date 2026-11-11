package ticketflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/investigation"
)

func TestBondrenConfigRequiresOriginAndExplicitWriteCredentials(t *testing.T) {
	valid := Config{Source: "bondren", Origin: "https://app.bondren.example", TokenEnv: "TRIAGELAB_TEST_BONDREN_TOKEN", Policy: Policy{Tag: "reproduce"}}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"unknown source":             func(c *Config) { c.Source = "notion" },
		"missing origin":             func(c *Config) { c.Origin = "" },
		"origin with path":           func(c *Config) { c.Origin = "https://app.bondren.example/workspace" },
		"origin with query":          func(c *Config) { c.Origin = "https://app.bondren.example?tenant=x" },
		"plain http off loopback":    func(c *Config) { c.Origin = "http://app.bondren.example" },
		"origin with user":           func(c *Config) { c.Origin = "https://token@app.bondren.example" },
		"missing token":              func(c *Config) { c.TokenEnv = "" },
		"token value instead of env": func(c *Config) { c.TokenEnv = "bondren-secret" },
		"missing tag":                func(c *Config) { c.Policy.Tag = "" },
		"repository":                 func(c *Config) { c.Repository = "owner/repo" },
		"workspace":                  func(c *Config) { c.Workspace = "acme" },
		"team":                       func(c *Config) { c.Team = "ENG" },
		"Zendesk account":            func(c *Config) { c.Subdomain = "example" },
		"Zendesk client":             func(c *Config) { c.ClientID = "triagelab" },
		"Zendesk callback":           func(c *Config) { c.RedirectURI = "http://localhost:8080/oauth/zendesk/callback" },
		"investigation comments":     func(c *Config) { c.InvestigationComments = true },
		"priority":                   func(c *Config) { c.Policy.Priorities = []string{"urgent"} },
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
				t.Fatal("accepted invalid Bondren configuration")
			}
		})
	}
	valid.Policy.AutoQuestions, valid.Policy.MaxQuestions = true, 2
	valid.InternalNotes, valid.PollSeconds = true, 60
	if err := valid.validate(); err != nil {
		t.Fatalf("rejected configured Bondren write workflow: %v", err)
	}
	loopback := valid
	loopback.Origin = "http://127.0.0.1:5174"
	if err := loopback.validate(); err != nil {
		t.Fatalf("rejected loopback development origin: %v", err)
	}
}

func TestLoadBondrenConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bondren.json")
	body := `{"source":"bondren","origin":"https://app.bondren.example","token_env":"TRIAGELAB_BONDREN_TOKEN","policy":{"tag":"reproduce"}}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil || config.provider() != "bondren" || config.Origin != "https://app.bondren.example" {
		t.Fatalf("wrong Bondren configuration: %+v %v", config, err)
	}
}

func TestOpenBondrenEnablesOnlyConfiguredWrites(t *testing.T) {
	const variable = "TRIAGELAB_TEST_BONDREN_TOKEN"
	t.Setenv(variable, "test-bondren-agent-token")
	for _, feature := range []string{"none", "clarifications", "internal notes"} {
		t.Run(feature, func(t *testing.T) {
			config := Config{Source: "bondren", Origin: "https://app.bondren.example", TokenEnv: variable, Policy: Policy{Tag: "reproduce"}}
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
			if flow.source.(*bondrenSource).writable != (feature != "none") || flow.Settings().InternalNotes != config.InternalNotes {
				t.Fatal("connector write capability does not match explicit configuration")
			}
		})
	}
}

func TestOpenBondrenKeepsTokenServerSideAndDoesNotUseOAuth(t *testing.T) {
	config := Config{Source: "bondren", Origin: "https://app.bondren.example", TokenEnv: "TRIAGELAB_TEST_BONDREN_TOKEN", Policy: Policy{Tag: "reproduce"}}
	for _, token := range []string{"", "secret\ninvalid"} {
		t.Setenv(config.TokenEnv, token)
		if _, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig()); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("missing/malformed token accepted or disclosed: %v", err)
		}
	}
	const token = "test-bondren-agent-token"
	t.Setenv(config.TokenEnv, token)
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if flow.Provider() != "bondren" || flow.Account() != "https://app.bondren.example" || !flow.hasSecret([]byte(token)) {
		t.Fatal("Bondren source identity or secret filtering is missing")
	}
	if _, _, err := flow.BeginOAuth("localhost:8080"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("Bondren started Zendesk OAuth: %v", err)
	}
	if flow.OAuthStatus().Connected {
		t.Fatal("Bondren reports a Zendesk grant")
	}
	if _, err := flow.Clarify(t.Context(), 42, "revision"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("Bondren clarification was not blocked: %v", err)
	}
}

func TestBondrenProcedureRejectsZendeskOnlyPolicy(t *testing.T) {
	config := Config{Source: "bondren", Origin: "https://app.bondren.example", TokenEnv: "TRIAGELAB_TEST_BONDREN_TOKEN",
		Policy: Policy{Tag: "reproduce"}}
	t.Setenv(config.TokenEnv, "test-bondren-agent-token")
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	version := flow.Procedure()
	version.Procedure.Policy.Priorities = []string{"urgent"}
	if err := flow.validateProcedureSet(version); err == nil {
		t.Fatal("Bondren procedure accepted Zendesk priorities")
	}
}

func TestOpenBondrenSeedsIssueIDsFromStoredCases(t *testing.T) {
	const origin = "https://app.bondren.example"
	dir := filepath.Join(t.TempDir(), "cases")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(Case{TicketID: 42, Account: origin, Source: "bondren",
		URL: origin + "/work/support/" + bondrenIssueUUIDF, State: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "42.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	config := Config{Source: "bondren", Origin: origin, TokenEnv: "TRIAGELAB_TEST_BONDREN_TOKEN", Policy: Policy{Tag: "reproduce"}}
	t.Setenv(config.TokenEnv, "test-bondren-agent-token")
	flow, err := Open(config, dir, investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if uuid, ok := flow.source.(*bondrenSource).issueUUIDFor(42); !ok || uuid != bondrenIssueUUIDF {
		t.Fatalf("stored case URL did not seed its issue ID: %q %v", uuid, ok)
	}
}

func TestResolveTicketIsBondrenOnly(t *testing.T) {
	t.Setenv("TRIAGELAB_TEST_LINEAR_TOKEN", "test-linear-api-key")
	linear, err := Open(Config{Source: "linear", Workspace: "acme", Team: "ENG",
		TokenEnv: "TRIAGELAB_TEST_LINEAR_TOKEN", Policy: Policy{Tag: "reproduce"}},
		filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer linear.Close()
	if _, err := linear.ResolveTicket(t.Context(), "42"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("Linear reference resolution was not blocked: %v", err)
	}
	t.Setenv("TRIAGELAB_TEST_BONDREN_TOKEN", "test-bondren-agent-token")
	bondren, err := Open(Config{Source: "bondren", Origin: "https://app.bondren.example",
		TokenEnv: "TRIAGELAB_TEST_BONDREN_TOKEN", Policy: Policy{Tag: "reproduce"}},
		filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer bondren.Close()
	if _, err := bondren.ResolveTicket(t.Context(), "not-an-issue"); !errors.Is(err, ErrReference) {
		t.Fatalf("malformed Bondren reference produced %v, want ErrReference", err)
	}
	if id, err := bondren.ResolveTicket(t.Context(), " 42 "); err != nil || id != 42 {
		t.Fatalf("Bondren number reference resolved to %d: %v", id, err)
	}
}
