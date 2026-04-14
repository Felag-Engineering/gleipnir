package trigger

import (
	"encoding/hex"
	"testing"
)

func TestGenerateWebhookSecret(t *testing.T) {
	t.Run("length is 64 chars", func(t *testing.T) {
		s, err := GenerateWebhookSecret()
		if err != nil {
			t.Fatalf("GenerateWebhookSecret() error: %v", err)
		}
		if len(s) != 64 {
			t.Errorf("len = %d, want 64", len(s))
		}
	})

	t.Run("output is valid hex", func(t *testing.T) {
		s, err := GenerateWebhookSecret()
		if err != nil {
			t.Fatalf("GenerateWebhookSecret() error: %v", err)
		}
		decoded, err := hex.DecodeString(s)
		if err != nil {
			t.Errorf("not valid hex: %v", err)
		}
		if len(decoded) != 32 {
			t.Errorf("decoded length = %d, want 32", len(decoded))
		}
	})

	t.Run("successive calls differ", func(t *testing.T) {
		a, err := GenerateWebhookSecret()
		if err != nil {
			t.Fatalf("first call error: %v", err)
		}
		b, err := GenerateWebhookSecret()
		if err != nil {
			t.Fatalf("second call error: %v", err)
		}
		if a == b {
			t.Error("two successive calls returned the same secret")
		}
	})
}
