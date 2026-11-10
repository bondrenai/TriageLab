package auth

import (
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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeIdP is a minimal OpenID provider: discovery, JWKS, and a token endpoint
// that mints signed ID tokens from a claims template.
type fakeIdP struct {
	server *httptest.Server
	key    crypto.Signer
	kid    string

	issuer string

	// mutable test knobs
	expectedCode   string
	expectedSecret string
	nonce          string
	claims         map[string]any
	jwks           func(w http.ResponseWriter)

	lastTokenForm url.Values
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key, kid: "key-1", expectedCode: "code-1", claims: map[string]any{
		"sub": "u123", "preferred_username": "pat", "groups": []string{"everyone"},
	}}
	idp.jwks = func(w http.ResponseWriter) {
		public := idp.key.Public().(*rsa.PublicKey)
		writeJSONForTest(w, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": idp.kid, "use": "sig", "alg": "RS256", "e": "AQAB",
			"n": base64.RawURLEncoding.EncodeToString(public.N.Bytes()),
		}}})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, map[string]string{
			"issuer":                 idp.issuer,
			"authorization_endpoint": idp.issuer + "/authorize",
			"token_endpoint":         idp.issuer + "/token",
			"jwks_uri":               idp.issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) { idp.jwks(w) })
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		idp.lastTokenForm = r.PostForm
		if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != idp.expectedCode {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if idp.expectedSecret != "" && r.PostForm.Get("client_secret") != idp.expectedSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		claims := map[string]any{}
		for k, v := range idp.claims {
			claims[k] = v
		}
		// Registered claims default to valid values; tests override them by
		// setting the key (including to nil, which marshals as JSON null).
		if _, ok := claims["iss"]; !ok {
			claims["iss"] = idp.issuer
		}
		if _, ok := claims["aud"]; !ok {
			claims["aud"] = "triagelab-client"
		}
		if _, ok := claims["exp"]; !ok {
			claims["exp"] = time.Now().Add(time.Hour).Unix()
		}
		if _, ok := claims["iat"]; !ok {
			claims["iat"] = time.Now().Unix()
		}
		if _, ok := claims["nonce"]; !ok {
			claims["nonce"] = idp.nonce
		}
		writeJSONForTest(w, map[string]string{"id_token": mintJWT(t, idp.key, idp.kid, claims)})
	})
	idp.server = httptest.NewServer(mux)
	idp.issuer = idp.server.URL
	t.Cleanup(idp.server.Close)
	return idp
}

func writeJSONForTest(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// mintJWT signs claims with the test key. The alg header follows the key type
// so both RS256 and ES256 paths can be exercised.
func mintJWT(t *testing.T, signer crypto.Signer, kid string, claims map[string]any) string {
	t.Helper()
	algorithm := "RS256"
	switch signer.(type) {
	case *ecdsa.PrivateKey:
		algorithm = "ES256"
	}
	header, _ := json.Marshal(map[string]string{"alg": algorithm, "kid": kid, "typ": "JWT"})
	body, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signingInput))
	var signature []byte
	switch key := signer.(type) {
	case *rsa.PrivateKey:
		signature, _ = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	case *ecdsa.PrivateKey:
		r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		signature = make([]byte, 64)
		r.FillBytes(signature[:32])
		s.FillBytes(signature[32:])
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func baseOIDCConfig(idp *fakeIdP) OIDCConfig {
	return OIDCConfig{
		Issuer:      idp.issuer,
		ClientID:    "triagelab-client",
		RedirectURI: "http://127.0.0.1:8080/api/auth/oidc/callback",
		RoleMap:     map[string]Role{"triagelab-admins": RoleAdmin, "triagelab-users": RoleUser},
	}
}

func TestOIDCConfigValidation(t *testing.T) {
	base := baseOIDCConfig(newFakeIdP(t))
	if _, err := base.Validate(""); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, mutate := range map[string]func(*OIDCConfig){
		"issuer":         func(c *OIDCConfig) { c.Issuer = "" },
		"http-issuer":    func(c *OIDCConfig) { c.Issuer = "http://idp.example.com" },
		"query-issuer":   func(c *OIDCConfig) { c.Issuer = "https://idp.example.com/?x=1" },
		"client-id":      func(c *OIDCConfig) { c.ClientID = "" },
		"both-roles":     func(c *OIDCConfig) { c.RoleClaim = "role" },
		"bad-role":       func(c *OIDCConfig) { c.RoleMap["x"] = Role("root") },
		"bad-default":    func(c *OIDCConfig) { c.DefaultRole = Role("owner") },
		"bad-redirect":   func(c *OIDCConfig) { c.RedirectURI = "https://idp.example.com/cb#frag" },
		"http-redirect":  func(c *OIDCConfig) { c.RedirectURI = "http://remote.example.com/cb" },
		"bad-secret-env": func(c *OIDCConfig) { c.ClientSecretEnv = "BAD NAME" },
	} {
		config := base
		mutate(&config)
		if _, err := config.Validate(""); err == nil {
			t.Fatalf("%s: invalid config accepted", name)
		}
	}
	config := OIDCConfig{Issuer: "https://idp.example.com", ClientID: "c"}
	normalized, err := config.Validate("http://127.0.0.1:8080/")
	if err != nil {
		t.Fatal(err)
	}
	if normalized.RedirectURI != "http://127.0.0.1:8080/api/auth/oidc/callback" ||
		normalized.UsernameClaim != "preferred_username" || normalized.GroupsClaim != "groups" ||
		normalized.DefaultRole != RoleViewer || strings.Join(normalized.Scopes, " ") != "openid profile email" {
		t.Fatalf("defaults wrong: %+v", normalized)
	}
	if _, err := config.Validate(""); err == nil {
		t.Fatal("missing redirect base accepted")
	}
}

// pendingNonce reads the pending nonce so tests can embed it in minted tokens.
func pendingNonce(p *OIDCProvider, state string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pending[state].nonce
}

// beginLogin creates a provider and pending login, records the nonce for the
// fake IdP, and returns the provider with the authorize URL and state.
func beginLogin(t *testing.T, idp *fakeIdP, config OIDCConfig) (*OIDCProvider, string, string) {
	t.Helper()
	provider, err := NewOIDCProvider(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	target, state, err := provider.BeginLogin()
	if err != nil {
		t.Fatal(err)
	}
	idp.nonce = pendingNonce(provider, state)
	idp.claims = map[string]any{"sub": "u123", "preferred_username": "pat"}
	return provider, target, state
}

func TestOIDCAuthorizationCodeFlow(t *testing.T) {
	idp := newFakeIdP(t)
	config, err := baseOIDCConfig(idp).Validate("")
	if err != nil {
		t.Fatal(err)
	}
	provider, target, state := beginLogin(t, idp, config)
	authorize, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	query := authorize.Query()
	if query.Get("client_id") != "triagelab-client" || query.Get("response_type") != "code" ||
		query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" ||
		query.Get("state") != state || query.Get("nonce") == "" ||
		query.Get("redirect_uri") != "http://127.0.0.1:8080/api/auth/oidc/callback" ||
		!strings.Contains(query.Get("scope"), "openid") {
		t.Fatalf("authorize URL missing parameters: %s", target)
	}
	challenge := query.Get("code_challenge")

	idp.claims["groups"] = []string{"everyone", "triagelab-users"}
	identity, err := provider.FinishLogin(context.Background(), nil, state, idp.expectedCode)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Username != "pat" || identity.Role != RoleUser || identity.Via != "oidc" {
		t.Fatalf("identity: %+v", identity)
	}
	// The PKCE verifier presented at the token endpoint matches the challenge
	// from the authorization URL.
	verifierDigest := sum256(idp.lastTokenForm.Get("code_verifier"))
	if base64.RawURLEncoding.EncodeToString(verifierDigest[:]) != challenge {
		t.Fatal("PKCE verifier does not match challenge")
	}

	// State values are one-time use.
	if _, err = provider.FinishLogin(context.Background(), nil, state, idp.expectedCode); !errors.Is(err, ErrOIDCState) {
		t.Fatalf("replayed state accepted: %v", err)
	}
	// Unknown states never authenticate.
	if _, err = provider.FinishLogin(context.Background(), nil, "forged-state", idp.expectedCode); !errors.Is(err, ErrOIDCState) {
		t.Fatalf("forged state accepted: %v", err)
	}
}

func TestOIDCRoleMapping(t *testing.T) {
	idp := newFakeIdP(t)
	cases := []struct {
		name   string
		claims map[string]any
		config func(*OIDCConfig)
		want   Role
	}{
		{"group-admin", map[string]any{"groups": []string{"triagelab-admins", "everyone"}}, nil, RoleAdmin},
		{"group-string", map[string]any{"groups": "triagelab-users everyone"}, nil, RoleUser},
		{"unmatched-default", map[string]any{"groups": []string{"everyone"}}, nil, RoleViewer},
		{"no-groups-claim", map[string]any{}, nil, RoleViewer},
		{"direct-claim", map[string]any{"workspace_role": "admin"}, func(c *OIDCConfig) { c.RoleClaim = "workspace_role"; c.RoleMap = nil }, RoleAdmin},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			config, err := baseOIDCConfig(idp).Validate("")
			if err != nil {
				t.Fatal(err)
			}
			if test.config != nil {
				test.config(&config)
			}
			provider, _, state := beginLogin(t, idp, config)
			idp.claims = map[string]any{"sub": "u123", "preferred_username": "pat"}
			for claim, value := range test.claims {
				idp.claims[claim] = value
			}
			identity, err := provider.FinishLogin(context.Background(), nil, state, idp.expectedCode)
			if err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			if identity.Role != test.want {
				t.Fatalf("%s: role %q want %q", test.name, identity.Role, test.want)
			}
		})
	}
}

func TestOIDCTokenRejection(t *testing.T) {
	idp := newFakeIdP(t)
	config, err := baseOIDCConfig(idp).Validate("")
	if err != nil {
		t.Fatal(err)
	}

	newLogin := func() (*OIDCProvider, string) {
		provider, _, state := beginLogin(t, idp, config)
		return provider, state
	}
	finish := func(provider *OIDCProvider, state string) error {
		_, err := provider.FinishLogin(context.Background(), nil, state, idp.expectedCode)
		return err
	}

	provider, state := newLogin()
	idp.claims["iss"] = "https://someone-else.example.com"
	if err := finish(provider, state); !errors.Is(err, ErrOIDCToken) {
		t.Fatalf("issuer mismatch accepted: %v", err)
	}

	provider, state = newLogin()
	idp.claims["iss"] = nil
	idp.claims["aud"] = "other-client"
	if err := finish(provider, state); !errors.Is(err, ErrOIDCToken) {
		t.Fatalf("audience mismatch accepted: %v", err)
	}

	provider, state = newLogin()
	idp.claims["aud"] = nil
	idp.claims["exp"] = time.Now().Add(-time.Hour).Unix()
	if err := finish(provider, state); !errors.Is(err, ErrOIDCToken) {
		t.Fatalf("expired token accepted: %v", err)
	}

	provider, state = newLogin()
	idp.claims["exp"] = nil
	idp.claims["nonce"] = "not-the-nonce"
	if err := finish(provider, state); !errors.Is(err, ErrOIDCToken) {
		t.Fatalf("nonce mismatch accepted: %v", err)
	}

	provider, state = newLogin()
	idp.claims["nonce"] = nil
	idp.kid = "rotated-key"
	if err := finish(provider, state); !errors.Is(err, ErrOIDCToken) {
		t.Fatalf("unknown key accepted: %v", err)
	}

	// A wrong authorization code fails at the exchange itself.
	provider, state = newLogin()
	idp.kid = "key-1"
	if _, err := provider.FinishLogin(context.Background(), nil, state, "wrong-code"); err == nil {
		t.Fatal("bad authorization code accepted")
	}
}

func TestOIDCRejectsNamesReservedForAPIClients(t *testing.T) {
	idp := newFakeIdP(t)
	config, err := baseOIDCConfig(idp).Validate("")
	if err != nil {
		t.Fatal(err)
	}
	// Every claim that can supply the username is covered, including the
	// fallbacks used when the configured claim is absent.
	for name, claims := range map[string]map[string]any{
		"username-claim": {"sub": "u123", "preferred_username": "client:billing"},
		"email-fallback": {"sub": "u123", "email": "client:billing"},
		"sub-fallback":   {"sub": "client:billing"},
		"padded":         {"sub": "u123", "preferred_username": "  client:billing  "},
	} {
		provider, _, state := beginLogin(t, idp, config)
		idp.claims = claims
		if _, err := provider.FinishLogin(context.Background(), nil, state, idp.expectedCode); !errors.Is(err, ErrOIDCToken) {
			t.Fatalf("%s signed in under a reserved API client name: %v", name, err)
		}
	}
	// A colon that does not start the name is an ordinary account.
	provider, _, state := beginLogin(t, idp, config)
	idp.claims = map[string]any{"sub": "u123", "preferred_username": "team:client:pat"}
	identity, err := provider.FinishLogin(context.Background(), nil, state, idp.expectedCode)
	if err != nil || identity.Username != "team:client:pat" {
		t.Fatalf("ordinary colon name rejected: %+v %v", identity, err)
	}
}

func TestOIDCClientSecretSent(t *testing.T) {
	idp := newFakeIdP(t)
	idp.expectedSecret = "s3cret"
	config, err := baseOIDCConfig(idp).Validate("")
	if err != nil {
		t.Fatal(err)
	}
	config.ClientSecretEnv = "TL_TEST_SECRET"
	provider, _, state := beginLogin(t, idp, config)
	if _, err = provider.FinishLogin(context.Background(), func(name string) string {
		if name != "TL_TEST_SECRET" {
			t.Fatalf("read wrong env %q", name)
		}
		return idp.expectedSecret
	}, state, idp.expectedCode); err != nil {
		t.Fatal(err)
	}
	if idp.lastTokenForm.Get("client_secret") != idp.expectedSecret {
		t.Fatal("client secret missing from token request")
	}
}

func TestOIDCDiscoveryMismatch(t *testing.T) {
	idp := newFakeIdP(t)
	config, err := baseOIDCConfig(idp).Validate("")
	if err != nil {
		t.Fatal(err)
	}
	config.Issuer = idp.issuer + "/other-realm"
	if _, err = NewOIDCProvider(context.Background(), config); err == nil {
		t.Fatal("issuer mismatch accepted at discovery")
	}
}

func TestOIDCES256Flow(t *testing.T) {
	idp := newFakeIdP(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	idp.key = key
	idp.kid = "ec-key-1"
	idp.jwks = func(w http.ResponseWriter) {
		public := key.PublicKey
		writeJSONForTest(w, map[string]any{"keys": []any{map[string]any{
			"kty": "EC", "kid": idp.kid, "use": "sig", "alg": "ES256", "crv": "P-256",
			"x": base64.RawURLEncoding.EncodeToString(public.X.FillBytes(make([]byte, 32))),
			"y": base64.RawURLEncoding.EncodeToString(public.Y.FillBytes(make([]byte, 32))),
		}}})
	}
	config, err := baseOIDCConfig(idp).Validate("")
	if err != nil {
		t.Fatal(err)
	}
	provider, _, state := beginLogin(t, idp, config)
	identity, err := provider.FinishLogin(context.Background(), nil, state, idp.expectedCode)
	if err != nil {
		t.Fatalf("ES256 verification failed: %v", err)
	}
	if identity.Username != "pat" {
		t.Fatalf("identity: %+v", identity)
	}
}

func TestClaimGroups(t *testing.T) {
	if got := claimGroups([]any{"a", 3, "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("array groups: %v", got)
	}
	if got := claimGroups("a b\tc"); len(got) != 3 {
		t.Fatalf("string groups: %v", got)
	}
	if got := claimGroups(nil); got != nil {
		t.Fatalf("nil groups: %v", got)
	}
	if got := claimGroups(42); got != nil {
		t.Fatalf("numeric groups: %v", got)
	}
	// Zitadel's project roles claim is an object keyed by role name.
	if got := claimGroups(map[string]any{"triagelab-admin": map[string]any{"org": "org"}, "": nil}); len(got) != 1 || got[0] != "triagelab-admin" {
		t.Fatalf("object groups: %v", got)
	}
}
