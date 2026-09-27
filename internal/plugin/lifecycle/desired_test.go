package lifecycle_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

var fixedClock = func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) }

// seedV2Plugin inserts a v2 plugin row whose manifest pins digest and declares
// the given resource limits (zero values omit the resources block, exercising
// the host-default fallback), with the given plugins.status.
func seedV2Plugin(t *testing.T, store *db.Store, pluginID, digest string, memoryMB, cpuMillicores int, status string) {
	t.Helper()
	ctx := context.Background()

	resourcesBlock := ""
	if memoryMB > 0 || cpuMillicores > 0 {
		resourcesBlock = fmt.Sprintf("  resources:\n    memory_mb: %d\n    cpu_millicores: %d\n", memoryMB, cpuMillicores)
	}
	manifest := fmt.Sprintf(`schema_version: "2"
name: test-plugin
version: "1.0.0"
package:
  registry_type: oci
  identifier: ghcr.io/acme/test-plugin@%s
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
%s`, digest, resourcesBlock)

	now := fixedClock().UTC().Format(time.RFC3339Nano)
	if _, err := store.Queries().CreatePlugin(ctx, db.CreatePluginParams{
		ID:               pluginID,
		Name:             "test-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: manifest,
		TrustedPubkey:    "",
		Status:           status,
		BinaryPath:       nil,
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
}

func seedInstance(t *testing.T, store *db.Store, instanceID, pluginID string) {
	t.Helper()
	now := fixedClock().UTC().Format(time.RFC3339Nano)
	if _, err := store.Queries().CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID:                    instanceID,
		PluginID:              pluginID,
		InstanceName:          "inst1",
		ConfigJson:            "{}",
		SubscriptionScopeJson: "{}",
		HandshakeVersions:     "{}",
		HealthState:           "unhealthy",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
}

func TestDesiredState_CreateForInstance_ManifestResources(t *testing.T) {
	store := testutil.NewTestStore(t)
	ctx := context.Background()
	digest := digestOf("desired-state-manifest")

	seedV2Plugin(t, store, "plugin-1", digest, 128, 250, "active")
	seedInstance(t, store, "inst-1", "plugin-1")

	d := lifecycle.NewDesiredState(fixedClock)
	row, err := d.CreateForInstance(ctx, store.Queries(), "inst-1")
	if err != nil {
		t.Fatalf("CreateForInstance: %v", err)
	}

	if row.ImageDigest != digest {
		t.Errorf("ImageDigest = %q, want %q", row.ImageDigest, digest)
	}
	if row.ImageRef != "ghcr.io/acme/test-plugin@"+digest {
		t.Errorf("ImageRef = %q, want the manifest's pinned identifier", row.ImageRef)
	}
	if row.DesiredState != lifecycle.DesiredStateRunning {
		t.Errorf("DesiredState = %q, want %q", row.DesiredState, lifecycle.DesiredStateRunning)
	}
	wantMemory := int64(128) << 20
	if row.MemoryLimitBytes == nil || *row.MemoryLimitBytes != wantMemory {
		t.Errorf("MemoryLimitBytes = %v, want %d (from the manifest)", row.MemoryLimitBytes, wantMemory)
	}
	if row.CpuLimitMillicores == nil || *row.CpuLimitMillicores != 250 {
		t.Errorf("CpuLimitMillicores = %v, want 250 (from the manifest)", row.CpuLimitMillicores)
	}

	// Exactly one row exists for the instance.
	fetched, err := store.Queries().GetPluginContainerByInstance(ctx, "inst-1")
	if err != nil {
		t.Fatalf("GetPluginContainerByInstance: %v", err)
	}
	if fetched.ID != row.ID {
		t.Errorf("fetched row ID = %q, want %q", fetched.ID, row.ID)
	}
}

func TestDesiredState_CreateForInstance_DefaultResources(t *testing.T) {
	store := testutil.NewTestStore(t)
	ctx := context.Background()
	digest := digestOf("desired-state-defaults")

	// No resources block in the manifest: the host default applies.
	seedV2Plugin(t, store, "plugin-1", digest, 0, 0, "active")
	seedInstance(t, store, "inst-1", "plugin-1")

	d := lifecycle.NewDesiredState(fixedClock)
	row, err := d.CreateForInstance(ctx, store.Queries(), "inst-1")
	if err != nil {
		t.Fatalf("CreateForInstance: %v", err)
	}

	if row.MemoryLimitBytes == nil || *row.MemoryLimitBytes != resources.DefaultMemoryBytes {
		t.Errorf("MemoryLimitBytes = %v, want the host default %d", row.MemoryLimitBytes, resources.DefaultMemoryBytes)
	}
	if row.CpuLimitMillicores == nil || *row.CpuLimitMillicores != resources.DefaultCPUMillicores {
		t.Errorf("CpuLimitMillicores = %v, want the host default %d", row.CpuLimitMillicores, resources.DefaultCPUMillicores)
	}
}

// TestDesiredState_CreateForInstance_StatusGate proves CreateForInstance
// fails closed unless the plugin has passed admin review. Unlike v1 (where
// process.Manager.Start gates on plugin.Status before ever spawning a
// subprocess), the reconciler converges ANY plugin_containers row it finds —
// so this check is the only thing standing between "pending_review" and a
// running container.
func TestDesiredState_CreateForInstance_StatusGate(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		wantErr bool
	}{
		{name: "pending_review is rejected", status: "pending_review", wantErr: true},
		{name: "removed is rejected", status: "removed", wantErr: true},
		{name: "active is accepted", status: "active", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			ctx := context.Background()
			digest := digestOf("status-gate-" + tt.status)

			seedV2Plugin(t, store, "plugin-1", digest, 0, 0, tt.status)
			seedInstance(t, store, "inst-1", "plugin-1")

			d := lifecycle.NewDesiredState(fixedClock)
			_, err := d.CreateForInstance(ctx, store.Queries(), "inst-1")
			if tt.wantErr {
				if !errors.Is(err, lifecycle.ErrPluginNotActive) {
					t.Fatalf("CreateForInstance: err = %v, want ErrPluginNotActive", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateForInstance: %v", err)
			}
		})
	}
}

// TestDesiredState_CreateForInstance_ConfirmedImageMatchesPlugin is the
// positive case: a plugin_container_images row exists for the manifest's
// pinned digest and names the SAME plugin, so it merely confirms what the
// manifest already said.
func TestDesiredState_CreateForInstance_ConfirmedImageMatchesPlugin(t *testing.T) {
	store := testutil.NewTestStore(t)
	ctx := context.Background()
	digest := digestOf("confirmed-match")

	seedV2Plugin(t, store, "plugin-1", digest, 0, 0, "active")
	seedInstance(t, store, "inst-1", "plugin-1")

	now := fixedClock().UTC().Format(time.RFC3339Nano)
	pluginID := "plugin-1"
	if _, err := store.Queries().UpsertContainerImage(ctx, db.UpsertContainerImageParams{
		Digest:     digest,
		Reference:  "ghcr.io/acme/test-plugin@" + digest,
		PluginID:   &pluginID,
		LoadedAt:   now,
		LastUsedAt: &now,
	}); err != nil {
		t.Fatalf("seed loaded image: %v", err)
	}

	d := lifecycle.NewDesiredState(fixedClock)
	row, err := d.CreateForInstance(ctx, store.Queries(), "inst-1")
	if err != nil {
		t.Fatalf("CreateForInstance: %v", err)
	}
	if row.ImageDigest != digest {
		t.Errorf("ImageDigest = %q, want the manifest's pinned digest %q", row.ImageDigest, digest)
	}
}

// TestDesiredState_CreateForInstance_ImageOwnedByDifferentPlugin_StillProvisions
// proves the owner-mismatch check is advisory, not a gate: digests are
// content-addressed, so two separately-approved plugins can legitimately
// declare the exact same base image. Failing closed here would let a
// still-pending plugin B deny service to an already-active plugin A just by
// declaring A's digest in its own (unapproved) manifest — provisioning must
// succeed regardless, using the manifest's own pinned digest.
func TestDesiredState_CreateForInstance_ImageOwnedByDifferentPlugin_StillProvisions(t *testing.T) {
	store := testutil.NewTestStore(t)
	ctx := context.Background()
	sharedDigest := digestOf("shared-digest")

	seedV2Plugin(t, store, "plugin-1", sharedDigest, 0, 0, "active")
	seedInstance(t, store, "inst-1", "plugin-1")

	// A different plugin's install recorded this exact digest as its own.
	now := fixedClock().UTC().Format(time.RFC3339Nano)
	otherPluginID := "plugin-2"
	if _, err := store.Queries().CreatePlugin(ctx, db.CreatePluginParams{
		ID:               otherPluginID,
		Name:             "other-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: "schema_version: \"2\"\n",
		TrustedPubkey:    "",
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("seed other plugin: %v", err)
	}
	if _, err := store.Queries().UpsertContainerImage(ctx, db.UpsertContainerImageParams{
		Digest:     sharedDigest,
		Reference:  "ghcr.io/acme/other-plugin@" + sharedDigest,
		PluginID:   &otherPluginID,
		LoadedAt:   now,
		LastUsedAt: &now,
	}); err != nil {
		t.Fatalf("seed loaded image: %v", err)
	}

	d := lifecycle.NewDesiredState(fixedClock)
	row, err := d.CreateForInstance(ctx, store.Queries(), "inst-1")
	if err != nil {
		t.Fatalf("CreateForInstance: unexpected error: %v", err)
	}
	if row.ImageDigest != sharedDigest {
		t.Errorf("ImageDigest = %q, want the manifest's own pinned digest %q", row.ImageDigest, sharedDigest)
	}
}

func TestDesiredState_SetDesired(t *testing.T) {
	store := testutil.NewTestStore(t)
	ctx := context.Background()
	digest := digestOf("set-desired")

	seedV2Plugin(t, store, "plugin-1", digest, 0, 0, "active")
	seedInstance(t, store, "inst-1", "plugin-1")

	d := lifecycle.NewDesiredState(fixedClock)
	if _, err := d.CreateForInstance(ctx, store.Queries(), "inst-1"); err != nil {
		t.Fatalf("CreateForInstance: %v", err)
	}

	if err := d.SetDesired(ctx, store.Queries(), "inst-1", lifecycle.DesiredStateStopped); err != nil {
		t.Fatalf("SetDesired: %v", err)
	}

	row, err := store.Queries().GetPluginContainerByInstance(ctx, "inst-1")
	if err != nil {
		t.Fatalf("GetPluginContainerByInstance: %v", err)
	}
	if row.DesiredState != lifecycle.DesiredStateStopped {
		t.Errorf("DesiredState = %q, want %q", row.DesiredState, lifecycle.DesiredStateStopped)
	}

	if err := d.SetDesired(ctx, store.Queries(), "inst-1", "sideways"); err == nil {
		t.Error("SetDesired with an invalid state: want an error, got nil")
	}
}

// TestDesiredState_SetDesired_StatusGate proves SetDesired(running) re-checks
// plugin.Status exactly like CreateForInstance does: a plugin approved when
// the instance was created can be removed later, and the admin Activate path
// (the kill switch's "on" side) must not resurrect a container for it.
// SetDesired(stopped) never needs the check — nothing about stopping a
// container should be blocked by the owning plugin's review state.
func TestDesiredState_SetDesired_StatusGate(t *testing.T) {
	tests := []struct {
		name         string
		pluginStatus string
		wantErr      bool
	}{
		{name: "pending_review is rejected", pluginStatus: "pending_review", wantErr: true},
		{name: "removed is rejected", pluginStatus: "removed", wantErr: true},
		{name: "active is accepted", pluginStatus: "active", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			ctx := context.Background()
			digest := digestOf("set-desired-status-gate-" + tt.pluginStatus)

			seedV2Plugin(t, store, "plugin-1", digest, 0, 0, "active")
			seedInstance(t, store, "inst-1", "plugin-1")

			d := lifecycle.NewDesiredState(fixedClock)
			if _, err := d.CreateForInstance(ctx, store.Queries(), "inst-1"); err != nil {
				t.Fatalf("CreateForInstance: %v", err)
			}

			// Flip the plugin's status after the container row already exists,
			// mirroring "approved, then later removed".
			if _, err := store.Queries().UpdatePluginStatus(ctx, db.UpdatePluginStatusParams{
				ID:              "plugin-1",
				Status:          tt.pluginStatus,
				UpdatedAt:       fixedClock().UTC().Format(time.RFC3339Nano),
				ExpectedVersion: 0,
			}); err != nil {
				t.Fatalf("UpdatePluginStatus: %v", err)
			}

			err := d.SetDesired(ctx, store.Queries(), "inst-1", lifecycle.DesiredStateRunning)
			if tt.wantErr {
				if !errors.Is(err, lifecycle.ErrPluginNotActive) {
					t.Fatalf("SetDesired(running): err = %v, want ErrPluginNotActive", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetDesired(running): %v", err)
			}
		})
	}
}

// TestDesiredState_SetDesired_StoppedNoRowIsANoOp proves SetDesired(stopped)
// succeeds when the instance has no plugin_containers row at all — an
// instance never provisioned (or already fully removed) is already not
// running, so there is nothing to stop.
func TestDesiredState_SetDesired_StoppedNoRowIsANoOp(t *testing.T) {
	store := testutil.NewTestStore(t)
	ctx := context.Background()
	digest := digestOf("set-desired-no-row")

	seedV2Plugin(t, store, "plugin-1", digest, 0, 0, "active")
	seedInstance(t, store, "inst-1", "plugin-1")
	// No CreateForInstance call: no plugin_containers row exists.

	d := lifecycle.NewDesiredState(fixedClock)
	if err := d.SetDesired(ctx, store.Queries(), "inst-1", lifecycle.DesiredStateStopped); err != nil {
		t.Errorf("SetDesired(stopped) with no container row: %v, want nil (no-op)", err)
	}
}

// TestDesiredState_SetDesired_RunningNoRowIsAnError proves the no-op only
// applies to "stopped": asking for "running" with no container row is a real
// error (there is nothing to flip to running), not a no-op.
func TestDesiredState_SetDesired_RunningNoRowIsAnError(t *testing.T) {
	store := testutil.NewTestStore(t)
	ctx := context.Background()
	digest := digestOf("set-desired-no-row-running")

	seedV2Plugin(t, store, "plugin-1", digest, 0, 0, "active")
	seedInstance(t, store, "inst-1", "plugin-1")

	d := lifecycle.NewDesiredState(fixedClock)
	if err := d.SetDesired(ctx, store.Queries(), "inst-1", lifecycle.DesiredStateRunning); err == nil {
		t.Error("SetDesired(running) with no container row: want an error, got nil")
	}
}

func TestDesiredState_Remove(t *testing.T) {
	store := testutil.NewTestStore(t)
	ctx := context.Background()
	digest := digestOf("remove")

	seedV2Plugin(t, store, "plugin-1", digest, 0, 0, "active")
	seedInstance(t, store, "inst-1", "plugin-1")

	d := lifecycle.NewDesiredState(fixedClock)
	if _, err := d.CreateForInstance(ctx, store.Queries(), "inst-1"); err != nil {
		t.Fatalf("CreateForInstance: %v", err)
	}

	if err := d.Remove(ctx, store.Queries(), "inst-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	_, err := store.Queries().GetPluginContainerByInstance(ctx, "inst-1")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetPluginContainerByInstance after Remove: err = %v, want sql.ErrNoRows", err)
	}

	// Removing again (no row left) must not error.
	if err := d.Remove(ctx, store.Queries(), "inst-1"); err != nil {
		t.Errorf("Remove on an already-removed instance: %v, want nil", err)
	}
}
