package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// Encrypt encrypts plaintext using AES-256-GCM with the given 32-byte key.
// Returns base64-encoded ciphertext (nonce prepended).
func Encrypt(key []byte, plaintext string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts base64-encoded ciphertext (nonce prepended) using AES-256-GCM.
func Decrypt(key []byte, encoded string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// MaskKey returns a masked version of an API key for display.
// Shows the prefix and last 4 characters separated by "...".
func MaskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "***"
	}
	// Find a natural prefix boundary: look for the second "-" in the key.
	prefix := key[:4]
	if first := strings.Index(key, "-"); first >= 0 {
		if second := strings.Index(key[first+1:], "-"); second >= 0 && first+1+second < 10 {
			prefix = key[:first+1+second+1]
		}
	}
	return prefix + "..." + key[len(key)-4:]
}

// ParseEncryptionKey parses a hex-encoded or base64-encoded 32-byte key.
func ParseEncryptionKey(s string) ([]byte, error) {
	if len(s) == 64 {
		key, err := hex.DecodeString(s)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}

	key, err := base64.StdEncoding.DecodeString(s)
	if err == nil && len(key) == 32 {
		return key, nil
	}

	return nil, fmt.Errorf("encryption key must be 32 bytes encoded as hex (64 chars) or base64 (44 chars)")
}
