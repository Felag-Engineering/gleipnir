package trigger

import (
	"context"
	"errors"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
)

// ErrNoSecret is returned by LoadWebhookSecret when the policy has no stored secret.
var ErrNoSecret = errors.New("no webhook secret stored for policy")

// ErrEncryptionKeyMissing is returned by LoadWebhookSecret when an encrypted
// secret exists in the DB but GLEIPNIR_ENCRYPTION_KEY is not configured.
var ErrEncryptionKeyMissing = errors.New("encryption key not set")

// SecretLoader loads and decrypts a policy's webhook secret from the DB.
type SecretLoader struct {
	q   *db.Queries
	key []byte // nil when GLEIPNIR_ENCRYPTION_KEY is not configured
}

// NewSecretLoader returns a SecretLoader. key may be nil when the encryption
// key environment variable is not set; LoadWebhookSecret will return
// ErrEncryptionKeyMissing in that case if a ciphertext is present.
func NewSecretLoader(q *db.Queries, key []byte) *SecretLoader {
	return &SecretLoader{q: q, key: key}
}

// LoadWebhookSecret fetches and decrypts the webhook secret for policyID.
// Returns ErrNoSecret when no secret is stored. Returns ErrEncryptionKeyMissing
// when a ciphertext is stored but the encryption key is absent.
func (l *SecretLoader) LoadWebhookSecret(ctx context.Context, policyID string) (string, error) {
	ciphertext, err := l.q.GetPolicyWebhookSecret(ctx, policyID)
	if err != nil {
		return "", fmt.Errorf("get webhook secret: %w", err)
	}
	if ciphertext == nil {
		return "", ErrNoSecret
	}
	if l.key == nil {
		return "", ErrEncryptionKeyMissing
	}
	plaintext, err := admin.Decrypt(l.key, *ciphertext)
	if err != nil {
		return "", fmt.Errorf("decrypt webhook secret: %w", err)
	}
	return plaintext, nil
}
