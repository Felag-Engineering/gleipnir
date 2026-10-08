//go:build substrate

package substrate_test

import (
	"net"
	"net/netip"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
)

// guardedOperatorAPI returns a real netguard.Guard by wrapping a throwaway
// loopback listener, the only way to obtain a non-zero one.
func guardedOperatorAPI(t *testing.T) netguard.Guard {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	guarded, err := netguard.Wrap(ln, netip.MustParsePrefix("10.83.0.0/16"), nil)
	if err != nil {
		t.Fatalf("netguard.Wrap: %v", err)
	}
	return guarded.Guard()
}
