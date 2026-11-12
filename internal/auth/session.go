package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"net/http"
	"sync"
	"time"
)

// Session lifetime is absolute: a session ends 12 hours after sign-in and all
// sessions end when the process exits, because the store is memory-only.
const (
	SessionLifetime = 12 * time.Hour
	sessionTokenBit = 256
)

// SessionCookie is the browser cookie name for workspace sessions.
const SessionCookie = "triagelab_session"

// Identity is the authenticated actor attached to a session or, for an API
// client, to one request. A client's Username is its ClientActor name and its
// Role is empty, so it passes no role check.
type Identity struct {
	Username string
	Role     Role
	// Via records how the actor authenticated: "password", "oidc", or
	// "oauth" for an API client.
	Via string
}

// SessionStore authenticates browser sessions with random tokens. Only the
// SHA-256 of each token is retained, so process memory does not hold usable
// credentials.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[[sha256.Size]byte]storedSession
}

type storedSession struct {
	identity Identity
	expires  time.Time
}

// NewSessionStore creates an empty store.
func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: map[[sha256.Size]byte]storedSession{}}
}

// Start creates a session and returns the cookie value to send to the browser.
func (s *SessionStore) Start(identity Identity) (string, error) {
	raw := make([]byte, sessionTokenBit/8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := encodeToken(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = map[[sha256.Size]byte]storedSession{}
	}
	now := time.Now()
	s.sessions[tokenHash(token)] = storedSession{identity: identity, expires: now.Add(SessionLifetime)}
	// Sweep expired sessions opportunistically; the map stays small because
	// this workspace has a handful of accounts.
	for key, session := range s.sessions {
		if session.expires.Before(now) {
			delete(s.sessions, key)
		}
	}
	return token, nil
}

// Lookup resolves a cookie value to its identity. Expired or unknown tokens
// fail with ok=false.
func (s *SessionStore) Lookup(token string) (Identity, bool) {
	if token == "" {
		return Identity{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[tokenHash(token)]
	if !ok || time.Now().After(session.expires) {
		return Identity{}, false
	}
	return session.identity, true
}

// End revokes a session token.
func (s *SessionStore) End(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash(token))
}

// Cookie builds the session cookie for a response. The UI is reached over
// plain HTTP through a port forward, so the Secure attribute stays off; the
// server is loopback-only and the cookie is Host-only and SameSite=Strict.
func SessionCookieFor(token string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: maxAge}
}

func encodeToken(raw []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, len(raw)*8/6+1)
	buffer, bits := 0, 0
	for _, b := range raw {
		buffer = buffer<<8 | int(b)
		bits += 8
		for bits >= 6 {
			bits -= 6
			out = append(out, alphabet[(buffer>>bits)&0x3f])
		}
	}
	if bits > 0 {
		out = append(out, alphabet[(buffer<<(6-bits))&0x3f])
	}
	return string(out)
}

func tokenHash(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

// LoginLimiter slows password guessing: after five consecutive failures an
// account is held for a growing delay. It is memory-only and per-process.
type LoginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempt
}

type loginAttempt struct {
	failures    int
	blockedTill time.Time
}

// NewLoginLimiter creates an empty limiter.
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{attempts: map[string]*loginAttempt{}}
}

// Allowed reports whether a login attempt may proceed now.
func (l *LoginLimiter) Allowed(username string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt, ok := l.attempts[username]
	return !ok || time.Now().After(attempt.blockedTill)
}

// RecordFailure extends the lockout after a failed attempt.
func (l *LoginLimiter) RecordFailure(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt, ok := l.attempts[username]
	if !ok {
		attempt = &loginAttempt{}
		if l.attempts == nil {
			l.attempts = map[string]*loginAttempt{}
		}
		l.attempts[username] = attempt
	}
	attempt.failures++
	if attempt.failures >= 5 {
		delay := time.Duration(1<<(attempt.failures-5)) * 30 * time.Second
		if delay > 15*time.Minute {
			delay = 15 * time.Minute
		}
		attempt.blockedTill = time.Now().Add(delay)
	}
}

// RecordSuccess clears failure state for an account.
func (l *LoginLimiter) RecordSuccess(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, username)
}
