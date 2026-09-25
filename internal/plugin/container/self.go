package container

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
)

// containerenvPath is where Podman writes an identity marker into every
// container it starts. A package-level var (not a const) purely so a test can
// point ResolveSelfContainerID at a fixture file instead of the real /run.
var containerenvPath = "/run/.containerenv"

// procSelfMountinfoPath is where the running process's mount table is
// recorded. Docker bind-mounts each container's own
// /var/lib/docker/containers/<id>/{hostname,hosts,resolv.conf} into it, and
// this file names the source path. Also injectable for tests.
var procSelfMountinfoPath = "/proc/self/mountinfo"

// osHostname is the injectable seam over os.Hostname, so a test can assert
// the corroboration step (info.Hostname == osHostname()) without depending on
// the real test process's actual hostname.
var osHostname = os.Hostname

// containerenvIDPattern matches Podman's `id="<64hex>"` line in
// /run/.containerenv. Podman writes this file itself at container-creation
// time — it is not something the container's own running process can rewrite
// to name a different container, which is what makes it a usable identity
// source rather than a self-reported claim.
var containerenvIDPattern = regexp.MustCompile(`(?m)^id="([0-9a-f]{64})"\s*$`)

// mountinfoContainerIDPattern matches the host-side source path Docker's
// per-container bind mounts carry: /var/lib/docker/containers/<64hex>/
// {hostname,hosts,resolv.conf}. Like /run/.containerenv, this path is a fact
// the daemon asserted when it created the container, not something reachable
// by a process choosing its own hostname or environment.
var mountinfoContainerIDPattern = regexp.MustCompile(`containers/([0-9a-f]{64})/(?:hostname|hosts|resolv\.conf)\b`)

// selfContainerIDFromContainerenv extracts a container ID from the content of
// /run/.containerenv (Podman), or "" when no id= line is present — e.g.
// because the daemon is Docker, or the process is not containerized at all.
func selfContainerIDFromContainerenv(content string) string {
	m := containerenvIDPattern.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return m[1]
}

// selfContainerIDFromMountinfo extracts a container ID from the content of
// /proc/self/mountinfo (Docker), or "" when no matching bind-mount source is
// present.
func selfContainerIDFromMountinfo(content string) string {
	m := mountinfoContainerIDPattern.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return m[1]
}

// candidateSelfContainerID tries both unspoofable sources in turn. Neither
// depends on this process's own hostname, command line, or environment —
// both are facts the container runtime wrote at container-creation time,
// before this process's own contents ever ran.
func candidateSelfContainerID() string {
	if id := selfContainerIDFromContainerenv(readFileBestEffort(containerenvPath)); id != "" {
		return id
	}
	return selfContainerIDFromMountinfo(readFileBestEffort(procSelfMountinfoPath))
}

func readFileBestEffort(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// ResolveSelfContainerID determines the container ID of the process Gleipnir
// itself is running in, so the reconciler knows which container to attach to
// every plugin instance's network (spec §7: "Gleipnir's own container joins
// each instance network").
//
// The candidate ID comes ONLY from sources this process cannot spoof by
// choosing its own hostname, environment, or command line: Podman's
// /run/.containerenv id= line, or Docker's /proc/self/mountinfo entry for its
// own bind-mounted hostname/hosts/resolv.conf. This process's hostname is
// never used as a lookup key — an operator or attacker who could set
// Gleipnir's own hostname to name some OTHER container could otherwise steer
// self-attach onto it, which a hostname-as-key lookup could not tell apart
// from the real self.
//
// A candidate is corroborated, not trusted blind: rt.Inspect(candidate) must
// succeed, its ID must equal the candidate verbatim, and its Config.Hostname
// must equal this process's own os.Hostname(). The corroboration is a
// self-consistency check ("what I call myself" against "what the daemon says
// this candidate's hostname is") — it defends against a candidate ID that,
// through some environment or storage-driver oddity, resolves to an unrelated
// container, including one an attacker named to collide with Gleipnir's own
// hostname.
//
// Any failure — no candidate found, Inspect errors, or a corroboration
// mismatch — returns "" with a nil error: self-attach fails closed rather
// than guessing. The reconciler treats an empty result as "not containerized"
// (Config.SelfContainerID's "empty = skip" contract) and never attempts a
// self-attach for the pass.
func ResolveSelfContainerID(ctx context.Context, rt Runtime) (ContainerID, error) {
	candidate := candidateSelfContainerID()
	if candidate == "" || rt == nil {
		return "", nil
	}

	info, err := rt.Inspect(ctx, ContainerID(candidate))
	if err != nil {
		return "", nil
	}
	if string(info.ID) != candidate {
		return "", nil
	}

	hostname, err := osHostname()
	if err != nil || hostname == "" {
		return "", nil
	}
	if info.Hostname != hostname {
		return "", nil
	}
	return ContainerID(candidate), nil
}

// SelfAttacherConfig constructs a SelfAttacher. Every field is a fixed fact
// about Gleipnir's own identity and the reconciler's labeling/pooling scheme
// — none of it is taken per-call, which is what makes the resulting
// SelfAttacher's constraints structural rather than caller discipline.
type SelfAttacherConfig struct {
	Runtime     Runtime
	ContainerID ContainerID

	// ManagedLabelKey/Value name the label the reconciler stamps on every
	// network it creates (reconciler.LabelManaged/ManagedValue). Passed in
	// rather than hardcoded so this package stays label-agnostic.
	ManagedLabelKey   string
	ManagedLabelValue string

	// InstanceLabelKey names the label carrying the owning instance's ID
	// (reconciler.LabelInstance).
	InstanceLabelKey string

	// Pool is GLEIPNIR_PLUGIN_SUBNET_POOL. The zero value skips the
	// pool-containment check in validate — this package cannot invent a pool
	// it was never told, and a reconciler that has not resolved one yet must
	// not have that absence read as "anything goes" versus "not configured".
	Pool netip.Prefix
}

// SelfAttacher is Gleipnir's self-constrained handle for joining and leaving
// per-instance networks (spec §7: "Gleipnir's own container joins each
// instance network"). It wraps a Runtime and is constructed once with the
// container ID Gleipnir resolved for itself, the managed-network label the
// reconciler stamps on every network it creates, the instance label key, and
// (when known) the configured subnet pool.
//
// The container ID is fixed at construction and never taken as a call
// argument — nothing this type exposes can be asked to attach or detach any
// container other than the one it was built for. Attach/Detach additionally
// refuse a network that: does not carry the managed label, is not internal,
// is not labelled for the specific instance the caller names, or (when the
// caller and the configured pool both know it) has a subnet outside the pool
// or different from the one recorded for that instance. Every check runs
// against the caller-supplied NetworkInfo before either method ever reaches
// the socket, the same structural-refusal shape as
// ValidateCreate/ValidateCreateNetwork. Together these are what keep a
// Gleipnir compromise (or an ordinary bug) from being able to attach Gleipnir
// to a network it did not create, or to the wrong instance's network.
type SelfAttacher struct {
	runtime           Runtime
	containerID       ContainerID
	managedLabelKey   string
	managedLabelValue string
	instanceLabelKey  string
	pool              netip.Prefix
}

// NewSelfAttacher constructs a SelfAttacher from cfg. Runtime and ContainerID
// are required; the label keys default to empty (matching nothing, so an
// unconfigured attacher refuses every network) rather than panicking.
func NewSelfAttacher(cfg SelfAttacherConfig) *SelfAttacher {
	return &SelfAttacher{
		runtime:           cfg.Runtime,
		containerID:       cfg.ContainerID,
		managedLabelKey:   cfg.ManagedLabelKey,
		managedLabelValue: cfg.ManagedLabelValue,
		instanceLabelKey:  cfg.InstanceLabelKey,
		pool:              cfg.Pool,
	}
}

// ContainerID returns the container ID this attacher is scoped to.
func (s *SelfAttacher) ContainerID() ContainerID { return s.containerID }

// AttachTarget names exactly what an Attach/Detach call is being asked to do,
// so validate checks every precondition against one named value instead of a
// loose parameter list.
type AttachTarget struct {
	Network NetworkInfo

	// InstanceID is the instance the caller believes it is acting on. The
	// network's own instance label must match it exactly.
	InstanceID string

	// ExpectedSubnet is the subnet the reconciler's own database has recorded
	// as allocated to InstanceID, when it knows one. "" skips this specific
	// check rather than refusing on missing context — a fresh instance whose
	// subnet the caller has not looked up yet is not the same fact as a
	// mismatch.
	ExpectedSubnet string
}

// Attach joins Gleipnir's own container to target.Network, pinning
// pinnedIPv4 as its address on that network (see Runtime.ConnectNetwork for
// why pinning rather than an unpinned attach).
func (s *SelfAttacher) Attach(ctx context.Context, target AttachTarget, pinnedIPv4 net.IP) error {
	if err := s.validate(target); err != nil {
		return err
	}
	return s.runtime.ConnectNetwork(ctx, target.Network.ID, s.containerID, pinnedIPv4)
}

// Detach leaves target.Network. Same refusal as Attach.
func (s *SelfAttacher) Detach(ctx context.Context, target AttachTarget) error {
	if err := s.validate(target); err != nil {
		return err
	}
	return s.runtime.DisconnectNetwork(ctx, target.Network.ID, s.containerID)
}

func (s *SelfAttacher) validate(target AttachTarget) error {
	network := target.Network

	if network.Labels[s.managedLabelKey] != s.managedLabelValue {
		return &ConstraintViolationError{
			Kind: ViolationUnmanagedNetwork,
			Detail: fmt.Sprintf(
				"self-attach refuses a network without %s=%s: %q",
				s.managedLabelKey, s.managedLabelValue, network.Name,
			),
		}
	}
	if !network.Internal {
		return &ConstraintViolationError{
			Kind:   ViolationExternalNetwork,
			Detail: fmt.Sprintf("self-attach refuses a non-internal network: %q", network.Name),
		}
	}
	if got := network.Labels[s.instanceLabelKey]; got != target.InstanceID {
		return &ConstraintViolationError{
			Kind: ViolationInstanceMismatch,
			Detail: fmt.Sprintf(
				"network %q is labelled for instance %q, not the requested instance %q",
				network.Name, got, target.InstanceID,
			),
		}
	}

	if s.pool.IsValid() || target.ExpectedSubnet != "" {
		subnet, err := netip.ParsePrefix(network.Subnet)
		if err != nil {
			return &ConstraintViolationError{
				Kind:   ViolationSubnetMismatch,
				Detail: fmt.Sprintf("network %q has no readable subnet: %v", network.Name, err),
			}
		}
		if s.pool.IsValid() && !poolContains(s.pool, subnet) {
			return &ConstraintViolationError{
				Kind: ViolationSubnetMismatch,
				Detail: fmt.Sprintf(
					"network %q subnet %s falls outside the configured pool %s",
					network.Name, subnet, s.pool,
				),
			}
		}
		if target.ExpectedSubnet != "" && network.Subnet != target.ExpectedSubnet {
			return &ConstraintViolationError{
				Kind: ViolationSubnetMismatch,
				Detail: fmt.Sprintf(
					"network %q subnet %s does not match instance %q's allocated subnet %s",
					network.Name, network.Subnet, target.InstanceID, target.ExpectedSubnet,
				),
			}
		}
	}
	return nil
}

// poolContains reports whether subnet lies entirely inside pool.
func poolContains(pool, subnet netip.Prefix) bool {
	return subnet.Bits() >= pool.Bits() && pool.Contains(subnet.Addr())
}

// SelfAttached reports whether self (Gleipnir's own container, as most
// recently Inspected) is already a member of net. The reconciler plans an
// attach only when this reports false and a detach only when it reports
// true — the whole membership state it needs, re-derived every pass from a
// fresh Inspect rather than tracked between them.
func SelfAttached(self ContainerInfo, net NetworkInfo) bool {
	for _, id := range self.Networks {
		if id == net.ID {
			return true
		}
	}
	return false
}
