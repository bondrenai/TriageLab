package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// The OAuth surface for API clients is the client credentials grant and token
// revocation. There is no authorization endpoint, refresh token, or OpenID
// Connect provider.
const (
	OAuthTokenPath      = "/oauth/token"
	OAuthRevocationPath = "/oauth/revoke"

	oauthMaxBody = 8 << 10
)

// NewOAuthHandler serves the OAuth 2.0 client credentials grant (RFC 6749
// section 4.4) and token revocation (RFC 7009) for the clients in the store.
//
// zitadel/oidc owns the protocol: form decoding, how client credentials may be
// presented, grant dispatch, and error responses. The store owns credentials
// and tokens; the library's own token formats and storage are not used.
func NewOAuthHandler(clients *ClientStore) http.Handler {
	protocol := op.RegisterServer(&oauthServer{clients: clients}, op.Endpoints{
		Token:      op.NewEndpoint(OAuthTokenPath),
		Revocation: op.NewEndpoint(OAuthRevocationPath),
	},
		// The library default answers every origin with credentials allowed.
		op.WithServerCORSOptions(nil))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		switch {
		case r.URL.Path != OAuthTokenPath && r.URL.Path != OAuthRevocationPath:
			refuse(w, r, http.StatusNotFound, "unknown OAuth endpoint")
		case r.Method != http.MethodPost:
			w.Header().Set("Allow", http.MethodPost)
			refuse(w, r, http.StatusMethodNotAllowed, "use POST")
		case r.URL.RawQuery != "":
			// The library would also read parameters from the URL, where
			// credentials reach logs and histories (RFC 6749 section 2.3.1).
			refuse(w, r, http.StatusBadRequest, "send parameters in the request body, not the URL")
		default:
			r.Body = http.MaxBytesReader(w, r.Body, oauthMaxBody)
			protocol.ServeHTTP(basicChallenge{w}, r)
		}
	})
}

func refuse(w http.ResponseWriter, r *http.Request, status int, description string) {
	op.WriteError(w, r, op.NewStatusError(oidc.ErrInvalidRequest().WithDescription("%s", description), status), nil)
}

// basicChallenge adds the challenge RFC 7235 requires on a 401, which the
// library omits when it rejects client credentials.
type basicChallenge struct{ http.ResponseWriter }

func (w basicChallenge) WriteHeader(status int) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="TriageLab API clients", charset="UTF-8"`)
	}
	w.ResponseWriter.WriteHeader(status)
}

type oauthServer struct {
	op.UnimplementedServer
	clients *ClientStore
}

// VerifyClient authenticates the caller before the grant and revocation
// handlers run.
func (s *oauthServer) VerifyClient(_ context.Context, r *op.Request[op.ClientCredentials]) (op.Client, error) {
	if r.Data.ClientAssertion != "" {
		return nil, invalidClient("client assertions are not supported; authenticate with the client secret")
	}
	client, ok := s.clients.Authenticate(r.Data.ClientID, r.Data.ClientSecret)
	if !ok {
		return nil, invalidClient("unknown client or wrong client secret")
	}
	return oauthClient{id: client.ID}, nil
}

func invalidClient(description string) error {
	return op.NewStatusError(oidc.ErrInvalidClient().WithDescription("%s", description), http.StatusUnauthorized)
}

func (s *oauthServer) ClientCredentialsExchange(_ context.Context, r *op.ClientRequest[oidc.ClientCredentialsRequest]) (*op.Response, error) {
	requested, err := ParseScopes(r.Data.Scope)
	if err != nil {
		return nil, oidc.ErrInvalidScope().WithDescription("%s", err.Error())
	}
	token, granted, err := s.clients.IssueToken(r.Client.GetID(), requested)
	switch {
	case errors.Is(err, ErrScopeNotGranted):
		return nil, oidc.ErrInvalidScope().WithDescription("the client is not granted every requested scope")
	case errors.Is(err, ErrClientNotFound):
		return nil, invalidClient("unknown client or wrong client secret")
	case err != nil:
		return nil, oidc.ErrServerError().WithParent(err).WithDescription("could not issue an access token")
	}
	scope := make(oidc.SpaceDelimitedArray, len(granted))
	for i, name := range granted {
		scope[i] = string(name)
	}
	return op.NewResponse(&oidc.AccessTokenResponse{
		AccessToken: token,
		TokenType:   oidc.BearerToken,
		ExpiresIn:   uint64(AccessTokenLifetime / time.Second),
		Scope:       scope,
	}), nil
}

// Revocation ends one of the caller's own tokens. An unknown token, or one
// issued to another client, is not an error and stays untouched (RFC 7009
// section 2.2), so the endpoint reveals nothing about other clients' tokens.
func (s *oauthServer) Revocation(_ context.Context, r *op.ClientRequest[oidc.RevocationRequest]) (*op.Response, error) {
	s.clients.RevokeToken(r.Client.GetID(), r.Data.Token)
	return op.NewResponse(nil), nil
}

// oauthClient is the library's view of an authenticated API client: a
// confidential client limited to the client credentials grant.
type oauthClient struct{ id string }

func (c oauthClient) GetID() string                     { return c.id }
func (oauthClient) RedirectURIs() []string              { return nil }
func (oauthClient) PostLogoutRedirectURIs() []string    { return nil }
func (oauthClient) ApplicationType() op.ApplicationType { return op.ApplicationTypeWeb }
func (oauthClient) AuthMethod() oidc.AuthMethod         { return oidc.AuthMethodBasic }
func (oauthClient) ResponseTypes() []oidc.ResponseType  { return nil }
func (oauthClient) GrantTypes() []oidc.GrantType {
	return []oidc.GrantType{oidc.GrantTypeClientCredentials}
}
func (oauthClient) LoginURL(string) string              { return "" }
func (oauthClient) AccessTokenType() op.AccessTokenType { return op.AccessTokenTypeBearer }
func (oauthClient) IDTokenLifetime() time.Duration      { return 0 }
func (oauthClient) DevMode() bool                       { return false }
func (oauthClient) RestrictAdditionalIdTokenScopes() func([]string) []string {
	return noAdditionalScopes
}
func (oauthClient) RestrictAdditionalAccessTokenScopes() func([]string) []string {
	return noAdditionalScopes
}
func (oauthClient) IsScopeAllowed(string) bool           { return false }
func (oauthClient) IDTokenUserinfoClaimsAssertion() bool { return false }
func (oauthClient) ClockSkew() time.Duration             { return 0 }

func noAdditionalScopes([]string) []string { return nil }
