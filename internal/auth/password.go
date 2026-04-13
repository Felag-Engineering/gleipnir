// Package auth provides password hashing and session authentication middleware.
package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost is the work factor used when hashing passwords. Set to 12 rather
// than bcrypt.DefaultCost (10) to provide a stronger brute-force resistance
// margin on modern hardware.
const bcryptCost = 12

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
