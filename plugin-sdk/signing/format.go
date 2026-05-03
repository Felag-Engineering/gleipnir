package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Wire-format algorithm identifiers matching upstream Minisign.
var (
	SigAlgED25519        = [2]byte{'E', 'd'} // non-prehashed Ed25519 (we produce this)
	SigAlgED25519Prehash = [2]byte{'E', 'D'} // prehashed BLAKE2b (parse only)
	KDFAlgScrypt         = [2]byte{'S', 'c'} // upstream default KDF
	KDFAlgArgon2id       = [2]byte{'A', 'r'} // opt-in; requires minisign >= 0.11
	ChkAlgBlake2b        = [2]byte{'B', '2'}
)

// encryptedBlobLen is the length of the plaintext blob XOR'd by the KDF stream.
// Layout: sigalg[2] + keyid[8] + sk[64] + chk[32] = 106 bytes.
const encryptedBlobLen = 106

// PublicKey holds a Minisign public key.
type PublicKey struct {
	KeyID [8]byte
	Key   ed25519.PublicKey
}

// SecretKey holds a Minisign secret key. The KeyID, SecretKey, and Checksum
// fields always hold plaintext values. The EncryptedBlob field holds the
// 106-byte encrypted blob for serialization when KDFAlg is non-zero.
//
// Use GenerateKeypair to create, EncryptSecretKey to encrypt, and
// DecryptSecretKey to recover the raw Ed25519 private key.
type SecretKey struct {
	SigAlg      [2]byte
	KDFAlg      [2]byte
	ChkAlg      [2]byte
	KDFSalt     [32]byte
	KDFOpsLimit uint64
	KDFMemLimit uint64
	// Plaintext fields (always valid):
	KeyID     [8]byte
	SecretKey [64]byte
	Checksum  [32]byte
	// EncryptedBlob holds the XOR-encrypted blob for writing to disk.
	// Non-nil only after EncryptSecretKey is called.
	EncryptedBlob *[encryptedBlobLen]byte
}

// Signature holds a parsed Minisign .minisig file.
type Signature struct {
	SigAlg         [2]byte
	KeyID          [8]byte
	Sig            [64]byte
	TrustedComment string
	GlobalSig      [64]byte
}

// MarshalPublicKey serialises a public key to the upstream Minisign .pub format.
func MarshalPublicKey(pk PublicKey, untrustedComment string) []byte {
	if untrustedComment == "" {
		untrustedComment = "minisign public key"
	}
	// Binary: sigalg[2] || keyid[8] || pubkey[32]
	raw := make([]byte, 2+8+32)
	copy(raw[0:2], SigAlgED25519[:])
	copy(raw[2:10], pk.KeyID[:])
	copy(raw[10:42], pk.Key)

	encoded := base64.StdEncoding.EncodeToString(raw)
	return []byte(fmt.Sprintf("untrusted comment: %s\n%s\n", untrustedComment, encoded))
}

// ParsePublicKey parses a Minisign .pub file.
func ParsePublicKey(data []byte) (PublicKey, string, error) {
	comment, b64, err := parseTwoLineFile(data)
	if err != nil {
		return PublicKey{}, "", fmt.Errorf("parse public key: %w", err)
	}

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return PublicKey{}, "", fmt.Errorf("parse public key: decode base64: %w", err)
	}
	if len(raw) != 2+8+32 {
		return PublicKey{}, "", fmt.Errorf("parse public key: wrong length %d", len(raw))
	}

	var pk PublicKey
	copy(pk.KeyID[:], raw[2:10])
	pk.Key = make(ed25519.PublicKey, 32)
	copy(pk.Key, raw[10:42])
	return pk, comment, nil
}

// MarshalSecretKey serialises a secret key to the upstream Minisign .key format.
//
// Outer header (54 bytes): sigalg[2] kdfAlg[2] chkAlg[2] kdfSalt[32] opsLimit[8le] memLimit[8le]
// Encrypted blob (106 bytes): if sk.EncryptedBlob is set, uses it verbatim;
// otherwise writes plaintext sigalg||keyid||sk||chk.
func MarshalSecretKey(sk SecretKey, untrustedComment string) []byte {
	if untrustedComment == "" {
		untrustedComment = "minisign secret key"
	}

	raw := make([]byte, 160)
	copy(raw[0:2], sk.SigAlg[:])
	copy(raw[2:4], sk.KDFAlg[:])
	copy(raw[4:6], sk.ChkAlg[:])
	copy(raw[6:38], sk.KDFSalt[:])
	putLE64(raw[38:46], sk.KDFOpsLimit)
	putLE64(raw[46:54], sk.KDFMemLimit)

	if sk.EncryptedBlob != nil {
		copy(raw[54:160], sk.EncryptedBlob[:])
	} else {
		// Unencrypted: write plaintext blob
		copy(raw[54:56], sk.SigAlg[:])
		copy(raw[56:64], sk.KeyID[:])
		copy(raw[64:128], sk.SecretKey[:])
		copy(raw[128:160], sk.Checksum[:])
	}

	encoded := base64.StdEncoding.EncodeToString(raw)
	return []byte(fmt.Sprintf("untrusted comment: %s\n%s\n", untrustedComment, encoded))
}

// ParseSecretKey parses a Minisign .key file.
//
// The returned SecretKey has EncryptedBlob set to the raw 106-byte blob from
// the file (whether encrypted or not). The plaintext fields (KeyID, SecretKey,
// Checksum) are set from the blob directly and will contain encrypted bytes if
// the key is encrypted — call DecryptSecretKey to recover the plaintext.
func ParseSecretKey(data []byte) (SecretKey, string, error) {
	comment, b64, err := parseTwoLineFile(data)
	if err != nil {
		return SecretKey{}, "", fmt.Errorf("parse secret key: %w", err)
	}

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return SecretKey{}, "", fmt.Errorf("parse secret key: decode base64: %w", err)
	}
	if len(raw) < 160 {
		return SecretKey{}, "", fmt.Errorf("parse secret key: wrong length %d (expected 160)", len(raw))
	}

	var sk SecretKey
	copy(sk.SigAlg[:], raw[0:2])
	copy(sk.KDFAlg[:], raw[2:4])
	copy(sk.ChkAlg[:], raw[4:6])
	copy(sk.KDFSalt[:], raw[6:38])
	sk.KDFOpsLimit = getLE64(raw[38:46])
	sk.KDFMemLimit = getLE64(raw[46:54])

	// Store the blob (may be encrypted). The plaintext fields below may hold
	// encrypted bytes until DecryptSecretKey is called.
	var blob [encryptedBlobLen]byte
	copy(blob[:], raw[54:160])
	sk.EncryptedBlob = &blob

	// raw[54:56] = sigalg (XOR'd if encrypted; matches outer header when not)
	copy(sk.KeyID[:], raw[56:64])
	copy(sk.SecretKey[:], raw[64:128])
	copy(sk.Checksum[:], raw[128:160])
	return sk, comment, nil
}

// MarshalSignature serialises a Signature to the upstream .minisig format.
func MarshalSignature(sig Signature, untrustedComment string) []byte {
	if untrustedComment == "" {
		untrustedComment = "signature from minisign secret key"
	}

	// Line 2: base64(sigalg[2] || keyid[8] || sig[64])
	raw := make([]byte, 2+8+64)
	copy(raw[0:2], sig.SigAlg[:])
	copy(raw[2:10], sig.KeyID[:])
	copy(raw[10:74], sig.Sig[:])
	encoded := base64.StdEncoding.EncodeToString(raw)

	// Line 4: base64(sigalg[2] || keyid[8] || globalSig[64])
	globalRaw := make([]byte, 2+8+64)
	copy(globalRaw[0:2], sig.SigAlg[:])
	copy(globalRaw[2:10], sig.KeyID[:])
	copy(globalRaw[10:74], sig.GlobalSig[:])
	globalEncoded := base64.StdEncoding.EncodeToString(globalRaw)

	return []byte(fmt.Sprintf(
		"untrusted comment: %s\n%s\ntrusted comment: %s\n%s\n",
		untrustedComment, encoded, sig.TrustedComment, globalEncoded,
	))
}

// ParseSignature parses a .minisig file.
func ParseSignature(data []byte) (Signature, string, error) {
	lines := strings.SplitN(strings.TrimRight(string(data), "\n"), "\n", 4)
	if len(lines) < 4 {
		return Signature{}, "", errors.New("parse signature: expected 4 lines")
	}

	untrustedComment := strings.TrimPrefix(lines[0], "untrusted comment: ")
	if untrustedComment == lines[0] {
		return Signature{}, "", errors.New("parse signature: missing untrusted comment header")
	}

	raw, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		return Signature{}, "", fmt.Errorf("parse signature: decode sig base64: %w", err)
	}
	if len(raw) != 2+8+64 {
		return Signature{}, "", fmt.Errorf("parse signature: wrong sig length %d", len(raw))
	}

	trustedComment := strings.TrimPrefix(lines[2], "trusted comment: ")
	if trustedComment == lines[2] {
		return Signature{}, "", errors.New("parse signature: missing trusted comment header")
	}

	globalRaw, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil {
		return Signature{}, "", fmt.Errorf("parse signature: decode global sig base64: %w", err)
	}
	if len(globalRaw) != 2+8+64 {
		return Signature{}, "", fmt.Errorf("parse signature: wrong global sig length %d", len(globalRaw))
	}

	var sig Signature
	copy(sig.SigAlg[:], raw[0:2])
	copy(sig.KeyID[:], raw[2:10])
	copy(sig.Sig[:], raw[10:74])
	sig.TrustedComment = trustedComment
	copy(sig.GlobalSig[:], globalRaw[10:74])
	return sig, untrustedComment, nil
}

// parseTwoLineFile extracts (untrusted comment, base64 data) from a
// 2-line Minisign file (public key or secret key format).
func parseTwoLineFile(data []byte) (string, string, error) {
	lines := strings.SplitN(strings.TrimRight(string(data), "\n"), "\n", 2)
	if len(lines) < 2 {
		return "", "", errors.New("expected at least 2 lines")
	}
	comment := strings.TrimPrefix(lines[0], "untrusted comment: ")
	if comment == lines[0] {
		return "", "", errors.New("missing 'untrusted comment:' header")
	}
	return comment, strings.TrimSpace(lines[1]), nil
}

func putLE64(b []byte, v uint64) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	b[4] = byte(v >> 32)
	b[5] = byte(v >> 40)
	b[6] = byte(v >> 48)
	b[7] = byte(v >> 56)
}

func getLE64(b []byte) uint64 {
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}
