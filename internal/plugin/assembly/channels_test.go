//go:build substratev2

package assembly

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// seedInstance inserts an active plugin and one instance of it.
func seedInstance(t *testing.T, q *db.Queries, instanceID, manifest string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := q.CreatePlugin(context.Background(), db.CreatePluginParams{
		ID: "plug-" + instanceID, Name: "plug-" + instanceID, PluginVersion: "1.0.0", ManifestSnapshot: manifest,
		TrustedPubkey: "k", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := q.CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID: instanceID, PluginID: "plug-" + instanceID, InstanceName: instanceID, ConfigJson: "{}",
		SubscriptionScopeJson: "{}", HandshakeVersions: "{}", HealthState: "healthy",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
}

// The gate (hitl.Router asking a client for its channel capability) and the
// poll (PollScheduler resolving through ClientForServerID) must reason about
// the same *mcp.Client for an instance, including after a rotation repoints
// the managed row. Resolving the gate's client any other way would let the two
// diverge.
func TestChannelClientResolvers_ShareTheRegistryLookup(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewTestStore(t)
	q := store.Queries()
	registry := mcp.NewRegistry(q)
	seedInstance(t, q, "inst-1", v2ToolManifest)
	registrar, err := mcp.NewManagedRegistrar(q, model.NewULID, nil)
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	srv, err := registrar.Register(ctx, mcp.ManagedEndpoint{InstanceID: "inst-1", InstanceName: "inst-1", URL: "http://10.83.0.2:9000"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	clients := managedClients{ctx: ctx, servers: q, clients: registry}

	assertSameAsPoller := func(t *testing.T, label string) *mcp.Client {
		t.Helper()
		polled, err := registry.ClientForServerID(ctx, srv.ID)
		if err != nil {
			t.Fatalf("%s: ClientForServerID: %v", label, err)
		}
		request, err := requestClients{clients}.ChannelClientFor("inst-1")
		if err != nil {
			t.Fatalf("%s: request ChannelClientFor: %v", label, err)
		}
		notify, err := notifyClients{clients}.ChannelClientFor("inst-1")
		if err != nil {
			t.Fatalf("%s: notify ChannelClientFor: %v", label, err)
		}
		if request != any(polled) || notify != any(polled) {
			t.Fatalf("%s: gate resolved a different client than the poller does", label)
		}
		return polled
	}

	before := assertSameAsPoller(t, "before rotation")

	if _, err := registrar.Register(ctx, mcp.ManagedEndpoint{InstanceID: "inst-1", InstanceName: "inst-1", URL: "http://10.83.0.2:9001"}); err != nil {
		t.Fatalf("repoint: %v", err)
	}
	after := assertSameAsPoller(t, "after rotation")
	if after == before {
		t.Error("rotation did not produce a new client; the gate would still talk to the old generation")
	}
}

func TestChannelClientResolvers_UnresolvableInstanceIsAnErrorNotATypedNil(t *testing.T) {
	store := testutil.NewTestStore(t)
	clients := managedClients{ctx: context.Background(), servers: store.Queries(), clients: mcp.NewRegistry(store.Queries())}

	request, err := requestClients{clients}.ChannelClientFor("no-such-instance")
	if err == nil || request != nil {
		t.Errorf("request resolver = (%v, %v), want (nil, error): the router skips an entry only on a real error", request, err)
	}
	notify, err := notifyClients{clients}.ChannelClientFor("no-such-instance")
	if err == nil || notify != nil {
		t.Errorf("notify resolver = (%v, %v), want (nil, error)", notify, err)
	}
}

func TestTruncateHost(t *testing.T) {
	short := "api.example.com"
	if got := truncateHost(short); got != short {
		t.Errorf("truncateHost(%q) = %q, want unchanged", short, got)
	}
	long := strings.Repeat("a", maxAuditedHostLen+50)
	got := truncateHost(long)
	if len(got) != maxAuditedHostLen+len("...") || !strings.HasSuffix(got, "...") {
		t.Errorf("truncateHost(long) length = %d, want %d ending in an ellipsis", len(got), maxAuditedHostLen+3)
	}
}

func TestEgressAuditor_EgressDenied(t *testing.T) {
	store := testutil.NewTestStore(t)
	q := store.Queries()
	seedInstance(t, q, "inst-1", v2ToolManifest)
	auditor := egressAuditor{store: q}
	ctx := context.Background()

	auditor.EgressDenied(ctx, "inst-1", "evil.example", egress.DenyNotGranted)
	auditor.EgressDenied(ctx, "", "unknown.example", egress.DenyUnknownInstance)

	rows, err := q.ListPluginAuditEventsByType(ctx, db.ListPluginAuditEventsByTypeParams{EventType: egressAuditEventType, Limit: 10})
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByType: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(rows))
	}

	byHost := map[string]db.PluginAuditEvent{}
	for _, row := range rows {
		var payload map[string]string
		if err := json.Unmarshal([]byte(row.PayloadJson), &payload); err != nil {
			t.Fatalf("payload %q: %v", row.PayloadJson, err)
		}
		if row.Severity != "warning" {
			t.Errorf("severity = %q, want warning", row.Severity)
		}
		byHost[payload["host"]] = row
		if want := map[string]string{"evil.example": "not_granted", "unknown.example": "unknown_instance"}[payload["host"]]; payload["reason"] != want {
			t.Errorf("host %q reason = %q, want %q", payload["host"], payload["reason"], want)
		}
	}
	if row := byHost["evil.example"]; row.PluginInstanceID == nil || *row.PluginInstanceID != "inst-1" {
		t.Errorf("attributable denial instance = %v, want inst-1", row.PluginInstanceID)
	}
	if row := byHost["unknown.example"]; row.PluginInstanceID != nil {
		t.Errorf("unattributable denial instance = %v, want NULL", *row.PluginInstanceID)
	}
}
