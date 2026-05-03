package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
)

var (
	ErrSigInvalid             = errors.New("signing: signature verification failed")
	ErrKeyIDMismatch          = errors.New("signing: key ID mismatch")
	ErrTrustedCommentTampered = errors.New("signing: trusted comment signature verification failed")
)

// Sign signs payload with the raw Ed25519 private key and returns a Signature.
//
// The global signature covers sig || trustedComment, binding the trusted comment
// to the primary signature — tampering with the comment is detectable.
func Sign(rawSecret [64]byte, keyID [8]byte, payload []byte, trustedComment string) (Signature, error) {
	privKey := ed25519.PrivateKey(rawSecret[:])

	sig := ed25519.Sign(privKey, payload)

	globalInput := append(sig, []byte(trustedComment)...)
	globalSig := ed25519.Sign(privKey, globalInput)

	var s Signature
	s.SigAlg = SigAlgED25519
	s.KeyID = keyID
	copy(s.Sig[:], sig)
	s.TrustedComment = trustedComment
	copy(s.GlobalSig[:], globalSig)
	return s, nil
}

// Verify verifies a signature against payload and trusted comment.
func Verify(pk PublicKey, payload []byte, sig Signature, trustedComment string) error {
	if pk.KeyID != sig.KeyID {
		return ErrKeyIDMismatch
	}

	if !ed25519.Verify(pk.Key, payload, sig.Sig[:]) {
		return ErrSigInvalid
	}

	globalInput := append(sig.Sig[:], []byte(trustedComment)...)
	if !ed25519.Verify(pk.Key, globalInput, sig.GlobalSig[:]) {
		return ErrTrustedCommentTampered
	}

	return nil
}

// PluginPayload returns the payload to sign for a plugin bundle:
//
//	sha256(binary) || sha256(manifest)
//
// Both the CLI sign command and the host loader (#186) call this function to
// ensure both sides hash the same bytes in the same order. Spec §5.2.
func PluginPayload(binary, manifest []byte) []byte {
	bHash := sha256.Sum256(binary)
	mHash := sha256.Sum256(manifest)
	out := make([]byte, 64)
	copy(out[:32], bHash[:])
	copy(out[32:], mHash[:])
	return out
}
