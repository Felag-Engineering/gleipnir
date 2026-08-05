package container

import (
	"context"
	"errors"
	"io"
)

// ErrImageNotFound reports that the runtime has no image matching a reference.
// It is a sentinel rather than a typed error because the only question a
// caller ever asks is "is it there" — and an installer that just loaded an
// archive treats "not there" as a hard install failure, not as something to
// branch further on.
var ErrImageNotFound = errors.New("container: image not found")

// ImageInfo is what the runtime knows about one locally-present image.
//
// It carries every identity the daemon reports rather than a single "digest"
// field, because which one is populated depends on how the image arrived. An
// image loaded from an offline archive — the only way a managed plugin's image
// arrives (spec §7, ADR-056: fully offline-capable, no registry pull) — has an
// ID but usually no RepoDigests, since RepoDigests are assigned by a registry
// interaction that never happened. A caller verifying a manifest's pin must be
// able to check against whatever the daemon actually reports.
type ImageInfo struct {
	// ID is the content-addressable image ID ("sha256:..."), the config
	// digest. Always present, and the identity an offline load preserves.
	ID string

	// RepoTags are the repo:tag names the image is known by locally.
	RepoTags []string

	// RepoDigests are registry-assigned "repo@sha256:..." references. Usually
	// empty for an image that was loaded rather than pulled.
	RepoDigests []string

	// SizeBytes is the total on-disk size including layers. Zero when the
	// runtime does not report it.
	SizeBytes int64
}

// ImageRuntime is the image half of the container-runtime socket: loading an
// OCI archive and asking what is present afterwards.
//
// It is a separate interface from Runtime, and deliberately so. The installer
// needs exactly these two operations and must not be handed create/start/stop
// — an install has no business being able to run a container, and a narrow
// interface is what makes that a compile-time fact rather than a review note.
// Runtime embeds it, so one DockerRuntime satisfies both.
type ImageRuntime interface {
	// ImageLoad streams an OCI/Docker image archive into the runtime's local
	// image store. It returns after the daemon has finished the load, not when
	// the request is accepted: the daemon reports progress as a stream that
	// must be drained to completion, and returning early would let a caller
	// inspect for an image that is still being written.
	//
	// The progress stream is drained and discarded rather than parsed. What
	// was loaded is established afterwards by inspecting the digest the caller
	// already expects — a question with a yes/no answer — instead of by
	// scraping human-readable lines out of a stream whose format is not part
	// of any API contract.
	ImageLoad(ctx context.Context, archive io.Reader) error

	// ImageInspect reports what the runtime knows about ref, which may be a
	// digest, an image ID, or a repo:tag. It returns ErrImageNotFound when the
	// runtime has no such image.
	ImageInspect(ctx context.Context, ref string) (ImageInfo, error)
}
