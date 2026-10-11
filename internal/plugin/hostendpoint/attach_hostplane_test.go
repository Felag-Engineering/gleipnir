package hostendpoint

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

const attachTestManifest = `schema_version: "2"
name: test-plugin
version: "1.0.0"
package:
  registry_type: oci
  identifier: ghcr.io/acme/p@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
`

// Attaching makes a plugin's tools resolvable through the shared namespace;
// the host-plane invariant (no grantable host/... tool) must survive that.
func TestAssertHostPlane_AfterAttach(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "attach.db"))
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
		ID: pluginID, Name: "p", PluginVersion: "1.0.0",
		ManifestSnapshot: attachTestManifest, TrustedPubkey: "k", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: instanceID, PluginID: pluginID, InstanceName: "acme",
		ConfigJson: "{}", SubscriptionScopeJson: "{}", HandshakeVersions: "{}",
		HealthState: "healthy", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}

	fake := container.NewFake()
	containerID, err := fake.Create(ctx, container.CreateOptions{
		Name: "acme-1", Image: "ghcr.io/acme/p", Network: "gleipnir-plugin-inst-1",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Sysctls: container.RequiredSysctls(),
	})
	if err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	fake.SetIPAddress(containerID, "10.83.4.2")
	cid := string(containerID)
	if _, err := q.CreateContainerGeneration(ctx, db.CreateContainerGenerationParams{
		ID: model.NewULID(), PluginInstanceID: instanceID, Generation: 1, ContainerID: &cid,
		ImageDigest: "sha256:aaa", ConfigHash: "cfg", TokenHash: reconciler.HashInstanceToken("t"),
		Status: reconciler.GenActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateContainerGeneration: %v", err)
	}

	registrar, err := mcp.NewManagedRegistrar(q, model.NewULID, time.Now)
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	r, err := reconciler.New(reconciler.Config{
		Runtime:   fake,
		Store:     q,
		Rotations: q,
		Posture:   container.PostureRootlessPodman,
		Interval:  time.Hour,
		Attach:    &reconciler.AttachDeps{Store: q, Endpoints: registrar},
	})
	if err != nil {
		t.Fatalf("reconciler.New: %v", err)
	}
	if _, err := r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}

	id := instanceID
	row, err := q.GetMCPServerByPluginInstance(ctx, &id)
	if err != nil {
		t.Fatalf("no managed row after AttachPass: %v", err)
	}

	reg := toolregistry.New()
	if err := reg.Reserve(row.Name+".send_message", toolregistry.Source{Kind: toolregistry.KindMCP, Name: row.Name}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := AssertHostPlane(reg); err != nil {
		t.Errorf("AssertHostPlane after attach = %v, want nil", err)
	}
}
