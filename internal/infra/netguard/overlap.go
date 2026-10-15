package netguard

import (
	"fmt"
	"net"
	"net/netip"
)

// CheckPoolOverlap inspects the host's own network interfaces and returns an
// error if any live interface's subnet overlaps pool.
//
// This exists because the Listener guard only protects a listener that is
// actually wrapped; nothing stops GLEIPNIR_PLUGIN_SUBNET_POOL itself from
// overlapping an address the host already uses for something else. An
// overlap there is worse than a bind failure — it means a "plugin subnet"
// address is also a real interface address, so traffic meant for that
// interface and traffic the guard is supposed to be walling off both land on
// the same IP, defeating the LocalAddr check by making it ambiguous which
// side an address actually belongs to.
//
// The comparison is subnet-vs-subnet (netip.Prefix.Overlaps), not just
// "is this one interface address inside the pool": an interface's own
// advertised subnet can overlap the pool even when its specific address
// doesn't. A host on a /16 LAN whose address happens to fall outside a
// narrower plugin pool still shares part of that LAN's address space with
// the pool if the pool is a sub-range of it — and that's exactly the
// on-link collision this check exists to catch before it ever reaches a
// running Accept loop.
//
// Interfaces are walked via net.Interfaces + Interface.Addrs (rather than
// the flat net.InterfaceAddrs) specifically so a future, more targeted
// version of this check has interface names to filter on — see the TODO
// below.
//
// CheckPoolOverlap has no exceptions. A caller that can prove, from the
// container runtime, that specific interfaces are Gleipnir's own plugin-network
// attachments uses CheckPoolOverlapExcluding instead.
func CheckPoolOverlap(pool netip.Prefix) error {
	return CheckPoolOverlapExcluding(pool, nil)
}

// ExcludedAttachment names one interface the overlap check must not count: the
// address Gleipnir's own container holds on one per-instance plugin network.
//
// It is deliberately narrow. Subnet is a /24 inside the pool (the per-instance
// allocation size) and Addr is the reserved Gleipnir address on it, the
// subnet's network address plus 2. An interface is excluded only when both its
// masked subnet equals Subnet AND its own address equals Addr; an interface
// that merely overlaps, or sits on the right subnet at another address, still
// fails the check.
type ExcludedAttachment struct {
	Subnet netip.Prefix
	Addr   netip.Addr
}

const (
	attachmentPrefixBits = 24
	// reservedGleipnirOffset is Gleipnir's host offset within a per-instance
	// subnet. netguard is a leaf and cannot import the egress package that owns
	// this value; the v2 hook re-checks every exclusion against egress, so the
	// two cannot silently drift apart.
	reservedGleipnirOffset = 2
)

func (e ExcludedAttachment) validate(pool netip.Prefix) error {
	if !e.Subnet.IsValid() || !e.Subnet.Addr().Is4() {
		return fmt.Errorf("netguard: excluded subnet %s is not a valid IPv4 prefix", e.Subnet)
	}
	if e.Subnet.Bits() != attachmentPrefixBits {
		return fmt.Errorf("netguard: excluded subnet %s must be a /%d", e.Subnet, attachmentPrefixBits)
	}
	if e.Subnet != e.Subnet.Masked() {
		return fmt.Errorf("netguard: excluded subnet %s has host bits set", e.Subnet)
	}
	if !(pool.Bits() <= e.Subnet.Bits() && pool.Contains(e.Subnet.Addr())) {
		return fmt.Errorf("netguard: excluded subnet %s is outside pool %s", e.Subnet, pool)
	}
	if want := reservedAddr(e.Subnet); e.Addr != want {
		return fmt.Errorf("netguard: excluded address %s on %s is not the reserved address %s", e.Addr, e.Subnet, want)
	}
	return nil
}

// reservedAddr returns the subnet's network address plus the reserved offset.
// Only called for a validated /24, so the last octet cannot overflow.
func reservedAddr(subnet netip.Prefix) netip.Addr {
	b := subnet.Addr().As4()
	b[3] += reservedGleipnirOffset
	return netip.AddrFrom4(b)
}

// CheckPoolOverlapExcluding is CheckPoolOverlap, except that interfaces
// matching an entry of exclude exactly are not counted. Malformed or
// out-of-pool exclusions are an error, never silently ignored: an exclusion
// that cannot match anything is a caller bug worth surfacing.
func CheckPoolOverlapExcluding(pool netip.Prefix, exclude []ExcludedAttachment) error {
	for _, e := range exclude {
		if err := e.validate(pool); err != nil {
			return err
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("netguard: enumerate local interfaces: %w", err)
	}
	var addrs []net.Addr
	for _, iface := range ifaces {
		ifaceAddrs, err := iface.Addrs()
		if err != nil {
			return fmt.Errorf("netguard: enumerate addresses for interface %s: %w", iface.Name, err)
		}
		addrs = append(addrs, ifaceAddrs...)
	}
	return checkOverlap(pool, addrs, exclude)
}

// checkOverlap is CheckPoolOverlap's pure decision logic, factored out so
// tests can exercise it against a synthetic address list instead of the
// host's real interfaces.
func checkOverlap(pool netip.Prefix, addrs []net.Addr, exclude []ExcludedAttachment) error {
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if !ip.Is4() {
			// The pool is always IPv4 (validated at config load), so an IPv6
			// interface subnet can never overlap it.
			continue
		}

		ones, bits := ipNet.Mask.Size()
		if bits != 32 {
			// A non-canonical or non-IPv4 mask (Size returns 0,0 for those) —
			// nothing meaningful to compare against an IPv4 pool.
			continue
		}
		// Masked so the error names the interface's actual network (e.g.
		// 10.83.0.0/16), not its specific host address on that network.
		ifaceSubnet := netip.PrefixFrom(ip, ones).Masked()
		if pool.Overlaps(ifaceSubnet) {
			if isExcluded(ip, ifaceSubnet, exclude) {
				continue
			}
			return fmt.Errorf(
				"GLEIPNIR_PLUGIN_SUBNET_POOL %s overlaps local interface subnet %s; choose a non-overlapping pool",
				pool, ifaceSubnet,
			)
		}
	}
	return nil
}

func isExcluded(ip netip.Addr, ifaceSubnet netip.Prefix, exclude []ExcludedAttachment) bool {
	for _, e := range exclude {
		if e.Subnet == ifaceSubnet && e.Addr == ip {
			return true
		}
	}
	return false
}
