package container

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrManualModeWrite is returned by every write operation (Create, Start,
// Stop, Remove, CreateNetwork, RemoveNetwork) on a ReadOnlyRuntime. Manual
// posture is discovery/health-check only (spec §7): the operator manages
// plugin containers via their own compose file, and Gleipnir must never
// mutate what it did not create.
var ErrManualModeWrite = errors.New("container: manual posture does not permit socket writes")

// ReadOnlyRuntime wraps another Runtime and rejects every write operation with
// ErrManualModeWrite before it reaches inner. Read operations (Inspect,
// Stats, Logs, ListByLabel, ListNetworksByLabel) delegate to inner unchanged.
//
// This is the manual posture's Runtime. The full manual-mode discovery pass
// (declaring compose-managed containers by label, health-checking them) is a
// separate issue, but the posture and its write-impossibility guarantee must
// exist now — wrapping is how "impossible", not "discouraged", is enforced:
// no method on ReadOnlyRuntime can reach inner's write path no matter what a
// caller passes in.
type ReadOnlyRuntime struct {
	inner Runtime
}

// NewReadOnlyRuntime wraps inner so every write operation fails closed.
// inner may be nil when manual mode has no working socket connection at all
// (full manual-mode discovery is out of scope here); in that case read
// operations return an error from the nil check rather than panicking.
func NewReadOnlyRuntime(inner Runtime) *ReadOnlyRuntime {
	return &ReadOnlyRuntime{inner: inner}
}

func (r *ReadOnlyRuntime) Create(context.Context, CreateOptions) (ContainerID, error) {
	return "", ErrManualModeWrite
}

// ImageLoad is a write: it puts bytes into the daemon's image store. Manual
// posture means the operator loads the image themselves (spec §7), so the
// installer's job here is to accept the bundle, skip the load, and record that
// it did — which it can only do if this fails closed rather than quietly
// succeeding.
func (r *ReadOnlyRuntime) ImageLoad(context.Context, io.Reader) error {
	return ErrManualModeWrite
}

func (r *ReadOnlyRuntime) Start(context.Context, ContainerID) error {
	return ErrManualModeWrite
}

func (r *ReadOnlyRuntime) Stop(context.Context, ContainerID, time.Duration) error {
	return ErrManualModeWrite
}

func (r *ReadOnlyRuntime) Remove(context.Context, ContainerID, bool) error {
	return ErrManualModeWrite
}

func (r *ReadOnlyRuntime) CreateNetwork(context.Context, NetworkOptions) (NetworkID, error) {
	return "", ErrManualModeWrite
}

func (r *ReadOnlyRuntime) RemoveNetwork(context.Context, NetworkID) error {
	return ErrManualModeWrite
}

func (r *ReadOnlyRuntime) Inspect(ctx context.Context, id ContainerID) (ContainerInfo, error) {
	if r.inner == nil {
		return ContainerInfo{}, errNoManualDiscovery
	}
	return r.inner.Inspect(ctx, id)
}

// ImageInspect is a read, so it delegates: manual mode still needs to see what
// the operator loaded, otherwise nothing can verify their image against the
// manifest's pin.
func (r *ReadOnlyRuntime) ImageInspect(ctx context.Context, ref string) (ImageInfo, error) {
	if r.inner == nil {
		return ImageInfo{}, errNoManualDiscovery
	}
	return r.inner.ImageInspect(ctx, ref)
}

func (r *ReadOnlyRuntime) Stats(ctx context.Context, id ContainerID) (ContainerStats, error) {
	if r.inner == nil {
		return ContainerStats{}, errNoManualDiscovery
	}
	return r.inner.Stats(ctx, id)
}

func (r *ReadOnlyRuntime) Logs(ctx context.Context, id ContainerID, opts LogOptions) (io.ReadCloser, error) {
	if r.inner == nil {
		return nil, errNoManualDiscovery
	}
	return r.inner.Logs(ctx, id, opts)
}

func (r *ReadOnlyRuntime) ListByLabel(ctx context.Context, key, value string) ([]ContainerInfo, error) {
	if r.inner == nil {
		return nil, errNoManualDiscovery
	}
	return r.inner.ListByLabel(ctx, key, value)
}

func (r *ReadOnlyRuntime) ListNetworksByLabel(ctx context.Context, key, value string) ([]NetworkInfo, error) {
	if r.inner == nil {
		return nil, errNoManualDiscovery
	}
	return r.inner.ListNetworksByLabel(ctx, key, value)
}

func (r *ReadOnlyRuntime) Close() error {
	if r.inner == nil {
		return nil
	}
	return r.inner.Close()
}

// errNoManualDiscovery is returned by read operations on a ReadOnlyRuntime
// constructed without an inner Runtime. The full manual-mode discovery pass
// (issue-level scope: connecting to a socket purely for read access) has not
// landed yet; this keeps that gap explicit instead of a nil-pointer panic.
var errNoManualDiscovery = errors.New("container: manual posture has no discovery connection configured")
