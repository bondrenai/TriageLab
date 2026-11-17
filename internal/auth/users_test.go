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

func TestStoreReloadsExternalChangesAndSerializesConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	serverStore, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cliStore, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := serverStore.Create("alice", "password-123", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	// Separate Store handle (simulating `triagelab user ...` CLI) observes alice,
	// creates bob, and updates alice's role.
	if accounts := cliStore.Accounts(); len(accounts) != 1 || accounts[0].Username != "alice" {
		t.Fatalf("cli store did not see server create: %+v", accounts)
	}
	if err := cliStore.Create("bob", "password-456", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := cliStore.SetRole("alice", RoleUser); err != nil {
		t.Fatal(err)
	}
	if user, ok := serverStore.Authenticate("bob", "password-456"); !ok || user.Role != RoleViewer {
		t.Fatalf("server store did not reload external create: %+v %v", user, ok)
	}
	if user, ok, err := serverStore.Lookup("alice"); err != nil || !ok || user.Role != RoleUser {
		t.Fatalf("server store did not reload role change: %+v %v", user, ok)
	}
	// Concurrent write from serverStore preserves bob written by cliStore.
	if err := serverStore.Create("carol", "password-789", RoleUser); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cliStore.Lookup("carol"); err != nil || !ok {
		t.Fatal("cli store did not observe concurrent create")
	}
	if accounts := cliStore.Accounts(); len(accounts) != 3 {
		t.Fatalf("expected 3 preserved accounts across handles, got %+v", accounts)
	}
}

func TestStoreReadsSymlinkedReadOnlyFileAndReportsReloadErrors(t *testing.T) {
	// Secret and ConfigMap mounts expose files through ..data symlinks in a
	// read-only directory. Reads need neither a lock file nor O_NOFOLLOW.
	source := filepath.Join(t.TempDir(), "users.json")
	writer, err := OpenStore(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Create("alice", "password-123", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	mount := filepath.Join(t.TempDir(), "mount")
	if err := os.Mkdir(mount, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(mount, "users.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mount, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mount, 0o700) })
	reader, err := OpenStore(filepath.Join(mount, "users.json"))
	if err != nil {
		t.Fatalf("read-only symlinked store refused: %v", err)
	}
	if _, ok := reader.Authenticate("alice", "password-123"); !ok {
		t.Fatal("read-only symlinked store cannot authenticate")
	}
	if _, err := os.Stat(filepath.Join(mount, "users.json.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created a lock file: %v", err)
	}

	// A file that becomes unreadable is an error, not a missing account.
	if err := os.WriteFile(source, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := reader.Lookup("alice"); err == nil || ok {
		t.Fatalf("broken file reported as ok=%v err=%v", ok, err)
	}
}

func TestChangeAccountKeepsAnEnabledAdmin(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		if err := store.Create(name, "password-123", RoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Create("carol", "password-123", RoleViewer); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if err := store.ChangeAccount("alice", AccountChange{Disabled: &disabled, KeepEnabledAdmin: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeAccount("bob", AccountChange{Remove: true, KeepEnabledAdmin: true}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("removing the last admin: %v", err)
	}
	// Changes to non-admins are never refused by the guard.
	if err := store.ChangeAccount("carol", AccountChange{Disabled: &disabled, KeepEnabledAdmin: true}); err != nil {
		t.Fatal(err)
	}
	// The unguarded CLI path can still make any change.
	if err := store.Remove("bob"); err != nil {
		t.Fatal(err)
	}
}
