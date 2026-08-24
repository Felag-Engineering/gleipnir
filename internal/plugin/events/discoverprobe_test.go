package events_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

var probeFrozen = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

// probeFixture stands up a real store with one plugin instance whose managed
// mcp_servers row points at fake, mirroring internal/mcp/managed_test.go's
// managedFixture -- real queries plus the real ManagedRegistrar rather than a
// double, so the resolution path this probe actually depends on
// (GetMCPServerByPluginInstance -> Registry.ClientForServerID) is exercised
// for real, not asserted by inspection.
func probeFixture(t *testing.T, fake *mcp.FakeEventsServer) (*db.Store, *mcp.Registry, string) {
	t.Helper()
	s := testutil.NewTestStore(t)

	if _, err := s.DB().Exec(
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES ('pl1', 'test-plugin', '1.0.0', '{}', 'pubkey', 'active', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin: %v", err)
	}
	const instanceID = "inst1"
	if _, err := s.DB().Exec(
		`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, subscription_scope_json, handshake_versions, health_state, version, created_at, updated_at)
		 VALUES (?, 'pl1', 'instance-inst1', '{}', '{}', '{}', 'healthy', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		instanceID,
	); err != nil {
		t.Fatalf("insert plugin instance: %v", err)
	}

	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	registrar, err := mcp.NewManagedRegistrar(s.Queries(), func() string { return "srv1" }, func() time.Time { return probeFrozen })
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	if _, err := registrar.Register(context.Background(), mcp.ManagedEndpoint{
		InstanceID:   instanceID,
		InstanceName: "instance-inst1",
		URL:          srv.URL,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	return s, mcp.NewRegistry(s.Queries()), instanceID
}

// A server that never declares io.gleipnir/events reports a zero-value
// result -- not an error, and not itself a fault (caphealth decides whether
// silence is a fault, based on what the manifest attested).
func TestDiscoverProbe_ExtensionNotDeclared(t *testing.T) {
	fake := mcp.NewFakeEventsServer()
	fake.DeclareExtension = false
	s, registry, instanceID := probeFixture(t, fake)
	probe, err := events.NewDiscoverProbe(s.Queries(), registry)
	if err != nil {
		t.Fatalf("NewDiscoverProbe: %v", err)
	}

	got, err := probe.Discover(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got.ExtensionDeclared || got.VersionRefused || got.DeclaredVersion != "" || got.EventKinds != nil {
		t.Errorf("Discover() = %+v, want the zero value", got)
	}
}

// A server declaring the extension at a major version this host cannot read
// is refused -- events/discover is never even attempted -- and the result
// names the version that was declared.
func TestDiscoverProbe_VersionRefused(t *testing.T) {
	fake := mcp.NewFakeEventsServer()
	fake.RawCapability = json.RawMessage(`{"version":"2.0.0"}`)
	fake.Kinds = []mcp.EventKind{{Kind: "message"}}
	s, registry, instanceID := probeFixture(t, fake)
	probe, err := events.NewDiscoverProbe(s.Queries(), registry)
	if err != nil {
		t.Fatalf("NewDiscoverProbe: %v", err)
	}

	got, err := probe.Discover(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !got.ExtensionDeclared {
		t.Error("ExtensionDeclared = false, want true (the server DID declare it, just at a version this host can't read)")
	}
	if !got.VersionRefused {
		t.Fatal("VersionRefused = false, want true for an unsupported major version")
	}
	if got.DeclaredVersion != "2.0.0" {
		t.Errorf("DeclaredVersion = %q, want %q", got.DeclaredVersion, "2.0.0")
	}
	if got.EventKinds != nil {
		t.Errorf("EventKinds = %v, want nil: events/discover must not be attempted against a refused version", got.EventKinds)
	}
	if fake.DiscoverCalls != 0 {
		t.Errorf("events/discover was called %d times, want 0", fake.DiscoverCalls)
	}
}

// A version the wire didn't even carry is unreadable, same as an
// unsupported one -- refused, not guessed at.
func TestDiscoverProbe_VersionMissingIsRefused(t *testing.T) {
	fake := mcp.NewFakeEventsServer()
	fake.RawCapability = json.RawMessage(`{}`)
	s, registry, instanceID := probeFixture(t, fake)
	probe, err := events.NewDiscoverProbe(s.Queries(), registry)
	if err != nil {
		t.Fatalf("NewDiscoverProbe: %v", err)
	}

	got, err := probe.Discover(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !got.VersionRefused {
		t.Fatal("VersionRefused = false, want true for a missing version")
	}
	if got.DeclaredVersion != "" {
		t.Errorf("DeclaredVersion = %q, want empty", got.DeclaredVersion)
	}
}

// A server declaring a readable version has its kinds listed via
// events/discover, in server order.
func TestDiscoverProbe_ExtensionDeclaredListsKinds(t *testing.T) {
	fake := mcp.NewFakeEventsServer()
	fake.Kinds = []mcp.EventKind{{Kind: "message"}, {Kind: "reaction"}}
	s, registry, instanceID := probeFixture(t, fake)
	probe, err := events.NewDiscoverProbe(s.Queries(), registry)
	if err != nil {
		t.Fatalf("NewDiscoverProbe: %v", err)
	}

	got, err := probe.Discover(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !got.ExtensionDeclared || got.VersionRefused {
		t.Fatalf("Discover() = %+v, want a declared, readable extension", got)
	}
	want := []string{"message", "reaction"}
	if len(got.EventKinds) != len(want) {
		t.Fatalf("EventKinds = %v, want %v", got.EventKinds, want)
	}
	for i := range want {
		if got.EventKinds[i] != want[i] {
			t.Errorf("EventKinds[%d] = %q, want %q", i, got.EventKinds[i], want[i])
		}
	}
	if fake.DiscoverCalls != 1 {
		t.Errorf("events/discover was called %d times, want 1", fake.DiscoverCalls)
	}
}

// A plugin instance with no managed endpoint is a resolution failure, not a
// silent "no kinds" -- caphealth's prober treats a Discover error as a
// liveness fault, and that must not be confused with "the extension was
// simply not declared".
func TestDiscoverProbe_UnknownInstanceIsAnError(t *testing.T) {
	fake := mcp.NewFakeEventsServer()
	s, registry, _ := probeFixture(t, fake)
	probe, err := events.NewDiscoverProbe(s.Queries(), registry)
	if err != nil {
		t.Fatalf("NewDiscoverProbe: %v", err)
	}

	if _, err := probe.Discover(context.Background(), "no-such-instance"); err == nil {
		t.Fatal("Discover succeeded for an instance with no managed endpoint")
	}
}

func TestNewDiscoverProbe_RequiresDependencies(t *testing.T) {
	s := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(s.Queries())

	if _, err := events.NewDiscoverProbe(nil, registry); err == nil {
		t.Error("NewDiscoverProbe succeeded without a server lookup")
	}
	if _, err := events.NewDiscoverProbe(s.Queries(), nil); err == nil {
		t.Error("NewDiscoverProbe succeeded without a client resolver")
	}
}
