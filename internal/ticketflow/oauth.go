package ticketflow

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	OAuthCallbackPath = "/oauth/zendesk/callback"
	OAuthWindow       = 5 * time.Minute
	oauthFile         = ".zendesk-oauth"
)

var (
	ErrOAuthRequired = errors.New("connect or reconnect Zendesk in Ticket cases")
	ErrOAuthCallback = errors.New("invalid or expired Zendesk authorization")
	ErrOAuthDenied   = errors.New("Zendesk authorization was declined")
	ErrOAuthHost     = errors.New("open Ticket cases at the configured Zendesk callback origin")
	ErrOAuthExchange = errors.New("Zendesk did not grant the requested OAuth access")
	oauthIdentifier  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	oauthToken       = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)
)

type OAuthStatus struct {
	Account      string   `json:"account"`
	WorkspaceURL string   `json:"workspace_url"`
	Connected    bool     `json:"connected"`
	Scopes       []string `json:"scopes"`
}

type oauthPair struct {
	Access         string    `json:"access"`
	Refresh        string    `json:"refresh"`
	Expires        time.Time `json:"expires"`
	RefreshExpires time.Time `json:"refresh_expires"`
}

type oauthRecord struct {
	Account  string     `json:"account"`
	ClientID string     `json:"client_id"`
	Redirect string     `json:"redirect_uri"`
	Scopes   string     `json:"scopes"`
	Pair     *oauthPair `json:"tokens,omitempty"`
}

type oauthAttempt struct {
	state, browser, verifier string
	expires                  time.Time
}

type zendeskOAuth struct {
	mu                                            sync.Mutex
	account, clientID, redirect, host, scope, dir string
	http                                          *http.Client
	now                                           func() time.Time
	attempt                                       oauthAttempt
	pair                                          *oauthPair
}

func openOAuth(config Config, origin, dir string, client *http.Client) (*zendeskOAuth, error) {
	u, _ := url.Parse(config.RedirectURI) // Validated at startup.
	scope := "tickets:read"
	if config.Policy.AutoQuestions {
		scope += " tickets:write"
	}
	o := &zendeskOAuth{account: origin, clientID: config.ClientID, redirect: config.RedirectURI, host: u.Host,
		scope: scope, dir: dir, http: client, now: time.Now}
	if err := o.load(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *zendeskOAuth) load() error {
	f, err := os.OpenFile(filepath.Join(o.dir, oauthFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("could not open Zendesk OAuth state")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("Zendesk OAuth state must be a private regular file")
	}
	var stored oauthRecord
	data, err := io.ReadAll(io.LimitReader(f, 16<<10))
	if err != nil || len(data) >= 16<<10 || json.Unmarshal(data, &stored) != nil {
		return errors.New("invalid Zendesk OAuth state")
	}
	if stored.Account != o.account || stored.ClientID != o.clientID || stored.Redirect != o.redirect || stored.Scopes != o.scope {
		// Changing the allowed operation discards the old grant locally,
		// including when the operator switches back later.
		return o.persist(nil)
	}
	if stored.Pair != nil {
		if !validOAuthToken(stored.Pair.Access) || !validOAuthToken(stored.Pair.Refresh) {
			return errors.New("invalid Zendesk OAuth token state")
		}
		o.pair = stored.Pair
	}
	return nil
}

func (o *zendeskOAuth) persist(pair *oauthPair) error {
	data, err := json.Marshal(oauthRecord{o.account, o.clientID, o.redirect, o.scope, pair})
	if err != nil {
		return ErrStorage
	}
	if _, err := writePrivate(o.dir, oauthFile, data); err != nil {
		return ErrStorage
	}
	return nil
}

func (o *zendeskOAuth) status() OAuthStatus {
	o.mu.Lock()
	defer o.mu.Unlock()
	return OAuthStatus{Account: o.account, WorkspaceURL: "http://" + o.host + "/tickets", Scopes: strings.Fields(o.scope),
		Connected: o.pair != nil && (o.now().Before(o.pair.Expires) || o.now().Before(o.pair.RefreshExpires))}
}

func validOAuthToken(value string) bool { return len(value) <= 4096 && oauthToken.MatchString(value) }

func randomOAuthValue() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (o *zendeskOAuth) begin(host string) (string, string, error) {
	if host != o.host {
		return "", "", ErrOAuthHost
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	state, err := randomOAuthValue()
	if err != nil {
		return "", "", err
	}
	browser, err := randomOAuthValue()
	if err != nil {
		return "", "", err
	}
	verifier, err := randomOAuthValue()
	if err != nil {
		return "", "", err
	}
	o.attempt = oauthAttempt{state, browser, verifier, o.now().Add(OAuthWindow)}
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type": {"code"}, "client_id": {o.clientID}, "redirect_uri": {o.redirect}, "scope": {o.scope},
		"state": {state}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}
	return o.account + "/oauth/authorizations/new?" + query.Encode(), browser, nil
}

func (o *zendeskOAuth) finish(ctx context.Context, host, state, browser, code string, denied bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	a := o.attempt
	if host != o.host || a.state == "" || !o.now().Before(a.expires) ||
		subtle.ConstantTimeCompare([]byte(state), []byte(a.state)) != 1 ||
		subtle.ConstantTimeCompare([]byte(browser), []byte(a.browser)) != 1 {
		return ErrOAuthCallback
	}
	o.attempt = oauthAttempt{}
	if denied {
		return ErrOAuthDenied
	}
	if code == "" || len(code) > 4096 {
		return ErrOAuthCallback
	}
	pair, err := o.exchange(ctx, map[string]any{
		"grant_type": "authorization_code", "code": code, "code_verifier": a.verifier, "redirect_uri": o.redirect,
	})
	if err != nil {
		return err
	}
	if err := o.persist(pair); err != nil {
		o.pair = nil
		return err
	}
	o.pair = pair
	return nil
}

func (o *zendeskOAuth) exchange(ctx context.Context, fields map[string]any) (*oauthPair, error) {
	fields["client_id"], fields["scope"], fields["expires_in"] = o.clientID, o.scope, 1800
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, ErrOAuthExchange
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.account+"/oauth/tokens", bytes.NewReader(body))
	if err != nil {
		return nil, ErrOAuthExchange
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := o.http.Do(req)
	if err != nil {
		return nil, ErrOAuthExchange
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return nil, ErrOAuthExchange
	}
	var result struct {
		Access         string `json:"access_token"`
		Refresh        string `json:"refresh_token"`
		Type           string `json:"token_type"`
		Scope          string `json:"scope"`
		Expires        int64  `json:"expires_in"`
		RefreshExpires int64  `json:"refresh_token_expires_in"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<10))
	if err != nil || len(data) >= 16<<10 || json.Unmarshal(data, &result) != nil ||
		!strings.EqualFold(result.Type, "bearer") || !validOAuthToken(result.Access) || !validOAuthToken(result.Refresh) ||
		result.Expires <= 0 || result.Expires > 172800 || result.RefreshExpires < result.Expires || result.RefreshExpires > 7776000 {
		return nil, ErrOAuthExchange
	}
	scopes := strings.Fields(result.Scope)
	slices.Sort(scopes)
	if !slices.Equal(scopes, strings.Fields(o.scope)) {
		return nil, ErrOAuthExchange
	}
	now := o.now()
	return &oauthPair{result.Access, result.Refresh, now.Add(time.Duration(result.Expires) * time.Second),
		now.Add(time.Duration(result.RefreshExpires) * time.Second)}, nil
}

func (o *zendeskOAuth) access(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := o.pair
	if p == nil {
		return "", ErrOAuthRequired
	}
	now := o.now()
	if now.Add(time.Minute).Before(p.Expires) {
		return p.Access, nil
	}
	if !now.Before(p.RefreshExpires) {
		if now.Before(p.Expires) {
			return p.Access, nil
		}
		return "", ErrOAuthRequired
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// A refresh token can be consumed before the response reaches us. Save a
	// disconnected record first so a crash cannot replay a consumed token.
	if err := o.persist(nil); err != nil {
		return "", err
	}
	o.pair = nil
	pair, err := o.exchange(ctx, map[string]any{"grant_type": "refresh_token", "refresh_token": p.Refresh})
	if err != nil {
		return "", ErrOAuthRequired
	}
	if err := o.persist(pair); err != nil {
		return "", err
	}
	o.pair = pair
	return pair.Access, nil
}

func (o *zendeskOAuth) rejected(token string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pair != nil && o.pair.Access == token {
		o.pair.Expires = time.Time{}
	}
}

func (o *zendeskOAuth) secrets() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pair == nil {
		return nil
	}
	return []string{o.pair.Access, o.pair.Refresh}
}
