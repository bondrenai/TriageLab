package ticketflow

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/investigation"
)

func jiraConnection(id string, token string) Connection {
	config := Config{Source: "jira", Origin: "https://test-site.atlassian.net", Email: "triagelab@example.com",
		Policy: Policy{Tag: "reproduce"}}
	if token == "" {
		config.TokenEnv = "TRIAGELAB_TEST_JIRA_TOKEN"
	}
	return Connection{ID: id, Label: "Support desk", Config: config, Token: token}
}

func TestConnectionRegistryRoundtrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tickets")
	stored := jiraConnection("0a1b2c3d", "test-jira-api-token")
	env := Connection{ID: "0a1b2c3e", Config: Config{Source: "github", Repository: "owner/repo",
		TokenEnv: "TRIAGELAB_GITHUB_TOKEN", Policy: Policy{Tag: "reproduce"}}}
	if err := SaveConnections(root, []Connection{stored, env}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConnections(root)
	if err != nil || len(loaded) != 2 {
		t.Fatalf("registry roundtrip failed: %+v %v", loaded, err)
	}
	if loaded[0].Token != "test-jira-api-token" || loaded[0].CredentialMode() != "stored" ||
		loaded[1].CredentialMode() != "env" {
		t.Fatalf("registry lost credentials or modes: %+v", loaded)
	}
	info, err := os.Stat(filepath.Join(root, registryFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("registry is not a private file: %v %v", info, err)
	}
	dir, err := os.Stat(root)
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("registry directory is not private: %v %v", dir, err)
	}
}

func TestConnectionRegistryKeepsTokenOutOfConfigEncoding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tickets")
	if err := SaveConnections(root, []Connection{jiraConnection("0a1b2c3d", "test-jira-api-token")}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, registryFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"token":"test-jira-api-token"`) || strings.Contains(string(data), "token_env") {
		t.Fatalf("stored credential leaked into the configuration encoding: %s", data)
	}
}

func TestLoadConnectionsWithoutRegistry(t *testing.T) {
	connections, err := LoadConnections(filepath.Join(t.TempDir(), "tickets"))
	if err != nil || connections != nil {
		t.Fatalf("missing registry is not empty: %+v %v", connections, err)
	}
}

func TestConnectionRegistryRejectsInvalidRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tickets")
	for name, connection := range map[string]Connection{
		"short id":         {ID: "0a1b", Config: Config{Source: "github", Repository: "owner/repo", Policy: Policy{Tag: "reproduce"}}},
		"non-hex id":       {ID: "zzzzzzzz", Config: Config{Source: "github", Repository: "owner/repo", Policy: Policy{Tag: "reproduce"}}},
		"missing config":   {ID: "0a1b2c3d"},
		"invalid config":   {ID: "0a1b2c3d", Config: Config{Source: "jira", Policy: Policy{Tag: "reproduce"}}},
		"invalid token":    jiraConnection("0a1b2c3d", "secret value spaces"),
		"control in label": {ID: "0a1b2c3d", Label: "support\x00desk", Config: Config{Source: "github", Repository: "owner/repo", Policy: Policy{Tag: "reproduce"}}},
		"oversized label":  {ID: "0a1b2c3d", Label: strings.Repeat("x", 61), Config: Config{Source: "github", Repository: "owner/repo", Policy: Policy{Tag: "reproduce"}}},
		"credential jira without token": {ID: "0a1b2c3d", Config: Config{Source: "jira",
			Origin: "https://test-site.atlassian.net", Email: "triagelab@example.com", Policy: Policy{Tag: "reproduce"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := SaveConnections(root, []Connection{connection}); err == nil {
				t.Fatal("accepted an invalid connection record")
			}
		})
	}
	duplicated := []Connection{jiraConnection("0a1b2c3d", "test-jira-api-token"), jiraConnection("0a1b2c3d", "test-jira-api-token")}
	if err := SaveConnections(root, duplicated); err == nil {
		t.Fatal("accepted duplicate connection ids")
	}
	var full []Connection
	for i := 0; i <= maxConnections; i++ {
		full = append(full, Connection{ID: strings.Repeat("a", 7) + string(rune('0'+i)),
			Config: jiraConnection("0a1b2c3d", "").Config, Token: "test-jira-api-token"})
	}
	if err := SaveConnections(root, full); err == nil {
		t.Fatal("accepted more than the connection limit")
	}
}

func TestConnectionRegistryRejectsDriftedFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tickets")
	if err := SaveConnections(root, []Connection{jiraConnection("0a1b2c3d", "test-jira-api-token")}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, registryFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConnections(root); err == nil {
		t.Fatal("accepted a registry with relaxed permissions")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"connections":[],"extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConnections(root); err == nil {
		t.Fatal("accepted unknown registry fields")
	}
}

func TestStoredTokenAuthorizesWithoutEnvironment(t *testing.T) {
	connection := jiraConnection("0a1b2c3d", "test-jira-api-token")
	config, err := connection.Prepared()
	if err != nil {
		t.Fatal(err)
	}
	flow, err := Open(config, filepath.Join(t.TempDir(), "state"), investigation.DefaultConfig())
	if err != nil {
		t.Fatalf("stored credential rejected: %v", err)
	}
	defer flow.Close()
	if !flow.hasSecret([]byte("test-jira-api-token")) {
		t.Fatal("stored credential is not filtered from output")
	}
	invalid := Connection{ID: "0a1b2c3d", Config: connection.Config, Token: "secret value spaces"}
	config, err = invalid.Prepared()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(config, filepath.Join(t.TempDir(), "state"), investigation.DefaultConfig())
	if err == nil || strings.Contains(err.Error(), "secret value spaces") {
		t.Fatalf("malformed stored credential accepted or disclosed: %v", err)
	}
}

func TestConnectionStateDirRejectsTraversal(t *testing.T) {
	if _, err := ConnectionStateDir("/tmp/tickets", "../../etc"); err == nil {
		t.Fatal("accepted a non-identifier state directory name")
	}
	dir, err := ConnectionStateDir("/tmp/tickets", "0a1b2c3d")
	if err != nil || dir != filepath.Join("/tmp/tickets", "0a1b2c3d") {
		t.Fatalf("valid identifier rejected: %v", err)
	}
}

func TestVerifyGitHubConnection(t *testing.T) {
	g := newGitHub("owner/repo", "token-value")
	g.client.Transport = githubTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/owner/repo" || r.Header.Get("Authorization") != "Bearer token-value" {
			return nil, errors.New("unexpected GitHub verification request")
		}
		return githubResponse(200, `{"id":1}`), nil
	})
	if err := verifyGitHub(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	g.client.Transport = githubTransport(func(*http.Request) (*http.Response, error) {
		return githubResponse(401, "{}"), nil
	})
	if err := verifyGitHub(t.Context(), g); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("rejected token not reported: %v", err)
	}
}

func TestVerifyLinearConnection(t *testing.T) {
	l := newLinear("acme", "ENG", "lin_api_key")
	l.client.Transport = linearTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "lin_api_key" {
			return nil, errors.New("Linear key not sent")
		}
		return linearResponse(`{"viewer":{"id":"00000000-0000-4000-8000-000000000001","name":"Triage"}}`), nil
	})
	if err := verifyLinear(t.Context(), l); err != nil {
		t.Fatal(err)
	}
	l.client.Transport = linearTransport(func(*http.Request) (*http.Response, error) {
		// Linear reports authentication failures inside a 200 envelope that
		// still carries a non-null data field.
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
			`{"data":{"issue":null},"errors":[{"message":"invalid key","type":"INVALID_TOKEN"}]}`))}, nil
	})
	if err := verifyLinear(t.Context(), l); err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Fatalf("rejected key not reported: %v", err)
	}
}

func TestVerifyBondrenConnection(t *testing.T) {
	b := newBondren("https://app.bondren.example", "bondren-token")
	b.client.Transport = bondrenTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/issues/query" || r.Header.Get("Authorization") != "Bearer bondren-token" {
			return nil, errors.New("unexpected Bondren verification request")
		}
		return bondrenJSON(map[string]any{"items": []any{}}), nil
	})
	if err := verifyBondren(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	b.client.Transport = bondrenTransport(func(*http.Request) (*http.Response, error) {
		return bondrenResponse(401, "{}"), nil
	})
	if err := verifyBondren(t.Context(), b); err == nil || !strings.Contains(err.Error(), "rejected the agent token") {
		t.Fatalf("rejected token not reported: %v", err)
	}
}

func TestVerifyJiraConnection(t *testing.T) {
	j := newJira(jiraTestOrigin, jiraTestEmail, jiraTestToken)
	j.client.Transport = jiraTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/rest/api/3/myself" || r.Header.Get("Authorization") != jiraBasicAuthorization() {
			return nil, errors.New("unexpected Jira verification request")
		}
		return jiraJSON(map[string]any{"accountId": jiraReporterID}), nil
	})
	if err := verifyJira(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	j.client.Transport = jiraTransport(func(*http.Request) (*http.Response, error) {
		return jiraResponse(403, "{}"), nil
	})
	if err := verifyJira(t.Context(), j); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("rejected credentials not reported: %v", err)
	}
}

func TestVerifySourceValidatesBeforeRequest(t *testing.T) {
	if err := VerifySource(context.Background(), Config{Source: "jira", Policy: Policy{Tag: "reproduce"}}); err == nil {
		t.Fatal("verified an invalid configuration")
	}
	if err := VerifySource(context.Background(), Config{Source: "zendesk", Subdomain: "example",
		ClientID: "triagelab-local", RedirectURI: "http://127.0.0.1:8080" + OAuthCallbackPath,
		Policy: Policy{Tag: "support"}}); err != nil {
		t.Fatalf("Zendesk has nothing to verify before its OAuth flow: %v", err)
	}
}
