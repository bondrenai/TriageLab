// Package auth provides sign-in for the web workspace: local accounts with
// hashed passwords, in-memory sessions, and an OpenID Connect login flow.
//
// Roles are ordered: viewer < user < admin. Viewers read workspace state,
// users may start and steer investigations, and admins additionally define
// agents, procedures, and other accounts.
//
// It also provides OAuth access for external applications. Admins create API
// clients with scopes instead of roles; a client exchanges its secret for a
// short-lived access token and can only launch and read investigations.
package auth

import "fmt"

// Role names a workspace permission level.
type Role string

const (
	RoleViewer Role = "viewer"
	RoleUser   Role = "user"
	RoleAdmin  Role = "admin"
)

func (r Role) rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleUser:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// Valid reports whether r is one of the defined roles.
func (r Role) Valid() bool { return r.rank() > 0 }

// AtLeast reports whether r grants every permission of want.
func (r Role) AtLeast(want Role) bool { return r.rank() >= want.rank() }

// ParseRole validates operator-supplied role names.
func ParseRole(value string) (Role, error) {
	role := Role(value)
	if !role.Valid() {
		return "", fmt.Errorf("role must be one of viewer, user, admin")
	}
	return role, nil
}

// Highest returns the strongest role among values, for IdP group mapping.
func Highest(values []Role) Role {
	best := RoleViewer
	for _, value := range values {
		if value.rank() > best.rank() {
			best = value
		}
	}
	return best
}
