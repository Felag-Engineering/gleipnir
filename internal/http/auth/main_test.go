package auth

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestMain lowers the bcrypt work factor for the whole package: these tests
// hash passwords constantly (user creation, login, session setup) and cost-12
// hashing (~250ms each) was the dominant share of the package's runtime.
// TestHashPasswordCost restores the production cost locally to pin it.
func TestMain(m *testing.M) {
	restore := SetBcryptCostForTest(bcrypt.MinCost)
	code := m.Run()
	restore()
	os.Exit(code)
}
