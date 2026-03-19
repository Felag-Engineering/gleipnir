// Package auth provides password hashing and session authentication middleware.
package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword returns a bcrypt hash of plain using the default cost.
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
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
