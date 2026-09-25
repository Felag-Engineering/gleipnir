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
// TODO(#962): once the container substrate self-attaches Gleipnir to every
// per-instance network (the #958 design), Gleipnir's own interfaces will
// legitimately sit inside this pool by construction — that is the whole
// point of self-attach. This check will need to exclude interfaces on
// Gleipnir-labelled instance networks specifically at that point, rather
// than refusing any overlap outright as it does today. See the package doc's
// "No escape hatch" section: that exception has to be scoped and deliberate,
// not a blanket bypass.
func CheckPoolOverlap(pool netip.Prefix) error {
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
	return checkOverlap(pool, addrs)
}

// checkOverlap is CheckPoolOverlap's pure decision logic, factored out so
// tests can exercise it against a synthetic address list instead of the
// host's real interfaces.
func checkOverlap(pool netip.Prefix, addrs []net.Addr) error {
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
			return fmt.Errorf(
				"GLEIPNIR_PLUGIN_SUBNET_POOL %s overlaps local interface subnet %s; choose a non-overlapping pool",
				pool, ifaceSubnet,
			)
		}
	}
	return nil
}
