package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreCreateAuthenticatePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.EnabledLogin() {
		t.Fatal("empty store must not allow logins")
	}
	if err = store.Create("alice", "password-123", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err = store.Create("bob", "password-456", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err = store.Create("alice", "password-789", RoleUser); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	if _, ok := store.Authenticate("alice", "password-123"); !ok {
		t.Fatal("valid login rejected")
	}
	if _, ok := store.Authenticate("alice", "wrong"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, ok := store.Authenticate("ghost", "password-123"); ok {
		t.Fatal("unknown user accepted")
	}
	if !store.EnabledLogin() {
		t.Fatal("enabled login not detected")
	}
	if store.CountEnabledAdmins() != 1 {
		t.Fatalf("admin count %d", store.CountEnabledAdmins())
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if user, ok := reopened.Authenticate("alice", "password-123"); !ok || user.Role != RoleAdmin {
		t.Fatalf("state lost after reopen: %v %v", user, ok)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("user file mode %v", info.Mode().Perm())
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "password-123") {
		t.Fatal("plaintext password persisted")
	}
}

func TestStoreDisabledAndRoles(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Create("carol", "password-123", RoleUser); err != nil {
		t.Fatal(err)
	}
	if err = store.SetDisabled("carol", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate("carol", "password-123"); ok {
		t.Fatal("disabled account accepted")
	}
	if store.EnabledLogin() {
		t.Fatal("disabled account still counts as a login")
	}
	if err = store.SetDisabled("carol", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate("carol", "password-123"); !ok {
		t.Fatal("re-enabled account rejected")
	}
	if err = store.SetRole("carol", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if store.CountEnabledAdmins() != 1 {
		t.Fatal("role change not persisted in memory")
	}
	if err = store.SetRole("ghost", RoleAdmin); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	if err = store.Remove("carol"); err != nil {
		t.Fatal(err)
	}
	if err = store.Remove("carol"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("double remove: %v", err)
	}
}

func TestStoreSetPassword(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Create("dave", "password-123", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err = store.SetPassword("dave", "new-password-9"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate("dave", "password-123"); ok {
		t.Fatal("old password accepted")
	}
	if _, ok := store.Authenticate("dave", "new-password-9"); !ok {
		t.Fatal("new password rejected")
	}
	// Clearing the password removes local login but keeps the account.
	if err = store.SetPassword("dave", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate("dave", "new-password-9"); ok {
		t.Fatal("cleared password still accepted")
	}
	for _, weak := range []string{"short", "has\ncontrol"} {
		if err = store.SetPassword("dave", weak); err == nil {
			t.Fatalf("weak password accepted: %q", weak)
		}
	}
}

func TestStoreRejectsBadNamesAndFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "Alice", "-lead", "trailing-", "has space", "underline_is_ok_but_Ünicode_is_not", strings.Repeat("a", 33)} {
		if err = store.Create(name, "password-123", RoleViewer); err == nil {
			t.Fatalf("accepted username %q", name)
		}
	}
	if err = store.Create("ok.name-1_a", "password-123", RoleViewer); err != nil {
		t.Fatalf("valid username rejected: %v", err)
	}
	if err = store.Create("rolecheck", "password-123", Role("root")); err == nil {
		t.Fatal("invalid role accepted")
	}
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte(`{"users":[{"username":"UPPER","role":"user"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(broken); err == nil {
		t.Fatal("invalid username in file accepted")
	}
	if err := os.WriteFile(broken, []byte(`{"users":[]} trailing`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(broken); err == nil {
		t.Fatal("trailing content accepted")
	}
	if err := os.WriteFile(broken, []byte(`{"users":[{"username":"x","role":"user","password":"plain"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(broken); err == nil {
		t.Fatal("plaintext password field accepted")
	}
}
