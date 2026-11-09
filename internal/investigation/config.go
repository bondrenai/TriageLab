package investigation

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Endpoint             string `json:"endpoint"`
	Namespace            string `json:"namespace"`
	Agent                string `json:"agent"`
	Template             string `json:"template"`
	UserID               string `json:"user_id"`
	TokenEnv             string `json:"token_env"`
	InvestigationTimeout int    `json:"investigation_timeout"`
	ProcessTimeout       int    `json:"process_timeout"`
	SandboxTTL           int    `json:"sandbox_ttl"`
	MaxExperiments       int    `json:"max_experiments"`
	PollInterval         int    `json:"poll_interval"`
	RequestTimeout       int    `json:"request_timeout"`
	MaxOutputBytes       int    `json:"max_output_bytes"`
}

func DefaultConfig() Config {
	return Config{
		Endpoint: "http://127.0.0.1:8083/mcp", Namespace: "kagent", Agent: "triagelab-investigator",
		Template: "triagelab-python", UserID: "triagelab@localhost", TokenEnv: "TRIAGELAB_TOKEN",
		InvestigationTimeout: 600, ProcessTimeout: 60, SandboxTTL: 900, MaxExperiments: 8,
		PollInterval: 2, RequestTimeout: 20, MaxOutputBytes: 1 << 20,
	}
}

func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return c, err
		}
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 65537))
		d.DisallowUnknownFields()
		if err = d.Decode(&c); err != nil {
			return c, fmt.Errorf("configuration: %w", err)
		}
		var extra any
		if err = d.Decode(&extra); err != io.EOF {
			return c, fmt.Errorf("configuration must contain one JSON object")
		}
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	for name, value := range map[string]string{"endpoint": c.Endpoint, "namespace": c.Namespace, "agent": c.Agent, "template": c.Template, "user_id": c.UserID, "token_env": c.TokenEnv} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must be nonempty", name)
		}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("endpoint must be HTTP(S) without credentials, query or fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return fmt.Errorf("HTTP is allowed only on loopback; use an SSH tunnel")
	}
	if c.InvestigationTimeout < 1 || c.ProcessTimeout < 1 || c.RequestTimeout < 1 || c.PollInterval < 1 || c.MaxExperiments < 1 || c.MaxOutputBytes < 1 {
		return fmt.Errorf("limits must be positive")
	}
	if c.InvestigationTimeout > 3000 || c.ProcessTimeout > c.InvestigationTimeout || c.RequestTimeout > 60 || c.PollInterval > 10 || c.SandboxTTL < c.InvestigationTimeout+2*c.RequestTimeout || c.SandboxTTL > 3600 || c.MaxExperiments > 32 || c.MaxOutputBytes > 4<<20 {
		return fmt.Errorf("limits exceed MVP bounds or sandbox TTL does not cover investigation and cleanup")
	}
	return nil
}
