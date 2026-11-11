// Package resultchat discusses saved investigation reports with a tool-free
// model completion. It does not run investigations or change their evidence.
package resultchat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const (
	MaxMessageBytes = 4096
	MaxAnswerBytes  = 16384
	// MaxHistoryMessages is the durable conversation limit. Model requests
	// additionally retain only the newest messages fitting a 32 KiB budget.
	MaxHistoryMessages = 64
)

// Config selects an explicit OpenAI-compatible Chat Completions endpoint.
// Endpoint is the complete URL, including /v1/chat/completions. An optional
// model gateway can provide that endpoint; no gateway is started here.
type Config struct {
	Endpoint       string `json:"endpoint"`
	Model          string `json:"model"`
	APIKeyEnv      string `json:"api_key_env"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxTokens      int    `json:"max_tokens"`
}

func DefaultConfig() Config {
	return Config{TimeoutSeconds: 60, MaxTokens: 2048}
}

func Load(path string) (Config, error) {
	c := DefaultConfig()
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("open result chat configuration: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return c, fmt.Errorf("result chat configuration could not be read within 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid result chat configuration")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, fmt.Errorf("result chat configuration must contain one JSON object")
	}
	return c, c.Validate()
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (c Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || len(c.Endpoint) > 2048 || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("result chat endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	loopback := literalLoopback(u.Hostname())
	if u.Scheme == "http" && !loopback {
		return fmt.Errorf("result chat HTTP endpoints must use a literal loopback address")
	}
	if c.APIKeyEnv == "" && !loopback {
		return fmt.Errorf("result chat api_key_env is required outside literal loopback")
	}
	if c.APIKeyEnv != "" && !envName.MatchString(c.APIKeyEnv) {
		return fmt.Errorf("result chat api_key_env must be an environment variable name")
	}
	if strings.TrimSpace(c.Model) != c.Model || c.Model == "" || len(c.Model) > 128 || strings.IndexFunc(c.Model, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return fmt.Errorf("result chat model must be a nonempty name of at most 128 bytes")
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 || c.MaxTokens < 256 || c.MaxTokens > 8192 {
		return fmt.Errorf("result chat timeout_seconds must be 1–120 and max_tokens must be 256–8192")
	}
	return nil
}

func literalLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
