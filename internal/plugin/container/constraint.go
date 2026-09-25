package container

import (
	"fmt"
	"net/netip"
)

// ViolationKind identifies which self-constraint rule a CreateOptions or
// NetworkOptions request violated. Callers can switch on Kind rather than
// string-matching Error().
type ViolationKind string

const (
	ViolationExtraMount      ViolationKind = "extra_mount"
	ViolationPrivileged      ViolationKind = "privileged"
	ViolationAddedCapability ViolationKind = "added_capability"
	ViolationHostNetwork     ViolationKind = "host_network"
	ViolationNoNetwork       ViolationKind = "no_network"
	ViolationExternalNetwork ViolationKind = "external_network"

	// ViolationMissingCapDrop is Create's self-constraint (spec §7 hardening,
	// #958 finding 2, tightened by #1021 review item V2): every managed
	// container must drop ALL capabilities.
	ViolationMissingCapDrop ViolationKind = "missing_cap_drop"

	// ViolationMissingSecurityOpt is Create's self-constraint (#1021 review
	// item V2): every managed container must run with no-new-privileges.
	ViolationMissingSecurityOpt ViolationKind = "missing_security_opt"

	// ViolationIPv6Enabled is CreateNetwork's self-constraint (#1021 review
	// item V1): every managed instance network must have IPv6 disabled.
	ViolationIPv6Enabled ViolationKind = "ipv6_enabled"

	// ViolationUnmanagedNetwork is SelfAttacher's own self-constraint (see
	// self.go): Gleipnir's own container may join or leave only a network
	// carrying the managed label it was constructed with.
	ViolationUnmanagedNetwork ViolationKind = "unmanaged_network"

	// ViolationInstanceMismatch is SelfAttacher's own self-constraint: the
	// network's instance label must name the exact instance the caller is
	// acting on, not merely some managed instance.
	ViolationInstanceMismatch ViolationKind = "instance_mismatch"

	// ViolationSubnetMismatch is SelfAttacher's own self-constraint: the
	// network's subnet must fall inside the configured plugin-subnet pool,
	// and must equal the instance's allocated subnet when the caller knows
	// what that is.
	ViolationSubnetMismatch ViolationKind = "subnet_mismatch"

	// ViolationReservedAddrInRange is CreateNetwork's self-constraint (#1021
	// review item 3): when a network request carries an IPRange, the address
	// Gleipnir's self-attach reserves (the same one egress.GleipnirAddrOf
	// derives) must fall inside the Subnet but outside that IPRange — a
	// plugin container's own dynamically-allocated address must never be
	// able to land on the address self-attach pins.
	ViolationReservedAddrInRange ViolationKind = "reserved_addr_in_range"
)

// requiredCapDrop is the capability-drop value ValidateCreate requires (#1021
// review item V2, tightened from #958 finding 2's original NET_RAW-only bar):
// every managed container drops ALL capabilities. An image that genuinely
// needs one back (e.g. a legitimate CHOWN-on-startup entrypoint) is expected
// to run that step in its own build rather than at container-create time —
// the alternative, admitting a caller-chosen subset, is exactly the surface a
// hostile or careless create request would use to keep the one capability it
// wants.
const requiredCapDrop = "ALL"

// requiredSecurityOpt is the security-opt value ValidateCreate requires
// (#1021 review item V2), paired with dropping ALL capabilities: with every
// capability gone, a setuid-root binary in the image is the remaining way to
// regain privilege, and no-new-privileges is what closes that specific door.
const requiredSecurityOpt = "no-new-privileges"

// ConstraintViolationError is returned by Create/CreateNetwork when the
// requested options would violate Gleipnir's self-constraint on container
// creation (spec §7). This is a security boundary enforced in the wrapper
// itself, not caller discipline — every Runtime implementation validates
// through ValidateCreate/ValidateCreateNetwork before touching the socket.
type ConstraintViolationError struct {
	Kind   ViolationKind
	Detail string
}

func (e *ConstraintViolationError) Error() string {
	return fmt.Sprintf("container: self-constraint violated (%s): %s", e.Kind, e.Detail)
}

// ValidateCreate checks opts against the self-constraint rules: no mounts
// beyond the per-instance volume, no privileged mode, no added capabilities,
// and attachment to exactly one non-host network. It returns a
// *ConstraintViolationError describing the first violation found, or nil if
// opts is acceptable.
func ValidateCreate(opts CreateOptions) error {
	if len(opts.Mounts) > 0 {
		return &ConstraintViolationError{
			Kind:   ViolationExtraMount,
			Detail: fmt.Sprintf("%d mount(s) requested beyond the per-instance volume", len(opts.Mounts)),
		}
	}
	if opts.Privileged {
		return &ConstraintViolationError{
			Kind:   ViolationPrivileged,
			Detail: "privileged mode is never permitted for managed plugin containers",
		}
	}
	if len(opts.CapAdd) > 0 {
		return &ConstraintViolationError{
			Kind:   ViolationAddedCapability,
			Detail: fmt.Sprintf("added capabilities are never permitted: %v", opts.CapAdd),
		}
	}
	if opts.Network == "" {
		return &ConstraintViolationError{
			Kind:   ViolationNoNetwork,
			Detail: "a container must attach to its per-instance internal network",
		}
	}
	if opts.Network == hostNetworkMode {
		return &ConstraintViolationError{
			Kind:   ViolationHostNetwork,
			Detail: "the host network is never permitted for managed plugin containers",
		}
	}
	if !containsString(opts.CapDrop, requiredCapDrop) {
		return &ConstraintViolationError{
			Kind: ViolationMissingCapDrop,
			Detail: fmt.Sprintf(
				"a managed container must drop %s; got CapDrop=%v", requiredCapDrop, opts.CapDrop,
			),
		}
	}
	if !containsString(opts.SecurityOpt, requiredSecurityOpt) {
		return &ConstraintViolationError{
			Kind: ViolationMissingSecurityOpt,
			Detail: fmt.Sprintf(
				"a managed container must set %s; got SecurityOpt=%v", requiredSecurityOpt, opts.SecurityOpt,
			),
		}
	}
	return nil
}

func containsString(haystack []string, want string) bool {
	for _, got := range haystack {
		if got == want {
			return true
		}
	}
	return false
}

// ValidateCreateNetwork checks opts against the self-constraint rules that
// every network Gleipnir creates is internal-only (spec §7: "internal
// networks only"), has IPv6 disabled (#1021 review item V1), and — when the
// request carries an IPRange — reserves the self-attach address outside it
// (#1021 review item 3). It returns a *ConstraintViolationError describing
// the first violation found, or nil if opts is acceptable.
func ValidateCreateNetwork(opts NetworkOptions) error {
	if !opts.Internal {
		return &ConstraintViolationError{
			Kind:   ViolationExternalNetwork,
			Detail: "every plugin network must be internal-only (no default gateway to the outside)",
		}
	}
	if opts.EnableIPv6 {
		return &ConstraintViolationError{
			Kind:   ViolationIPv6Enabled,
			Detail: "every plugin network must have IPv6 disabled",
		}
	}
	if opts.IPRange != "" {
		if err := validateIPRangeExcludesReserved(opts); err != nil {
			return err
		}
	}
	return nil
}

// validateIPRangeExcludesReserved checks that the reserved self-attach
// address (gleipnirReservedAddr) is inside opts.Subnet but outside
// opts.IPRange — the dynamic-allocation pool a plugin container's own
// (unpinned) address is drawn from must never be able to include it.
func validateIPRangeExcludesReserved(opts NetworkOptions) error {
	if opts.Subnet == "" {
		return &ConstraintViolationError{
			Kind:   ViolationReservedAddrInRange,
			Detail: "an IPRange requires a Subnet to validate the reserved address against",
		}
	}
	subnet, err := netip.ParsePrefix(opts.Subnet)
	if err != nil {
		return &ConstraintViolationError{
			Kind:   ViolationReservedAddrInRange,
			Detail: fmt.Sprintf("subnet %q does not parse: %v", opts.Subnet, err),
		}
	}
	ipRange, err := netip.ParsePrefix(opts.IPRange)
	if err != nil {
		return &ConstraintViolationError{
			Kind:   ViolationReservedAddrInRange,
			Detail: fmt.Sprintf("IPRange %q does not parse: %v", opts.IPRange, err),
		}
	}

	reserved := gleipnirReservedAddr(subnet)
	if !subnet.Contains(reserved) {
		return &ConstraintViolationError{
			Kind:   ViolationReservedAddrInRange,
			Detail: fmt.Sprintf("reserved address %s is not inside subnet %s", reserved, subnet),
		}
	}
	if ipRange.Contains(reserved) {
		return &ConstraintViolationError{
			Kind:   ViolationReservedAddrInRange,
			Detail: fmt.Sprintf("IPRange %s must exclude the reserved self-attach address %s", ipRange, reserved),
		}
	}
	return nil
}

// gleipnirReservedAddr computes the address egress.GleipnirAddrOf derives
// from the same subnet — the second usable address, one past the network's
// own gateway. Duplicated here rather than imported: this package must not
// depend on internal/plugin/egress (container is the lower-level primitive
// egress builds on, not the reverse). Kept in sync by hand; egress.GleipnirAddrOf's
// own doc names itself as the one place that formula is authoritative.
func gleipnirReservedAddr(subnet netip.Prefix) netip.Addr {
	base := subnet.Masked().Addr().As4()
	base[3] += 2
	return netip.AddrFrom4(base)
}
