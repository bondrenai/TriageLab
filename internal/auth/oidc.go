package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrOIDCState covers missing, expired, or replayed login states; ErrOIDCToken
// covers token exchanges and ID tokens that fail verification.
var (
	ErrOIDCState = errors.New("the sign-in session expired; start again")
	ErrOIDCToken = errors.New("the identity provider returned an unacceptable token")
)

const (
	oidcLoginWindow  = 10 * time.Minute
	oidcJWKSLifetime = 24 * time.Hour
	oidcClockSkew    = 2 * time.Minute
	oidcMaxBody      = 1 << 20
)

// OIDCConfig configures one OpenID Connect provider. The client secret is
// read from an environment variable so it never lives in the JSON file.
type OIDCConfig struct {
	Issuer          string          `json:"issuer"`
	ClientID        string          `json:"client_id"`
	ClientSecretEnv string          `json:"client_secret_env,omitempty"`
	Scopes          []string        `json:"scopes,omitempty"`
	UsernameClaim   string          `json:"username_claim,omitempty"`
	RoleClaim       string          `json:"role_claim,omitempty"`
	GroupsClaim     string          `json:"groups_claim,omitempty"`
	RoleMap         map[string]Role `json:"role_map,omitempty"`
	DefaultRole     Role            `json:"default_role,omitempty"`
	RedirectURI     string          `json:"redirect_uri,omitempty"`
}

func (c OIDCConfig) secretEnv() string {
	if c.ClientSecretEnv == "" {
		return "TRIAGELAB_OIDC_CLIENT_SECRET"
	}
	return c.ClientSecretEnv
}

func (c OIDCConfig) scopes() string {
	return strings.Join(c.Scopes, " ")
}

// Validate normalizes defaults and returns the effective configuration. It
// errors on values that could weaken verification. redirectBase supplies the
// default redirect URI when the operator has not pinned one.
func (c OIDCConfig) Validate(redirectBase string) (OIDCConfig, error) {
	if c.Issuer == "" {
		return c, fmt.Errorf("oidc issuer is required")
	}
	issuer, err := httpsOrLoopbackURL(c.Issuer)
	if err != nil || issuer.RawQuery != "" {
		return c, fmt.Errorf("oidc issuer must be an HTTPS URL (or loopback HTTP) without query or fragment")
	}
	c.Issuer = strings.TrimSuffix(issuer.String(), "/")
	if c.ClientID == "" {
		return c, fmt.Errorf("oidc client_id is required")
	}
	if len(c.Scopes) == 0 {
		c.Scopes = []string{"openid", "profile", "email"}
	}
	hasOpenID := false
	for _, scope := range c.Scopes {
		if scope == "" || len(scope) > 128 || len(c.Scopes) > 16 {
			return c, fmt.Errorf("oidc scopes must be 1-16 nonempty values")
		}
		if scope == "openid" {
			hasOpenID = true
		}
	}
	if !hasOpenID {
		return c, fmt.Errorf("oidc scopes must include openid")
	}
	if c.UsernameClaim == "" {
		c.UsernameClaim = "preferred_username"
	}
	if c.RoleClaim != "" && len(c.RoleMap) > 0 {
		return c, fmt.Errorf("use either oidc role_claim or groups_claim with role_map, not both")
	}
	if c.GroupsClaim == "" {
		c.GroupsClaim = "groups"
	}
	if len(c.RoleMap) > 64 {
		return c, fmt.Errorf("oidc role_map holds at most 64 groups")
	}
	for group, role := range c.RoleMap {
		if group == "" || len(group) > 256 || !role.Valid() {
			return c, fmt.Errorf("oidc role_map entries need a group name and a viewer, user, or admin role")
		}
	}
	if c.DefaultRole == "" {
		c.DefaultRole = RoleViewer
	}
	if !c.DefaultRole.Valid() {
		return c, fmt.Errorf("oidc default_role must be viewer, user, or admin")
	}
	if c.RedirectURI == "" {
		if redirectBase == "" {
			return c, fmt.Errorf("oidc redirect_uri is required when the listener address is unknown")
		}
		c.RedirectURI = strings.TrimSuffix(redirectBase, "/") + "/api/auth/oidc/callback"
	}
	redirect, err := httpsOrLoopbackURL(c.RedirectURI)
	if err != nil || redirect.RawQuery != "" || redirect.Fragment != "" {
		return c, fmt.Errorf("oidc redirect_uri must be an HTTPS or loopback HTTP URL without query or fragment")
	}
	if c.ClientSecretEnv != "" && !validEnvName(c.ClientSecretEnv) {
		return c, fmt.Errorf("oidc client_secret_env must be a valid environment variable name")
	}
	return c, nil
}

func validEnvName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// httpsOrLoopbackURL accepts HTTPS anywhere and plain HTTP only on loopback,
// matching the workspace's transport rules.
func httpsOrLoopbackURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("not a usable URL")
	}
	if u.Scheme == "http" {
		hostname := u.Hostname()
		ip := net.ParseIP(hostname)
		if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("plain HTTP is allowed only on loopback")
		}
	}
	return u, nil
}

// OIDCProvider performs the authorization-code flow with PKCE against one
// issuer. Pending logins and signing keys live in memory only.
type OIDCProvider struct {
	config OIDCConfig
	client *http.Client

	mu          sync.Mutex
	pending     map[string]pendingLogin
	keys        map[string]crypto.PublicKey
	keysFetched time.Time

	authorizationEndpoint string
	tokenEndpoint         string
	jwksURL               string
}

type pendingLogin struct {
	nonce    string
	verifier string
	expires  time.Time
}

// NewOIDCProvider resolves the issuer's discovery document once so that a bad
// provider configuration fails at startup, not at first sign-in. The config
// must already be validated and normalized.
func NewOIDCProvider(ctx context.Context, config OIDCConfig) (*OIDCProvider, error) {
	issuer, err := httpsOrLoopbackURL(config.Issuer)
	if err != nil || config.ClientID == "" {
		return nil, fmt.Errorf("validate the oidc configuration first")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	discoveryURL := strings.TrimSuffix(issuer.String(), "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc discovery returned %s", resp.Status)
	}
	var document struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, oidcMaxBody)).Decode(&document); err != nil {
		return nil, fmt.Errorf("oidc discovery document: %w", err)
	}
	if document.Issuer != strings.TrimSuffix(config.Issuer, "/") {
		return nil, fmt.Errorf("oidc discovery issuer %q does not match the configured issuer", document.Issuer)
	}
	for name, endpoint := range map[string]string{
		"authorization_endpoint": document.AuthorizationEndpoint,
		"token_endpoint":         document.TokenEndpoint,
		"jwks_uri":               document.JWKSURI,
	} {
		u, err := httpsOrLoopbackURL(endpoint)
		if err != nil || u.RawQuery != "" {
			return nil, fmt.Errorf("oidc discovery %s must be an HTTPS URL without query or fragment", name)
		}
	}
	return &OIDCProvider{
		config:                config,
		client:                client,
		pending:               map[string]pendingLogin{},
		authorizationEndpoint: document.AuthorizationEndpoint,
		tokenEndpoint:         document.TokenEndpoint,
		jwksURL:               document.JWKSURI,
	}, nil
}

// BeginLogin starts an authorization request and returns the provider URL
// plus the opaque state value that must round-trip through the browser
// cookie. A fresh PKCE verifier and nonce are held server-side.
func (p *OIDCProvider) BeginLogin() (target, state string, err error) {
	verifier := make([]byte, 48)
	if _, err = rand.Read(verifier); err != nil {
		return "", "", err
	}
	rawState := make([]byte, 32)
	if _, err = rand.Read(rawState); err != nil {
		return "", "", err
	}
	nonce := make([]byte, 24)
	if _, err = rand.Read(nonce); err != nil {
		return "", "", err
	}
	state = encodeToken(rawState)
	verifierText := encodeToken(verifier)
	nonceText := encodeToken(nonce)
	verifierDigest := sum256(verifierText)
	challenge := base64.RawURLEncoding.EncodeToString(verifierDigest[:])

	p.mu.Lock()
	now := time.Now()
	p.pending[state] = pendingLogin{nonce: nonceText, verifier: verifierText, expires: now.Add(oidcLoginWindow)}
	for key, login := range p.pending {
		if login.expires.Before(now) {
			delete(p.pending, key)
		}
	}
	p.mu.Unlock()

	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.config.ClientID},
		"redirect_uri":          {p.config.RedirectURI},
		"scope":                 {p.config.scopes()},
		"state":                 {state},
		"nonce":                 {nonceText},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return p.authorizationEndpoint + "?" + values.Encode(), state, nil
}

// FinishLogin exchanges the authorization code and verifies the resulting ID
// token before creating a workspace identity. Each state may be used once.
func (p *OIDCProvider) FinishLogin(ctx context.Context, secretReader func(string) string, state, code string) (Identity, error) {
	p.mu.Lock()
	login, ok := p.pending[state]
	if ok {
		delete(p.pending, state)
	}
	p.mu.Unlock()
	if !ok || time.Now().After(login.expires) {
		return Identity{}, ErrOIDCState
	}
	claims, err := p.exchange(ctx, secretReader, login, code)
	if err != nil {
		return Identity{}, err
	}
	return p.identityFrom(claims, login.nonce)
}

func (p *OIDCProvider) exchange(ctx context.Context, secretReader func(string) string, login pendingLogin, code string) (map[string]any, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.config.RedirectURI},
		"client_id":     {p.config.ClientID},
		"code_verifier": {login.verifier},
	}
	if secretReader != nil {
		if secret := strings.TrimSpace(secretReader(p.config.secretEnv())); secret != "" {
			form.Set("client_secret", secret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oidc token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, oidcMaxBody))
	if err != nil {
		return nil, ErrOIDCToken
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: token endpoint returned %s", ErrOIDCToken, resp.Status)
	}
	var tokens struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokens); err != nil || tokens.IDToken == "" {
		return nil, fmt.Errorf("%w: token response has no id_token", ErrOIDCToken)
	}
	claims, err := p.verifyIDToken(ctx, tokens.IDToken)
	if err != nil {
		return nil, err
	}
	if nonce, ok := claims["nonce"].(string); !ok || nonce != login.nonce {
		return nil, fmt.Errorf("%w: nonce mismatch", ErrOIDCToken)
	}
	return claims, nil
}

// identityFrom maps verified claims to a workspace identity and role.
func (p *OIDCProvider) identityFrom(claims map[string]any, nonce string) (Identity, error) {
	username := ""
	for _, claim := range []string{p.config.UsernameClaim, "email", "sub"} {
		if value, ok := claims[claim].(string); ok && strings.TrimSpace(value) != "" {
			username = strings.TrimSpace(value)
			break
		}
	}
	if username == "" {
		return Identity{}, fmt.Errorf("%w: no usable username claim", ErrOIDCToken)
	}
	if len(username) > 64 {
		username = username[:64]
	}
	role, err := p.roleFrom(claims)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Username: username, Role: role, Via: "oidc"}, nil
}

func (p *OIDCProvider) roleFrom(claims map[string]any) (Role, error) {
	if p.config.RoleClaim != "" {
		value, ok := claims[p.config.RoleClaim].(string)
		if !ok {
			return "", fmt.Errorf("%w: id token has no %s claim", ErrOIDCToken, p.config.RoleClaim)
		}
		role, err := ParseRole(strings.TrimSpace(strings.ToLower(value)))
		if err != nil {
			return "", fmt.Errorf("%w: %s claim %q is not a workspace role", ErrOIDCToken, p.config.RoleClaim, value)
		}
		return role, nil
	}
	groups := claimGroups(claims[p.config.GroupsClaim])
	var matched []Role
	for _, group := range groups {
		if role, ok := p.config.RoleMap[group]; ok {
			matched = append(matched, role)
		}
	}
	if len(matched) == 0 {
		return p.config.DefaultRole, nil
	}
	return Highest(matched), nil
}

// claimGroups accepts the common group claim shapes: a JSON array of
// strings, a space-separated string, and an object keyed by group name
// (Zitadel's project roles claim).
func claimGroups(value any) []string {
	switch typed := value.(type) {
	case []any:
		groups := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok && s != "" {
				groups = append(groups, s)
			}
		}
		return groups
	case string:
		return strings.Fields(typed)
	case map[string]any:
		groups := make([]string, 0, len(typed))
		for group := range typed {
			if group != "" {
				groups = append(groups, group)
			}
		}
		return groups
	}
	return nil
}

// verifyIDToken checks the signature against the provider JWKS and the
// registered claims iss, aud, and exp, returning the decoded claim set.
func (p *OIDCProvider) verifyIDToken(ctx context.Context, token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("%w: malformed id token", ErrOIDCToken)
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	headerRaw, err := decodeSegment(parts[0])
	if err != nil || json.Unmarshal(headerRaw, &header) != nil {
		return nil, fmt.Errorf("%w: malformed id token header", ErrOIDCToken)
	}
	if header.Algorithm != "RS256" && header.Algorithm != "ES256" {
		return nil, fmt.Errorf("%w: signing algorithm %q is not allowed", ErrOIDCToken, header.Algorithm)
	}
	claimsRaw, err := decodeSegment(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: malformed id token claims", ErrOIDCToken)
	}
	claims := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(claimsRaw))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return nil, fmt.Errorf("%w: malformed id token claims", ErrOIDCToken)
	}
	if err := p.verifySignature(ctx, header.Algorithm, header.KeyID, parts[0]+"."+parts[1], parts[2]); err != nil {
		return nil, err
	}
	if issuer, ok := claims["iss"].(string); !ok || strings.TrimSuffix(issuer, "/") != strings.TrimSuffix(p.config.Issuer, "/") {
		return nil, fmt.Errorf("%w: issuer mismatch", ErrOIDCToken)
	}
	if !audienceContains(claims["aud"], p.config.ClientID) {
		return nil, fmt.Errorf("%w: audience mismatch", ErrOIDCToken)
	}
	expires, ok := numericClaim(claims, "exp")
	if !ok || time.Now().After(time.Unix(expires, 0).Add(oidcClockSkew)) {
		return nil, fmt.Errorf("%w: id token expired", ErrOIDCToken)
	}
	if issued, ok := numericClaim(claims, "iat"); ok && time.Unix(issued, 0).After(time.Now().Add(oidcClockSkew)) {
		return nil, fmt.Errorf("%w: id token issued in the future", ErrOIDCToken)
	}
	return claims, nil
}

func audienceContains(aud any, clientID string) bool {
	switch typed := aud.(type) {
	case string:
		return typed == clientID
	case []any:
		for _, item := range typed {
			if s, ok := item.(string); ok && s == clientID {
				return true
			}
		}
	}
	return false
}

func numericClaim(claims map[string]any, name string) (int64, bool) {
	number, ok := claims[name].(json.Number)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	return value, err == nil
}

func (p *OIDCProvider) verifySignature(ctx context.Context, algorithm, keyID, signingInput, signatureSegment string) error {
	signature, err := decodeSegment(signatureSegment)
	if err != nil {
		return fmt.Errorf("%w: malformed signature", ErrOIDCToken)
	}
	digest := sum256(signingInput)
	key, err := p.signingKey(ctx, keyID)
	if err != nil {
		return err
	}
	switch algorithm {
	case "RS256":
		publicKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("%w: key is not an RSA key", ErrOIDCToken)
		}
		if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
			return fmt.Errorf("%w: bad signature", ErrOIDCToken)
		}
	case "ES256":
		publicKey, ok := key.(*ecdsa.PublicKey)
		if !ok || publicKey.Curve != elliptic.P256() {
			return fmt.Errorf("%w: key is not a P-256 key", ErrOIDCToken)
		}
		if len(signature) != 64 {
			return fmt.Errorf("%w: bad signature", ErrOIDCToken)
		}
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])
		if !ecdsa.Verify(publicKey, digest[:], r, s) {
			return fmt.Errorf("%w: bad signature", ErrOIDCToken)
		}
	}
	return nil
}

func (p *OIDCProvider) signingKey(ctx context.Context, keyID string) (crypto.PublicKey, error) {
	p.mu.Lock()
	fresh := p.keys != nil && time.Now().Before(p.keysFetched.Add(oidcJWKSLifetime))
	var key crypto.PublicKey
	if p.keys != nil {
		key = p.keys[keyID]
	}
	p.mu.Unlock()
	if key != nil && fresh {
		return key, nil
	}
	if err := p.refreshKeys(ctx); err != nil {
		if key == nil {
			return nil, err
		}
		return key, nil
	}
	p.mu.Lock()
	key = p.keys[keyID]
	p.mu.Unlock()
	if key == nil {
		return nil, fmt.Errorf("%w: no signing key %q", ErrOIDCToken, keyID)
	}
	return key, nil
}

func (p *OIDCProvider) refreshKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("oidc jwks fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc jwks returned %s", resp.Status)
	}
	var document struct {
		Keys []struct {
			Type        string `json:"kty"`
			KeyID       string `json:"kid"`
			Use         string `json:"use"`
			Algorithm   string `json:"alg"`
			Curve       string `json:"crv"`
			Modulus     string `json:"n"`
			Exponent    string `json:"e"`
			XCoordinate string `json:"x"`
			YCoordinate string `json:"y"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, oidcMaxBody)).Decode(&document); err != nil {
		return fmt.Errorf("oidc jwks document: %w", err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, entry := range document.Keys {
		if entry.KeyID == "" || (entry.Use != "" && entry.Use != "sig") {
			continue
		}
		switch {
		case entry.Type == "RSA" && (entry.Algorithm == "" || entry.Algorithm == "RS256"):
			key, err := rsaKey(entry.Modulus, entry.Exponent)
			if err != nil {
				continue
			}
			keys[entry.KeyID] = key
		case entry.Type == "EC" && entry.Curve == "P-256" && (entry.Algorithm == "" || entry.Algorithm == "ES256"):
			key, err := ecKey(entry.XCoordinate, entry.YCoordinate)
			if err != nil {
				continue
			}
			keys[entry.KeyID] = key
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("oidc jwks has no usable signing keys")
	}
	p.mu.Lock()
	p.keys, p.keysFetched = keys, time.Now()
	p.mu.Unlock()
	return nil
}

func rsaKey(modulus, exponent string) (crypto.PublicKey, error) {
	n, err := decodeSegment(modulus)
	if err != nil {
		return nil, err
	}
	e, err := decodeSegment(exponent)
	if err != nil || len(e) > 8 {
		return nil, fmt.Errorf("bad exponent")
	}
	var exponentValue uint64
	for _, b := range e {
		exponentValue = exponentValue<<8 | uint64(b)
	}
	if exponentValue < 3 || exponentValue&1 == 0 || exponentValue > 1<<31 {
		return nil, fmt.Errorf("bad exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponentValue)}, nil
}

func ecKey(x, y string) (crypto.PublicKey, error) {
	xb, err := decodeSegment(x)
	if err != nil || len(xb) != 32 {
		return nil, fmt.Errorf("bad x coordinate")
	}
	yb, err := decodeSegment(y)
	if err != nil || len(yb) != 32 {
		return nil, fmt.Errorf("bad y coordinate")
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(),
		X: new(big.Int).SetBytes(xb), Y: new(big.Int).SetBytes(yb)}, nil
}

func decodeSegment(segment string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(segment, "="))
}

func sum256(value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(value))
}
