package admin

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	plaintext := "sk-ant-api03-test-key-1234567890"
	encrypted, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if encrypted == plaintext {
		t.Fatal("encrypted text should differ from plaintext")
	}

	decrypted, err := Decrypt(key, encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if decrypted != plaintext {
		t.Fatalf("got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptWithWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	rand.Read(key1)
	rand.Read(key2)

	encrypted, err := Encrypt(key1, "secret")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Decrypt(key2, encrypted)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key")
	}
}

func TestEncryptInvalidKeyLength(t *testing.T) {
	_, err := Encrypt([]byte("tooshort"), "data")
	if err == nil {
		t.Fatal("expected error for invalid key length")
	}
}

func TestMaskKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sk-ant-api03-abcdefghij1234567890", "sk-ant-...7890"},
		{"AIza1234", "***"},
		{"abc", "***"},
		{"", ""},
	}
	for _, tt := range tests {
		got := MaskKey(tt.input)
		if got != tt.want {
			t.Errorf("MaskKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseEncryptionKey(t *testing.T) {
	raw := make([]byte, 32)
	rand.Read(raw)
	hexStr := hex.EncodeToString(raw)

	key, err := ParseEncryptionKey(hexStr)
	if err != nil {
		t.Fatalf("parse hex key: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("key length = %d, want 32", len(key))
	}
}
