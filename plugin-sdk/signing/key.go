package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/scrypt"
)

var (
	ErrBadPassphrase    = errors.New("signing: bad passphrase or corrupted key")
	ErrChecksumMismatch = errors.New("signing: key checksum mismatch — wrong passphrase or corrupted key")
	ErrUnsupportedKDF   = errors.New("signing: unsupported KDF algorithm")
)

// GenerateKeypair generates a new Ed25519 keypair with a random 8-byte KeyID.
// The returned SecretKey is unencrypted (KDFAlg == zero). Call EncryptSecretKey
// before writing to disk.
func GenerateKeypair(rng io.Reader) (PublicKey, SecretKey, error) {
	if rng == nil {
		rng = rand.Reader
	}

	pub, priv, err := ed25519.GenerateKey(rng)
	if err != nil {
		return PublicKey{}, SecretKey{}, fmt.Errorf("signing: generate ed25519 key: %w", err)
	}

	var keyID [8]byte
	if _, err := io.ReadFull(rng, keyID[:]); err != nil {
		return PublicKey{}, SecretKey{}, fmt.Errorf("signing: generate key id: %w", err)
	}

	sk := SecretKey{
		SigAlg: SigAlgED25519,
		KDFAlg: [2]byte{0, 0},
		ChkAlg: ChkAlgBlake2b,
		KeyID:  keyID,
	}
	copy(sk.SecretKey[:], priv)
	sk.Checksum = computeChecksum(sk.SigAlg, keyID, priv)

	pk := PublicKey{
		KeyID: keyID,
		Key:   pub,
	}
	return pk, sk, nil
}

// EncryptSecretKey encrypts a plaintext secret key with passphrase using kdf.
//
// The KDF derives a 106-byte stream XOR'd against:
//
//	sigalg[2] || keyid[8] || sk[64] || chk[32]
//
// The returned SecretKey has EncryptedBlob set for serialization; the plaintext
// fields (KeyID, SecretKey, Checksum) are cleared for safety.
func EncryptSecretKey(sk SecretKey, passphrase []byte, kdf [2]byte) (SecretKey, error) {
	var salt [32]byte
	if _, err := io.ReadFull(rand.Reader, salt[:]); err != nil {
		return SecretKey{}, fmt.Errorf("signing: generate kdf salt: %w", err)
	}

	var opsLimit, memLimit uint64
	switch kdf {
	case KDFAlgScrypt:
		opsLimit = 1 << 20  // N = 1048576
		memLimit = 33554432 // upstream MEMLIMIT_DEFAULT (32 MiB); informational only — scrypt memory is determined by N, r, p above
	case KDFAlgArgon2id:
		opsLimit = 2      // time cost
		memLimit = 1 << 26 // 64 MiB
	default:
		return SecretKey{}, ErrUnsupportedKDF
	}

	stream, err := deriveKDFStream(passphrase, salt[:], kdf, opsLimit, memLimit)
	if err != nil {
		return SecretKey{}, err
	}

	// Build the plaintext blob and XOR with stream.
	var blob [encryptedBlobLen]byte
	copy(blob[0:2], sk.SigAlg[:])
	copy(blob[2:10], sk.KeyID[:])
	copy(blob[10:74], sk.SecretKey[:])
	copy(blob[74:106], sk.Checksum[:])
	for i := range blob {
		blob[i] ^= stream[i]
	}

	encrypted := sk
	encrypted.KDFAlg = kdf
	encrypted.KDFSalt = salt
	encrypted.KDFOpsLimit = opsLimit
	encrypted.KDFMemLimit = memLimit
	encrypted.EncryptedBlob = &blob
	// Clear plaintext fields so they are not accidentally used.
	encrypted.KeyID = [8]byte{}
	encrypted.SecretKey = [64]byte{}
	encrypted.Checksum = [32]byte{}
	return encrypted, nil
}

// DecryptSecretKey decrypts an encrypted secret key and verifies its checksum.
// Returns the raw 64-byte Ed25519 private key and the decrypted KeyID.
//
// For encrypted keys, sk.KeyID holds still-encrypted bytes; callers MUST use
// the returned keyID (not sk.KeyID) to avoid a KeyID mismatch at verification.
//
// For unencrypted keys (KDFAlg == zero) the plaintext fields are used directly
// after checksum verification.
func DecryptSecretKey(sk SecretKey, passphrase []byte) (raw [64]byte, keyID [8]byte, err error) {
	if sk.KDFAlg == ([2]byte{}) {
		// Unencrypted: plaintext fields are valid.
		expected := computeChecksum(sk.SigAlg, sk.KeyID, sk.SecretKey[:])
		if sk.Checksum != expected {
			return [64]byte{}, [8]byte{}, ErrChecksumMismatch
		}
		return sk.SecretKey, sk.KeyID, nil
	}

	if sk.EncryptedBlob == nil {
		return [64]byte{}, [8]byte{}, fmt.Errorf("signing: encrypted key has no blob")
	}

	stream, err := deriveKDFStream(passphrase, sk.KDFSalt[:], sk.KDFAlg, sk.KDFOpsLimit, sk.KDFMemLimit)
	if err != nil {
		return [64]byte{}, [8]byte{}, err
	}

	// Decrypt by XOR-ing again (XOR is its own inverse).
	var blob [encryptedBlobLen]byte
	for i := range blob {
		blob[i] = sk.EncryptedBlob[i] ^ stream[i]
	}

	// blob layout: sigalg[2] || keyid[8] || sk[64] || chk[32]
	var sigAlg [2]byte
	copy(sigAlg[:], blob[0:2])
	copy(keyID[:], blob[2:10])
	var rawSK [64]byte
	copy(rawSK[:], blob[10:74])
	var storedChk [32]byte
	copy(storedChk[:], blob[74:106])

	computed := computeChecksum(sigAlg, keyID, rawSK[:])
	if storedChk != computed {
		return [64]byte{}, [8]byte{}, ErrChecksumMismatch
	}

	return rawSK, keyID, nil
}

// computeChecksum returns blake2b-256 of sigalg || keyid || rawSecretKey.
func computeChecksum(sigAlg [2]byte, keyID [8]byte, rawSK []byte) [32]byte {
	h, _ := blake2b.New256(nil)
	h.Write(sigAlg[:])
	h.Write(keyID[:])
	h.Write(rawSK)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// deriveKDFStream derives a 106-byte key stream from passphrase + salt.
func deriveKDFStream(passphrase, salt []byte, kdf [2]byte, opsLimit, memLimit uint64) ([]byte, error) {
	switch kdf {
	case KDFAlgScrypt:
		// N = opsLimit, r = 8, p = 1
		stream, err := scrypt.Key(passphrase, salt, int(opsLimit), 8, 1, encryptedBlobLen)
		if err != nil {
			return nil, fmt.Errorf("signing: scrypt: %w", err)
		}
		return stream, nil
	case KDFAlgArgon2id:
		// time = opsLimit, memory = memLimit/1024 (KiB), threads = 1
		stream := argon2.IDKey(passphrase, salt, uint32(opsLimit), uint32(memLimit/1024), 1, encryptedBlobLen)
		return stream, nil
	default:
		return nil, ErrUnsupportedKDF
	}
}
