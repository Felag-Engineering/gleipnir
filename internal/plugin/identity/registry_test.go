package identity_test

import (
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
)

func TestRegistry_IssueLookupRoundtrip(t *testing.T) {
	t.Parallel()

	r := identity.New()
	token, err := r.Issue("instance-A")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" {
		t.Fatal("Issue returned empty token")
	}

	gotID, ok := r.Lookup(token)
	if !ok {
		t.Fatal("Lookup returned ok=false for a freshly issued token")
	}
	if gotID != "instance-A" {
		t.Errorf("Lookup returned instance %q, want instance-A", gotID)
	}
}

func TestRegistry_IssueAutoRevokesPriorToken(t *testing.T) {
	t.Parallel()

	r := identity.New()

	t1, err := r.Issue("instance-B")
	if err != nil {
		t.Fatalf("first Issue: %v", err)
	}

	t2, err := r.Issue("instance-B")
	if err != nil {
		t.Fatalf("second Issue: %v", err)
	}

	// The first token must no longer be valid after re-issuing for the same instance.
	if _, ok := r.Lookup(t1); ok {
		t.Error("Lookup(t1) returned ok=true after t1 was auto-revoked by a second Issue")
	}

	// The new token must resolve to the same instance.
	gotID, ok := r.Lookup(t2)
	if !ok {
		t.Fatal("Lookup(t2) returned ok=false")
	}
	if gotID != "instance-B" {
		t.Errorf("Lookup(t2) = %q, want instance-B", gotID)
	}
}

func TestRegistry_RevokeRemoves(t *testing.T) {
	t.Parallel()

	r := identity.New()
	token, _ := r.Issue("instance-C")

	r.Revoke(token)

	if _, ok := r.Lookup(token); ok {
		t.Error("Lookup returned ok=true after Revoke")
	}
}

func TestRegistry_RevokeInstanceRemovesAll(t *testing.T) {
	t.Parallel()

	r := identity.New()
	token, _ := r.Issue("instance-D")

	r.RevokeInstance("instance-D")

	if _, ok := r.Lookup(token); ok {
		t.Error("Lookup returned ok=true after RevokeInstance")
	}
}

func TestRegistry_LookupUnknownToken(t *testing.T) {
	t.Parallel()

	r := identity.New()

	_, ok := r.Lookup("not-a-real-token")
	if ok {
		t.Error("Lookup returned ok=true for unknown token in empty registry")
	}
}

func TestRegistry_TokensAreUnpredictable(t *testing.T) {
	t.Parallel()

	r := identity.New()
	seen := make(map[string]struct{}, 1000)

	for i := 0; i < 1000; i++ {
		instanceID := "inst-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		token, err := r.Issue(instanceID)
		if err != nil {
			t.Fatalf("Issue(%q): %v", instanceID, err)
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("duplicate token %q at iteration %d", token, i)
		}
		seen[token] = struct{}{}

		// base64url with no padding: 32 bytes → 43 characters.
		if len(token) < 32 {
			t.Errorf("token %q is shorter than 32 bytes encoded", token)
		}
	}
}

// TestRegistry_LookupRejectsWrongLength verifies that tokens whose decoded byte
// length differs from the expected 32 bytes are rejected without a successful
// lookup. This is a functional correctness check for the length guard that
// prevents wrong-length inputs from reaching the constant-time comparison.
func TestRegistry_LookupRejectsWrongLength(t *testing.T) {
	t.Parallel()

	r := identity.New()
	_, err := r.Issue("instance-len")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"too_short_16_bytes", "AAAAAAAAAAAAAAAAAAAAAA"},  // 16 bytes base64url
		{"too_long_48_bytes", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, // 48 bytes base64url
		{"not_base64", "!!!not-valid-base64!!!"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, ok := r.Lookup(tc.token)
			if ok {
				t.Errorf("Lookup(%q) returned ok=true, want false for wrong-length/invalid token", tc.name)
			}
		})
	}
}
