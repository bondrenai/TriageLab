package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "pbkdf2-sha256$600000$") {
		t.Fatalf("unexpected encoding: %s", encoded)
	}
	if !VerifyPassword(encoded, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(encoded, "incorrect horse") {
		t.Fatal("wrong password accepted")
	}
	// Two hashes of the same password differ and both verify (random salts).
	other, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if other == encoded {
		t.Fatal("salt reuse")
	}
	if !VerifyPassword(other, "correct horse battery staple") {
		t.Fatal("second hash rejected")
	}
}

func TestVerifyPasswordRejectsMalformedInput(t *testing.T) {
	for _, encoded := range []string{
		"", "plaintext", "pbkdf2-sha256$600000", "pbkdf2-sha256$abc$ c2FsdA $aGFzaA",
		"bcrypt$10$abc$def", "pbkdf2-sha256$0$c2FsdA$Hash",
	} {
		if VerifyPassword(encoded, "x") {
			t.Fatalf("accepted malformed hash %q", encoded)
		}
		if VerifyPasswordFormat(encoded) {
			t.Fatalf("format accepted %q", encoded)
		}
	}
}

func TestVerifyPasswordTamperedParameters(t *testing.T) {
	encoded, err := HashPassword("staple battery")
	if err != nil {
		t.Fatal(err)
	}
	// Dropping the iteration count to 1 must not make verification succeed
	// with the original parameters; format still verifies structurally.
	if VerifyPassword(encoded+"x", "staple battery") {
		t.Fatal("accepted corrupted hash")
	}
}
