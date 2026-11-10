package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// oauthFixture serves the OAuth endpoints for one client, "ci", that holds
// both scopes.
type oauthFixture struct {
	store   *ClientStore
	handler http.Handler
	secret  string
}

func newOAuthFixture(t *testing.T) oauthFixture {
	t.Helper()
	store := newClientStore(t)
	secret := mustCreateClient(t, store, "ci", ScopeRun, ScopeRead)
	return oauthFixture{store: store, handler: NewOAuthHandler(store), secret: secret}
}

// post sends a form, with HTTP Basic client credentials when id is set.
func (f oauthFixture) post(path string, form url.Values, id, secret string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if id != "" {
		r.SetBasicAuth(id, secret)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
}

func decodeToken(t *testing.T, w *httptest.ResponseRecorder) tokenResponse {
	t.Helper()
	var response tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not JSON: %d %q", w.Code, w.Body)
	}
	return response
}

var clientCredentials = url.Values{"grant_type": {"client_credentials"}}

func TestClientCredentialsGrant(t *testing.T) {
	f := newOAuthFixture(t)
	w := f.post(OAuthTokenPath, clientCredentials, "ci", f.secret)
	token := decodeToken(t, w)
	if w.Code != 200 || token.TokenType != "Bearer" || token.ExpiresIn != int(AccessTokenLifetime/time.Second) ||
		token.Scope != "investigations:run investigations:read" || !strings.HasPrefix(token.AccessToken, accessTokenPrefix) {
		t.Fatalf("token response: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Pragma") != "no-cache" ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("token response headers: %v", w.Header())
	}
	if grant, ok := f.store.LookupToken(token.AccessToken); !ok || grant.ClientID != "ci" {
		t.Fatalf("issued token does not resolve: %+v %v", grant, ok)
	}

	// Credentials in the body are the other method RFC 6749 section 2.3.1 allows.
	body := url.Values{"grant_type": {"client_credentials"}, "client_id": {"ci"}, "client_secret": {f.secret}}
	if w = f.post(OAuthTokenPath, body, "", ""); w.Code != 200 || decodeToken(t, w).AccessToken == "" {
		t.Fatalf("body credentials: %d %s", w.Code, w.Body)
	}

	// A scope request narrows the token, in any order and with repeats.
	narrowed := url.Values{"grant_type": {"client_credentials"}, "scope": {"investigations:read investigations:read"}}
	w = f.post(OAuthTokenPath, narrowed, "ci", f.secret)
	token = decodeToken(t, w)
	if w.Code != 200 || token.Scope != "investigations:read" {
		t.Fatalf("narrowed scope: %d %s", w.Code, w.Body)
	}
	if grant, _ := f.store.LookupToken(token.AccessToken); grant.Allows(ScopeRun) || !grant.Allows(ScopeRead) {
		t.Fatalf("narrowed grant: %+v", grant)
	}
	both := url.Values{"grant_type": {"client_credentials"}, "scope": {"investigations:read investigations:run"}}
	if w = f.post(OAuthTokenPath, both, "ci", f.secret); decodeToken(t, w).Scope != "investigations:run investigations:read" {
		t.Fatalf("both scopes: %d %s", w.Code, w.Body)
	}
}

func TestTokenScopeIsLimitedToTheClientGrant(t *testing.T) {
	f := newOAuthFixture(t)
	readerSecret := mustCreateClient(t, f.store, "reader", ScopeRead)
	for name, scope := range map[string]string{
		"ungranted":      "investigations:run",
		"partly-granted": "investigations:read investigations:run",
		"unknown":        "investigations:read admin",
		"wildcard":       "*",
		"case-sensitive": "Investigations:Read",
	} {
		form := url.Values{"grant_type": {"client_credentials"}, "scope": {scope}}
		w := f.post(OAuthTokenPath, form, "reader", readerSecret)
		if response := decodeToken(t, w); w.Code != 400 || response.Error != "invalid_scope" || response.AccessToken != "" {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// Without a scope parameter the token carries exactly the client's grant.
	w := f.post(OAuthTokenPath, clientCredentials, "reader", readerSecret)
	if token := decodeToken(t, w); w.Code != 200 || token.Scope != "investigations:read" {
		t.Fatalf("default scope: %d %s", w.Code, w.Body)
	}
}

func TestTokenEndpointRejectsBadClients(t *testing.T) {
	f := newOAuthFixture(t)
	wrong := f.post(OAuthTokenPath, clientCredentials, "ci", "tlcs_wrong")
	if response := decodeToken(t, wrong); wrong.Code != 401 || response.Error != "invalid_client" || response.AccessToken != "" {
		t.Fatalf("wrong secret: %d %s", wrong.Code, wrong.Body)
	}
	if challenge := wrong.Header().Get("WWW-Authenticate"); !strings.HasPrefix(challenge, "Basic ") {
		t.Fatalf("401 without a challenge: %q", challenge)
	}
	// An unknown client is indistinguishable from a wrong secret.
	unknown := f.post(OAuthTokenPath, clientCredentials, "ghost", f.secret)
	if unknown.Code != wrong.Code || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown client differs from wrong secret: %d %s", unknown.Code, unknown.Body)
	}
	if err := f.store.SetDisabled("ci", true); err != nil {
		t.Fatal(err)
	}
	disabled := f.post(OAuthTokenPath, clientCredentials, "ci", f.secret)
	if disabled.Code != wrong.Code || disabled.Body.String() != wrong.Body.String() {
		t.Fatalf("disabled client differs from wrong secret: %d %s", disabled.Code, disabled.Body)
	}
	if err := f.store.SetDisabled("ci", false); err != nil {
		t.Fatal(err)
	}

	for name, form := range map[string]url.Values{
		"no-secret":    {"grant_type": {"client_credentials"}, "client_id": {"ci"}},
		"empty-secret": {"grant_type": {"client_credentials"}, "client_id": {"ci"}, "client_secret": {""}},
		"client-assertion": {"grant_type": {"client_credentials"}, "client_id": {"ci"}, "client_assertion": {"e30.e30.sig"},
			"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}},
	} {
		w := f.post(OAuthTokenPath, form, "", "")
		if response := decodeToken(t, w); w.Code != 401 || response.Error != "invalid_client" || response.AccessToken != "" {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

func TestTokenEndpointRejectsMalformedRequests(t *testing.T) {
	f := newOAuthFixture(t)
	refused := func(name string, w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if response := decodeToken(t, w); w.Code != status || response.Error == "" || response.AccessToken != "" {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	refused("no credentials", f.post(OAuthTokenPath, clientCredentials, "", ""), 400)
	refused("missing grant", f.post(OAuthTokenPath, url.Values{}, "ci", f.secret), 400)

	// RFC 6749 section 2.3: one authentication method per request.
	twice := url.Values{"grant_type": {"client_credentials"}, "client_id": {"ci"}, "client_secret": {f.secret}}
	refused("two methods", f.post(OAuthTokenPath, twice, "ci", f.secret), 400)

	// Only the client credentials grant exists, even for a valid client.
	for _, grant := range []string{"authorization_code", "refresh_token", "password", "implicit",
		"urn:ietf:params:oauth:grant-type:device_code", "urn:ietf:params:oauth:grant-type:token-exchange",
		"urn:ietf:params:oauth:grant-type:jwt-bearer"} {
		form := url.Values{"grant_type": {grant}, "code": {"x"}, "redirect_uri": {"http://127.0.0.1/cb"},
			"refresh_token": {"x"}, "device_code": {"x"}, "assertion": {"e30.e30.sig"},
			"subject_token": {"x"}, "subject_token_type": {"urn:ietf:params:oauth:token-type:access_token"}}
		refused(grant, f.post(OAuthTokenPath, form, "ci", f.secret), 400)
	}

	// Parameters in the URL are refused even when they would authenticate.
	query := url.Values{"grant_type": {"client_credentials"}, "client_id": {"ci"}, "client_secret": {f.secret}}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+OAuthTokenPath+"?"+query.Encode(), nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	refused("URL parameters", w, 400)

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		r = httptest.NewRequest(method, "http://127.0.0.1:8080"+OAuthTokenPath, nil)
		r.SetBasicAuth("ci", f.secret)
		w = httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
			t.Fatalf("%s: %d allow=%q", method, w.Code, w.Header().Get("Allow"))
		}
	}

	oversized := url.Values{"grant_type": {"client_credentials"}, "padding": {strings.Repeat("x", oauthMaxBody)}}
	refused("oversized body", f.post(OAuthTokenPath, oversized, "ci", f.secret), 400)

	r = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+OAuthTokenPath,
		strings.NewReader(`{"grant_type":"client_credentials"}`))
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth("ci", f.secret)
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	refused("JSON body", w, 400)

	r = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/authorize", nil)
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	refused("unknown endpoint", w, 404)
}

func TestOAuthEndpointsGrantNoCrossOriginAccess(t *testing.T) {
	f := newOAuthFixture(t)
	for _, method := range []string{http.MethodPost, http.MethodOptions} {
		r := httptest.NewRequest(method, "http://127.0.0.1:8080"+OAuthTokenPath, strings.NewReader(clientCredentials.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://app.example.com")
		r.Header.Set("Access-Control-Request-Method", http.MethodPost)
		r.Header.Set("Access-Control-Request-Headers", "authorization")
		r.SetBasicAuth("ci", f.secret)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		for header := range w.Header() {
			if strings.HasPrefix(header, "Access-Control-") {
				t.Fatalf("%s answered a cross-origin request with %s: %q", method, header, w.Header().Get(header))
			}
		}
	}
}

func TestTokenRevocation(t *testing.T) {
	f := newOAuthFixture(t)
	otherSecret := mustCreateClient(t, f.store, "other", ScopeRead)
	issue := func(id, secret string) string {
		t.Helper()
		w := f.post(OAuthTokenPath, clientCredentials, id, secret)
		token := decodeToken(t, w).AccessToken
		if w.Code != 200 || token == "" {
			t.Fatalf("issue for %s: %d %s", id, w.Code, w.Body)
		}
		return token
	}
	alive := func(token string) bool {
		_, ok := f.store.LookupToken(token)
		return ok
	}
	own, sibling, foreign := issue("ci", f.secret), issue("ci", f.secret), issue("other", otherSecret)

	// Another client's token is left alone, and the answer does not say so.
	if w := f.post(OAuthRevocationPath, url.Values{"token": {foreign}}, "ci", f.secret); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("foreign revocation: %d %q", w.Code, w.Body)
	}
	if !alive(foreign) {
		t.Fatal("a client revoked another client's token")
	}
	if w := f.post(OAuthRevocationPath, url.Values{"token": {"tlat_unknown"}}, "ci", f.secret); w.Code != 200 {
		t.Fatalf("unknown token: %d %s", w.Code, w.Body)
	}

	// The hint is advisory; an access token is found whatever it says.
	form := url.Values{"token": {own}, "token_type_hint": {"refresh_token"}}
	if w := f.post(OAuthRevocationPath, form, "ci", f.secret); w.Code != 200 {
		t.Fatalf("revocation: %d %s", w.Code, w.Body)
	}
	if alive(own) || !alive(sibling) {
		t.Fatalf("revocation ended the wrong tokens: own=%v sibling=%v", alive(own), alive(sibling))
	}

	// Revocation needs the client's credentials and a token.
	w := f.post(OAuthRevocationPath, url.Values{"token": {sibling}}, "ci", "tlcs_wrong")
	if w.Code != 401 || !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic ") || !alive(sibling) {
		t.Fatalf("unauthenticated revocation: %d %s", w.Code, w.Body)
	}
	if w = f.post(OAuthRevocationPath, url.Values{}, "ci", f.secret); w.Code != 400 {
		t.Fatalf("revocation without a token: %d %s", w.Code, w.Body)
	}
}

func TestStandardOAuthClientInterop(t *testing.T) {
	f := newOAuthFixture(t)
	server := httptest.NewServer(f.handler)
	defer server.Close()

	for name, style := range map[string]oauth2.AuthStyle{
		"auto-detect": oauth2.AuthStyleAutoDetect,
		"basic":       oauth2.AuthStyleInHeader,
		"body":        oauth2.AuthStyleInParams,
	} {
		config := clientcredentials.Config{ClientID: "ci", ClientSecret: f.secret, AuthStyle: style,
			TokenURL: server.URL + OAuthTokenPath, Scopes: []string{string(ScopeRead)}}
		token, err := config.Token(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		remaining := time.Until(token.Expiry)
		if token.TokenType != "Bearer" || !token.Valid() ||
			remaining > AccessTokenLifetime || remaining < AccessTokenLifetime-time.Minute {
			t.Fatalf("%s: token %s valid=%v expires in %v", name, token.TokenType, token.Valid(), remaining)
		}
		grant, ok := f.store.LookupToken(token.AccessToken)
		if !ok || grant.ClientID != "ci" || grant.Allows(ScopeRun) || !grant.Allows(ScopeRead) {
			t.Fatalf("%s: grant %+v %v", name, grant, ok)
		}
	}

	// A standard client sees a wrong secret as the OAuth error it expects.
	bad := clientcredentials.Config{ClientID: "ci", ClientSecret: "tlcs_wrong", AuthStyle: oauth2.AuthStyleInHeader,
		TokenURL: server.URL + OAuthTokenPath}
	_, err := bad.Token(context.Background())
	var retrieve *oauth2.RetrieveError
	if !errors.As(err, &retrieve) || retrieve.ErrorCode != "invalid_client" || retrieve.Response.StatusCode != 401 {
		t.Fatalf("wrong secret through a standard client: %v", err)
	}
}
