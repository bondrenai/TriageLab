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
	"time"
	"unicode"
)

// ErrUserNotFound, ErrUserExists, and ErrEmptyStore distinguish store errors
// for callers that report them to operators.
var (
	ErrUserNotFound = errors.New("user not found")
	ErrUserExists   = errors.New("user already exists")
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
// concurrent use; writes are atomic renames within the same directory.
type Store struct {
	mu    sync.Mutex
	path  string
	users map[string]User
}

type usersFile struct {
	Users []User `json:"users"`
}

// OpenStore loads the user file at path, creating nothing until the first
// save. A missing file yields an empty store.
func OpenStore(path string) (*Store, error) {
	store := &Store{path: path, users: map[string]User{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var data usersFile
	d := json.NewDecoder(io.LimitReader(f, maxUsersFile+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&data); err != nil {
		return nil, fmt.Errorf("user file: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("user file must contain one JSON object")
	}
	if len(data.Users) > maxUsers {
		return nil, fmt.Errorf("user file exceeds %d accounts", maxUsers)
	}
	for _, user := range data.Users {
		if !ValidUsername(user.Username) || !user.Role.Valid() {
			return nil, fmt.Errorf("user file contains an invalid account entry")
		}
		if _, duplicate := store.users[user.Username]; duplicate {
			return nil, fmt.Errorf("user file contains duplicate account %q", user.Username)
		}
		if user.Password != "" && !VerifyPasswordFormat(user.Password) {
			return nil, fmt.Errorf("user file contains an unreadable password hash")
		}
		store.users[user.Username] = user
	}
	return store, nil
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

// Authenticate verifies a local password login. Missing accounts and wrong
// passwords are indistinguishable to callers.
func (s *Store) Authenticate(username, password string) (User, bool) {
	s.mu.Lock()
	user, ok := s.users[username]
	s.mu.Unlock()
	if !ok || user.Disabled || user.Password == "" {
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
	if _, exists := s.users[username]; exists {
		return ErrUserExists
	}
	if len(s.users) >= maxUsers {
		return fmt.Errorf("the user file holds at most %d accounts", maxUsers)
	}
	now := time.Now().UTC()
	s.users[username] = User{Username: username, Role: role, Password: password, CreatedAt: now, UpdatedAt: now}
	return s.saveLocked()
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
	user, ok := s.users[username]
	if !ok {
		return ErrUserNotFound
	}
	user.Password, user.UpdatedAt = encoded, time.Now().UTC()
	s.users[username] = user
	return s.saveLocked()
}

// SetRole changes an account role.
func (s *Store) SetRole(username string, role Role) error {
	if !role.Valid() {
		return fmt.Errorf("role must be one of viewer, user, admin")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[username]
	if !ok {
		return ErrUserNotFound
	}
	user.Role, user.UpdatedAt = role, time.Now().UTC()
	s.users[username] = user
	return s.saveLocked()
}

// SetDisabled enables or disables an account without deleting it.
func (s *Store) SetDisabled(username string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[username]
	if !ok {
		return ErrUserNotFound
	}
	user.Disabled, user.UpdatedAt = disabled, time.Now().UTC()
	s.users[username] = user
	return s.saveLocked()
}

// Remove deletes an account.
func (s *Store) Remove(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[username]; !ok {
		return ErrUserNotFound
	}
	delete(s.users, username)
	return s.saveLocked()
}

// Accounts lists accounts without hash material, ordered by name.
func (s *Store) Accounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	count := 0
	for _, user := range s.users {
		if user.Role == RoleAdmin && !user.Disabled {
			count++
		}
	}
	return count
}

// EnabledLogin reports whether any enabled account with a password exists.
func (s *Store) EnabledLogin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".users-*")
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
	return os.Rename(name, s.path)
}
