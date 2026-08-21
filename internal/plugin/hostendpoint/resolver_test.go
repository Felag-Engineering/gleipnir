package hostendpoint

import (
	"context"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// seedGeneration writes the plugin → instance → generation chain a token
// lookup resolves through, mirroring the reconciler's own seed shape.
func seedGeneration(t *testing.T, q *db.Queries, instanceID, token, status string) (genID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	pluginID := model.NewULID()
	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID: pluginID, Name: "p-" + instanceID, PluginVersion: "1.0.0",
		ManifestSnapshot: "{}", TrustedPubkey: "k", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: instanceID, PluginID: pluginID, InstanceName: "i-" + instanceID,
		ConfigJson: "{}", SubscriptionScopeJson: "{}", HandshakeVersions: "{}",
		HealthState: "healthy", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
	genID = model.NewULID()
	if _, err := q.CreateContainerGeneration(ctx, db.CreateContainerGenerationParams{
		ID: genID, PluginInstanceID: instanceID, Generation: 3,
		ImageDigest: "sha256:abc", ConfigHash: "cfg",
		TokenHash: reconciler.HashInstanceToken(token), Status: status,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateContainerGeneration: %v", err)
	}
	return genID
}

func TestGenerationTokenResolver(t *testing.T) {
	store := testutil.NewTestStore(t)
	q := store.Queries()
	resolver := GenerationTokenResolver{Querier: q}
	ctx := context.Background()

	t.Run("an active generation's token resolves to its instance and generation", func(t *testing.T) {
		seedGeneration(t, q, "inst-active", "tok-active", "active")
		id, ok, err := resolver.ResolveToken(ctx, "tok-active")
		if err != nil || !ok {
			t.Fatalf("ResolveToken = (%+v, %v, %v), want ok", id, ok, err)
		}
		if id.InstanceID != "inst-active" || id.Generation != 3 {
			t.Errorf("identity = %+v, want {inst-active 3}", id)
		}
	})

	t.Run("a draining generation still authenticates — tokens are revoked at retire, not at switch", func(t *testing.T) {
		// The rotation invariant (#813): revoking during drain would fail the
		// in-flight work draining exists to protect. A resolver that rejected
		// on status rather than revocation would re-introduce exactly that.
		seedGeneration(t, q, "inst-drain", "tok-drain", "draining")
		if _, ok, err := resolver.ResolveToken(ctx, "tok-drain"); err != nil || !ok {
			t.Fatalf("draining generation's token must authenticate; ok=%v err=%v", ok, err)
		}
	})

	t.Run("a retired generation's revoked token rejects", func(t *testing.T) {
		genID := seedGeneration(t, q, "inst-retired", "tok-retired", "stopped")
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if n, err := q.RevokeContainerGenerationToken(ctx, db.RevokeContainerGenerationTokenParams{
			ID: genID, TokenRevokedAt: &now, UpdatedAt: now,
		}); err != nil || n != 1 {
			t.Fatalf("RevokeContainerGenerationToken: n=%d err=%v", n, err)
		}
		if _, ok, err := resolver.ResolveToken(ctx, "tok-retired"); err != nil || ok {
			t.Fatalf("revoked token resolved; ok=%v err=%v — a retired generation can impersonate a live one", ok, err)
		}
	})

	t.Run("an unknown token rejects without error", func(t *testing.T) {
		if _, ok, err := resolver.ResolveToken(ctx, "never-minted"); err != nil || ok {
			t.Fatalf("unknown token: ok=%v err=%v, want (false, nil)", ok, err)
		}
	})
}
