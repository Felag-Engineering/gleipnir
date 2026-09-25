package hostendpoint

import (
	"context"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

// envCapturingRuntime records the Env every Create call carried, so a test can
// recover a minted instance token the same way a real container would (by
// reading its own environment) rather than reaching into the reconciler's
// private in-memory stash.
type envCapturingRuntime struct {
	container.Runtime
	lastEnv []string
}

func (e *envCapturingRuntime) Create(ctx context.Context, opts container.CreateOptions) (container.ContainerID, error) {
	e.lastEnv = opts.Env
	return e.Runtime.Create(ctx, opts)
}

// instanceTokenFrom extracts GLEIPNIR_INSTANCE_TOKEN from a container's Env,
// mirroring how plugin-sdk/hostclient reads it in the real container.
func instanceTokenFrom(env []string) string {
	const prefix = "GLEIPNIR_INSTANCE_TOKEN="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

// TestFirstBoot_TokenAuthenticatesAgainstTheRealResolver drives a genuinely
// fresh instance -- no generation row, no container -- through the
// reconciler's real first-boot sequence (ActionBeginFirstGeneration, then
// ReconcileRotations' create/health-gate/switch) over a real SQLite store, and
// proves the token the container was handed is the one GenerationTokenResolver
// accepts. Minting a token and checking a token are two different pieces of
// code; a test that only exercises one of them would not catch the two
// disagreeing about the stored form.
func TestFirstBoot_TokenAuthenticatesAgainstTheRealResolver(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "firstboot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	q := store.Queries()

	const instanceID = "inst-1"
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
		HealthState: "unhealthy", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
	if _, err := q.CreatePluginContainer(ctx, db.CreatePluginContainerParams{
		ID: model.NewULID(), PluginInstanceID: instanceID,
		ImageRef: "ghcr.io/acme/p:1", ImageDigest: "sha256:aaa", ConfigHash: "cfg-1",
		NetworkName: "gleipnir-plugin-" + instanceID, DesiredState: reconciler.DesiredRunning,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginContainer: %v", err)
	}

	allocator, err := reconciler.NewSubnetAllocator(q, netip.MustParsePrefix("10.91.0.0/16"), func() string { return now })
	if err != nil {
		t.Fatalf("NewSubnetAllocator: %v", err)
	}
	rt := &envCapturingRuntime{Runtime: container.NewFake()}
	r, err := reconciler.New(reconciler.Config{
		Runtime:   rt,
		Store:     q,
		Rotations: q,
		Subnets:   allocator,
		Posture:   container.PostureRootlessPodman,
		Interval:  time.Hour,
	})
	if err != nil {
		t.Fatalf("reconciler.New: %v", err)
	}

	// Network, then generation-1 minted, then rotation's create/promote/switch
	// carries it the rest of the way to active -- exactly the sequence a later
	// rotation follows, because first boot is the special case of having no
	// prior generation to supersede.
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("network pass: %v", err)
	}
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("begin-first-generation pass: %v", err)
	}
	for i, step := range []string{"create", "promote", "switch"} {
		if _, err := r.ReconcileRotations(ctx); err != nil {
			t.Fatalf("rotation pass %d (%s): %v", i, step, err)
		}
	}

	gens, err := q.ListContainerGenerationsByInstance(ctx, instanceID)
	if err != nil {
		t.Fatalf("ListContainerGenerationsByInstance: %v", err)
	}
	if len(gens) != 1 {
		t.Fatalf("generations = %d, want exactly 1", len(gens))
	}
	if gens[0].Generation != 1 || gens[0].Status != reconciler.GenActive {
		t.Fatalf("generation = %d/%q, want 1/active", gens[0].Generation, gens[0].Status)
	}

	token := instanceTokenFrom(rt.lastEnv)
	if token == "" {
		t.Fatal("the container was created with no GLEIPNIR_INSTANCE_TOKEN")
	}

	resolver := GenerationTokenResolver{Querier: q}
	id, ok, err := resolver.ResolveToken(ctx, token)
	if err != nil || !ok {
		t.Fatalf("ResolveToken(firstBootToken) = (%+v, %v, %v), want ok", id, ok, err)
	}
	if id.InstanceID != instanceID || id.Generation != 1 {
		t.Errorf("identity = %+v, want {%s 1}", id, instanceID)
	}
}
