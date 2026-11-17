package webserver

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bondrenai/TriageLab/internal/auth"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

const zendeskBrowserCookie = "triagelab_zendesk_oauth"

func oauthCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: zendeskBrowserCookie, Value: value, Path: ticketflow.OAuthCallbackPath,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// zendeskAuthorize starts the browser OAuth flow for one Zendesk connection.
// The cookie carries the connection id with the browser secret so the shared
// callback can route the answer back to this connection.
func (s *Server) zendeskAuthorize(w http.ResponseWriter, r *http.Request, conn *ticketConn) {
	target, browser, err := conn.flow.BeginOAuth(r.Host)
	switch {
	case errors.Is(err, ticketflow.ErrOAuthHost):
		fail(w, 409, err.Error())
	case err != nil:
		fail(w, 500, "Could not start Zendesk authorization")
	default:
		actor := actorFrom(r.Context())
		s.rememberOAuthInitiator(conn.id+"."+browser, actor)
		s.recordAudit(actor, "ticket.oauth.start", conn.id, "ok", "")
		http.SetCookie(w, oauthCookie(conn.id+"."+browser, int(ticketflow.OAuthWindow/time.Second)))
		jsonResponse(w, 200, map[string]string{"url": target})
	}
}

func (s *Server) zendeskCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "Method not allowed")
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		fail(w, 400, "Invalid Zendesk authorization")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	cookie, cookieErr := r.Cookie(zendeskBrowserCookie)
	id, browser, splitErr := splitOAuthCookie(cookie)
	if err != nil || cookieErr != nil || splitErr != nil || len(query["state"]) != 1 || len(query["code"]) > 1 || len(query["error"]) > 1 {
		fail(w, 400, "Invalid Zendesk authorization")
		return
	}
	conn := s.ticketConnection(id)
	if conn == nil || conn.flow.Provider() != "zendesk" {
		fail(w, 404, "Zendesk is not configured")
		return
	}
	// The public callback has no session; attribute it to the admin whose
	// browser started this flow.
	initiator := s.takeOAuthInitiator(id + "." + browser)
	err = conn.flow.FinishOAuth(r.Context(), r.Host, query.Get("state"), browser, query.Get("code"), query.Get("error") != "")
	result := "connected"
	switch {
	case errors.Is(err, ticketflow.ErrOAuthCallback):
		s.recordAudit(initiator, "ticket.oauth.finish", id, "denied", "invalid or expired authorization")
		fail(w, 400, "Invalid or expired Zendesk authorization")
		return
	case errors.Is(err, ticketflow.ErrBusy):
		fail(w, 409, "A ticket action is running. Refresh this page to finish connecting Zendesk.")
		return
	case errors.Is(err, ticketflow.ErrOAuthDenied):
		result = "denied"
	case err != nil:
		result = "failed"
	}
	s.recordAudit(initiator, "ticket.oauth.finish", id, result, "")
	http.SetCookie(w, oauthCookie("", -1))
	http.Redirect(w, r, "/tickets?zendesk="+result, http.StatusSeeOther)
}

// splitOAuthCookie separates the connection id prefix from the browser secret.
func splitOAuthCookie(cookie *http.Cookie) (id, browser string, err error) {
	if cookie == nil {
		return "", "", errors.New("missing Zendesk cookie")
	}
	id, browser, found := strings.Cut(cookie.Value, ".")
	if !found || !connectionIDPath.MatchString(id) || browser == "" {
		return "", "", errors.New("invalid Zendesk cookie")
	}
	return id, browser, nil
}

type oauthInitiator struct {
	identity auth.Identity
	expires  time.Time
}

// rememberOAuthInitiator keeps the starting admin in memory for the OAuth
// window, keyed by the connection and the browser-bound secret.
func (s *Server) rememberOAuthInitiator(key string, identity auth.Identity) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	now := time.Now()
	if s.oauthInitiators == nil {
		s.oauthInitiators = map[string]oauthInitiator{}
	}
	for k, v := range s.oauthInitiators {
		if now.After(v.expires) {
			delete(s.oauthInitiators, k)
		}
	}
	if len(s.oauthInitiators) >= maxOAuthInitiators {
		return
	}
	s.oauthInitiators[key] = oauthInitiator{identity: identity, expires: now.Add(ticketflow.OAuthWindow)}
}

// takeOAuthInitiator returns and forgets the identity that started a flow, or
// an anonymous identity when it is unknown or expired.
func (s *Server) takeOAuthInitiator(key string) auth.Identity {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	initiator, ok := s.oauthInitiators[key]
	delete(s.oauthInitiators, key)
	if !ok || time.Now().After(initiator.expires) {
		return auth.Identity{}
	}
	return initiator.identity
}

const maxOAuthInitiators = 64
