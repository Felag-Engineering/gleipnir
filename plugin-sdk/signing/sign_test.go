package signing

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	payload := []byte("plugin binary hash goes here")
	comment := "timestamp:1000000\tname:myplugin\tversion:1.0.0"

	sig, err := Sign(sk.SecretKey, sk.KeyID, payload, comment)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if err := Verify(pk, payload, sig, comment); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestVerifyTamperedPayload(t *testing.T) {
	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	payload := []byte("original payload")
	sig, err := Sign(sk.SecretKey, sk.KeyID, payload, "comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	tampered := []byte("tampered payload")
	if err := Verify(pk, tampered, sig, "comment"); err != ErrSigInvalid {
		t.Errorf("expected ErrSigInvalid for tampered payload, got: %v", err)
	}
}

func TestVerifyTamperedTrustedComment(t *testing.T) {
	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	payload := []byte("payload")
	sig, err := Sign(sk.SecretKey, sk.KeyID, payload, "original comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if err := Verify(pk, payload, sig, "tampered comment"); err != ErrTrustedCommentTampered {
		t.Errorf("expected ErrTrustedCommentTampered, got: %v", err)
	}
}

func TestVerifyKeyIDMismatch(t *testing.T) {
	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	payload := []byte("payload")
	sig, err := Sign(sk.SecretKey, sk.KeyID, payload, "comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Modify key ID in signature.
	sig.KeyID[0] ^= 0xff

	if err := Verify(pk, payload, sig, "comment"); err != ErrKeyIDMismatch {
		t.Errorf("expected ErrKeyIDMismatch, got: %v", err)
	}
}

func TestVerifyWrongPublicKey(t *testing.T) {
	_, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate sk: %v", err)
	}
	// Different keypair for verification.
	pk2, _, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate pk2: %v", err)
	}

	payload := []byte("payload")
	sig, err := Sign(sk.SecretKey, sk.KeyID, payload, "comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Use a mismatched key ID to trigger the right code path.
	pk2.KeyID = sk.KeyID
	if err := Verify(pk2, payload, sig, "comment"); err == nil {
		t.Error("expected error verifying with wrong public key, got nil")
	}
}

func TestPluginPayload(t *testing.T) {
	binary := []byte("fake binary content")
	manifest := []byte("name: myplugin\nversion: 1.0.0\n")

	payload := PluginPayload(binary, manifest)
	if len(payload) != 64 {
		t.Errorf("payload length: got %d, want 64", len(payload))
	}

	// Verify the hashes.
	bHash := sha256.Sum256(binary)
	mHash := sha256.Sum256(manifest)
	if !bytes.Equal(payload[:32], bHash[:]) {
		t.Error("binary hash mismatch")
	}
	if !bytes.Equal(payload[32:], mHash[:]) {
		t.Error("manifest hash mismatch")
	}
}

func TestPluginPayloadDeterministic(t *testing.T) {
	binary := []byte("binary")
	manifest := []byte("manifest")

	p1 := PluginPayload(binary, manifest)
	p2 := PluginPayload(binary, manifest)
	if !bytes.Equal(p1, p2) {
		t.Error("PluginPayload is not deterministic")
	}
}

func TestSignEncryptedKeyRoundTrip(t *testing.T) {
	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	passphrase := []byte("s3cr3t")
	enc, err := EncryptSecretKey(sk, passphrase, KDFAlgScrypt)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	raw, keyID, err := DecryptSecretKey(enc, passphrase)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	payload := PluginPayload([]byte("bin"), []byte("manifest"))
	comment := "timestamp:999"
	sig, err := Sign(raw, keyID, payload, comment)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := Verify(pk, payload, sig, comment); err != nil {
		t.Errorf("verify: %v", err)
	}
}
