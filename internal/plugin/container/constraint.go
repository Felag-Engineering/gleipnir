package container

import "fmt"

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
)

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
	return nil
}

// ValidateCreateNetwork checks opts against the self-constraint rule that
// every network Gleipnir creates is internal-only (spec §7: "internal
// networks only"). It returns a *ConstraintViolationError when Internal is
// false, or nil if opts is acceptable.
func ValidateCreateNetwork(opts NetworkOptions) error {
	if !opts.Internal {
		return &ConstraintViolationError{
			Kind:   ViolationExternalNetwork,
			Detail: "every plugin network must be internal-only (no default gateway to the outside)",
		}
	}
	return nil
}
