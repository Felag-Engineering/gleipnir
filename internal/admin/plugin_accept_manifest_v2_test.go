package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle"
)

const (
	acceptV2Digest    = "sha256:0a666c9d2a6e22855c3a96c6d3de6e9f5223f5c70375999d1bc32fade6d4ebbb"
	acceptV2NewDigest = "sha256:1b777d0e3b7f33966d4b07d7e4ef7f0a6334a6d81486000e2cd43abef7e5fccc"
)

func acceptV2Manifest(version, digest, extra string) string {
	return `schema_version: "2"
name: v2-tool
version: "` + version + `"
package:
  registry_type: oci
  identifier: ghcr.io/acme/v2-tool@` + digest + `
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
  resources:
    memory_mb: 64
    cpu_millicores: 100
` + extra
}

type acceptV2Fixture struct {
	store      *db.Store
	h          *PluginHandler
	instanceID string
	candidate  string
}

// newAcceptV2Fixture seeds an active v2 plugin with one provisioned instance
// held in pending_manifest_approval and a pending candidate that moves the
// image digest and raises the memory limit.
func newAcceptV2Fixture(t *testing.T, status string, approveV2 bool) *acceptV2Fixture {
	t.Helper()
	ctx := context.Background()
	fixedClock := func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) }
	store := newPluginTestStore(t)
	now := fixedClock().UTC().Format(time.RFC3339Nano)

	if _, err := store.Queries().CreatePlugin(ctx, db.CreatePluginParams{
		ID:               "plugin-v2",
		Name:             "v2-tool",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: acceptV2Manifest("1.0.0", acceptV2Digest, ""),
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}

	h := newTestPluginHandler(store.Queries(), fixedClock, testPluginHandlerConfig{
		store:       store,
		provisioner: lifecycle.NewDesiredState(fixedClock),
		approveV2:   approveV2,
	})
	rec := serveCreateInstance(h, "plugin-v2", []byte(`{"instance_name":"inst1"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create instance: %d %s", rec.Code, rec.Body.String())
	}
	var created createInstanceResponse
	if err := json.Unmarshal(parseDataResponse(t, rec), &created); err != nil {
		t.Fatalf("unmarshal instance: %v", err)
	}

	f := &acceptV2Fixture{store: store, h: h, instanceID: created.ID}
	f.candidate = strings.Replace(acceptV2Manifest("1.1.0", acceptV2NewDigest, ""), "memory_mb: 64", "memory_mb: 128", 1)

	if status != "active" {
		if _, err := store.DB().ExecContext(ctx, `UPDATE plugins SET status = ? WHERE id = 'plugin-v2'`, status); err != nil {
			t.Fatalf("set status: %v", err)
		}
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE plugin_instances SET health_state = 'pending_manifest_approval' WHERE id = ?`, f.instanceID); err != nil {
		t.Fatalf("hold instance: %v", err)
	}
	if err := store.Queries().UpsertPluginPendingManifest(ctx, db.UpsertPluginPendingManifestParams{
		PluginID:          "plugin-v2",
		CandidateManifest: f.candidate,
		OldVersion:        "1.0.0",
		NewVersion:        "1.1.0",
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("seed pending manifest: %v", err)
	}
	return f
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// accept posts accept-manifest with the hash of the pending candidate, the way
// the review UI does after reading the detail response.
func (f *acceptV2Fixture) accept() *httptest.ResponseRecorder {
	return f.acceptWithBody(`{"expected_candidate_sha256":"` + sha256Hex(f.candidate) + `"}`)
}

func (f *acceptV2Fixture) acceptWithBody(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-v2/accept-manifest", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-v2"})
	rec := httptest.NewRecorder()
	f.h.AcceptManifest(rec, req)
	return rec
}

func TestAcceptManifest_V2(t *testing.T) {
	ctx := context.Background()

	t.Run("refused without the v2 flag, nothing changes", func(t *testing.T) {
		f := newAcceptV2Fixture(t, "active", false)
		before, _ := f.store.Queries().GetPluginContainerByInstance(ctx, f.instanceID)

		rec := f.accept()
		if rec.Code == http.StatusOK {
			t.Fatalf("status = 200, want a refusal; body: %s", rec.Body.String())
		}

		plugin, _ := f.store.Queries().GetPluginByID(ctx, "plugin-v2")
		if strings.Contains(plugin.ManifestSnapshot, acceptV2NewDigest) {
			t.Error("a v2 manifest was accepted by a build that does not enable v2")
		}
		after, _ := f.store.Queries().GetPluginContainerByInstance(ctx, f.instanceID)
		if after.ImageDigest != before.ImageDigest || after.Version != before.Version {
			t.Error("desired state changed without the flag")
		}
		if _, err := f.store.Queries().GetPluginPendingManifest(ctx, "plugin-v2"); err != nil {
			t.Errorf("pending manifest was consumed: %v", err)
		}
	})

	t.Run("accepted with the flag: snapshot and desired image move together", func(t *testing.T) {
		f := newAcceptV2Fixture(t, "active", true)
		before, _ := f.store.Queries().GetPluginContainerByInstance(ctx, f.instanceID)

		rec := f.accept()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		plugin, _ := f.store.Queries().GetPluginByID(ctx, "plugin-v2")
		if plugin.ManifestSnapshot != f.candidate {
			t.Error("manifest_snapshot is not the accepted candidate")
		}
		if plugin.PluginVersion != "1.1.0" || plugin.Status != "active" {
			t.Errorf("plugin = version %q status %q, want 1.1.0 active", plugin.PluginVersion, plugin.Status)
		}
		if _, err := f.store.Queries().GetPluginPendingManifest(ctx, "plugin-v2"); err == nil {
			t.Error("pending manifest survives acceptance")
		}

		row, err := f.store.Queries().GetPluginContainerByInstance(ctx, f.instanceID)
		if err != nil {
			t.Fatalf("GetPluginContainerByInstance: %v", err)
		}
		if row.ImageDigest != acceptV2NewDigest {
			t.Errorf("desired image digest = %q, want %q: the reconciler would never rotate", row.ImageDigest, acceptV2NewDigest)
		}
		if row.MemoryLimitBytes == nil || *row.MemoryLimitBytes != int64(128)<<20 {
			t.Errorf("desired memory = %v, want 128 MiB", row.MemoryLimitBytes)
		}
		if row.DesiredState != before.DesiredState || row.ConfigHash != before.ConfigHash || row.NetworkName != before.NetworkName {
			t.Error("resync changed fields other than image and limits")
		}

		inst, _ := f.store.Queries().GetPluginInstanceByID(ctx, f.instanceID)
		if inst.HealthState != string(model.PluginHealthStateHealthy) {
			t.Errorf("instance health = %q, want healthy", inst.HealthState)
		}
	})

	t.Run("the runtime image ID recorded for the candidate is what the new desired digest resolves to", func(t *testing.T) {
		f := newAcceptV2Fixture(t, "active", true)
		// The installer records this when it loads and verifies the candidate's
		// image; accepting loads nothing, so the record must simply survive and
		// be keyed by the digest the desired row moves to.
		localID := "sha256:runtime-local-id"
		if _, err := f.store.Queries().UpsertContainerImage(ctx, db.UpsertContainerImageParams{
			Digest: acceptV2NewDigest, Reference: "ghcr.io/acme/v2-tool@" + acceptV2NewDigest,
			LoadedAt: "2026-08-06T12:00:00Z", LocalImageID: &localID,
		}); err != nil {
			t.Fatalf("seed image record: %v", err)
		}

		if rec := f.accept(); rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
		}

		row, err := f.store.Queries().GetPluginContainerByInstance(ctx, f.instanceID)
		if err != nil {
			t.Fatalf("GetPluginContainerByInstance: %v", err)
		}
		img, err := f.store.Queries().GetContainerImage(ctx, row.ImageDigest)
		if err != nil {
			t.Fatalf("no image record for the desired digest %q: %v", row.ImageDigest, err)
		}
		if img.LocalImageID == nil || *img.LocalImageID != localID {
			t.Errorf("local_image_id = %v, want %q", img.LocalImageID, localID)
		}
	})

	t.Run("accepting does not approve a plugin awaiting review", func(t *testing.T) {
		f := newAcceptV2Fixture(t, "pending_review", true)

		if rec := f.accept(); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		plugin, _ := f.store.Queries().GetPluginByID(ctx, "plugin-v2")
		if plugin.Status != "pending_review" {
			t.Errorf("status = %q, want pending_review preserved", plugin.Status)
		}
	})

	t.Run("the hash is required and must match the pending candidate", func(t *testing.T) {
		tests := []struct {
			name string
			body string
			want int
		}{
			{name: "missing hash", body: `{}`, want: http.StatusBadRequest},
			{name: "empty body", body: ``, want: http.StatusBadRequest},
			{name: "stale hash", body: `{"expected_candidate_sha256":"` + sha256Hex("an older candidate") + `"}`, want: http.StatusConflict},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				f := newAcceptV2Fixture(t, "active", true)
				if rec := f.acceptWithBody(tt.body); rec.Code != tt.want {
					t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.want, rec.Body.String())
				}
				plugin, _ := f.store.Queries().GetPluginByID(ctx, "plugin-v2")
				if strings.Contains(plugin.ManifestSnapshot, acceptV2NewDigest) {
					t.Error("a candidate was accepted without a matching hash")
				}
				if _, err := f.store.Queries().GetPluginPendingManifest(ctx, "plugin-v2"); err != nil {
					t.Errorf("pending manifest was consumed: %v", err)
				}
			})
		}
	})

	t.Run("the accepted hash is audited", func(t *testing.T) {
		f := newAcceptV2Fixture(t, "active", true)
		if rec := f.accept(); rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
		}
		events, err := f.store.Queries().ListPluginAuditEventsByType(ctx, db.ListPluginAuditEventsByTypeParams{EventType: auditManifestAccepted, Limit: 1})
		if err != nil || len(events) != 1 {
			t.Fatalf("audit events = %v, %v; want one", events, err)
		}
		if !strings.Contains(events[0].PayloadJson, sha256Hex(f.candidate)) {
			t.Errorf("audit payload lacks the candidate hash: %s", events[0].PayloadJson)
		}
	})

	t.Run("a candidate of the other schema generation is refused", func(t *testing.T) {
		f := newAcceptV2Fixture(t, "active", true)
		if err := f.store.Queries().UpsertPluginPendingManifest(ctx, db.UpsertPluginPendingManifestParams{
			PluginID:          "plugin-v2",
			CandidateManifest: v1ManifestYAML,
			OldVersion:        "1.0.0",
			NewVersion:        "1.0.0",
			CreatedAt:         "2026-08-06T12:00:00Z",
			UpdatedAt:         "2026-08-06T12:00:00Z",
		}); err != nil {
			t.Fatalf("seed v1 candidate: %v", err)
		}

		if rec := f.accept(); rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
		}
		plugin, _ := f.store.Queries().GetPluginByID(ctx, "plugin-v2")
		if strings.Contains(plugin.ManifestSnapshot, "schema_version: v1") {
			t.Error("a v1 candidate replaced a v2 snapshot")
		}
	})
}

func TestPluginDetail_ExposesPendingV2Candidate(t *testing.T) {
	f := newAcceptV2Fixture(t, "active", true)

	detail := func() pluginDetailResponse {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/plugin-v2", nil)
		req = withChiParams(req, map[string]string{"id": "plugin-v2"})
		rec := httptest.NewRecorder()
		f.h.GetPluginDetail(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("detail status = %d; body: %s", rec.Code, rec.Body.String())
		}
		var resp pluginDetailResponse
		if err := json.Unmarshal(parseDataResponse(t, rec), &resp); err != nil {
			t.Fatalf("unmarshal detail: %v", err)
		}
		return resp
	}

	got := detail()
	if got.V2 == nil || got.V2.Image.Digest != acceptV2Digest {
		t.Errorf("v2 block = %+v, want the consented image", got.V2)
	}
	if got.PendingV2 == nil || got.PendingV2.Image.Digest != acceptV2NewDigest {
		t.Fatalf("pending_v2 = %+v, want the candidate's image", got.PendingV2)
	}
	if got.PendingV2.Resources == nil || got.PendingV2.Resources.MemoryMB != 128 {
		t.Errorf("pending_v2 resources = %+v, want the candidate's 128 MiB", got.PendingV2.Resources)
	}
	if got.PendingManifestSHA256 != sha256Hex(f.candidate) {
		t.Errorf("pending_manifest_sha256 = %q, want the candidate's hash", got.PendingManifestSHA256)
	}

	if err := f.store.Queries().DeletePluginPendingManifest(context.Background(), "plugin-v2"); err != nil {
		t.Fatalf("DeletePluginPendingManifest: %v", err)
	}
	if got := detail(); got.PendingV2 != nil || got.PendingManifestSHA256 != "" {
		t.Errorf("detail shows a pending block with nothing pending: %+v", got)
	}
}
