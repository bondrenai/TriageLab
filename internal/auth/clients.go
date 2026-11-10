package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Scope names one permission an admin grants to an API client.
type Scope string

const (
	// ScopeRun lets a client launch investigations, and read and steer the
	// ones it launched.
	ScopeRun Scope = "investigations:run"
	// ScopeRead lets a client read every investigation in the workspace.
	ScopeRead Scope = "investigations:read"
)

// Scopes lists every grantable scope in canonical order.
func Scopes() []Scope { return []Scope{ScopeRun, ScopeRead} }

var errUnknownScope = fmt.Errorf("scopes are %s and %s", ScopeRun, ScopeRead)

// ParseScopes validates scope names and returns them deduplicated in
// canonical order. Each value may hold several space-separated names, as an
// OAuth scope parameter does. No names yields no scopes.
func ParseScopes(values []string) ([]Scope, error) {
	requested := map[Scope]bool{}
	for _, value := range values {
		for _, name := range strings.Fields(value) {
			if !slices.Contains(Scopes(), Scope(name)) {
				return nil, errUnknownScope
			}
			requested[Scope(name)] = true
		}
	}
	var scopes []Scope
	for _, scope := range Scopes() {
		if requested[scope] {
			scopes = append(scopes, scope)
		}
	}
	return scopes, nil
}

// grantable normalizes the scopes of a client, which needs at least one.
func grantable(scopes []Scope) ([]Scope, error) {
	names := make([]string, len(scopes))
	for i, scope := range scopes {
		names[i] = string(scope)
	}
	parsed, err := ParseScopes(names)
	if err != nil {
		return nil, err
	}
	if len(parsed) == 0 {
		return nil, errors.New("grant at least one scope")
	}
	return parsed, nil
}

// ErrClientNotFound, ErrClientExists, and ErrScopeNotGranted distinguish
// store errors for callers that report them to operators and OAuth clients.
var (
	ErrClientNotFound  = errors.New("API client not found")
	ErrClientExists    = errors.New("API client already exists")
	ErrScopeNotGranted = errors.New("scope not granted to this API client")
)

const (
	// AccessTokenLifetime is absolute. Tokens are memory-only like sessions,
	// so a restart ends them early and clients request a new one.
	AccessTokenLifetime = time.Hour

	// ClientActorPrefix marks an API client in report provenance. Local
	// account names cannot contain ':' and single sign-on names cannot start
	// with this prefix, so an account never passes for a client.
	ClientActorPrefix = "client:"

	maxClients         = 64
	maxClientsFile     = 1 << 20
	maxTokensPerClient = 32
	clientSecretPrefix = "tlcs_"
	accessTokenPrefix  = "tlat_"
	secretHashPrefix   = "sha256$"
)

// ClientActor is the name recorded as started_by for runs a client launches.
func ClientActor(id string) string { return ClientActorPrefix + id }

// ValidClientID applies the account-name rules, which keep client IDs safe in
// URLs, HTTP Basic credentials, logs, and report provenance.
func ValidClientID(id string) bool { return ValidUsername(id) }

// APIClient is an OAuth client an admin created for an external application.
// It never carries secret material.
type APIClient struct {
	ID        string    `json:"id"`
	Scopes    []Scope   `json:"scopes"`
	Disabled  bool      `json:"disabled"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// storedClient adds the encoded secret hash kept in the client file.
type storedClient struct {
	APIClient
	Secret string `json:"secret"`
}

type clientsFile struct {
	Clients []storedClient `json:"clients"`
}

// Grant is what a live access token authorizes: the client it was issued to
// and the scopes granted at issue.
type Grant struct {
	ClientID string
	Scopes   []Scope
}

// Allows reports whether the grant includes scope.
func (g Grant) Allows(scope Scope) bool { return slices.Contains(g.Scopes, scope) }

type issuedToken struct {
	clientID string
	scopes   []Scope
	expires  time.Time
}

// ClientStore keeps API clients in one JSON file and their access tokens in
// memory. Only hashes of secrets and tokens are retained. Every change to a
// client's grant (scopes, secret, disabling, removal) also ends its tokens, so
// a live token always reflects a grant that is still in force. All methods are
// safe for concurrent use.
type ClientStore struct {
	mu      sync.Mutex
	path    string
	clients map[string]storedClient
	tokens  map[[sha256.Size]byte]issuedToken
	now     func() time.Time
}

// OpenClientStore loads the client file at path. A missing file yields an
// empty store; nothing is written until the first client is created.
func OpenClientStore(path string) (*ClientStore, error) {
	store := &ClientStore{path: path, clients: map[string]storedClient{},
		tokens: map[[sha256.Size]byte]issuedToken{}, now: time.Now}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var data clientsFile
	d := json.NewDecoder(io.LimitReader(f, maxClientsFile+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&data); err != nil {
		return nil, fmt.Errorf("API client file: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("API client file must contain one JSON object")
	}
	if len(data.Clients) > maxClients {
		return nil, fmt.Errorf("API client file exceeds %d clients", maxClients)
	}
	for _, client := range data.Clients {
		scopes, err := grantable(client.Scopes)
		if err != nil || !ValidClientID(client.ID) || !validSecretHash(client.Secret) {
			return nil, fmt.Errorf("API client file contains an invalid client entry")
		}
		if _, duplicate := store.clients[client.ID]; duplicate {
			return nil, fmt.Errorf("API client file contains duplicate client %q", client.ID)
		}
		client.Scopes = scopes
		store.clients[client.ID] = client
	}
	return store, nil
}

// Path is the file backing the store.
func (s *ClientStore) Path() string { return s.path }

// Clients lists clients without secret material, ordered by ID.
func (s *ClientStore) Clients() []APIClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients := make([]APIClient, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client.public())
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].ID < clients[j].ID })
	return clients
}

func (c storedClient) public() APIClient {
	client := c.APIClient
	client.Scopes = slices.Clone(c.Scopes)
	return client
}

// Create adds a client and returns its generated secret. The secret is not
// retained and cannot be shown again.
func (s *ClientStore) Create(id string, scopes []Scope, createdBy string) (string, error) {
	if !ValidClientID(id) {
		return "", fmt.Errorf("client IDs are 1-32 characters of lowercase letters, digits, '-', '.', '_'")
	}
	scopes, err := grantable(scopes)
	if err != nil {
		return "", err
	}
	secret, err := newCredential(clientSecretPrefix)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.clients[id]; exists {
		return "", ErrClientExists
	}
	if len(s.clients) >= maxClients {
		return "", fmt.Errorf("the client file holds at most %d clients", maxClients)
	}
	now := s.now().UTC()
	client := storedClient{Secret: hashSecret(secret), APIClient: APIClient{
		ID: id, Scopes: scopes, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now}}
	if err := s.commitLocked(id, &client); err != nil {
		return "", err
	}
	return secret, nil
}

// SetScopes replaces a client's scopes and ends its tokens.
func (s *ClientStore) SetScopes(id string, scopes []Scope) error {
	scopes, err := grantable(scopes)
	if err != nil {
		return err
	}
	return s.update(id, func(client *storedClient) { client.Scopes = scopes })
}

// SetDisabled enables or disables a client without deleting it. Its tokens
// end either way; a re-enabled client requests new ones.
func (s *ClientStore) SetDisabled(id string, disabled bool) error {
	return s.update(id, func(client *storedClient) { client.Disabled = disabled })
}

// RotateSecret replaces a client's secret, ends its tokens, and returns the
// new secret once.
func (s *ClientStore) RotateSecret(id string) (string, error) {
	secret, err := newCredential(clientSecretPrefix)
	if err != nil {
		return "", err
	}
	if err := s.update(id, func(client *storedClient) { client.Secret = hashSecret(secret) }); err != nil {
		return "", err
	}
	return secret, nil
}

// Remove deletes a client and ends its tokens.
func (s *ClientStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[id]; !ok {
		return ErrClientNotFound
	}
	if err := s.commitLocked(id, nil); err != nil {
		return err
	}
	s.revokeClientLocked(id)
	return nil
}

func (s *ClientStore) update(id string, change func(*storedClient)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[id]
	if !ok {
		return ErrClientNotFound
	}
	change(&client)
	client.UpdatedAt = s.now().UTC()
	if err := s.commitLocked(id, &client); err != nil {
		return err
	}
	s.revokeClientLocked(id)
	return nil
}

// commitLocked replaces one client, or removes it when next is nil, and
// saves. A failed save restores the previous entry, so memory never holds a
// client or secret that the file does not.
func (s *ClientStore) commitLocked(id string, next *storedClient) error {
	previous, existed := s.clients[id]
	if next == nil {
		delete(s.clients, id)
	} else {
		s.clients[id] = *next
	}
	err := s.saveLocked()
	if err != nil {
		if existed {
			s.clients[id] = previous
		} else {
			delete(s.clients, id)
		}
	}
	return err
}

func (s *ClientStore) saveLocked() error {
	data := clientsFile{Clients: make([]storedClient, 0, len(s.clients))}
	for _, client := range s.clients {
		data.Clients = append(data.Clients, client)
	}
	sort.Slice(data.Clients, func(i, j int) bool { return data.Clients[i].ID < data.Clients[j].ID })
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(s.path, append(content, '\n'))
}

func (s *ClientStore) revokeClientLocked(id string) {
	for key, issued := range s.tokens {
		if issued.clientID == id {
			delete(s.tokens, key)
		}
	}
}

// Authenticate verifies a client secret. Unknown clients, disabled clients,
// and wrong secrets are indistinguishable to callers.
func (s *ClientStore) Authenticate(id, secret string) (APIClient, bool) {
	s.mu.Lock()
	client, ok := s.clients[id]
	s.mu.Unlock()
	match := subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(client.Secret)) == 1
	if !ok || client.Disabled || !match {
		return APIClient{}, false
	}
	return client.public(), true
}

// IssueToken mints an access token for an enabled client. An empty request
// grants every scope the client holds; otherwise each requested scope must be
// one of them.
func (s *ClientStore) IssueToken(id string, requested []Scope) (string, []Scope, error) {
	token, err := newCredential(accessTokenPrefix)
	if err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[id]
	if !ok || client.Disabled {
		return "", nil, ErrClientNotFound
	}
	granted := slices.Clone(client.Scopes)
	if len(requested) > 0 {
		for _, scope := range requested {
			if !slices.Contains(client.Scopes, scope) {
				return "", nil, ErrScopeNotGranted
			}
		}
		granted = slices.Clone(requested)
	}
	// Sweep expired tokens and bound what one client can hold: at the limit
	// its token closest to expiry gives way to the new one.
	now := s.now()
	held := 0
	var oldest [sha256.Size]byte
	var oldestExpiry time.Time
	for key, issued := range s.tokens {
		switch {
		case !issued.expires.After(now):
			delete(s.tokens, key)
		case issued.clientID == id:
			held++
			if oldestExpiry.IsZero() || issued.expires.Before(oldestExpiry) {
				oldest, oldestExpiry = key, issued.expires
			}
		}
	}
	if held >= maxTokensPerClient {
		delete(s.tokens, oldest)
	}
	s.tokens[tokenHash(token)] = issuedToken{clientID: id, scopes: granted, expires: now.Add(AccessTokenLifetime)}
	return token, slices.Clone(granted), nil
}

// LookupToken resolves an access token to its grant. Expired, revoked, and
// unknown tokens fail with ok=false.
func (s *ClientStore) LookupToken(token string) (Grant, bool) {
	if token == "" {
		return Grant{}, false
	}
	key := tokenHash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	issued, ok := s.tokens[key]
	if !ok {
		return Grant{}, false
	}
	if !issued.expires.After(s.now()) {
		delete(s.tokens, key)
		return Grant{}, false
	}
	return Grant{ClientID: issued.clientID, Scopes: slices.Clone(issued.scopes)}, true
}

// RevokeToken ends a token, but only for the client it was issued to.
func (s *ClientStore) RevokeToken(id, token string) {
	if token == "" {
		return
	}
	key := tokenHash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if issued, ok := s.tokens[key]; ok && issued.clientID == id {
		delete(s.tokens, key)
	}
}

func newCredential(prefix string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + encodeToken(raw), nil
}

// Client secrets are 256-bit values generated here, never chosen by an
// operator. One SHA-256 is therefore enough at rest: there is no low-entropy
// input to stretch, and the token endpoint spends no key-derivation work on
// callers that have not authenticated yet.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return secretHashPrefix + base64.RawStdEncoding.EncodeToString(sum[:])
}

// validSecretHash checks the shape of a stored hash so a damaged client file
// fails at load time instead of leaving a client that can never authenticate.
func validSecretHash(encoded string) bool {
	digest, ok := strings.CutPrefix(encoded, secretHashPrefix)
	if !ok {
		return false
	}
	raw, err := base64.RawStdEncoding.DecodeString(digest)
	return err == nil && len(raw) == sha256.Size
}
