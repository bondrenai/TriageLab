package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// ErrUserNotFound, ErrUserExists, and ErrEmptyStore distinguish store errors
// for callers that report them to operators.
var (
	ErrUserNotFound = errors.New("user not found")
	ErrUserExists   = errors.New("user already exists")
	// ErrLastAdmin refuses a guarded change that would leave no enabled admin.
	ErrLastAdmin = errors.New("the workspace must keep at least one enabled admin")
)

const (
	maxUsers        = 256
	maxUsersFile    = 1 << 20
	maxPasswordUTF8 = 512
)

// User is one local account. Password holds the encoded PBKDF2 hash and is
// never serialized back to clients.
type User struct {
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	Password  string    `json:"password,omitempty"`
	Disabled  bool      `json:"disabled,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Account is the client-facing projection of a user without hash material.
type Account struct {
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Password  bool      `json:"has_password"`
}

func (u User) account() Account {
	return Account{Username: u.Username, Role: u.Role, Disabled: u.Disabled,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt, Password: u.Password != ""}
}

// Store keeps local accounts in one JSON file. All methods are safe for
// concurrent use across goroutines and processes. Writes replace the file by
// atomic rename, so reads need no lock: they reload whenever the file changes
// on disk and may follow symlinks (Secret and ConfigMap mounts use them).
// Writers serialize read-modify-write cycles with an advisory lock beside the
// file, which therefore needs a writable directory only for changes.
type Store struct {
	mu     sync.Mutex
	path   string
	users  map[string]User
	info   os.FileInfo
	loaded bool
}

type usersFile struct {
	Users []User `json:"users"`
}

// OpenStore loads the user file at path, creating nothing until the first
// save. A missing file yields an empty store.
func OpenStore(path string) (*Store, error) {
	store := &Store{path: path, users: map[string]User{}}
	if err := store.reloadIfChangedLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func decodeUsersFile(path string) (map[string]User, os.FileInfo, bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]User{}, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, false, fmt.Errorf("user file must be a regular file")
	}
	var data usersFile
	d := json.NewDecoder(io.LimitReader(f, maxUsersFile+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&data); err != nil {
		return nil, nil, false, fmt.Errorf("user file: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, nil, false, fmt.Errorf("user file must contain one JSON object")
	}
	if len(data.Users) > maxUsers {
		return nil, nil, false, fmt.Errorf("user file exceeds %d accounts", maxUsers)
	}
	users := make(map[string]User, len(data.Users))
	for _, user := range data.Users {
		if !ValidUsername(user.Username) || !user.Role.Valid() {
			return nil, nil, false, fmt.Errorf("user file contains an invalid account entry")
		}
		if _, duplicate := users[user.Username]; duplicate {
			return nil, nil, false, fmt.Errorf("user file contains duplicate account %q", user.Username)
		}
		if user.Password != "" && !VerifyPasswordFormat(user.Password) {
			return nil, nil, false, fmt.Errorf("user file contains an unreadable password hash")
		}
		users[user.Username] = user
	}
	return users, info, true, nil
}

// withWriteLock serializes read-modify-write cycles across processes.
func (s *Store) withWriteLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	lockFile, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()
	return fn()
}

func (s *Store) reloadIfChangedLocked() error {
	info, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.users = map[string]User{}
		s.info = nil
		s.loaded = false
		return nil
	}
	if err != nil {
		return err
	}
	if s.loaded && s.info != nil && os.SameFile(s.info, info) &&
		info.ModTime().Equal(s.info.ModTime()) && info.Size() == s.info.Size() {
		return nil
	}
	return s.reloadLocked()
}

func (s *Store) reloadLocked() error {
	users, loadedInfo, exists, err := decodeUsersFile(s.path)
	if err != nil {
		return err
	}
	s.users = users
	s.info = loadedInfo
	s.loaded = exists
	return nil
}

// Path is the file backing the store.
func (s *Store) Path() string { return s.path }

// ValidUsername keeps account names short, lowercase, and safe to embed in
// JSON, logs, and file names: alphanumerics with inner '-', '.', '_'.
func ValidUsername(name string) bool {
	if len(name) < 1 || len(name) > 32 {
		return false
	}
	last := len(name) - 1
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '.' || r == '_') && i > 0 && i < last:
		default:
			return false
		}
	}
	return true
}

// ValidPassword enforces basic length bounds without composition rules;
// operators choose their own policy and IdP users authenticate upstream.
func ValidPassword(password string) bool {
	return len(password) >= 8 && len(password) <= maxPasswordUTF8 &&
		strings.IndexFunc(password, unicode.IsControl) < 0
}

// Lookup returns the current record for username, reloading from disk if the
// store file changed. A non-nil error means the file could not be read, which
// callers must not mistake for a removed account.
func (s *Store) Lookup(username string) (User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		return User{}, false, err
	}
	user, ok := s.users[username]
	return user, ok, nil
}

// Authenticate verifies a local password login. Missing accounts and wrong
// passwords are indistinguishable to callers.
func (s *Store) Authenticate(username, password string) (User, bool) {
	s.mu.Lock()
	err := s.reloadIfChangedLocked()
	var user User
	var ok bool
	if err == nil {
		user, ok = s.users[username]
	}
	s.mu.Unlock()
	if err != nil || !ok || user.Disabled || user.Password == "" {
		return User{}, false
	}
	if !VerifyPassword(user.Password, password) {
		return User{}, false
	}
	return user, true
}

// Create adds an account. An empty password creates the account without a
// usable local login (for future OIDC-only accounts or before a password set).
func (s *Store) Create(username, password string, role Role) error {
	if !ValidUsername(username) {
		return fmt.Errorf("usernames are 1-32 characters of lowercase letters, digits, '-', '.', '_'")
	}
	if !role.Valid() {
		return fmt.Errorf("role must be one of viewer, user, admin")
	}
	if password != "" {
		if !ValidPassword(password) {
			return fmt.Errorf("passwords are 8-512 characters without control characters")
		}
		encoded, err := HashPassword(password)
		if err != nil {
			return err
		}
		password = encoded
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withWriteLock(func() error {
		if err := s.reloadLocked(); err != nil {
			return err
		}
		if _, exists := s.users[username]; exists {
			return ErrUserExists
		}
		if len(s.users) >= maxUsers {
			return fmt.Errorf("the user file holds at most %d accounts", maxUsers)
		}
		now := time.Now().UTC()
		s.users[username] = User{Username: username, Role: role, Password: password, CreatedAt: now, UpdatedAt: now}
		return s.saveLocked()
	})
}

// SetPassword replaces an account password, or clears local login when empty.
func (s *Store) SetPassword(username, password string) error {
	if password != "" && !ValidPassword(password) {
		return fmt.Errorf("passwords are 8-512 characters without control characters")
	}
	encoded := ""
	if password != "" {
		var err error
		if encoded, err = HashPassword(password); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withWriteLock(func() error {
		if err := s.reloadLocked(); err != nil {
			return err
		}
		user, ok := s.users[username]
		if !ok {
			return ErrUserNotFound
		}
		user.Password, user.UpdatedAt = encoded, time.Now().UTC()
		s.users[username] = user
		return s.saveLocked()
	})
}

// AccountChange describes one atomic update to an existing account.
// KeepEnabledAdmin refuses the change with ErrLastAdmin when it would leave
// no enabled admin; the check runs inside the cross-process write lock.
type AccountChange struct {
	Role             *Role
	Disabled         *bool
	Remove           bool
	KeepEnabledAdmin bool
}

// ChangeAccount applies change to username as one locked read-modify-write.
func (s *Store) ChangeAccount(username string, change AccountChange) error {
	if change.Role != nil && !change.Role.Valid() {
		return fmt.Errorf("role must be one of viewer, user, admin")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withWriteLock(func() error {
		if err := s.reloadLocked(); err != nil {
			return err
		}
		user, ok := s.users[username]
		if !ok {
			return ErrUserNotFound
		}
		wasAdmin := user.Role == RoleAdmin && !user.Disabled
		if change.Role != nil {
			user.Role = *change.Role
		}
		if change.Disabled != nil {
			user.Disabled = *change.Disabled
		}
		stillAdmin := !change.Remove && user.Role == RoleAdmin && !user.Disabled
		if change.KeepEnabledAdmin && wasAdmin && !stillAdmin && s.enabledAdminsLocked(username) == 0 {
			return ErrLastAdmin
		}
		if change.Remove {
			delete(s.users, username)
		} else {
			user.UpdatedAt = time.Now().UTC()
			s.users[username] = user
		}
		return s.saveLocked()
	})
}

// SetRole changes an account role.
func (s *Store) SetRole(username string, role Role) error {
	return s.ChangeAccount(username, AccountChange{Role: &role})
}

// SetDisabled enables or disables an account without deleting it.
func (s *Store) SetDisabled(username string, disabled bool) error {
	return s.ChangeAccount(username, AccountChange{Disabled: &disabled})
}

// Remove deletes an account.
func (s *Store) Remove(username string) error {
	return s.ChangeAccount(username, AccountChange{Remove: true})
}

func (s *Store) enabledAdminsLocked(except string) int {
	count := 0
	for _, user := range s.users {
		if user.Username != except && user.Role == RoleAdmin && !user.Disabled {
			count++
		}
	}
	return count
}

// Accounts lists accounts without hash material, ordered by name.
func (s *Store) Accounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadIfChangedLocked()
	accounts := make([]Account, 0, len(s.users))
	for _, user := range s.users {
		accounts = append(accounts, user.account())
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Username < accounts[j].Username })
	return accounts
}

// CountEnabledAdmins reports how many enabled local admins remain.
func (s *Store) CountEnabledAdmins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadIfChangedLocked()
	return s.enabledAdminsLocked("")
}

// EnabledLogin reports whether any enabled account with a password exists.
func (s *Store) EnabledLogin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		return false
	}
	for _, user := range s.users {
		if !user.Disabled && user.Password != "" {
			return true
		}
	}
	return false
}

func (s *Store) saveLocked() error {
	data := usersFile{Users: make([]User, 0, len(s.users))}
	for _, user := range s.users {
		data.Users = append(data.Users, user)
	}
	sort.Slice(data.Users, func(i, j int) bool { return data.Users[i].Username < data.Users[j].Username })
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateFile(s.path, append(content, '\n')); err != nil {
		return err
	}
	if info, err := os.Stat(s.path); err == nil {
		s.info = info
		s.loaded = true
	}
	return nil
}

// writePrivateFile atomically replaces path with owner-only content, creating
// its directory when needed. The rename stays within one directory.
func writePrivateFile(path string, content []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
