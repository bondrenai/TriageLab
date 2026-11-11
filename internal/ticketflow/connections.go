package ticketflow

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Connections persist as one registry file inside the workspace ticket
// directory. Each connection keeps its cases, procedures, and Zendesk OAuth
// state in a private subdirectory named by its identifier.
const (
	maxConnections = 8
	registryFile   = "registry.json"
	registryBytes  = 64 << 10
)

var connectionID = regexp.MustCompile(`^[0-9a-f]{8}$`)

// Connection is one saved ticket source. Token holds a credential supplied
// through the workspace UI; it stays on the server, is never part of Config's
// JSON encoding, and never appears in an API response.
type Connection struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	Config Config `json:"config"`
	Token  string `json:"token,omitempty"`
}

// CredentialMode reports how the connection authorizes with its source: a
// token stored by the server, a token_env environment variable, or nothing
// (anonymous GitHub reads).
func (c Connection) CredentialMode() string {
	switch {
	case c.Token != "":
		return "stored"
	case c.Config.TokenEnv != "":
		return "env"
	default:
		return "none"
	}
}

// Prepared returns the connection's configuration with implicit defaults
// applied and any stored credential attached, after full validation.
func (c Connection) Prepared() (Config, error) {
	config := c.Config.withDefaults().WithToken(c.Token)
	return config, config.validate()
}

// NewConnectionID returns a random identifier that also names the
// connection's state directory.
func NewConnectionID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ConnectionStateDir returns the private state directory for a connection.
func ConnectionStateDir(root, id string) (string, error) {
	if !connectionID.MatchString(id) {
		return "", errors.New("invalid connection id")
	}
	return filepath.Join(root, id), nil
}

// SourceAccount returns the account identifier a configuration connects to:
// the repository, workspace and team, site or helpdesk origin. It matches the
// account each connector reports after opening.
func SourceAccount(config Config) string {
	switch config.provider() {
	case "github":
		return "https://github.com/" + config.Repository
	case "linear":
		return "https://linear.app/" + config.Workspace + "/" + config.Team
	case "bondren":
		return strings.TrimSuffix(config.Origin, "/")
	case "jira":
		return strings.TrimSuffix(config.Origin, "/")
	default:
		return "https://" + config.Subdomain + ".zendesk.com"
	}
}

func validateConnections(connections []Connection) error {
	if len(connections) > maxConnections {
		return fmt.Errorf("a workspace keeps at most %d ticket connections", maxConnections)
	}
	seen := map[string]bool{}
	for _, c := range connections {
		if !connectionID.MatchString(c.ID) {
			return errors.New("connection id must be eight hexadecimal characters")
		}
		if seen[c.ID] {
			return fmt.Errorf("connection id %s is duplicated", c.ID)
		}
		seen[c.ID] = true
		if utf8.RuneCountInString(c.Label) > 60 || strings.IndexFunc(c.Label, unicode.IsControl) >= 0 {
			return errors.New("connection label must be 60 plain characters or fewer")
		}
		if c.Token != "" && !validOAuthToken(c.Token) {
			return fmt.Errorf("connection %s stores an invalid token", c.ID)
		}
		if _, err := c.Prepared(); err != nil {
			return fmt.Errorf("connection %s: %w", c.ID, err)
		}
	}
	return nil
}

// LoadConnections reads the saved connection registry. A missing registry is
// an empty workspace, not an error.
func LoadConnections(root string) ([]Connection, error) {
	f, err := os.Open(filepath.Join(root, registryFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("could not open the ticket connection registry")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("the ticket connection registry must be a private regular file")
	}
	var registry struct {
		Connections []Connection `json:"connections"`
	}
	d := json.NewDecoder(io.LimitReader(f, registryBytes+1))
	d.DisallowUnknownFields()
	if d.Decode(&registry) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid ticket connection registry")
	}
	if err := validateConnections(registry.Connections); err != nil {
		return nil, err
	}
	return registry.Connections, nil
}

// SaveConnections validates and atomically persists the connection registry.
func SaveConnections(root string, connections []Connection) error {
	if err := validateConnections(connections); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("ticket connections need a private directory with mode 0700")
	}
	data, err := json.Marshal(struct {
		Connections []Connection `json:"connections"`
	}{connections})
	if err != nil {
		return err
	}
	_, err = writePrivate(root, registryFile, data)
	return err
}
