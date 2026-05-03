package signing

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	restore := SetScryptNForTesting(1 << 14)
	defer restore()
	os.Exit(m.Run())
}
