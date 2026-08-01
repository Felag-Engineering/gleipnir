package main

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/felag-engineering/gleipnir/internal/http/auth"
)

// TestMain lowers the bcrypt work factor for the whole package: create-user
// and reset-password tests hash through auth.HashPassword, and cost-12
// hashing (~250ms each) was a large share of the package's runtime.
func TestMain(m *testing.M) {
	restore := auth.SetBcryptCostForTest(bcrypt.MinCost)
	code := m.Run()
	restore()
	os.Exit(code)
}
