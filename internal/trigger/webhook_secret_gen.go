package trigger

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateWebhookSecret returns a cryptographically random 64-character
// lowercase hex string (32 bytes of entropy). This length matches the assumption
// in ValidateSignature, which uses the secret directly as the HMAC key.
func GenerateWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
