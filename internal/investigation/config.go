package investigation

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

type Config struct {
	Endpoint             string              `json:"endpoint"`
	Namespace            string              `json:"namespace"`
	Agent                string              `json:"agent"`
	Template             string              `json:"template"`
	UserID               string              `json:"user_id"`
	TokenEnv             string              `json:"token_env"`
	InvestigationTimeout int                 `json:"investigation_timeout"`
	ProcessTimeout       int                 `json:"process_timeout"`
	SandboxTTL           int                 `json:"sandbox_ttl"`
	MaxExperiments       int                 `json:"max_experiments"`
	PollInterval         int                 `json:"poll_interval"`
	RequestTimeout       int                 `json:"request_timeout"`
	MaxOutputBytes       int                 `json:"max_output_bytes"`
	ManualAgents         map[string][]string `json:"manual_agents,omitempty"`
	// AllowClusterHTTP permits plain-HTTP endpoints on cluster-internal
	// service names only. It exists for a dedicated in-cluster deployment
	// whose pod network is the trust boundary; everything else stays
	// loopback-only.
	AllowClusterHTTP bool `json:"allow_cluster_http,omitempty"`
	// Version identifies the TriageLab build that runs investigations and is
	// recorded in reports. The composing command sets it after loading; a
	// configuration file cannot.
	Version string `json:"-"`
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
	if len(c.ManualAgents) > 32 {
		return fmt.Errorf("configure at most 32 manual agents")
	}
	for name, tools := range c.ManualAgents {
		if !agent.ValidResourceName(name) {
			return fmt.Errorf("manual agent names must be valid Kubernetes resource names")
		}
		if err := agent.ValidateExtraTools(tools); err != nil {
			return fmt.Errorf("manual agent tool selection: %w", err)
		}
	}
	if err := kagent.CheckEndpoint(c.Endpoint, c.AllowClusterHTTP); err != nil {
		return err
	}
	if c.InvestigationTimeout < 1 || c.ProcessTimeout < 1 || c.RequestTimeout < 1 || c.PollInterval < 1 || c.MaxExperiments < 1 || c.MaxOutputBytes < 1 {
		return fmt.Errorf("limits must be positive")
	}
	if c.InvestigationTimeout > 3000 || c.ProcessTimeout > c.InvestigationTimeout || c.RequestTimeout > 60 || c.PollInterval > 10 || c.SandboxTTL < c.InvestigationTimeout+2*c.RequestTimeout || c.SandboxTTL > 3600 || c.MaxExperiments > 32 || c.MaxOutputBytes > 4<<20 {
		return fmt.Errorf("limits exceed MVP bounds or sandbox TTL does not cover investigation and cleanup")
	}
	return nil
}

// SelectManualAgent resolves an operator-configured choice without changing the workspace default.
func (c Config) SelectManualAgent(name string) (Config, []string, error) {
	if name != "" {
		if _, allowed := c.ManualAgents[name]; !allowed {
			return c, nil, fmt.Errorf("choose an agent configured for manual investigations")
		}
		c.Agent = name
	}
	return c, c.ManualAgents[c.Agent], nil
}
