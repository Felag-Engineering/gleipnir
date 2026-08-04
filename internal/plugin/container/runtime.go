// Package container is the foundation of the container substrate (spec §7,
// ADR-056): a typed wrapper over the official Go Docker SDK that talks to a
// container-runtime socket. Podman's Docker-compatible socket is a first-class
// target — nothing in this package assumes Docker specifically.
//
// This package covers only the operations the reconciler needs: create,
// start, stop, remove, inspect, stats, logs, and per-instance network
// management, plus a level-triggered list-by-label primitive. It does not
// implement the reconciler itself, subnet allocation, egress grants,
// generation rotation, or image GC — those build on top of Runtime in later
// issues.
package container

import (
	"context"
	"io"
	"time"
)

// ContainerID is the runtime-assigned identifier returned by Create.
type ContainerID string

// NetworkID is the runtime-assigned identifier returned by CreateNetwork.
type NetworkID string

// ContainerState mirrors the runtime's container lifecycle states. Values
// match the Docker/Podman API vocabulary verbatim so callers can reason about
// them without a translation table.
type ContainerState string

const (
	ContainerStateCreated    ContainerState = "created"
	ContainerStateRunning    ContainerState = "running"
	ContainerStatePaused     ContainerState = "paused"
	ContainerStateRestarting ContainerState = "restarting"
	ContainerStateRemoving   ContainerState = "removing"
	ContainerStateExited     ContainerState = "exited"
	ContainerStateDead       ContainerState = "dead"
)

// MountType identifies the kind of an additional (non-instance-volume) mount
// expressed in CreateOptions.Mounts. Self-constraint rejects every one of
// these unconditionally — see ValidateCreate.
type MountType string

const (
	MountTypeBind   MountType = "bind"
	MountTypeVolume MountType = "volume"
	MountTypeTmpfs  MountType = "tmpfs"
)

// hostNetworkMode is the runtime's magic string for "share the host's network
// namespace". Self-constraint rejects it outright (spec §7: "internal
// networks only").
const hostNetworkMode = "host"

// VolumeMount is the single per-instance volume a container may mount. It is
// the only mount contract self-constraint permits (spec §7: "no mounts beyond
// a per-instance volume").
type VolumeMount struct {
	// Name is the runtime-level volume name (e.g. a per-instance named
	// volume the reconciler manages independently of this package).
	Name string
	// MountPath is where the volume is mounted inside the container.
	MountPath string
	ReadOnly  bool
}

// Mount describes an additional bind/volume/tmpfs mount requested beyond
// CreateOptions.Volume. Its only purpose in this package is to give the
// self-constraint tests something to reject — see CreateOptions.Mounts.
type Mount struct {
	Type     MountType
	Source   string
	Target   string
	ReadOnly bool
}

// Resources caps a container's cgroup resource usage (spec §7: "Resource
// limits become enforced cgroup caps"). A zero value in either field means no
// limit is applied for that resource.
type Resources struct {
	// MemoryBytes is the hard memory limit in bytes.
	MemoryBytes int64
	// NanoCPUs is the CPU quota in the Docker API's nano-CPU units (1e9 == 1
	// full core).
	NanoCPUs int64
}

// CreateOptions describes a container to create. This is Gleipnir's own typed
// shape, not the Docker SDK's wire types — ValidateCreate runs against this
// struct before any translation to the SDK's container.Config/HostConfig
// happens, so a caller-side mistake never reaches the socket.
type CreateOptions struct {
	// Name is the container name. The reconciler is expected to derive this
	// from the plugin instance ID (and generation, across a rotation) so
	// ListByLabel plus this name together give it a stable identity across
	// restarts.
	Name string

	// Image is the image reference to run. Spec §7 expects a digest-pinned
	// reference loaded from the signed bundle's OCI archive.
	Image string

	// Labels are attached to the container for ListByLabel discovery. The
	// reconciler is expected to set at least an instance-ID label.
	Labels map[string]string

	// Env is passed through verbatim as KEY=VALUE entries.
	Env []string

	// Command overrides the image's default command when non-empty.
	Command []string

	// Volume is the single per-instance volume mounted into the container.
	// This is the only mount self-constraint permits — see Mounts below.
	Volume VolumeMount

	// Mounts holds any additional bind/volume/tmpfs mounts beyond Volume.
	// Self-constraint (spec §7: "no mounts beyond a per-instance volume")
	// rejects any non-empty value here with a *ConstraintViolationError
	// before the socket is ever touched. The field exists so a hostile
	// create request can be expressed and tested — production callers must
	// never populate it.
	Mounts []Mount

	// Network is the internal-only per-instance network this container
	// attaches to (spec §7: "one dedicated internal network per plugin
	// instance"). Self-constraint requires a non-empty value that is not the
	// runtime's host-network mode.
	Network string

	// Privileged and CapAdd exist only so a hostile request can be expressed
	// and rejected by ValidateCreate. Production callers must never set them
	// — self-constraint requires Privileged == false and CapAdd == nil.
	Privileged bool
	CapAdd     []string

	Resources Resources
}

// NetworkOptions describes an internal-only per-instance network to create.
type NetworkOptions struct {
	Name   string
	Labels map[string]string
	// Subnet is an explicit CIDR (e.g. "10.42.3.0/24"). Subnet allocation
	// itself is the reconciler's job (out of scope here); an empty value
	// lets the runtime choose.
	Subnet string
	// Internal must be true — self-constraint rejects a request to create a
	// network with external connectivity (spec §7: "internal networks
	// only"). The field exists (rather than being implicit) so a hostile
	// request can be expressed and tested.
	Internal bool
}

// ContainerInfo is a point-in-time snapshot of a container's identity and
// lifecycle state, returned by Inspect and ListByLabel.
type ContainerInfo struct {
	ID     ContainerID
	Name   string
	Image  string
	Labels map[string]string
	State  ContainerState
	// Health is the runtime healthcheck status ("", "starting", "healthy",
	// "unhealthy"). Spec §7's per-capability health layers on top of this
	// container-level liveness signal; it does not replace it.
	Health    string
	CreatedAt time.Time
}

// NetworkInfo is a point-in-time snapshot of a network, returned by
// ListNetworksByLabel.
type NetworkInfo struct {
	ID       NetworkID
	Name     string
	Labels   map[string]string
	Internal bool
}

// ContainerStats is a single-sample resource usage reading (spec §7: "the
// RSS sampler's role is served by container stats"). It intentionally does
// not stream — the reconciler polls this on its own reconcile cadence rather
// than holding a long-lived stats connection open per container.
type ContainerStats struct {
	CPUPercent       float64
	MemoryUsageBytes uint64
	MemoryLimitBytes uint64
}

// LogOptions controls Logs. Tail == "" means the runtime's default (all
// available output); Since == "" means no lower time bound.
type LogOptions struct {
	Tail       string
	Since      time.Time
	Timestamps bool
	Follow     bool
}

// Runtime is the typed interface over a container-runtime socket (Docker or
// Podman's Docker-compatible socket) that the reconciler needs: level-
// triggered list-by-label, cheap inspect, and the create/start/stop/remove/
// network operations to converge one step at a time.
//
// Self-constraint (spec §7: "Gleipnir self-constrains its create calls") is
// enforced inside every implementation's Create/CreateNetwork — a hostile
// CreateOptions/NetworkOptions value fails with a *ConstraintViolationError
// before it reaches the socket. This is a security boundary, not caller
// discipline: both DockerRuntime and Fake call the same ValidateCreate /
// ValidateCreateNetwork functions.
type Runtime interface {
	Create(ctx context.Context, opts CreateOptions) (ContainerID, error)
	Start(ctx context.Context, id ContainerID) error
	Stop(ctx context.Context, id ContainerID, timeout time.Duration) error
	Remove(ctx context.Context, id ContainerID, force bool) error
	Inspect(ctx context.Context, id ContainerID) (ContainerInfo, error)
	Stats(ctx context.Context, id ContainerID) (ContainerStats, error)
	Logs(ctx context.Context, id ContainerID, opts LogOptions) (io.ReadCloser, error)

	// ListByLabel returns every container carrying the label key=value. The
	// reconciler's level-triggered convergence loop is built on this plus
	// Inspect — it never tracks state beyond what it can re-list.
	ListByLabel(ctx context.Context, key, value string) ([]ContainerInfo, error)

	CreateNetwork(ctx context.Context, opts NetworkOptions) (NetworkID, error)
	RemoveNetwork(ctx context.Context, id NetworkID) error
	ListNetworksByLabel(ctx context.Context, key, value string) ([]NetworkInfo, error)

	// Close releases the underlying socket connection. Safe to call once at
	// shutdown; implementations should make repeat calls harmless.
	Close() error
}
