// Package auth provides password hashing and session authentication middleware.
package auth

import "golang.org/x/crypto/bcrypt"

// productionBcryptCost is the work factor used when hashing passwords in
// production. Set to 12 rather than bcrypt.DefaultCost (10) to provide a
// stronger brute-force resistance margin on modern hardware.
// TestHashPasswordCost pins this value.
const productionBcryptCost = 12

// bcryptCost is the live work factor. It is a var (not a const) solely so
// tests can lower it via SetBcryptCostForTest — hashing at cost 12 takes
// ~250ms per call, which multiplies into minutes across the handler tests
// that create users and log in. Production code must never reassign it.
var bcryptCost = productionBcryptCost

// SetBcryptCostForTest overrides the bcrypt work factor and returns a restore
// func. Tests (typically a package's TestMain) pass bcrypt.MinCost so
// user-creation and login setup stop dominating suite runtime; the embedded
// cost travels inside each hash, so verification of existing hashes is
// unaffected. Follows the SetXForTest convention (see CLAUDE.md "Testing
// time-dependent code" rule 3). Never call from production code.
func SetBcryptCostForTest(cost int) (restore func()) {
	prev := bcryptCost
	bcryptCost = cost
	return func() { bcryptCost = prev }
}

// HashPassword returns a bcrypt hash of plain.
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword returns nil if plain matches hash, or an error otherwise.
// Use errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) to distinguish a
// wrong password from other failures.
func CheckPassword(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}
