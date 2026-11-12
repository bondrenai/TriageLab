package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func newClientStore(t *testing.T) *ClientStore {
	t.Helper()
	store, err := OpenClientStore(filepath.Join(t.TempDir(), "auth", "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustCreateClient(t *testing.T, store *ClientStore, id string, scopes ...Scope) string {
	t.Helper()
	secret, err := store.Create(id, scopes, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestClientStoreLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth", "clients.json")
	store, err := OpenClientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secret := mustCreateClient(t, store, "ci-triage", ScopeRun, ScopeRead)
	if !strings.HasPrefix(secret, clientSecretPrefix) || len(secret) < 40 {
		t.Fatalf("secret shape: %q", secret[:len(clientSecretPrefix)])
	}
	if _, err = store.Create("ci-triage", []Scope{ScopeRun}, "admin"); !errors.Is(err, ErrClientExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	for _, invalid := range []struct {
		id     string
		scopes []Scope
	}{
		{"UPPER", []Scope{ScopeRun}},
		{"has space", []Scope{ScopeRun}},
		{"ok", nil},
		{"ok", []Scope{Scope("root:everything")}},
	} {
		if _, err = store.Create(invalid.id, invalid.scopes, "admin"); err == nil {
			t.Fatalf("accepted client %q with %v", invalid.id, invalid.scopes)
		}
	}

	clients := store.Clients()
	if len(clients) != 1 || clients[0].ID != "ci-triage" || clients[0].CreatedBy != "admin" ||
		!slices.Equal(clients[0].Scopes, []Scope{ScopeRun, ScopeRead}) {
		t.Fatalf("listing: %+v", clients)
	}
	encoded, _ := json.Marshal(clients)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "sha256") {
		t.Fatalf("listing leaks secret material: %s", encoded)
	}

	if client, ok := store.Authenticate("ci-triage", secret); !ok || client.ID != "ci-triage" {
		t.Fatalf("valid secret rejected: %v %v", client, ok)
	}
	if _, ok := store.Authenticate("ci-triage", "tlcs_forged"); ok {
		t.Fatal("wrong secret accepted")
	}
	if _, ok := store.Authenticate("ghost", secret); ok {
		t.Fatal("unknown client accepted")
	}

	// The file holds only the hash, with owner-only permissions.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("client file mode %v", info.Mode().Perm())
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), secret) {
		t.Fatal("plaintext secret persisted")
	}
	if !strings.Contains(string(content), secretHashPrefix) {
		t.Fatalf("no secret hash persisted: %s", content)
	}

	// Rotation invalidates the old secret and shows the new one exactly once.
	rotated, err := store.RotateSecret("ci-triage")
	if err != nil {
		t.Fatal(err)
	}
	if rotated == secret {
		t.Fatal("rotation returned the same secret")
	}
	if _, ok := store.Authenticate("ci-triage", secret); ok {
		t.Fatal("old secret accepted after rotation")
	}
	if _, ok := store.Authenticate("ci-triage", rotated); !ok {
		t.Fatal("rotated secret rejected")
	}
	if err = store.SetDisabled("ci-triage", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate("ci-triage", rotated); ok {
		t.Fatal("disabled client accepted")
	}
	if err = store.SetDisabled("ci-triage", false); err != nil {
		t.Fatal(err)
	}
	if err = store.SetScopes("ci-triage", []Scope{ScopeRead}); err != nil {
		t.Fatal(err)
	}
	if scopes := store.Clients()[0].Scopes; !slices.Equal(scopes, []Scope{ScopeRead}) {
		t.Fatalf("scope change not applied: %v", scopes)
	}
	for _, missing := range []string{"set-scopes", "rotate", "disable"} {
		var err error
		switch missing {
		case "set-scopes":
			err = store.SetScopes(missing, []Scope{ScopeRead})
		case "rotate":
			_, err = store.RotateSecret(missing)
		case "disable":
			err = store.SetDisabled(missing, true)
		}
		if !errors.Is(err, ErrClientNotFound) {
			t.Fatalf("%s on missing client: %v", missing, err)
		}
	}

	// State survives a reopen.
	reopened, err := OpenClientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Authenticate("ci-triage", rotated); !ok {
		t.Fatal("state lost after reopen")
	}
	if err = reopened.Remove("ci-triage"); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Remove("ci-triage"); !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("double remove: %v", err)
	}
	if clients = reopened.Clients(); len(clients) != 0 {
		t.Fatalf("client survived removal: %+v", clients)
	}
}

func TestClientStoreRejectsDamagedFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unknown-field": `{"clients":[{"id":"ci","secret":"sha256$AAAA","scopes":["investigations:run"],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","note":"x"}]}`,
		"plaintext":     `{"clients":[{"id":"ci","secret":"tlcs_plain","scopes":["investigations:run"]}]}`,
		"bad-hash":      `{"clients":[{"id":"ci","secret":"sha256$short","scopes":["investigations:run"]}]}`,
		"unknown-scope": `{"clients":[{"id":"ci","secret":"sha256$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","scopes":["root:everything"]}]}`,
		"no-scopes":     `{"clients":[{"id":"ci","secret":"sha256$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","scopes":[]}]}`,
		"bad-id":        `{"clients":[{"id":"Not A Slug","secret":"sha256$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","scopes":["investigations:run"]}]}`,
		"duplicate":     `{"clients":[{"id":"ci","secret":"sha256$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","scopes":["investigations:run"]},{"id":"ci","secret":"sha256$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","scopes":["investigations:read"]}]}`,
		"trailing":      `{"clients":[]} trailing`,
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenClientStore(path); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// A valid file with the exact hash format still loads.
	digest := hashSecret("tlcs_x")
	valid := `{"clients":[{"id":"ci","secret":"` + digest + `","scopes":["investigations:run"],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]}`
	path := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenClientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate("ci", "tlcs_x"); !ok {
		t.Fatal("valid file rejected")
	}
}

// failingSaves makes the next write fail by putting a directory in the way of
// the atomic rename, and returns a function that restores writability.
func failingSaves(t *testing.T, path string) func() {
	t.Helper()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "blocker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFailedSaveLeavesNoClientBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth", "clients.json")
	store, err := OpenClientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restore := failingSaves(t, path)
	if _, err = store.Create("ci", []Scope{ScopeRun}, "admin"); err == nil {
		t.Fatal("create succeeded through a blocked file")
	}
	if clients := store.Clients(); len(clients) != 0 {
		t.Fatalf("client kept in memory after a failed save: %+v", clients)
	}
	restore()
	secret := mustCreateClient(t, store, "ci", ScopeRun)
	token, _, err := store.IssueToken("ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	restore = failingSaves(t, path)
	if _, err = store.RotateSecret("ci"); err == nil {
		t.Fatal("rotation succeeded through a blocked file")
	}
	// The old secret and its tokens survive a failed rotation, because the
	// grant is only revoked after the file agrees.
	if _, ok := store.Authenticate("ci", secret); !ok {
		t.Fatal("secret lost to a failed save")
	}
	if _, ok := store.LookupToken(token); !ok {
		t.Fatal("token revoked by a failed save")
	}
	restore()
}

func TestAccessTokenLifecycle(t *testing.T) {
	store := newClientStore(t)
	clock := time.Now()
	store.now = func() time.Time { return clock }
	mustCreateClient(t, store, "ci", ScopeRun, ScopeRead)

	token, granted, err := store.IssueToken("ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, accessTokenPrefix) {
		t.Fatalf("token shape: %q", token)
	}
	if !slices.Equal(granted, []Scope{ScopeRun, ScopeRead}) {
		t.Fatalf("default grant: %v", granted)
	}
	grant, ok := store.LookupToken(token)
	if !ok || grant.ClientID != "ci" || !grant.Allows(ScopeRun) || !grant.Allows(ScopeRead) {
		t.Fatalf("lookup: %+v %v", grant, ok)
	}
	if _, ok = store.LookupToken("tlat_forged"); ok {
		t.Fatal("forged token accepted")
	}
	if _, ok = store.LookupToken(""); ok {
		t.Fatal("empty token accepted")
	}
	if !grant.Allows(ScopeRun) || grant.Allows(Scope("root")) {
		t.Fatal("Allows")
	}

	// Requests narrow the grant; ungranted scopes are refused.
	token, granted, err = store.IssueToken("ci", []Scope{ScopeRead})
	if err != nil || !slices.Equal(granted, []Scope{ScopeRead}) {
		t.Fatalf("down-scope: %v %v", granted, err)
	}
	if grant, _ = store.LookupToken(token); grant.Allows(ScopeRun) {
		t.Fatal("down-scoped token kept the run scope")
	}
	readOnly := newClientStore(t)
	mustCreateClient(t, readOnly, "reader", ScopeRead)
	if _, _, err = readOnly.IssueToken("reader", []Scope{ScopeRun}); !errors.Is(err, ErrScopeNotGranted) {
		t.Fatalf("ungranted scope: %v", err)
	}
	if _, _, err = readOnly.IssueToken("ghost", nil); !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("unknown client: %v", err)
	}

	// Tokens expire on their absolute lifetime.
	clock = clock.Add(AccessTokenLifetime)
	if _, ok = store.LookupToken(token); ok {
		t.Fatal("expired token accepted")
	}

	// Revocation ends exactly the caller's token.
	first, _, _ := store.IssueToken("ci", nil)
	second, _, _ := store.IssueToken("ci", nil)
	store.RevokeToken("reader", first)
	if _, ok = store.LookupToken(first); !ok {
		t.Fatal("another client revoked a foreign token")
	}
	store.RevokeToken("ci", first)
	if _, ok = store.LookupToken(first); ok {
		t.Fatal("revoked token accepted")
	}
	if _, ok = store.LookupToken(second); !ok {
		t.Fatal("revocation touched a sibling token")
	}
}

func TestGrantChangesEndTokens(t *testing.T) {
	for name, change := range map[string]func(store *ClientStore){
		"scopes":    func(store *ClientStore) { _ = store.SetScopes("ci", []Scope{ScopeRead}) },
		"disable":   func(store *ClientStore) { _ = store.SetDisabled("ci", true) },
		"rotate":    func(store *ClientStore) { _, _ = store.RotateSecret("ci") },
		"remove":    func(store *ClientStore) { _ = store.Remove("ci") },
		"re-enable": func(store *ClientStore) { _ = store.SetDisabled("ci", false) },
	} {
		store := newClientStore(t)
		mustCreateClient(t, store, "ci", ScopeRun)
		mustCreateClient(t, store, "other", ScopeRead)
		own, _, err := store.IssueToken("ci", nil)
		if err != nil {
			t.Fatal(err)
		}
		foreign, _, _ := store.IssueToken("other", nil)
		change(store)
		if _, ok := store.LookupToken(own); ok {
			t.Fatalf("%s left the client's tokens alive", name)
		}
		if _, ok := store.LookupToken(foreign); !ok {
			t.Fatalf("%s ended another client's tokens", name)
		}
	}
}

func TestTokensPerClientAreBounded(t *testing.T) {
	store := newClientStore(t)
	clock := time.Now()
	store.now = func() time.Time { return clock }
	mustCreateClient(t, store, "ci", ScopeRun)

	issued := make([]string, 0, maxTokensPerClient+3)
	for i := range maxTokensPerClient + 3 {
		clock = clock.Add(time.Duration(i) * time.Second)
		token, _, err := store.IssueToken("ci", nil)
		if err != nil {
			t.Fatal(err)
		}
		issued = append(issued, token)
	}
	alive := 0
	for _, token := range issued {
		if _, ok := store.LookupToken(token); ok {
			alive++
		}
	}
	if alive != maxTokensPerClient {
		t.Fatalf("client holds %d live tokens, want %d", alive, maxTokensPerClient)
	}
	// The earliest tokens gave way; the newest survive.
	for _, token := range issued[:3] {
		if _, ok := store.LookupToken(token); ok {
			t.Fatal("unbounded token growth")
		}
	}
	if _, ok := store.LookupToken(issued[len(issued)-1]); !ok {
		t.Fatal("newest token evicted")
	}
}
