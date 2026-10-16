package loader

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

const upgradePlugin = "upgrade-plugin"

// upgradeBase installs version 1.0.0 of upgradePlugin, activates it and gives
// it one healthy instance, then returns what a later bundle will be compared
// against. The installed image is digestOf("base").
type upgradeBase struct {
	store    *db.Store
	rt       *container.Fake
	in       *OCIInstaller
	signer   *ociSigner
	pluginID string
	row      db.Plugin
	notified []string
}

func newUpgradeBase(t *testing.T) *upgradeBase {
	t.Helper()
	ctx := context.Background()
	store, rt, in := newOCIFixture(t, false)
	b := &upgradeBase{store: store, rt: rt, in: in, signer: newOCISigner(t)}
	in.OnInstalled(func(_ context.Context, pluginID string) { b.notified = append(b.notified, pluginID) })

	digest := digestOf("base")
	rt.PendingImages = []container.ImageInfo{{ID: digest}}
	res, err := in.Install(ctx, buildOCIBundle(t, ociBundleOptions{
		name: upgradePlugin, version: "1.0.0", digest: digest, seed: "base", signer: b.signer,
	}))
	if err != nil {
		t.Fatalf("install base: %v", err)
	}
	b.pluginID = res.PluginID

	if _, err := store.DB().ExecContext(ctx, `UPDATE plugins SET status = 'active' WHERE id = ?`, b.pluginID); err != nil {
		t.Fatalf("activate plugin: %v", err)
	}
	seedPluginInstance(t, store.Queries(), b.pluginID, "inst-1", model.PluginHealthStateHealthy)
	b.row = b.plugin(t)
	return b
}

func (b *upgradeBase) plugin(t *testing.T) db.Plugin {
	t.Helper()
	p, err := b.store.Queries().GetPluginByName(context.Background(), upgradePlugin)
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	return p
}

func (b *upgradeBase) auditCount(t *testing.T, eventType string) int {
	t.Helper()
	rows, err := b.store.Queries().ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: eventType,
		Limit:     50,
	})
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByType(%s): %v", eventType, err)
	}
	return len(rows)
}

func (b *upgradeBase) instanceState(t *testing.T) string {
	t.Helper()
	inst, err := b.store.Queries().GetPluginInstanceByID(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("GetPluginInstanceByID: %v", err)
	}
	return inst.HealthState
}

// withGleipnirBlock renders the base manifest for digest with extra lines
// appended inside the gleipnir: block and a replacement description.
func withGleipnirBlock(version, digest, description, extra string) []byte {
	m := string(v2Manifest(upgradePlugin, version, digest))
	m = strings.Replace(m, "a containerized test plugin", description, 1)
	return []byte(m + extra)
}

func TestOCIInstall_UpgradeOfExistingPlugin(t *testing.T) {
	baseDigest := digestOf("base")
	nextDigest := digestOf("next")

	tests := []struct {
		name string
		// bundle builds the incoming bundle; the signer is the base's unless
		// the case rotates it.
		bundle func(t *testing.T, b *upgradeBase) string
		// pending is the image the runtime will report after the load.
		pending string

		wantReason       string // InstallRejectedError.Reason; empty = success
		wantSnapshotSame bool
		wantVersion      string
		wantLoads        int
		wantNotified     int
		wantState        model.PluginHealthState
		wantAudits       []string
		wantNoAudits     []string
		wantPending      bool   // plugin_pending_manifests row exists
		wantImageRow     string // digest expected in plugin_container_images, "" = none beyond base
	}{
		{
			name: "identical re-drop is a no-op",
			bundle: func(t *testing.T, b *upgradeBase) string {
				return buildOCIBundle(t, ociBundleOptions{name: upgradePlugin, version: "1.0.0", digest: baseDigest, seed: "base", signer: b.signer})
			},
			pending:          baseDigest,
			wantSnapshotSame: true,
			wantVersion:      "1.0.0",
			wantLoads:        1,
			wantState:        model.PluginHealthStateHealthy,
			wantAudits:       []string{auditReinstallIgnored},
			wantNoAudits:     []string{auditManifestCosmeticChange, auditManifestMaterialChange},
		},
		{
			name: "cosmetic change hot-applies and notifies",
			bundle: func(t *testing.T, b *upgradeBase) string {
				return buildOCIBundle(t, ociBundleOptions{
					name: upgradePlugin, version: "1.0.1", digest: baseDigest, seed: "base", signer: b.signer,
					manifestOverride: withGleipnirBlock("1.0.1", baseDigest, "a nicer description", ""),
				})
			},
			pending:      baseDigest,
			wantVersion:  "1.0.1",
			wantLoads:    2,
			wantNotified: 1,
			wantState:    model.PluginHealthStateHealthy,
			wantAudits:   []string{auditManifestCosmeticChange},
			wantNoAudits: []string{auditManifestMaterialChange, auditPubkeyMismatch},
		},
		{
			name: "changed image digest is blocked",
			bundle: func(t *testing.T, b *upgradeBase) string {
				return buildOCIBundle(t, ociBundleOptions{name: upgradePlugin, version: "1.1.0", digest: nextDigest, seed: "next", signer: b.signer})
			},
			pending:          nextDigest,
			wantReason:       rejectMaterialChange,
			wantSnapshotSame: true,
			wantVersion:      "1.0.0",
			wantLoads:        2,
			wantState:        model.PluginHealthStatePendingManifestApproval,
			wantAudits:       []string{auditManifestMaterialChange},
			wantNoAudits:     []string{auditManifestCosmeticChange},
			wantPending:      true,
			wantImageRow:     nextDigest,
		},
		{
			name: "new egress grant is blocked",
			bundle: func(t *testing.T, b *upgradeBase) string {
				return buildOCIBundle(t, ociBundleOptions{
					name: upgradePlugin, version: "1.0.1", digest: baseDigest, seed: "base", signer: b.signer,
					manifestOverride: withGleipnirBlock("1.0.1", baseDigest, "a containerized test plugin", "  egress:\n    - domain: api.example.com\n"),
				})
			},
			pending:          baseDigest,
			wantReason:       rejectMaterialChange,
			wantSnapshotSame: true,
			wantVersion:      "1.0.0",
			wantLoads:        2,
			wantState:        model.PluginHealthStatePendingManifestApproval,
			wantAudits:       []string{auditManifestMaterialChange},
			wantPending:      true,
		},
		{
			name: "resource increase is blocked",
			bundle: func(t *testing.T, b *upgradeBase) string {
				return buildOCIBundle(t, ociBundleOptions{
					name: upgradePlugin, version: "1.0.1", digest: baseDigest, seed: "base", signer: b.signer,
					manifestOverride: withGleipnirBlock("1.0.1", baseDigest, "a containerized test plugin", "  resources:\n    memory_mb: 2048\n"),
				})
			},
			pending:          baseDigest,
			wantReason:       rejectMaterialChange,
			wantSnapshotSame: true,
			wantVersion:      "1.0.0",
			wantLoads:        2,
			wantState:        model.PluginHealthStatePendingManifestApproval,
			wantAudits:       []string{auditManifestMaterialChange},
			wantPending:      true,
		},
		{
			name: "different signing key waits for approval and loads nothing",
			bundle: func(t *testing.T, b *upgradeBase) string {
				return buildOCIBundle(t, ociBundleOptions{name: upgradePlugin, version: "1.1.0", digest: nextDigest, seed: "next"})
			},
			pending:          nextDigest,
			wantReason:       rejectPubkeyMismatch,
			wantSnapshotSame: true,
			wantVersion:      "1.0.0",
			wantLoads:        1,
			wantState:        model.PluginHealthStatePendingKeyApproval,
			wantAudits:       []string{auditPubkeyMismatch},
			wantNoAudits:     []string{auditManifestMaterialChange},
		},
		{
			name: "image that is not the pinned digest changes nothing",
			bundle: func(t *testing.T, b *upgradeBase) string {
				return buildOCIBundle(t, ociBundleOptions{name: upgradePlugin, version: "1.1.0", digest: nextDigest, seed: "next", signer: b.signer})
			},
			pending:          digestOf("something-else"),
			wantReason:       rejectImageDigestMismatch,
			wantSnapshotSame: true,
			wantVersion:      "1.0.0",
			wantLoads:        2,
			wantState:        model.PluginHealthStateHealthy,
			wantAudits:       []string{auditImageDigestMismatch},
			wantNoAudits:     []string{auditManifestMaterialChange},
		},
		{
			name: "downgrade is refused",
			bundle: func(t *testing.T, b *upgradeBase) string {
				return buildOCIBundle(t, ociBundleOptions{name: upgradePlugin, version: "0.9.0", digest: nextDigest, seed: "next", signer: b.signer})
			},
			pending:          nextDigest,
			wantReason:       rejectDowngrade,
			wantSnapshotSame: true,
			wantVersion:      "1.0.0",
			wantLoads:        1,
			wantState:        model.PluginHealthStateHealthy,
			wantAudits:       []string{auditDowngradeRefused},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			b := newUpgradeBase(t)

			bundle := tt.bundle(t, b)
			b.rt.PendingImages = []container.ImageInfo{{ID: tt.pending}}
			_, err := b.in.Install(ctx, bundle)

			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("Install: %v", err)
				}
			} else {
				var rejected *InstallRejectedError
				if !errors.As(err, &rejected) || rejected.Reason != tt.wantReason {
					t.Fatalf("Install error = %v, want InstallRejectedError reason %q", err, tt.wantReason)
				}
			}

			got := b.plugin(t)
			if same := got.ManifestSnapshot == b.row.ManifestSnapshot; same != tt.wantSnapshotSame {
				t.Errorf("snapshot unchanged = %v, want %v", same, tt.wantSnapshotSame)
			}
			if tt.wantSnapshotSame && got.Version != b.row.Version {
				t.Errorf("row version = %d, want unchanged %d: a refused bundle must not touch the row", got.Version, b.row.Version)
			}
			if got.PluginVersion != tt.wantVersion {
				t.Errorf("plugin_version = %q, want %q", got.PluginVersion, tt.wantVersion)
			}
			if got.Status != statusActive {
				t.Errorf("status = %q, want active preserved", got.Status)
			}
			if got.TrustedPubkey != b.row.TrustedPubkey {
				t.Error("pinned key changed")
			}
			if b.rt.Loads != tt.wantLoads {
				t.Errorf("image loads = %d, want %d", b.rt.Loads, tt.wantLoads)
			}
			if len(b.notified) != tt.wantNotified {
				t.Errorf("OnInstalled fired %d times, want %d", len(b.notified), tt.wantNotified)
			}
			if state := b.instanceState(t); state != string(tt.wantState) {
				t.Errorf("instance health = %q, want %q", state, tt.wantState)
			}
			for _, ev := range tt.wantAudits {
				if b.auditCount(t, ev) == 0 {
					t.Errorf("no %s audit event", ev)
				}
			}
			for _, ev := range tt.wantNoAudits {
				if n := b.auditCount(t, ev); n != 0 {
					t.Errorf("%d unexpected %s audit events", n, ev)
				}
			}

			pending, pendingErr := b.store.Queries().GetPluginPendingManifest(ctx, b.pluginID)
			if tt.wantPending {
				if pendingErr != nil {
					t.Fatalf("no pending manifest recorded: %v", pendingErr)
				}
				if pending.CandidateManifest == b.row.ManifestSnapshot {
					t.Error("pending candidate is the consented snapshot, not the incoming manifest")
				}
			} else if pendingErr == nil {
				t.Error("a pending manifest was recorded for a change that was not blocked")
			}

			img, imgErr := b.store.Queries().GetContainerImage(ctx, nextDigest)
			if wantRow := tt.wantImageRow == nextDigest; wantRow != (imgErr == nil) {
				t.Errorf("image row for the candidate digest present = %v, want %v", imgErr == nil, wantRow)
			}
			// Accept-manifest loads nothing: the ID it will later create from
			// is the one recorded here, when the candidate was verified.
			if imgErr == nil && (img.LocalImageID == nil || *img.LocalImageID != tt.pending) {
				t.Errorf("candidate local_image_id = %v, want %q", img.LocalImageID, tt.pending)
			}
		})
	}
}

// The blocked candidate keeps its manifest verbatim in the audit event, so the
// admin reviews exactly what was signed.
func TestOCIInstall_MaterialChangeAuditCarriesCandidate(t *testing.T) {
	ctx := context.Background()
	b := newUpgradeBase(t)

	nextDigest := digestOf("next")
	b.rt.PendingImages = []container.ImageInfo{{ID: nextDigest}}
	_, err := b.in.Install(ctx, buildOCIBundle(t, ociBundleOptions{name: upgradePlugin, version: "1.1.0", digest: nextDigest, seed: "next", signer: b.signer}))
	var rejected *InstallRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("Install error = %v, want InstallRejectedError", err)
	}

	assertAuditEvent(t, b.store, auditManifestMaterialChange, severityHigh)
	rows, err := b.store.Queries().ListPluginAuditEventsByType(ctx, db.ListPluginAuditEventsByTypeParams{EventType: auditManifestMaterialChange, Limit: 1})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	for _, want := range []string{`"candidate_manifest_b64"`, `"new_image_digest":"` + nextDigest + `"`, `"package.digest"`} {
		if !strings.Contains(rows[0].PayloadJson, want) {
			t.Errorf("audit payload missing %s: %s", want, rows[0].PayloadJson)
		}
	}
}

// A v2 bundle may not replace a v1 row, and a v1 bundle may not replace a v2
// row: each would swap what runs under the other's consent.
func TestInstall_SchemaCrossoverIsRefused(t *testing.T) {
	ctx := context.Background()

	t.Run("v2 bundle over a v1 row", func(t *testing.T) {
		store, rt, in := newOCIFixture(t, false)
		v1Tar, _ := signedPluginTarball(t, upgradePlugin, "1.0.0")
		v1ID, err := in.base.Install(ctx, v1Tar)
		if err != nil {
			t.Fatalf("install v1: %v", err)
		}
		before, _ := store.Queries().GetPluginByName(ctx, upgradePlugin)

		digest := digestOf("base")
		rt.PendingImages = []container.ImageInfo{{ID: digest}}
		_, err = in.Install(ctx, buildOCIBundle(t, ociBundleOptions{name: upgradePlugin, version: "2.0.0", digest: digest, seed: "base"}))
		assertCrossoverRefused(t, err)

		after, _ := store.Queries().GetPluginByName(ctx, upgradePlugin)
		if after.ID != v1ID || after.ManifestSnapshot != before.ManifestSnapshot || after.Version != before.Version {
			t.Error("a v2 bundle changed a v1 row")
		}
		if rt.Loads != 0 {
			t.Errorf("image loaded %d times for a refused crossover", rt.Loads)
		}
		assertAuditEvent(t, store, auditSchemaCrossover, severityHigh)
	})

	t.Run("v1 bundle over a v2 row", func(t *testing.T) {
		b := newUpgradeBase(t)
		v1Tar, _ := signedPluginTarball(t, upgradePlugin, "2.0.0")
		_, err := b.in.base.Install(ctx, v1Tar)
		assertCrossoverRefused(t, err)

		after := b.plugin(t)
		if after.ManifestSnapshot != b.row.ManifestSnapshot || after.Version != b.row.Version || after.BinaryPath != nil {
			t.Error("a v1 bundle changed a v2 row")
		}
		if state := b.instanceState(t); state != string(model.PluginHealthStateHealthy) {
			t.Errorf("instance health = %q, want it untouched", state)
		}
		assertAuditEvent(t, b.store, auditSchemaCrossover, severityHigh)
	})
}

func assertCrossoverRefused(t *testing.T, err error) {
	t.Helper()
	var rejected *InstallRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != rejectSchemaCrossover {
		t.Fatalf("Install error = %v, want InstallRejectedError reason %q", err, rejectSchemaCrossover)
	}
}
