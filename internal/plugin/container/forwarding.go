package container

import (
	"fmt"
	"os"
	"strings"
)

// The kernel switches that must all read their required value before
// Gleipnir may self-attach to a plugin instance network (#958 finding 2,
// extended by #1021 review item 2). If any forwarding switch reads "1",
// Gleipnir's own container could route packets between two networks it is
// attached to — turning the very container meant to reach each instance into
// a bridge between them, which would undo the east-west isolation the
// per-instance network topology exists to establish. disable_ipv6 close a
// narrower gap: even with forwarding off, an interface that autoconfigures a
// link-local fe80:: address is one more thing living on Gleipnir's own
// container that a future component (the operator API, an admin listener)
// could be reachable on from inside an instance network unless it is turned
// off at the source. Package-level vars (not consts) so a test can point them
// at fixture files.
var (
	ipv4ForwardPath        = "/proc/sys/net/ipv4/ip_forward"
	ipv4DefaultForwardPath = "/proc/sys/net/ipv4/conf/default/forwarding"
	ipv6ForwardPath        = "/proc/sys/net/ipv6/conf/all/forwarding"
	ipv6DefaultForwardPath = "/proc/sys/net/ipv6/conf/default/forwarding"
	ipv6DisableDefaultPath = "/proc/sys/net/ipv6/conf/default/disable_ipv6"
)

// ipv6SysctlDir is the directory whose absence means this kernel was built
// without IPv6 support at all. When it is absent, every IPv6-specific check
// below is trivially satisfied — there is no IPv6 stack for Gleipnir's own
// container to route through or expose a link-local address on. Injectable
// (with pathExists) so a test can simulate an IPv6-less kernel without
// depending on the real one.
var ipv6SysctlDir = "/proc/sys/net/ipv6"

// pathExists is the injectable seam over checking whether ipv6SysctlDir is
// present.
var pathExists = func(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readSysctl is the injectable seam over the /proc reads above.
var readSysctl = func(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ForwardingEnabledError is returned by CheckForwardingDisabled when a kernel
// switch could not be confirmed at its required value — either because it
// read something else, or because it could not be read at all. Both cases
// fail closed: if the precondition cannot be confirmed, self-attach must not
// be enabled on the strength of a guess.
type ForwardingEnabledError struct {
	Path string
	// Want is the value the switch is required to read (e.g. "0" for a
	// forwarding switch, "1" for disable_ipv6).
	Want string
	// Value is the raw (trimmed) sysctl content, or "" when the read itself
	// failed — Err distinguishes the two.
	Value string
	Err   error
}

func (e *ForwardingEnabledError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("container: reading %s: %v", e.Path, e.Err)
	}
	return fmt.Sprintf("container: %s reads %q, want %q — self-attach requires this switch set before it will run", e.Path, e.Value, e.Want)
}

func (e *ForwardingEnabledError) Unwrap() error { return e.Err }

// CheckForwardingDisabled reads Gleipnir's own network-namespace kernel
// switches and returns a *ForwardingEnabledError naming the first one not
// confirmed at its required value, or nil when every applicable switch
// checks out. Self-attach must not be enabled unless this returns nil (spec
// §7 / #958 finding 2 / #1021 review item 2): a container attached to two
// isolated instance networks with forwarding enabled could route packets
// between them, undoing east-west isolation through the one component every
// instance network trusts enough to let in; a stray link-local IPv6 address
// is a narrower version of the same exposure.
//
// The IPv4 checks (ip_forward, conf/default/forwarding) always fail closed
// on a read error — a host running IPv4 with no readable /proc/sys/net/ipv4
// is not something to guess about. The IPv6 checks are skipped entirely, not
// failed, when /proc/sys/net/ipv6 itself does not exist: a kernel built
// without IPv6 support has no stack for these switches to guard, and failing
// closed on ITS absence would refuse self-attach on every such host
// regardless of how it is actually configured.
func CheckForwardingDisabled() error {
	ipv4Checks := []struct{ path, want string }{
		{ipv4ForwardPath, "0"},
		{ipv4DefaultForwardPath, "0"},
	}
	for _, c := range ipv4Checks {
		if err := checkSysctl(c.path, c.want); err != nil {
			return err
		}
	}

	if !pathExists(ipv6SysctlDir) {
		return nil
	}
	ipv6Checks := []struct{ path, want string }{
		{ipv6ForwardPath, "0"},
		{ipv6DefaultForwardPath, "0"},
		{ipv6DisableDefaultPath, "1"},
	}
	for _, c := range ipv6Checks {
		if err := checkSysctl(c.path, c.want); err != nil {
			return err
		}
	}
	return nil
}

// checkSysctl reads path and fails closed (via a *ForwardingEnabledError)
// unless it reads exactly want.
func checkSysctl(path, want string) error {
	raw, err := readSysctl(path)
	if err != nil {
		return &ForwardingEnabledError{Path: path, Want: want, Err: err}
	}
	value := strings.TrimSpace(raw)
	if value != want {
		return &ForwardingEnabledError{Path: path, Want: want, Value: value}
	}
	return nil
}
