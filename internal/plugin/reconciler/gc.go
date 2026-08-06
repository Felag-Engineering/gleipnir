package reconciler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// GC reclaims what nothing needs any more: loaded images no live generation
// runs, per-instance networks and subnets left behind by a removed instance,
// and the hashes of revoked generation tokens once they have outlived their
// usefulness for audit correlation (spec §7, ADR-056).
//
// It is level-triggered like every other pass here. There is no delete queue
// and no "pending cleanup" state: each pass asks what is unreferenced right now
// and reclaims a bounded slice of it, so a crashed pass costs one interval and
// a missed reclaim is simply found again next time. Cleanup is convergence
// toward "nothing unreferenced exists", not a sequence to drive to completion.
//
// It is a SEPARATE pass from ReconcileOnce rather than a branch inside it, and
// meant for a slower cadence. The convergence loop is latency-sensitive — an
// operator who starts an instance wants a container within one interval —
// while nothing gets worse for a few minutes because a dead image is still on
// disk. Running the two at the same rate would spend the reclaim work's cost
// on every convergence pass for no benefit.

// defaultTokenRetention is how long a revoked generation token's hash is kept.
//
// The window exists because a revoked token is briefly still evidence: a host
// RPC that arrived moments before the revocation is correlated to its
// generation by that hash, and purging on the instant of revocation would make
// the end of a generation's life unattributable — exactly the window a
// rotation's problems show up in. A day is long enough to investigate a
// rotation that went wrong and short enough that material does not accumulate
// indefinitely.
const defaultTokenRetention = 24 * time.Hour

// defaultImagesPerPass bounds how many images one pass reclaims.
//
// Image removal is the slowest thing this package asks a daemon to do — it
// unlinks layers and can block for seconds on a large image. An unbounded pass
// after an uninstall of several plugins would hold the socket long enough to
// delay the convergence work an operator is actually waiting on. Five per pass
// drains any realistic backlog over a few minutes.
const defaultImagesPerPass = 5

// defaultTokensPerPass bounds token purges per pass. Higher than the image
// bound because a purge is a single indexed UPDATE, not a socket call.
const defaultTokensPerPass = 100

// purgedTokenPrefix marks a token hash that has been purged. The column is NOT
// NULL UNIQUE, so the hash is replaced by a per-row tombstone rather than
// cleared — and the tombstone has to be unique per row, which is why the row's
// own id is appended.
//
// This literal is duplicated in the 'purged:%' guard on
// ListPurgeableGenerationTokens and PurgeContainerGenerationToken. Keep the
// three in step: a mismatch would make the sweep re-purge every row forever.
const purgedTokenPrefix = "purged:"

// GCStore is the narrow slice of persistence GC needs. *db.Queries satisfies
// it.
type GCStore interface {
	ListPluginContainers(ctx context.Context) ([]db.PluginContainer, error)

	ListUnreferencedContainerImages(ctx context.Context) ([]db.PluginContainerImage, error)
	CountContainerImageReferences(ctx context.Context, imageDigest string) (int64, error)
	DeleteContainerImage(ctx context.Context, digest string) error

	ListContainerSubnets(ctx context.Context) ([]db.PluginContainerSubnet, error)

	ListPurgeableGenerationTokens(ctx context.Context, arg db.ListPurgeableGenerationTokensParams) ([]db.PluginContainerGeneration, error)
	PurgeContainerGenerationToken(ctx context.Context, arg db.PurgeContainerGenerationTokenParams) (int64, error)
}

// ImageCandidate is one loaded image GC believes nothing needs.
type ImageCandidate struct {
	Digest    string `json:"digest"`
	Reference string `json:"reference"`
	SizeBytes int64  `json:"size_bytes"`
}

// GCResult summarizes one cleanup pass.
type GCResult struct {
	// Applied is false in manual posture: everything below was identified and
	// nothing was acted on. Reporting without reclaiming is the whole of
	// manual-mode GC — the operator loaded those images and owns those
	// networks, so naming what is unreferenced is all Gleipnir may do.
	Applied bool `json:"applied"`

	// ImagesReclaimable is what the pass found unreferenced, whether or not it
	// removed them. In manual posture this is the entire output.
	ImagesReclaimable []ImageCandidate `json:"images_reclaimable"`

	// ImagesReclaimed and BytesReclaimed count what was actually removed.
	ImagesReclaimed int   `json:"images_reclaimed"`
	BytesReclaimed  int64 `json:"bytes_reclaimed"`

	// ImagesDeferred is how many reclaimable images this pass left for the
	// next one because of the per-pass bound. Reported rather than silent: a
	// bounded pass that says nothing about what it skipped reads as "there was
	// nothing else to do".
	ImagesDeferred int `json:"images_deferred"`

	// ImagesRetainedByRecheck counts images the pre-removal reference re-check
	// saved after the list said they were free. A non-zero value is the race
	// this check exists for actually happening, which is worth being able to
	// see rather than infer.
	ImagesRetainedByRecheck int `json:"images_retained_by_recheck"`

	// SubnetsReleasable names the instances holding an allocation nothing
	// claims; SubnetsReleased counts those actually returned to the pool.
	SubnetsReleasable []string `json:"subnets_releasable"`
	SubnetsReleased   int      `json:"subnets_released"`

	// TokensPurged counts revoked generation-token hashes replaced by a
	// tombstone this pass.
	TokensPurged int `json:"tokens_purged"`

	// Errors counts individual reclaims that failed. Like the convergence
	// loop, a failed reclaim is not fatal: the next pass finds the same
	// unreferenced resource and tries again.
	Errors int `json:"errors"`
}

// ReconcileGC runs one cleanup pass.
//
// It returns an error only when the pass could not be attempted. An individual
// reclaim that fails is counted in GCResult.Errors and logged — a daemon that
// refuses to remove an image is telling GC that Gleipnir's records disagreed
// with it, and the right response is to leave the image alone and try again
// next pass, not to escalate.
func (r *Reconciler) ReconcileGC(ctx context.Context) (GCResult, error) {
	if r.gc == nil {
		return GCResult{}, errors.New("reconciler: no GC store configured")
	}

	result := GCResult{Applied: r.posture != container.PostureManual}

	if err := r.gcImages(ctx, &result); err != nil {
		return GCResult{}, err
	}
	if err := r.gcSubnets(ctx, &result); err != nil {
		return GCResult{}, err
	}
	if err := r.gcTokens(ctx, &result); err != nil {
		return GCResult{}, err
	}
	return result, nil
}

// gcImages reclaims loaded images no live generation runs.
//
// Three layers stand between a live plugin and a removed image, and each is
// there because the one after it is not sufficient alone. The list query
// excludes every digest a non-terminal generation pins, including a draining
// one mid-rotation — the old generation keeps serving until the new one passes
// its gate, so its image is in use in exactly the window it looks stalest. The
// re-check immediately before the socket call closes the gap between the list
// and the removal, which a rotation starting mid-pass would otherwise open. And
// the daemon's own "in use by a container" refusal catches the case where
// Gleipnir's records are simply wrong, which is why the removal is never
// forced.
func (r *Reconciler) gcImages(ctx context.Context, result *GCResult) error {
	unreferenced, err := r.gc.ListUnreferencedContainerImages(ctx)
	if err != nil {
		return fmt.Errorf("listing unreferenced images: %w", err)
	}

	for _, img := range unreferenced {
		result.ImagesReclaimable = append(result.ImagesReclaimable, ImageCandidate{
			Digest:    img.Digest,
			Reference: img.Reference,
			SizeBytes: derefInt64(img.SizeBytes),
		})
	}
	if !result.Applied || len(unreferenced) == 0 {
		return nil
	}

	budget := r.imagesPerPass
	if len(unreferenced) > budget {
		result.ImagesDeferred = len(unreferenced) - budget
		unreferenced = unreferenced[:budget]
	}

	for _, img := range unreferenced {
		refs, err := r.gc.CountContainerImageReferences(ctx, img.Digest)
		if err != nil {
			result.Errors++
			logctx.Logger(ctx).ErrorContext(ctx, "gc: counting image references failed",
				"digest", img.Digest, "err", err)
			continue
		}
		if refs > 0 {
			// A generation claimed this digest between the list and now.
			result.ImagesRetainedByRecheck++
			continue
		}

		if err := r.runtime.ImageRemove(ctx, img.Digest); err != nil && !errors.Is(err, container.ErrImageNotFound) {
			result.Errors++
			logctx.Logger(ctx).WarnContext(ctx, "gc: image removal refused",
				"digest", img.Digest, "reference", img.Reference, "err", err)
			continue
		}

		// The accounting row goes only after the image is gone (or was already
		// gone). The reverse order would drop the only record that the digest
		// was ever loaded while the bytes were still on disk, leaving an image
		// nothing would ever reclaim again.
		if err := r.gc.DeleteContainerImage(ctx, img.Digest); err != nil {
			result.Errors++
			logctx.Logger(ctx).ErrorContext(ctx, "gc: deleting image record failed",
				"digest", img.Digest, "err", err)
			continue
		}

		result.ImagesReclaimed++
		result.BytesReclaimed += derefInt64(img.SizeBytes)
		logctx.Logger(ctx).InfoContext(ctx, "gc: reclaimed image",
			"digest", img.Digest, "reference", img.Reference, "size_bytes", derefInt64(img.SizeBytes))
	}
	return nil
}

// gcSubnets returns to the pool every allocation nothing claims.
//
// This is a backstop, not the primary path: the convergence loop releases a
// subnet as the second half of tearing an instance's network down. What lands
// here is what that never got to do — a desired row deleted while the network
// removal was failing, or a host that stopped between the two.
//
// The condition is deliberately conjunctive. An allocation is releasable only
// when no desired row claims the instance AND no managed network for it still
// exists, because a subnet handed to another instance while the old network is
// still up produces an overlap the runtime refuses at create time — turning a
// clean teardown into a stuck one, which is the exact failure the second half
// of the ordering in removeNetwork exists to avoid.
func (r *Reconciler) gcSubnets(ctx context.Context, result *GCResult) error {
	if r.subnets == nil {
		return nil
	}

	allocations, err := r.gc.ListContainerSubnets(ctx)
	if err != nil {
		return fmt.Errorf("listing subnet allocations: %w", err)
	}
	if len(allocations) == 0 {
		return nil
	}

	desired, err := r.gc.ListPluginContainers(ctx)
	if err != nil {
		return fmt.Errorf("listing desired containers: %w", err)
	}
	networks, err := r.runtime.ListNetworksByLabel(ctx, LabelManaged, ManagedValue)
	if err != nil {
		return fmt.Errorf("listing managed networks: %w", err)
	}

	for _, instanceID := range orphanedSubnets(allocations, desired, networks) {
		result.SubnetsReleasable = append(result.SubnetsReleasable, instanceID)
		if !result.Applied {
			continue
		}
		if err := r.subnets.Release(ctx, instanceID); err != nil {
			result.Errors++
			logctx.Logger(ctx).ErrorContext(ctx, "gc: releasing subnet failed",
				"instance_id", instanceID, "err", err)
			continue
		}
		result.SubnetsReleased++
		logctx.Logger(ctx).InfoContext(ctx, "gc: released orphaned subnet", "instance_id", instanceID)
	}
	return nil
}

// orphanedSubnets returns the instance IDs holding an allocation that neither a
// desired row nor a surviving network claims, sorted.
//
// Pure over its inputs, like planFor and Discover: the whole condition is
// testable without a socket, and the sort makes two passes over an unchanged
// world produce identical output.
func orphanedSubnets(
	allocations []db.PluginContainerSubnet,
	desired []db.PluginContainer,
	networks []container.NetworkInfo,
) []string {
	claimed := make(map[string]bool, len(desired)+len(networks))
	for _, row := range desired {
		claimed[row.PluginInstanceID] = true
	}
	for _, n := range networks {
		if id := n.Labels[LabelInstance]; id != "" {
			claimed[id] = true
		}
	}

	var orphans []string
	for _, alloc := range allocations {
		if !claimed[alloc.PluginInstanceID] {
			orphans = append(orphans, alloc.PluginInstanceID)
		}
	}
	sort.Strings(orphans)
	return orphans
}

// gcTokens replaces the hash of every long-revoked generation token with a
// tombstone.
//
// This runs in manual posture too, unlike the other two. Manual mode constrains
// what Gleipnir does to the OPERATOR's resources — their containers, their
// images, their networks. A token hash in Gleipnir's own database is neither:
// it is credential material Gleipnir minted, and declining to clean it up in
// the posture chosen for caution would have it accumulate exactly where an
// operator was being careful.
func (r *Reconciler) gcTokens(ctx context.Context, result *GCResult) error {
	cutoff := r.gcNow().Add(-r.tokenRetention).UTC().Format(time.RFC3339Nano)
	rows, err := r.gc.ListPurgeableGenerationTokens(ctx, db.ListPurgeableGenerationTokensParams{
		Cutoff: &cutoff,
		Limit:  int64(defaultTokensPerPass),
	})
	if err != nil {
		return fmt.Errorf("listing purgeable generation tokens: %w", err)
	}

	now := r.gcNow().UTC().Format(time.RFC3339Nano)
	for _, row := range rows {
		affected, err := r.gc.PurgeContainerGenerationToken(ctx, db.PurgeContainerGenerationTokenParams{
			ID:        row.ID,
			TokenHash: purgedTokenPrefix + row.ID,
			UpdatedAt: now,
		})
		if err != nil {
			result.Errors++
			logctx.Logger(ctx).ErrorContext(ctx, "gc: purging generation token failed",
				"generation_id", row.ID, "instance_id", row.PluginInstanceID, "err", err)
			continue
		}
		// Zero rows means a concurrent sweep purged it first, which is an
		// ordinary outcome and not worth counting as work this pass did.
		if affected > 0 {
			result.TokensPurged++
		}
	}
	return nil
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
