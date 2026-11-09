package webserver

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

const zendeskBrowserCookie = "triagelab_zendesk_oauth"

func oauthCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: zendeskBrowserCookie, Value: value, Path: ticketflow.OAuthCallbackPath,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func (s *Server) zendeskAuthorize(w http.ResponseWriter, r *http.Request) {
	target, browser, err := s.tickets.BeginOAuth(r.Host)
	switch {
	case errors.Is(err, ticketflow.ErrOAuthHost):
		fail(w, 409, err.Error())
	case err != nil:
		fail(w, 500, "Could not start Zendesk authorization")
	default:
		http.SetCookie(w, oauthCookie(browser, int(ticketflow.OAuthWindow/time.Second)))
		jsonResponse(w, 200, map[string]string{"url": target})
	}
}

func (s *Server) zendeskCallback(w http.ResponseWriter, r *http.Request) {
	if s.tickets == nil {
		fail(w, 404, "Zendesk is not configured")
		return
	}
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
	if err != nil || cookieErr != nil || len(query["state"]) != 1 || len(query["code"]) > 1 || len(query["error"]) > 1 {
		fail(w, 400, "Invalid Zendesk authorization")
		return
	}
	err = s.tickets.FinishOAuth(r.Context(), r.Host, query.Get("state"), cookie.Value, query.Get("code"), query.Get("error") != "")
	result := "connected"
	switch {
	case errors.Is(err, ticketflow.ErrOAuthCallback):
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
	http.SetCookie(w, oauthCookie("", -1))
	http.Redirect(w, r, "/tickets?zendesk="+result, http.StatusSeeOther)
}
