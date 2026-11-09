package investigation

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/bondrenai/TriageLab/internal/agent"
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
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("endpoint must be HTTP(S) without credentials, query or fragment")
	}
	if u.Scheme == "http" && !isLoopbackHostname(u.Hostname()) {
		if !c.AllowClusterHTTP {
			return fmt.Errorf("HTTP is allowed only on loopback; use an SSH tunnel or allow_cluster_http inside a dedicated cluster")
		}
		if !isClusterServiceHostname(u.Hostname()) {
			return fmt.Errorf("allow_cluster_http permits HTTP only on *.svc or *.svc.cluster.local service endpoints")
		}
	}
	if c.InvestigationTimeout < 1 || c.ProcessTimeout < 1 || c.RequestTimeout < 1 || c.PollInterval < 1 || c.MaxExperiments < 1 || c.MaxOutputBytes < 1 {
		return fmt.Errorf("limits must be positive")
	}
	if c.InvestigationTimeout > 3000 || c.ProcessTimeout > c.InvestigationTimeout || c.RequestTimeout > 60 || c.PollInterval > 10 || c.SandboxTTL < c.InvestigationTimeout+2*c.RequestTimeout || c.SandboxTTL > 3600 || c.MaxExperiments > 32 || c.MaxOutputBytes > 4<<20 {
		return fmt.Errorf("limits exceed MVP bounds or sandbox TTL does not cover investigation and cleanup")
	}
	return nil
}

func isLoopbackHostname(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// isClusterServiceHostname reports whether host is an in-cluster Kubernetes
// service DNS name. IP-literal and public names stay excluded so an enabled
// opt-in cannot hide a broader network exposure.
func isClusterServiceHostname(host string) bool {
	lower := strings.ToLower(host)
	return strings.HasSuffix(lower, ".svc") || strings.HasSuffix(lower, ".svc.cluster.local")
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
