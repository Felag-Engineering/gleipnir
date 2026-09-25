package netguard

import (
	"net"
	"net/netip"
	"strings"
	"testing"
)

func ipNetAddr(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("test setup: ParseCIDR(%q): %v", cidr, err)
	}
	ipNet.IP = ip // net.ParseCIDR masks IP to the network address; keep the host bits
	return ipNet
}

func TestCheckOverlap(t *testing.T) {
	pool := netip.MustParsePrefix("10.83.0.0/16")

	t.Run("no overlap returns nil", func(t *testing.T) {
		addrs := []net.Addr{
			ipNetAddr(t, "192.168.1.5/24"),
			ipNetAddr(t, "127.0.0.1/8"),
		}
		if err := checkOverlap(pool, addrs); err != nil {
			t.Errorf("checkOverlap: unexpected error: %v", err)
		}
	})

	t.Run("an interface address inside the pool is refused", func(t *testing.T) {
		addrs := []net.Addr{
			ipNetAddr(t, "192.168.1.5/24"),
			ipNetAddr(t, "10.83.4.1/24"),
		}
		err := checkOverlap(pool, addrs)
		if err == nil {
			t.Fatal("checkOverlap: expected an error, got nil")
		}
		wantSubstrings := []string{
			"GLEIPNIR_PLUGIN_SUBNET_POOL",
			"10.83.0.0/16",
			"overlaps local interface subnet",
			"10.83.4.0/24",
			"choose a non-overlapping pool",
		}
		for _, want := range wantSubstrings {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err.Error(), want)
			}
		}
	})

	// This is the finding checkOverlap exists to close: a naive "is the
	// interface's address inside the pool" check would miss it, because
	// 10.83.9.1 itself is not in the narrower pool below — but the
	// interface's whole /16 LAN subnet is, so any address in that LAN
	// (including plenty outside 10.83.9.0/24) could collide with a plugin
	// gateway address in the pool.
	t.Run("a wider interface subnet that contains the pool is refused, even though the interface's own address sits outside it", func(t *testing.T) {
		narrowPool := netip.MustParsePrefix("10.83.4.0/24")
		addrs := []net.Addr{
			ipNetAddr(t, "10.83.9.1/16"), // subnet 10.83.0.0/16 contains 10.83.4.0/24
		}
		err := checkOverlap(narrowPool, addrs)
		if err == nil {
			t.Fatal("checkOverlap: expected an error, got nil")
		}
		if !strings.Contains(err.Error(), "10.83.0.0/16") {
			t.Errorf("error %q does not name the interface's actual (masked) subnet", err.Error())
		}
	})

	t.Run("an interface address contained by the pool but on a narrower subnet is refused", func(t *testing.T) {
		// The reverse direction: the interface's own subnet is narrower than
		// (contained by) the pool. Overlaps must catch this direction too.
		addrs := []net.Addr{
			ipNetAddr(t, "10.83.4.1/32"),
		}
		if err := checkOverlap(pool, addrs); err == nil {
			t.Error("checkOverlap: expected an error, got nil")
		}
	})

	t.Run("non-IPNet addresses are ignored, not treated as errors", func(t *testing.T) {
		addrs := []net.Addr{
			&net.UnixAddr{Name: "/tmp/whatever.sock", Net: "unix"},
		}
		if err := checkOverlap(pool, addrs); err != nil {
			t.Errorf("checkOverlap: unexpected error: %v", err)
		}
	})

	t.Run("IPv6 interface addresses can never overlap an IPv4 pool", func(t *testing.T) {
		addrs := []net.Addr{
			ipNetAddr(t, "fd00::1/64"),
		}
		if err := checkOverlap(pool, addrs); err != nil {
			t.Errorf("checkOverlap: unexpected error: %v", err)
		}
	})
}

// TestCheckPoolOverlap_RealInterfaces exercises the real net.InterfaceAddrs
// path (not just the injectable checkOverlap core) against a pool chosen from
// TEST-NET-3 (RFC 5737, 203.0.113.0/24) specifically because it is reserved
// for documentation and can never be a real interface address on any host,
// so this assertion can't be flaky.
func TestCheckPoolOverlap_RealInterfaces(t *testing.T) {
	pool := netip.MustParsePrefix("203.0.113.0/24")
	if err := CheckPoolOverlap(pool); err != nil {
		t.Errorf("CheckPoolOverlap: unexpected error against a documentation-only pool: %v", err)
	}
}
