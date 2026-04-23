package auth

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashSessionToken returns the SHA-256 hex digest of a raw session token.
//
// Session tokens are 256-bit random values stored in cookies. We hash before
// any DB interaction so a database leak does not yield usable session tokens.
// SHA-256 (not bcrypt) is appropriate here because the tokens are already
// high-entropy random values — brute-force attacks against them are infeasible.
func HashSessionToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
