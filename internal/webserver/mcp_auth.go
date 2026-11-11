package webserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/bondrenai/TriageLab/internal/auth"
)

// MCPConfig configures machine access separately from browser sign-in. A
// gateway may authenticate end users and supply one of these scoped credentials;
// forwarded identity headers never confer workspace permissions.
type MCPConfig struct {
	Principals     []MCPPrincipal `json:"principals"`
	AllowedOrigins []string       `json:"allowed_origins,omitempty"`
}

type MCPPrincipal struct {
	Name     string    `json:"name"`
	Role     auth.Role `json:"role"`
	TokenEnv string    `json:"token_env"`
}

type mcpCredential struct {
	hash     [sha256.Size]byte
	identity auth.Identity
}

type mcpAccess struct {
	handler     http.Handler
	credentials []mcpCredential
	origins     map[string]bool
}

func LoadMCPConfig(path string) (MCPConfig, error) {
	var config MCPConfig
	f, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return config, errors.New("MCP configuration must fit within 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&config); err != nil {
		return config, fmt.Errorf("MCP configuration: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return config, errors.New("MCP configuration must contain one JSON object")
	}
	return config, nil
}

// EnableMCP must be called before serving requests. Secrets are resolved at
// startup and never sent to kagent, the chat provider, or the browser.
func (s *Server) EnableMCP(config MCPConfig) error {
	if len(config.Principals) == 0 || len(config.Principals) > 32 {
		return errors.New("MCP requires between 1 and 32 named principals")
	}
	if len(config.AllowedOrigins) > 16 {
		return errors.New("MCP allows at most 16 browser origins")
	}
	access := &mcpAccess{origins: make(map[string]bool)}
	var secrets []string
	names := make(map[string]bool)
	hashes := make(map[[sha256.Size]byte]bool)
	namePattern := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	envPattern := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	for _, p := range config.Principals {
		if !namePattern.MatchString(p.Name) || names[p.Name] {
			return errors.New("MCP principal names must be unique lowercase names of at most 32 characters")
		}
		if p.Role != auth.RoleViewer && p.Role != auth.RoleUser {
			return errors.New("MCP principal role must be viewer or user")
		}
		if !envPattern.MatchString(p.TokenEnv) {
			return errors.New("MCP token_env must name an environment variable")
		}
		token := os.Getenv(p.TokenEnv)
		if len(token) < 32 || len(token) > 512 || strings.IndexFunc(token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return fmt.Errorf("MCP credential for %s must contain 32 to 512 printable non-space ASCII characters", p.Name)
		}
		hash := sha256.Sum256([]byte(token))
		if hashes[hash] {
			return errors.New("MCP principals must use distinct credentials")
		}
		names[p.Name], hashes[hash] = true, true
		access.credentials = append(access.credentials, mcpCredential{hash, auth.Identity{Username: "mcp:" + p.Name, Role: p.Role, Via: "mcp"}})
		secrets = append(secrets, token)
	}
	for _, origin := range config.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && allowedHost(u.Host))) {
			return errors.New("MCP allowed_origins must be exact HTTPS origins (or HTTP loopback origins)")
		}
		access.origins[origin] = true
	}
	access.handler = s.newMCPHandler()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mcp != nil || s.closed {
		return errors.New("MCP is already configured or the workspace is closed")
	}
	s.mcp = access
	s.secrets = append(s.secrets, secrets...)
	return nil
}

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	access := s.mcp
	if access == nil {
		fail(w, http.StatusNotFound, "MCP is not configured")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail(w, http.StatusBadRequest, "MCP does not accept query parameters")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if origin != "http://"+r.Host && !access.origins[origin] {
			fail(w, http.StatusForbidden, "MCP origin is not allowed")
			return
		}
	} else if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		fail(w, http.StatusForbidden, "MCP origin is required for cross-origin browser requests")
		return
	}
	// Authorization is the only credential channel. Cookies, query parameters,
	// and claimed roles in proxy headers cannot authenticate an MCP request.
	values := r.Header.Values("Authorization")
	var identity auth.Identity
	if len(values) == 1 && len(values[0]) <= 519 {
		parts := strings.Split(values[0], " ")
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != "" {
			hash := sha256.Sum256([]byte(parts[1]))
			for _, credential := range access.credentials {
				if subtle.ConstantTimeCompare(hash[:], credential.hash[:]) == 1 {
					identity = credential.identity
				}
			}
		}
	}
	if !identity.Role.Valid() {
		w.Header().Set("WWW-Authenticate", `Bearer realm="triagelab-mcp"`)
		fail(w, http.StatusUnauthorized, "A valid MCP credential is required")
		return
	}
	access.handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, identity)))
}
