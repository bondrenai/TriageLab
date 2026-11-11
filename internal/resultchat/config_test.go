package resultchat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRequiresExplicitSecureDestinationAndCredentials(t *testing.T) {
	for _, endpoint := range []string{"https://model.example/v1/chat/completions", "http://127.0.0.1:8080/v1/chat/completions", "http://[::1]:8080/v1/chat/completions"} {
		c := DefaultConfig()
		c.Endpoint, c.Model, c.APIKeyEnv = endpoint, "model", "TRIAGELAB_CHAT_API_KEY"
		if err := c.Validate(); err != nil {
			t.Errorf("valid endpoint rejected: %v", err)
		}
	}
	for _, endpoint := range []string{"", "http://model.example/v1/chat/completions", "http://localhost:8080/v1/chat/completions", "https://user:secret@model.example/v1/chat/completions", "https://model.example/v1/chat/completions?token=secret", "https://model.example/v1/chat/completions?", "https://model.example/v1/chat/completions#fragment", "file:///tmp/socket"} {
		c := DefaultConfig()
		c.Endpoint, c.Model, c.APIKeyEnv = endpoint, "model", "TRIAGELAB_CHAT_API_KEY"
		if err := c.Validate(); err == nil {
			t.Errorf("invalid endpoint accepted: %q", endpoint)
		}
	}
	c := DefaultConfig()
	c.Endpoint, c.Model = "https://model.example/v1/chat/completions", "model"
	if c.Validate() == nil {
		t.Fatal("non-loopback provider accepted without configured key env")
	}
	c.Endpoint = "http://127.0.0.1:8080/v1/chat/completions"
	if err := c.Validate(); err != nil {
		t.Fatalf("keyless literal-loopback proxy rejected: %v", err)
	}
	for _, name := range []string{"token-value-with-dashes", "A\nB", "=secret"} {
		c.APIKeyEnv = name
		if c.Validate() == nil {
			t.Fatal("invalid env name accepted")
		}
	}
}

func TestLoadAndNewDoNotExposeCredentialValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.json")
	if err := os.WriteFile(path, []byte(`{"endpoint":"https://model.example/v1/chat/completions","model":"model","api_key_env":"TRIAGELAB_RESULTCHAT_TEST_KEY"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.TimeoutSeconds != 60 || c.MaxTokens != 2048 {
		t.Fatalf("configuration defaults: %#v %v", c, err)
	}
	t.Setenv(c.APIKeyEnv, "private\ninvalid-key")
	if _, err := New(c); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("credential validation leaked value: %v", err)
	}
	t.Setenv(c.APIKeyEnv, "")
	if _, err := New(c); err == nil {
		t.Fatal("missing configured credential accepted")
	}
	t.Setenv(c.APIKeyEnv, "private-valid-key")
	client, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	for _, data := range []string{
		`{"endpoint":"https://model.example","model":"model","api_key":"private-valid-key"}`,
		`{"endpoint":"https://model.example","model":"model","api_key_env":"TRIAGELAB_RESULTCHAT_TEST_KEY"} {}`,
		`null`, strings.Repeat(" ", 65537),
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || strings.Contains(err.Error(), "private-valid-key") {
			t.Fatalf("invalid config accepted or leaked: %v", err)
		}
	}
}
