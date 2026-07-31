package api_test

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/felag-engineering/gleipnir/internal/http/auth"
)

// TestMain lowers the bcrypt work factor for the whole package: router tests
// create users and log in through the real auth handlers, and cost-12 hashing
// (~250ms each) was the dominant share of the package's runtime.
func TestMain(m *testing.M) {
	restore := auth.SetBcryptCostForTest(bcrypt.MinCost)
	code := m.Run()
	restore()
	os.Exit(code)
}
