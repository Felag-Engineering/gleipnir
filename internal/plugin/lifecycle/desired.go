package lifecycle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle/desiredstate"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
	manifestv2 "github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// Desired-state values for plugin_containers.desired_state (schema CHECK).
// Aliased from desiredstate rather than redeclared, so internal/admin (which
// imports desiredstate directly to avoid pulling internal/plugin/loader and
// internal/plugin/container into its build graph) and this package always
// agree on the exact same values.
const (
	DesiredStateRunning = desiredstate.Running
	DesiredStateStopped = desiredstate.Stopped
)

// ErrCASConflict is returned by SetDesired when the row's version has moved
// since it was read (ADR-038): another writer won, and the caller must not
// assume its own write applied.
var ErrCASConflict = errors.New("lifecycle: CAS conflict")

// ErrPluginNotActive is desiredstate.ErrPluginNotActive, aliased for the same
// reason as the DesiredState* constants above: the same variable, not a
// look-alike copy, so errors.Is behaves identically regardless of which
// package a caller checked it against.
var ErrPluginNotActive = desiredstate.ErrPluginNotActive

// DesiredState writes and updates the container substrate's desired-state
// rows (plugin_containers). It has no opinion about whether a container
// actually exists — that is the reconciler's job, reading these rows on its
// own schedule.
//
// Every method takes the caller's *db.Queries rather than opening its own
// transaction. A plugin_containers row and the plugin_instances row it
// belongs to must commit or roll back together — the admin create-instance
// and delete-instance handlers open the transaction and pass it through, so a
// failure on either side leaves neither behind (issue #349's lesson, applied
// here).
type DesiredState struct {
	clock func() time.Time
}

// NewDesiredState constructs a DesiredState. clock defaults to time.Now when nil.
func NewDesiredState(clock func() time.Time) *DesiredState {
	if clock == nil {
		clock = time.Now
	}
	return &DesiredState{clock: clock}
}

// CreateForInstance writes the plugin_containers desired-state row for a
// freshly created instance: it fails closed unless the plugin is active, then
// takes the pinned image ref/digest from the approved manifest and confirms
// it against plugin_container_images, and resolves the effective resource
// limits (resources.Resolve), before inserting the row with
// desired_state=running. There is no per-instance resource override column
// yet, so only the manifest and the host default currently apply.
func (d *DesiredState) CreateForInstance(ctx context.Context, q *db.Queries, instanceID string) (db.PluginContainer, error) {
	instance, err := q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		return db.PluginContainer{}, fmt.Errorf("lifecycle: get plugin instance %q: %w", instanceID, err)
	}

	plugin, err := q.GetPluginByID(ctx, instance.PluginID)
	if err != nil {
		return db.PluginContainer{}, fmt.Errorf("lifecycle: get plugin %q: %w", instance.PluginID, err)
	}
	if plugin.Status != string(model.PluginStatusActive) {
		return db.PluginContainer{}, fmt.Errorf("lifecycle: plugin %q has status %q: %w", plugin.Name, plugin.Status, ErrPluginNotActive)
	}

	m, err := manifestv2.Parse([]byte(plugin.ManifestSnapshot))
	if err != nil {
		return db.PluginContainer{}, fmt.Errorf("lifecycle: parse v2 manifest for plugin %q: %w", plugin.Name, err)
	}

	imageRef, imageDigest, err := d.confirmImage(ctx, q, plugin, m)
	if err != nil {
		return db.PluginContainer{}, err
	}

	declaredLimits, err := manifestLimits(m)
	if err != nil {
		return db.PluginContainer{}, fmt.Errorf("lifecycle: plugin %q: %w", plugin.Name, err)
	}
	limits := resources.Resolve(declaredLimits, resources.Limits{})

	now := d.clock().UTC().Format(time.RFC3339Nano)
	row, err := q.CreatePluginContainer(ctx, db.CreatePluginContainerParams{
		ID:                 model.NewULID(),
		PluginInstanceID:   instanceID,
		ImageRef:           imageRef,
		ImageDigest:        imageDigest,
		ConfigHash:         hashConfig(instance.ConfigJson),
		NetworkName:        networkNameFor(instanceID),
		MemoryLimitBytes:   int64Ptr(limits.MemoryBytes),
		CpuLimitMillicores: int64Ptr(limits.CPUMillicores),
		DesiredState:       DesiredStateRunning,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	if err != nil {
		return db.PluginContainer{}, fmt.Errorf("lifecycle: create plugin_containers row for instance %q: %w", instanceID, err)
	}
	return row, nil
}

// confirmImage takes the image reference and digest from the APPROVED
// manifest_snapshot — the one thing an admin actually reviewed — never from a
// second, independently-editable table. plugin_container_images is consulted
// only to WARN, never to fail closed: a digest is content-addressed, so two
// unrelated (and separately approved) plugins can legitimately declare the
// exact same base image, and refusing to provision over that coincidence
// would let a still-pending plugin B deny service to an already-active
// plugin A just by declaring A's digest in its own manifest before B is ever
// approved. A missing row is equally fine (manual posture, or the host has
// not confirmed the load yet) — the manifest pin is what runs either way.
func (d *DesiredState) confirmImage(ctx context.Context, q *db.Queries, plugin db.Plugin, m *manifestv2.Manifest) (imageRef, imageDigest string, err error) {
	imageRef = m.Package.Identifier
	imageDigest = m.Package.Digest()

	img, err := q.GetContainerImage(ctx, imageDigest)
	switch {
	case err == nil:
		if img.PluginID != nil && *img.PluginID != plugin.ID {
			slog.WarnContext(ctx, "plugin_containers: image digest also recorded under a different plugin",
				"digest", imageDigest, "plugin_id", plugin.ID, "plugin_name", plugin.Name, "other_plugin_id", *img.PluginID)
		}
	case errors.Is(err, sql.ErrNoRows):
		// Manual posture, or the host has not confirmed the load yet.
	default:
		return "", "", fmt.Errorf("lifecycle: confirm image %s for plugin %q: %w", imageDigest, plugin.Name, err)
	}
	return imageRef, imageDigest, nil
}

// SetDesired flips an instance's desired_state between running and stopped.
// Wiring this into an admin lifecycle action (deactivate/activate) is a later
// issue; it is implemented now because DesiredState is the row's one write
// path and every write belongs next to the others.
//
// UpdatePluginContainerDesiredState rewrites the whole row rather than one
// column, so every field the caller didn't intend to change is read back and
// passed through unmodified under the same ADR-038 CAS guard as the rest of
// this table.
func (d *DesiredState) SetDesired(ctx context.Context, q *db.Queries, instanceID, state string) error {
	if state != DesiredStateRunning && state != DesiredStateStopped {
		return fmt.Errorf("lifecycle: invalid desired_state %q", state)
	}

	// Re-check plugin.Status before ever asking for "running": a plugin
	// approved when the instance was created can be removed later, and the
	// kill switch (admin Activate) must not resurrect a container for it.
	// Stopping never needs this check — there is no version of "stop this
	// container" that a removed plugin's status should block.
	if state == DesiredStateRunning {
		instance, err := q.GetPluginInstanceByID(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("lifecycle: get plugin instance %q: %w", instanceID, err)
		}
		plugin, err := q.GetPluginByID(ctx, instance.PluginID)
		if err != nil {
			return fmt.Errorf("lifecycle: get plugin %q: %w", instance.PluginID, err)
		}
		if plugin.Status != string(model.PluginStatusActive) {
			return fmt.Errorf("lifecycle: plugin %q has status %q: %w", plugin.Name, plugin.Status, ErrPluginNotActive)
		}
	}

	row, err := q.GetPluginContainerByInstance(ctx, instanceID)
	if err != nil {
		if state == DesiredStateStopped && errors.Is(err, sql.ErrNoRows) {
			// Nothing to stop is success, not failure: an instance with no
			// container row (v1, or one never provisioned) is already not
			// running. Only "make this run" needs a row to flip.
			return nil
		}
		return fmt.Errorf("lifecycle: get plugin_containers row for instance %q: %w", instanceID, err)
	}

	n, err := q.UpdatePluginContainerDesiredState(ctx, db.UpdatePluginContainerDesiredStateParams{
		ImageRef:           row.ImageRef,
		ImageDigest:        row.ImageDigest,
		ConfigHash:         row.ConfigHash,
		NetworkName:        row.NetworkName,
		MemoryLimitBytes:   row.MemoryLimitBytes,
		CpuLimitMillicores: row.CpuLimitMillicores,
		DesiredState:       state,
		UpdatedAt:          d.clock().UTC().Format(time.RFC3339Nano),
		ID:                 row.ID,
		ExpectedVersion:    row.Version,
	})
	if err != nil {
		return fmt.Errorf("lifecycle: set desired_state for instance %q: %w", instanceID, err)
	}
	if n == 0 {
		return fmt.Errorf("lifecycle: set desired_state for instance %q: %w", instanceID, ErrCASConflict)
	}
	return nil
}

// Remove deletes the plugin_containers desired-state row for instanceID. A
// missing row is not an error: an instance that never got one (v1, or a
// provisioner-less test) has nothing to remove, and the FK's ON DELETE
// CASCADE from plugin_instances already covers the row anyway — this call is
// what makes removal explicit and independent of that cascade running first.
func (d *DesiredState) Remove(ctx context.Context, q *db.Queries, instanceID string) error {
	row, err := q.GetPluginContainerByInstance(ctx, instanceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lifecycle: get plugin_containers row for instance %q: %w", instanceID, err)
	}
	if err := q.DeletePluginContainer(ctx, row.ID); err != nil {
		return fmt.Errorf("lifecycle: delete plugin_containers row %q: %w", row.ID, err)
	}
	return nil
}

// manifestLimits converts a manifest's declared resources (MiB, millicores)
// into resources.Limits. A manifest with no resources block returns the zero
// Limits, which resources.Resolve reads as "the manifest said nothing" and
// falls through to the host default.
func manifestLimits(m *manifestv2.Manifest) (resources.Limits, error) {
	r := m.Gleipnir.Resources
	if r == nil {
		return resources.Limits{}, nil
	}
	return resources.FromManifestMiB(r.MemoryMB, r.CPUMillicores)
}

// hashConfig computes the plugin_containers.config_hash column: a hex
// SHA-256 of the instance's effective config. Its only consumer is "did the
// config change" (a rotation trigger), so the algorithm only needs to be
// stable, not reversible or canonical against key reordering — config_json
// is already the exact byte string every writer agreed on.
func hashConfig(configJSON string) string {
	sum := sha256.Sum256([]byte(configJSON))
	return hex.EncodeToString(sum[:])
}

// networkNameFor derives an instance's dedicated container network name. This
// mirrors internal/plugin/reconciler's own fallback convention exactly (its
// defaultNetworkName only falls back to recomputing this for a row that
// predates network management) — CreateForInstance is what makes every new
// row carry the name outright, so the reconciler's fallback should never
// actually fire for a v2-installed instance.
func networkNameFor(instanceID string) string {
	return "gleipnir-plugin-" + instanceID
}

func int64Ptr(v int64) *int64 { return &v }
