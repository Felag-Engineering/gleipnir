package cmd

import (
	"os"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

func TestMain(m *testing.M) {
	restore := signing.SetScryptNForTesting(1 << 14)
	defer restore()
	os.Exit(m.Run())
}
