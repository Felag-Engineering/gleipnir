package signing

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestPublicKeyRoundTrip(t *testing.T) {
	pk, _, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	comment := "test public key"
	data := MarshalPublicKey(pk, comment)

	got, gotComment, err := ParsePublicKey(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if gotComment != comment {
		t.Errorf("comment: got %q, want %q", gotComment, comment)
	}
	if got.KeyID != pk.KeyID {
		t.Errorf("key ID mismatch")
	}
	if !bytes.Equal(got.Key, pk.Key) {
		t.Errorf("public key bytes mismatch")
	}
}

func TestPublicKeyDefaultComment(t *testing.T) {
	pk, _, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	data := MarshalPublicKey(pk, "")
	_, comment, err := ParsePublicKey(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if comment == "" {
		t.Error("expected default comment, got empty string")
	}
}

func TestParsePublicKeyErrors(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"no header", []byte("justonelineofbase64==\n")},
		{"truncated base64", []byte("untrusted comment: test\nnotbase64!!!\n")},
		{"wrong length", []byte("untrusted comment: test\n" + base64.StdEncoding.EncodeToString(make([]byte, 10)) + "\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ParsePublicKey(tc.data); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestSecretKeyRoundTripUnencrypted(t *testing.T) {
	_, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	comment := "test secret key"
	data := MarshalSecretKey(sk, comment)

	got, gotComment, err := ParseSecretKey(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if gotComment != comment {
		t.Errorf("comment: got %q, want %q", gotComment, comment)
	}
	if got.KeyID != sk.KeyID {
		t.Errorf("key ID mismatch: got %x, want %x", got.KeyID, sk.KeyID)
	}
	if got.SecretKey != sk.SecretKey {
		t.Errorf("secret key mismatch")
	}
	if got.Checksum != sk.Checksum {
		t.Errorf("checksum mismatch")
	}
}

func TestSecretKeyRoundTripEncryptedScrypt(t *testing.T) {
	testSecretKeyRoundTripEncrypted(t, KDFAlgScrypt)
}

func TestSecretKeyRoundTripEncryptedArgon2(t *testing.T) {
	testSecretKeyRoundTripEncrypted(t, KDFAlgArgon2id)
}

func testSecretKeyRoundTripEncrypted(t *testing.T, kdf [2]byte) {
	t.Helper()
	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	passphrase := []byte("hunter2")
	enc, err := EncryptSecretKey(sk, passphrase, kdf)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	data := MarshalSecretKey(enc, "encrypted key")
	parsed, _, err := ParseSecretKey(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	raw, _, err := DecryptSecretKey(parsed, passphrase)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if raw != sk.SecretKey {
		t.Errorf("decrypted key does not match original")
	}

	// Verify the decrypted key can sign and the public key verifies.
	payload := []byte("test payload")
	sig, err := Sign(raw, pk.KeyID, payload, "test comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := Verify(pk, payload, sig, "test comment"); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestParseSecretKeyErrors(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"no header", []byte("notaheader\ndata\n")},
		{"truncated base64", []byte("untrusted comment: test\nbadbase64!!\n")},
		{"too short", []byte("untrusted comment: test\n" + base64.StdEncoding.EncodeToString(make([]byte, 10)) + "\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ParseSecretKey(tc.data); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestSignatureRoundTrip(t *testing.T) {
	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	payload := []byte("hello world")
	comment := "timestamp:12345\tname:myplugin"
	sig, err := Sign(sk.SecretKey, sk.KeyID, payload, comment)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	data := MarshalSignature(sig, "sig from key")
	got, _, err := ParseSignature(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if err := Verify(pk, payload, got, comment); err != nil {
		t.Errorf("verify after round-trip: %v", err)
	}
}

func TestParseSignatureErrors(t *testing.T) {
	validSigB64 := base64.StdEncoding.EncodeToString(make([]byte, 74))
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"only 2 lines", []byte("untrusted comment: x\ndata\n")},
		{"no untrusted header", []byte("bad header\ndata\ntrusted comment: tc\nglobal\n")},
		{"bad sig base64", []byte("untrusted comment: x\n!!bad!!\ntrusted comment: tc\n" + validSigB64 + "\n")},
		{"no trusted header", []byte("untrusted comment: x\n" + validSigB64 + "\nbad header\n" + validSigB64 + "\n")},
		{"wrong sig length", []byte("untrusted comment: x\n" + base64.StdEncoding.EncodeToString(make([]byte, 10)) + "\ntrusted comment: tc\n" + validSigB64 + "\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ParseSignature(tc.data); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
