package signing

import (
	"errors"
	"testing"
)

func TestGenerateKeypair(t *testing.T) {
	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if len(pk.Key) != 32 {
		t.Errorf("public key length: got %d, want 32", len(pk.Key))
	}
	if pk.KeyID != sk.KeyID {
		t.Errorf("key ID mismatch between public and secret key")
	}
	if sk.SigAlg != SigAlgED25519 {
		t.Errorf("sig alg: got %v, want Ed", sk.SigAlg)
	}
	if sk.ChkAlg != ChkAlgBlake2b {
		t.Errorf("chk alg: got %v, want B2", sk.ChkAlg)
	}
	if sk.KDFAlg != ([2]byte{}) {
		t.Errorf("new key should be unencrypted (KDFAlg=0), got %v", sk.KDFAlg)
	}
}

func TestDecryptUnencryptedKey(t *testing.T) {
	_, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	raw, keyID, err := DecryptSecretKey(sk, nil)
	if err != nil {
		t.Fatalf("decrypt unencrypted: %v", err)
	}
	if raw != sk.SecretKey {
		t.Errorf("decrypted key does not match original")
	}
	if keyID != sk.KeyID {
		t.Errorf("decrypted keyID does not match original")
	}
}

func TestEncryptDecryptScrypt(t *testing.T) {
	testEncryptDecrypt(t, KDFAlgScrypt, []byte("passphrase-scrypt"))
}

func TestEncryptDecryptArgon2id(t *testing.T) {
	testEncryptDecrypt(t, KDFAlgArgon2id, []byte("passphrase-argon2"))
}

func testEncryptDecrypt(t *testing.T, kdf [2]byte, passphrase []byte) {
	t.Helper()
	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	enc, err := EncryptSecretKey(sk, passphrase, kdf)
	if err != nil {
		t.Fatalf("encrypt (%v): %v", kdf, err)
	}

	if enc.KDFAlg != kdf {
		t.Errorf("kdf alg: got %v, want %v", enc.KDFAlg, kdf)
	}
	if enc.EncryptedBlob == nil {
		t.Error("EncryptedBlob should be set after encryption")
	}
	// Plaintext fields should be cleared.
	if enc.SecretKey != ([64]byte{}) {
		t.Error("SecretKey plaintext field should be zeroed after encryption")
	}

	raw, keyID, err := DecryptSecretKey(enc, passphrase)
	if err != nil {
		t.Fatalf("decrypt (%v): %v", kdf, err)
	}
	if raw != sk.SecretKey {
		t.Errorf("decrypted key mismatch")
	}
	if keyID != pk.KeyID {
		t.Errorf("decrypted keyID mismatch: got %x, want %x", keyID, pk.KeyID)
	}

	// Confirm the decrypted key is the Ed25519 private key matching the public key.
	payload := []byte("test")
	sig, err := Sign(raw, keyID, payload, "test comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := Verify(pk, payload, sig, "test comment"); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestDecryptBadPassphrase(t *testing.T) {
	_, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	enc, err := EncryptSecretKey(sk, []byte("correct"), KDFAlgScrypt)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if _, _, err := DecryptSecretKey(enc, []byte("wrong")); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("expected ErrBadPassphrase for wrong passphrase, got: %v", err)
	}
}

func TestDecryptBadPassphraseArgon2(t *testing.T) {
	_, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	enc, err := EncryptSecretKey(sk, []byte("correct"), KDFAlgArgon2id)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if _, _, err := DecryptSecretKey(enc, []byte("wrong")); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("expected ErrBadPassphrase for wrong passphrase, got: %v", err)
	}
}

func TestEncryptUnsupportedKDF(t *testing.T) {
	_, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	_, err = EncryptSecretKey(sk, []byte("pass"), [2]byte{'X', 'X'})
	if err != ErrUnsupportedKDF {
		t.Errorf("expected ErrUnsupportedKDF, got: %v", err)
	}
}

func TestChecksumMismatchOnCorruptedKey(t *testing.T) {
	_, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Corrupt the checksum field of an unencrypted key.
	sk.Checksum[0] ^= 0xff

	if _, _, err := DecryptSecretKey(sk, nil); err != ErrChecksumMismatch {
		t.Errorf("expected ErrChecksumMismatch for corrupted key, got: %v", err)
	}
}
