package auth

import (
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	store := NewSessionStore()
	token, err := store.Start(Identity{Username: "alice", Role: RoleUser, Via: "password"})
	if err != nil || token == "" {
		t.Fatalf("start: %v %q", err, token)
	}
	if identity, ok := store.Lookup(token); !ok || identity.Username != "alice" || !identity.Role.AtLeast(RoleUser) {
		t.Fatalf("lookup: %v %v", identity, ok)
	}
	if _, ok := store.Lookup(token + "x"); ok {
		t.Fatal("unknown token accepted")
	}
	if _, ok := store.Lookup(""); ok {
		t.Fatal("empty token accepted")
	}
	store.End(token)
	if _, ok := store.Lookup(token); ok {
		t.Fatal("revoked token accepted")
	}
	// Distinct tokens per session.
	second, _ := store.Start(Identity{Username: "alice", Role: RoleUser})
	if second == token {
		t.Fatal("token reuse")
	}
}

func TestSessionExpiry(t *testing.T) {
	store := NewSessionStore()
	token, err := store.Start(Identity{Username: "bob", Role: RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	key := tokenHash(token)
	session := store.sessions[key]
	session.expires = time.Now().Add(-time.Second)
	store.sessions[key] = session
	store.mu.Unlock()
	if _, ok := store.Lookup(token); ok {
		t.Fatal("expired token accepted")
	}
}

func TestLoginLimiterBlocksAfterFailures(t *testing.T) {
	limiter := NewLoginLimiter()
	for i := 0; i < 4; i++ {
		if !limiter.Allowed("carol") {
			t.Fatalf("blocked after %d failures", i)
		}
		limiter.RecordFailure("carol")
	}
	if !limiter.Allowed("carol") {
		t.Fatal("fifth attempt blocked early")
	}
	limiter.RecordFailure("carol")
	if limiter.Allowed("carol") {
		t.Fatal("sixth attempt allowed during lockout")
	}
	if !limiter.Allowed("dave") {
		t.Fatal("lockout leaked to another account")
	}
	limiter.RecordSuccess("carol")
	if !limiter.Allowed("carol") {
		t.Fatal("lockout survived success")
	}
}

func TestRoles(t *testing.T) {
	if !RoleAdmin.AtLeast(RoleUser) || !RoleAdmin.AtLeast(RoleViewer) || !RoleUser.AtLeast(RoleViewer) {
		t.Fatal("rank ordering broken")
	}
	if RoleViewer.AtLeast(RoleUser) || RoleUser.AtLeast(RoleAdmin) {
		t.Fatal("rank escalation broken")
	}
	if Role("root").Valid() || Role("").Valid() {
		t.Fatal("unknown roles valid")
	}
	if _, err := ParseRole("auditor"); err == nil {
		t.Fatal("parsed unknown role")
	}
	if role, err := ParseRole("admin"); err != nil || role != RoleAdmin {
		t.Fatalf("parse admin: %v %v", role, err)
	}
	if Highest([]Role{RoleViewer, RoleUser}) != RoleUser || Highest(nil) != RoleViewer {
		t.Fatal("highest misbehaves")
	}
}
