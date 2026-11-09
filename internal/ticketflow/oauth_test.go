package ticketflow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func oauthConfig(write bool) Config {
	return Config{
		Subdomain: "example", ClientID: "triagelab-local",
		RedirectURI: "http://127.0.0.1:8080" + OAuthCallbackPath,
		Policy:      Policy{Tag: "reproduce", AutoQuestions: write, MaxQuestions: 1},
	}
}

func TestOAuthConsentAndRefresh(t *testing.T) {
	ctx := context.Background()
	config := oauthConfig(true)
	now := time.Now().UTC()
	var mu sync.Mutex
	var challenge string
	var codes, refreshes int
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var form map[string]any
		if r.Method != http.MethodPost || r.URL.Path != "/oauth/tokens" || r.Header.Get("Authorization") != "" ||
			json.NewDecoder(r.Body).Decode(&form) != nil || form["client_id"] != config.ClientID ||
			form["scope"] != "tickets:read tickets:write" || form["expires_in"] != float64(1800) || form["client_secret"] != nil {
			t.Error("unexpected OAuth token request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var access, refresh string
		switch form["grant_type"] {
		case "authorization_code":
			codes++
			verifier, _ := form["code_verifier"].(string)
			hash := sha256.Sum256([]byte(verifier))
			if form["code"] != "fixture-code" || form["redirect_uri"] != config.RedirectURI ||
				len(verifier) < 43 || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
				t.Error("code exchange was not bound to the PKCE request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			access, refresh = "test-access-1", "test-refresh-1"
		case "refresh_token":
			refreshes++
			if form["refresh_token"] != "test-refresh-1" || form["code_verifier"] != nil {
				t.Error("unexpected refresh token exchange")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			access, refresh = "test-access-2", "test-refresh-2"
		default:
			t.Error("unexpected grant")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refresh,
			"scope": "tickets:write tickets:read", "token_type": "bearer", "expires_in": 1800, "refresh_token_expires_in": 2592000})
	}))
	defer provider.Close()
	dir := t.TempDir()
	o, err := openOAuth(config, provider.URL, dir, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	o.now = func() time.Time { return now }
	if _, _, err := o.begin("localhost:8080"); !errors.Is(err, ErrOAuthHost) {
		t.Fatalf("unregistered callback origin: %v", err)
	}
	target, browser, err := o.begin(o.host)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	state := query.Get("state")
	mu.Lock()
	challenge = query.Get("code_challenge")
	mu.Unlock()
	if u.Path != "/oauth/authorizations/new" || query.Get("scope") != "tickets:read tickets:write" ||
		query.Get("code_challenge_method") != "S256" || query.Get("redirect_uri") != config.RedirectURI ||
		len(state) != 43 || browser == state {
		t.Fatal("invalid consent parameters")
	}
	for _, values := range [][2]string{{"wrong-state", browser}, {state, "different-browser"}} {
		if err := o.finish(ctx, o.host, values[0], values[1], "fixture-code", false); !errors.Is(err, ErrOAuthCallback) {
			t.Fatalf("mismatched state or browser accepted: %v", err)
		}
	}
	if err := o.finish(ctx, o.host, state, browser, "fixture-code", false); err != nil {
		t.Fatal(err)
	}
	if err := o.finish(ctx, o.host, state, browser, "fixture-code", false); !errors.Is(err, ErrOAuthCallback) {
		t.Fatalf("replayed callback accepted: %v", err)
	}
	if access, err := o.access(ctx); err != nil || access != "test-access-1" {
		t.Fatalf("authorization did not store usable access: %v", err)
	}
	now = now.Add(30 * time.Minute)
	if access, err := o.access(ctx); err != nil || access != "test-access-2" {
		t.Fatalf("rotation did not update access: %v", err)
	}
	mu.Lock()
	if codes != 1 || refreshes != 1 {
		t.Errorf("got %d code and %d refresh requests", codes, refreshes)
	}
	mu.Unlock()
	info, err := os.Stat(filepath.Join(dir, oauthFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("token state is not private")
	}
	reopened, err := openOAuth(config, provider.URL, dir, provider.Client())
	if err != nil || reopened.pair == nil || reopened.pair.Refresh != "test-refresh-2" {
		t.Fatal("restart lost the rotated pair")
	}
	readOnly, err := openOAuth(oauthConfig(false), provider.URL, dir, provider.Client())
	if err != nil || readOnly.status().Connected {
		t.Fatal("draft policy reused a write grant")
	}
	target, _, err = readOnly.begin(readOnly.host)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(target)
	if u.Query().Get("scope") != "tickets:read" {
		t.Fatal("draft policy requested write scope")
	}
}

func TestOAuthRejectsUnexpectedScopes(t *testing.T) {
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"test-access","refresh_token":"test-refresh","token_type":"Bearer","scope":"read write","expires_in":1800,"refresh_token_expires_in":2592000}`))
	}))
	defer provider.Close()
	o, err := openOAuth(oauthConfig(false), provider.URL, t.TempDir(), provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, browser, err := o.begin(o.host)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.finish(context.Background(), o.host, o.attempt.state, browser, "fixture-code", false); !errors.Is(err, ErrOAuthExchange) || o.status().Connected {
		t.Fatalf("unexpected broader scope was accepted: %v", err)
	}
}

func TestOAuthLostRefreshResponseStopsReplay(t *testing.T) {
	ctx := context.Background()
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer provider.Close()
	config := oauthConfig(false)
	dir := t.TempDir()
	o, err := openOAuth(config, provider.URL, dir, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	o.pair = &oauthPair{Access: "expired-access", Refresh: "previous-refresh", Expires: time.Now().Add(-time.Second), RefreshExpires: time.Now().Add(time.Hour)}
	if err := o.persist(o.pair); err != nil {
		t.Fatal(err)
	}
	if _, err := o.access(ctx); !errors.Is(err, ErrOAuthRequired) {
		t.Fatalf("rotation should require reconnection: %v", err)
	}
	reopened, err := openOAuth(config, provider.URL, dir, provider.Client())
	if err != nil || reopened.status().Connected {
		t.Fatal("reopened a possibly consumed refresh token")
	}
	data, err := os.ReadFile(filepath.Join(dir, oauthFile))
	if err != nil || strings.Contains(string(data), "previous-refresh") {
		t.Fatal("previous token remained on disk")
	}
}
