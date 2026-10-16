package reconciler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// ErrImageUnavailable means the image a desired row pins is not present in the
// container runtime under a name this host can create from. It is a refusal,
// never a prompt to fetch: the image arrives only through the signed bundle,
// so a missing one is repaired by reinstalling, not by pulling.
var ErrImageUnavailable = errors.New("reconciler: pinned image is not present in the container runtime")

// ImageRecords reads the image accounting rows the installer wrote. *db.Queries
// satisfies it.
type ImageRecords interface {
	GetContainerImage(ctx context.Context, digest string) (db.PluginContainerImage, error)
}

// resolveImage returns the runtime-local name to create row's container from.
//
// The pin (row.ImageDigest) is the image's config digest, but a runtime does
// not necessarily answer to that name: `repo@sha256:<config digest>` is read as
// a registry MANIFEST digest, and under the containerd image store even the
// bare config digest does not resolve, because that store names a loaded image
// by its manifest digest. So the installer records the ID the runtime itself
// reported for the image it verified against the signed archive, and the
// container is created from that ID.
//
// Whatever the source, the image is inspected first and its absence is an
// error: a create naming a missing image would otherwise be free to pull
// something into a runtime that was never meant to fetch.
//
// A row with no recorded ID (written before the column existed) falls back to
// the bare config digest, which is the image ID on the classic Docker store and
// on Podman. That is accepted only when the runtime reports that exact ID back;
// on a containerd-store daemon it fails closed and the operator reinstalls the
// bundle, which records the ID.
func (r *Reconciler) resolveImage(ctx context.Context, row db.PluginContainer) (string, error) {
	digest := row.ImageDigest
	if digest == "" {
		return "", fmt.Errorf("%w: desired row for instance %q records no image digest", ErrImageUnavailable, row.PluginInstanceID)
	}

	recordedID, err := r.recordedLocalImageID(ctx, digest)
	if err != nil {
		return "", err
	}

	if recordedID != "" {
		info, err := r.runtime.ImageInspect(ctx, recordedID)
		if errors.Is(err, container.ErrImageNotFound) {
			return "", fmt.Errorf("%w: image %s (runtime ID %s) is gone, likely pruned; reinstall the plugin bundle to load it again",
				ErrImageUnavailable, digest, recordedID)
		}
		if err != nil {
			return "", fmt.Errorf("inspect image %s: %w", recordedID, err)
		}
		return info.ID, nil
	}

	info, err := r.runtime.ImageInspect(ctx, digest)
	if errors.Is(err, container.ErrImageNotFound) || (err == nil && info.ID != digest) {
		return "", fmt.Errorf("%w: image %s has no recorded runtime ID and does not resolve by digest on this runtime; reinstall the plugin bundle so the image is loaded and recorded",
			ErrImageUnavailable, digest)
	}
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", digest, err)
	}
	return info.ID, nil
}

// recordedLocalImageID returns the runtime ID the installer recorded for
// digest, or "" when there is no record or it predates the column.
func (r *Reconciler) recordedLocalImageID(ctx context.Context, digest string) (string, error) {
	if r.images == nil {
		return "", nil
	}
	rec, err := r.images.GetContainerImage(ctx, digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up image record %s: %w", digest, err)
	}
	if rec.LocalImageID == nil {
		return "", nil
	}
	return *rec.LocalImageID, nil
}
