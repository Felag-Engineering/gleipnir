package auth

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestHashPassword(t *testing.T) {
	cases := []struct {
		name    string
		plain   string
		wantErr bool
	}{
		{name: "typical password", plain: "correct-horse-battery-staple"},
		{name: "empty password", plain: ""},
		{name: "unicode password", plain: "pässwörd🔑"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := HashPassword(tc.plain)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("HashPassword() error = %v", err)
			}
			if hash == "" {
				t.Fatal("expected non-empty hash")
			}
			// Verify the hash round-trips correctly.
			if err := CheckPassword(hash, tc.plain); err != nil {
				t.Errorf("CheckPassword(hash, plain) = %v, want nil", err)
			}
		})
	}
}

func TestCheckPassword(t *testing.T) {
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	cases := []struct {
		name      string
		hash      string
		plain     string
		wantMatch bool
	}{
		{name: "correct password", hash: hash, plain: "correct-password", wantMatch: true},
		{name: "wrong password", hash: hash, plain: "wrong-password", wantMatch: false},
		{name: "empty input", hash: hash, plain: "", wantMatch: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckPassword(tc.hash, tc.plain)
			matched := err == nil
			if matched != tc.wantMatch {
				t.Errorf("CheckPassword() matched = %v, want %v (err: %v)", matched, tc.wantMatch, err)
			}
		})
	}
}

func TestHashPasswordSaltUniqueness(t *testing.T) {
	// Same plaintext must produce different hashes each call (bcrypt embeds a random salt).
	h1, err := HashPassword("same-input")
	if err != nil {
		t.Fatalf("first HashPassword: %v", err)
	}
	h2, err := HashPassword("same-input")
	if err != nil {
		t.Fatalf("second HashPassword: %v", err)
	}
	if h1 == h2 {
		t.Error("two hashes of the same plaintext are identical — salt randomness is broken")
	}
}

func TestHashPasswordCost(t *testing.T) {
	hash, err := HashPassword("any-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost != bcryptCost {
		t.Errorf("bcrypt cost = %d, want %d", cost, bcryptCost)
	}
}

func TestCheckPasswordMismatchError(t *testing.T) {
	hash, err := HashPassword("mypassword")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	err = CheckPassword(hash, "wrongpassword")
	if err == nil {
		t.Fatal("expected error for wrong password, got nil")
	}
	// Callers that need to distinguish wrong-password from internal errors can
	// inspect the error type — verify the bcrypt sentinel is returned.
	if err != bcrypt.ErrMismatchedHashAndPassword {
		t.Errorf("error = %v, want bcrypt.ErrMismatchedHashAndPassword", err)
	}
}
