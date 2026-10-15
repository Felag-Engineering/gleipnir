package netguard

import (
	"net"
	"net/netip"
	"strings"
	"testing"
)

func attachment(subnet, addr string) ExcludedAttachment {
	return ExcludedAttachment{Subnet: netip.MustParsePrefix(subnet), Addr: netip.MustParseAddr(addr)}
}

func TestCheckOverlapExcluding(t *testing.T) {
	pool := netip.MustParsePrefix("10.83.0.0/16")
	own := attachment("10.83.4.0/24", "10.83.4.2")

	tests := []struct {
		name    string
		addrs   []string
		exclude []ExcludedAttachment
		wantErr bool
	}{
		{"exact match is excluded", []string{"10.83.4.2/24"}, []ExcludedAttachment{own}, false},
		{"empty exclusion list behaves as before", []string{"10.83.4.2/24"}, nil, true},
		{"no overlap with empty list", []string{"192.168.1.5/24"}, nil, false},
		{"gateway address on the excluded subnet still fails", []string{"10.83.4.1/24"}, []ExcludedAttachment{own}, true},
		{"other host address on the excluded subnet still fails", []string{"10.83.4.5/24"}, []ExcludedAttachment{own}, true},
		{"reserved address with a different mask still fails", []string{"10.83.4.2/16"}, []ExcludedAttachment{own}, true},
		{"overlapping interface on another subnet still fails", []string{"10.83.9.2/24"}, []ExcludedAttachment{own}, true},
		{"excluded attachment does not hide a real LAN overlap", []string{"10.83.4.2/24", "10.83.0.7/16"}, []ExcludedAttachment{own}, true},
		{"several attachments all excluded", []string{"10.83.4.2/24", "10.83.5.2/24"}, []ExcludedAttachment{own, attachment("10.83.5.0/24", "10.83.5.2")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var addrs []net.Addr
			for _, a := range tt.addrs {
				addrs = append(addrs, ipNetAddr(t, a))
			}
			err := checkOverlap(pool, addrs, tt.exclude)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkOverlap error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckPoolOverlapExcluding_RejectsInvalidExclusions(t *testing.T) {
	pool := netip.MustParsePrefix("10.83.0.0/16")
	tests := []struct {
		name string
		ex   ExcludedAttachment
		want string
	}{
		{"outside the pool", attachment("10.84.4.0/24", "10.84.4.2"), "outside pool"},
		{"not a /24", attachment("10.83.4.0/25", "10.83.4.2"), "must be a /24"},
		{"wider than a /24", attachment("10.83.0.0/16", "10.83.0.2"), "must be a /24"},
		{"host bits set", attachment("10.83.4.7/24", "10.83.4.2"), "host bits"},
		{"wrong address .1", attachment("10.83.4.0/24", "10.83.4.1"), "reserved address"},
		{"wrong address .5", attachment("10.83.4.0/24", "10.83.4.5"), "reserved address"},
		{"zero value", ExcludedAttachment{}, "not a valid IPv4"},
		{"IPv6", attachment("fd00::/24", "fd00::2"), "not a valid IPv4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckPoolOverlapExcluding(pool, []ExcludedAttachment{tt.ex})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
