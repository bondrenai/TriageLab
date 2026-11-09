package investigation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigAndBounds(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Endpoint = "http://remote.example/mcp" }, func(c *Config) { c.Endpoint = "https://user:pass@example.com/mcp" },
		func(c *Config) { c.Endpoint = "https://example.com/mcp?token=x" }, func(c *Config) { c.Namespace = "" },
		func(c *Config) { c.ProcessTimeout = 0 }, func(c *Config) { c.SandboxTTL = 1 }, func(c *Config) { c.MaxExperiments = 33 },
		func(c *Config) { c.RequestTimeout = 61 }, func(c *Config) { c.MaxOutputBytes = 5 << 20 },
	} {
		c := DefaultConfig()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("invalid config accepted: %+v", c)
		}
	}
}

func TestConfigClusterHTTPOptIn(t *testing.T) {
	clusterService := func(c *Config) { c.Endpoint = "http://triagelab-tools.kagent.svc.cluster.local:8083/mcp" }
	c := DefaultConfig()
	clusterService(&c)
	if err := c.Validate(); err == nil {
		t.Fatal("cluster service HTTP endpoint accepted without the opt-in")
	}
	c.AllowClusterHTTP = true
	if err := c.Validate(); err != nil {
		t.Fatalf("cluster service HTTP endpoint rejected with the opt-in: %v", err)
	}
	for _, endpoint := range []string{
		"http://triagelab-tools.kagent.svc.example.net/mcp", "http://10.0.0.1:8083/mcp",
		"http://kagent.example.com/mcp", "http://triagelab-tools.kagent/mcp",
	} {
		c := DefaultConfig()
		c.AllowClusterHTTP = true
		c.Endpoint = endpoint
		if err := c.Validate(); err == nil {
			t.Fatalf("non-service HTTP endpoint accepted with the opt-in: %s", endpoint)
		}
	}
}

func TestConfigRejectsUnknownFieldsAndTrailingObjects(t *testing.T) {
	for _, data := range []string{`{"surprise":true}`, `{} {}`, `{"poll_interval":1.5}`, `{"process_timeout":false}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("invalid config accepted: %s", data)
		}
	}
}
