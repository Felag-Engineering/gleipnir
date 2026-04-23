package auth

import (
	"testing"
)

func TestHashSessionToken(t *testing.T) {
	t.Run("returns 64-character hex string", func(t *testing.T) {
		got := HashSessionToken("some-random-token")
		if len(got) != 64 {
			t.Errorf("len = %d, want 64", len(got))
		}
		for _, c := range got {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("non-hex character %q in output %q", c, got)
				break
			}
		}
	})

	t.Run("same input produces same output", func(t *testing.T) {
		input := "deterministic-token"
		first := HashSessionToken(input)
		second := HashSessionToken(input)
		if first != second {
			t.Errorf("got %q then %q for same input", first, second)
		}
	})

	t.Run("different inputs produce different outputs", func(t *testing.T) {
		a := HashSessionToken("token-a")
		b := HashSessionToken("token-b")
		if a == b {
			t.Errorf("different inputs produced the same hash %q", a)
		}
	})

	t.Run("known SHA-256 vector", func(t *testing.T) {
		// echo -n "test" | sha256sum → 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
		want := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
		got := HashSessionToken("test")
		if got != want {
			t.Errorf("HashSessionToken(%q) = %q, want %q", "test", got, want)
		}
	})
}
