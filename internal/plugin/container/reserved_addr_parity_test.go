package container_test

import (
	"net/netip"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
)

// #1033 f: the ".2" formula lives in two places because container must not
// import egress. This pins them to the same output so a change to one cannot
// silently leave the other reserving a different address.
func TestGleipnirReservedAddrMatchesEgress(t *testing.T) {
	subnets := []string{
		"10.83.0.0/24",
		"10.83.4.0/24",
		"10.83.255.0/24",
		"10.83.4.77/24", // unmasked host bits: both must still derive from the network address
		"192.168.7.0/25",
	}
	for _, subnet := range subnets {
		t.Run(subnet, func(t *testing.T) {
			want, err := egress.GleipnirAddrOf(subnet)
			if err != nil {
				t.Fatalf("egress.GleipnirAddrOf: %v", err)
			}
			got := container.GleipnirReservedAddr(netip.MustParsePrefix(subnet))
			if got.String() != want.String() {
				t.Errorf("container reserves %s, egress.GleipnirAddrOf = %s", got, want)
			}
		})
	}
}
