package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// --- fixtures ---------------------------------------------------------------

var managedFrozen = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

// managedFixture stands up a real store with one plugin and n instances, plus a
// registrar over it. Real queries rather than a double: the per-instance
// uniqueness and the CASCADE are properties of the schema, and a double would
// assert the test author's reading of them.
func managedFixture(t *testing.T, instances int) (*db.Store, *ManagedRegistrar, []string) {
	t.Helper()
	s := testutil.NewTestStore(t)

	if _, err := s.DB().Exec(
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES ('pl1', 'slack', '1.0.0', '{}', 'pubkey', 'active', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin: %v", err)
	}

	ids := make([]string, instances)
	for i := range ids {
		ids[i] = fmt.Sprintf("inst%d", i)
		if _, err := s.DB().Exec(
			`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, subscription_scope_json, handshake_versions, health_state, version, created_at, updated_at)
			 VALUES (?, 'pl1', ?, '{}', '{}', '{}', 'healthy', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
			ids[i], "instance-"+ids[i],
		); err != nil {
			t.Fatalf("insert plugin instance: %v", err)
		}
	}

	var seq int
	reg, err := NewManagedRegistrar(s.Queries(),
		func() string { seq++; return fmt.Sprintf("srv-%d", seq) },
		func() time.Time { return managedFrozen },
	)
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	return s, reg, ids
}

func endpointFor(instanceID, url string) ManagedEndpoint {
	return ManagedEndpoint{InstanceID: instanceID, InstanceName: "instance-" + instanceID, URL: url}
}

// --- trust tier -------------------------------------------------------------

// The tier is derived from one column, so there is no state in which a row
// claims one thing and points at another.
func TestTrustTierOf(t *testing.T) {
	instance := "inst0"
	empty := ""

	tests := []struct {
		name string
		row  db.McpServer
		want TrustTier
	}{
		{name: "no instance is external", row: db.McpServer{}, want: TrustTierExternal},
		{name: "an instance is managed", row: db.McpServer{PluginInstanceID: &instance}, want: TrustTierManaged},
		{
			name: "an empty-string instance is external, not a managed row with no instance",
			row:  db.McpServer{PluginInstanceID: &empty},
			want: TrustTierExternal,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrustTierOf(tc.row); got != tc.want {
				t.Errorf("TrustTierOf = %q, want %q", got, tc.want)
			}
			if got := IsManaged(tc.row); got != (tc.want == TrustTierManaged) {
				t.Errorf("IsManaged = %v, inconsistent with tier %q", got, tc.want)
			}
		})
	}
}

// A client built without an opinion reports the safe answer, because the
// permissive direction here means honouring a host-plane extension declared by
// a server nobody vouched for.
func TestClient_TrustTierDefaultsToExternal(t *testing.T) {
	if got := NewClient("http://x").TrustTier(); got != TrustTierExternal {
		t.Errorf("default trust tier = %q, want external", got)
	}
	if NewClient("http://x", WithTrustTier(TrustTierManaged)).negotiatesGleipnirExtensions() != true {
		t.Error("a managed client does not negotiate io.gleipnir extensions")
	}
	if NewClient("http://x").negotiatesGleipnirExtensions() {
		t.Error("an external client negotiates io.gleipnir extensions")
	}
}

// --- register / deregister --------------------------------------------------

func TestManagedRegistrar_RegisterCreatesAModernPinnedEntry(t *testing.T) {
	s, reg, ids := managedFixture(t, 1)
	ctx := context.Background()

	srv, err := reg.Register(ctx, endpointFor(ids[0], "http://10.83.0.2:8080/mcp"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if TrustTierOf(srv) != TrustTierManaged {
		t.Error("the created entry is not managed")
	}
	if srv.Name != "instance-inst0" {
		t.Errorf("name = %q; the server name is the tool namespace prefix and must be the instance's stable name", srv.Name)
	}
	if srv.ProtocolVersion == nil || *srv.ProtocolVersion != ProtocolVersion20260728 {
		t.Errorf("protocol_version = %v, want the modern pin", srv.ProtocolVersion)
	}

	// It is an ordinary row: everything downstream — discovery, the tool
	// namespace, canonical-schema persistence — reads it the same way it reads
	// an operator-registered server. That is the whole claim of one client stack.
	all, err := s.Queries().ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	if len(all) != 1 || all[0].ID != srv.ID {
		t.Errorf("the managed entry is not in the ordinary server list: %+v", all)
	}
}

// Register is idempotent because the caller is level-triggered: "already
// registered" is the common outcome of a pass, not an error.
func TestManagedRegistrar_RegisterIsIdempotent(t *testing.T) {
	_, reg, ids := managedFixture(t, 1)
	ctx := context.Background()
	ep := endpointFor(ids[0], "http://10.83.0.2:8080/mcp")

	first, err := reg.Register(ctx, ep)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	second, err := reg.Register(ctx, ep)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("a repeated registration minted a new row: %s then %s", first.ID, second.ID)
	}
}

// The routing flip. One row per INSTANCE, not per generation: a new generation
// repoints the URL in place, so the tool namespace prefix — and every policy
// grant that names it — survives the upgrade untouched.
func TestManagedRegistrar_RotationRepointsInPlace(t *testing.T) {
	s, reg, ids := managedFixture(t, 1)
	ctx := context.Background()

	genOne, err := reg.Register(ctx, endpointFor(ids[0], "http://10.83.0.2:8080/mcp"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	genTwo, err := reg.Register(ctx, endpointFor(ids[0], "http://10.83.0.3:8080/mcp"))
	if err != nil {
		t.Fatalf("Register after rotation: %v", err)
	}

	if genTwo.ID != genOne.ID {
		t.Error("the rotation created a second registry entry; the row is per instance")
	}
	if genTwo.Name != genOne.Name {
		t.Errorf("the namespace prefix changed across a rotation (%q → %q); every policy grant would break",
			genOne.Name, genTwo.Name)
	}
	if genTwo.Url != "http://10.83.0.3:8080/mcp" {
		t.Errorf("url = %q, want the new generation's address", genTwo.Url)
	}

	rows, err := s.Queries().ListManagedMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListManagedMCPServers: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("got %d managed rows after a rotation, want 1", len(rows))
	}
}

// The URL is part of the registry cache's invalidation key, so the repoint IS
// the flip: the next resolve rebuilds against the new address, while a *Client
// already resolved into a running run keeps the base URL it was built with and
// drains against the generation it started on.
func TestManagedRotation_FlipsNewResolvesAndDrainsOldClients(t *testing.T) {
	_, reg, ids := managedFixture(t, 1)
	ctx := context.Background()

	before, err := reg.Register(ctx, endpointFor(ids[0], "http://10.83.0.2:8080/mcp"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	inFlight := (&Registry{}).newClientForServer(before)

	after, err := reg.Register(ctx, endpointFor(ids[0], "http://10.83.0.3:8080/mcp"))
	if err != nil {
		t.Fatalf("Register after rotation: %v", err)
	}
	fresh := (&Registry{}).newClientForServer(after)

	if inFlight.serverURL == fresh.serverURL {
		t.Fatal("both clients point at the same address; nothing flipped")
	}
	if inFlight.serverURL != "http://10.83.0.2:8080/mcp" {
		t.Errorf("the in-flight client was repointed to %q; in-flight work must drain against its own generation", inFlight.serverURL)
	}
	if serverConfigOf(before) == serverConfigOf(after) {
		t.Error("the rotation did not change the cache key; a cached client would keep serving the retired generation")
	}
}

func TestManagedRegistrar_DeregisterIsIdempotentAndFreesTheNamespace(t *testing.T) {
	s, reg, ids := managedFixture(t, 1)
	ctx := context.Background()

	srv, err := reg.Register(ctx, endpointFor(ids[0], "http://10.83.0.2:8080/mcp"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := s.Queries().UpsertMCPTool(ctx, db.UpsertMCPToolParams{
		ID: "tool-1", ServerID: srv.ID, Name: "post_message",
		Description: "d", InputSchema: "{}", CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertMCPTool: %v", err)
	}

	if err := reg.Deregister(ctx, ids[0]); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	// A stop pass runs more than once; the goal state holds either way.
	if err := reg.Deregister(ctx, ids[0]); err != nil {
		t.Fatalf("second Deregister: %v", err)
	}

	if _, err := s.Queries().GetMCPServerByPluginInstance(ctx, &ids[0]); err == nil {
		t.Error("the entry survived deregistration")
	}
	// The cascade is what releases the names: reservations are rebuilt from the
	// DB, so the namespace frees itself rather than needing a second
	// bookkeeping call that a future path could forget.
	tools, err := s.Queries().ListMCPToolsByServer(ctx, srv.ID)
	if err != nil {
		t.Fatalf("ListMCPToolsByServer: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("%d tools survived the endpoint; their dot-names are still reserved", len(tools))
	}
}

// Deleting the instance removes the route. CASCADE rather than SET NULL: a row
// pointing at a deleted instance is a dangling endpoint the agent could still
// resolve a tool through, not history worth keeping.
func TestManagedEntry_CascadesWithItsInstance(t *testing.T) {
	s, reg, ids := managedFixture(t, 1)
	ctx := context.Background()

	if _, err := reg.Register(ctx, endpointFor(ids[0], "http://10.83.0.2:8080/mcp")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := s.DB().Exec(`DELETE FROM plugin_instances WHERE id = ?`, ids[0]); err != nil {
		t.Fatalf("delete instance: %v", err)
	}

	rows, err := s.Queries().ListManagedMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListManagedMCPServers: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d managed entries outlived their instance", len(rows))
	}
}

func TestManagedRegistrar_RejectsIncompleteEndpoints(t *testing.T) {
	_, reg, ids := managedFixture(t, 1)
	ctx := context.Background()

	tests := []struct {
		name string
		ep   ManagedEndpoint
	}{
		{name: "no instance", ep: ManagedEndpoint{InstanceName: "n", URL: "http://x"}},
		{name: "no name", ep: ManagedEndpoint{InstanceID: ids[0], URL: "http://x"}},
		{name: "no url", ep: ManagedEndpoint{InstanceID: ids[0], InstanceName: "n"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reg.Register(ctx, tc.ep); err == nil {
				t.Error("Register accepted an incomplete endpoint")
			}
		})
	}
	if err := reg.Deregister(ctx, ""); err == nil {
		t.Error("Deregister accepted an empty instance ID")
	}
}

// --- namespace (issue #194 invariant) ---------------------------------------

// A managed endpoint's tools live in the SAME `<source>.<tool>` namespace as an
// external server's, so a collision between the two is caught by the same
// arbiter. Moving plugins onto this transport must not open a second namespace
// where a plugin could shadow an MCP server's tool.
func TestManagedAndExternalToolsShareOneNamespace(t *testing.T) {
	arbiter := toolregistry.New()
	external := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "github"}
	managed := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "instance-inst0"}

	if err := arbiter.Reserve("shared.post_message", external); err != nil {
		t.Fatalf("reserving the external tool: %v", err)
	}

	err := arbiter.Reserve("shared.post_message", managed)
	if err == nil {
		t.Fatal("a managed plugin took a dot-name an external server already owns")
	}
	var conflict *toolregistry.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want a *toolregistry.ConflictError naming the existing owner", err)
	}
	if conflict.Existing != external {
		t.Errorf("Existing = %+v, want the external server", conflict.Existing)
	}

	// And the reverse, since a namespace enforced in only one direction is a
	// namespace an ordering change can defeat.
	if err := arbiter.Reserve("plugin-only.send", managed); err != nil {
		t.Fatalf("reserving the plugin tool: %v", err)
	}
	if err := arbiter.Reserve("plugin-only.send", external); err == nil {
		t.Error("an external server took a dot-name a managed plugin already owns")
	}
}

// --- concurrency + queue ----------------------------------------------------

func TestServerLimits_ResolveAndUnbounded(t *testing.T) {
	got := ServerLimits{}.resolve()
	if got.MaxConcurrent != DefaultMaxConcurrentCalls || got.MaxQueueDepth != DefaultMaxQueueDepth {
		t.Errorf("zero limits resolved to %+v, want the dispatch-pool defaults", got)
	}
	if !(ServerLimits{MaxConcurrent: -1, MaxQueueDepth: -1}).unbounded() {
		t.Error("negative limits are not treated as unbounded")
	}
	if newServerGate(ServerLimits{MaxConcurrent: -1, MaxQueueDepth: -1}) != nil {
		t.Error("an unbounded limit built a gate")
	}
	// A Client built directly is unbounded — every probe and test path keeps
	// behaving as it did before the gate existed.
	if NewClient("http://x").callGate != nil {
		t.Error("a directly-constructed client has a gate")
	}
}

// The semaphore bounds work in flight; the queue bounds callers waiting. Past
// the depth the answer is immediate, because the whole point is to not add one
// more blocked goroutine.
func TestServerGate_QueueDepthRejectsWithoutWaiting(t *testing.T) {
	gate := newServerGate(ServerLimits{MaxConcurrent: 1, MaxQueueDepth: 2})
	ctx := context.Background()

	// One holder (in the semaphore) plus one waiter fills the depth of 2.
	releaseHeld, err := gate.acquire(ctx)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	waiting := make(chan error, 1)
	go func() {
		release, err := gate.acquire(ctx)
		waiting <- err
		if err == nil {
			release()
		}
	}()

	// Synchronize on the queue actually being full rather than sleeping: the
	// third caller is rejected only once the second has claimed its slot.
	//
	// The probe uses an already-cancelled context so it can never block. The
	// queue check runs before the semaphore wait, so a full queue still answers
	// ErrQueueFull; a probe admitted instead reports its own cancellation and
	// gives its slot straight back. Telling those two apart is exactly what the
	// gate promises, so the synchronization is itself the assertion.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, probeErr := gate.acquire(cancelled)
		if errors.Is(probeErr, ErrQueueFull) {
			break
		}
		if !errors.Is(probeErr, context.Canceled) {
			t.Fatalf("probe error = %v, want either a full queue or its own cancellation", probeErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("the queue never filled; the third caller was admitted")
		}
	}

	releaseHeld()
	if err := <-waiting; err != nil {
		t.Errorf("the queued caller failed once a slot freed: %v", err)
	}
}

// A caller's own deadline expiring while it waits is a different answer from
// the queue being full, and an operator reading one must not be told the other.
func TestServerGate_ContextDeadlineIsDistinctFromQueueFull(t *testing.T) {
	gate := newServerGate(ServerLimits{MaxConcurrent: 1, MaxQueueDepth: 10})

	release, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = gate.acquire(ctx)
	if err == nil {
		t.Fatal("acquire succeeded past the concurrency cap")
	}
	if errors.Is(err, ErrQueueFull) {
		t.Error("a cancelled caller was reported as a full queue")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the caller's own cancellation", err)
	}
}

// Releasing must free BOTH the semaphore slot and the queue slot. Freeing only
// one would leak the other and the gate would close permanently after
// MaxQueueDepth calls — a failure that looks like the server going silent.
func TestServerGate_ReleaseFreesBothSlots(t *testing.T) {
	gate := newServerGate(ServerLimits{MaxConcurrent: 2, MaxQueueDepth: 2})
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		release, err := gate.acquire(ctx)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		release()
	}
}

func TestServerGate_ConcurrentAcquireNeverExceedsTheCap(t *testing.T) {
	const maxConcurrent = 3
	gate := newServerGate(ServerLimits{MaxConcurrent: maxConcurrent, MaxQueueDepth: 100})
	ctx := context.Background()

	var mu sync.Mutex
	live, peak := 0, 0

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := gate.acquire(ctx)
			if err != nil {
				return
			}
			defer release()

			mu.Lock()
			live++
			if live > peak {
				peak = live
			}
			mu.Unlock()

			mu.Lock()
			live--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if peak > maxConcurrent {
		t.Errorf("peak concurrency was %d, over the cap of %d", peak, maxConcurrent)
	}
}

// The gate is claimed before anything else the call does, so a rejection costs
// nothing — which matters most at exactly the moment it fires.
func TestCallTool_QueueFullIsReportedWithoutReachingTheServer(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", WithServerLimits(ServerLimits{MaxConcurrent: 1, MaxQueueDepth: 1}))

	release, err := client.callGate.acquire(context.Background())
	if err != nil {
		t.Fatalf("seeding the gate: %v", err)
	}
	defer release()

	_, err = client.CallTool(context.Background(), "any", nil, CallOptions{})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("CallTool error = %v, want ErrQueueFull", err)
	}
	if !strings.Contains(err.Error(), "any") {
		t.Errorf("the rejection does not name the tool: %v", err)
	}
}

// --- registry wiring --------------------------------------------------------

func TestRegistry_BuildsManagedClientsWithTheirTierAndLimits(t *testing.T) {
	instance := "inst0"
	r := NewRegistry(nil, WithServerCallLimits(
		ServerLimits{MaxConcurrent: 4, MaxQueueDepth: 4},
		map[string]ServerLimits{"noisy": {MaxConcurrent: 1, MaxQueueDepth: 1}},
	))

	managed := r.newClientForServer(db.McpServer{ID: "s1", Name: "slack", Url: "http://x", PluginInstanceID: &instance})
	if managed.TrustTier() != TrustTierManaged {
		t.Error("a managed row produced an external client")
	}
	if !managed.negotiatesGleipnirExtensions() {
		t.Error("a managed client does not negotiate io.gleipnir extensions")
	}

	external := r.newClientForServer(db.McpServer{ID: "s2", Name: "github", Url: "http://y"})
	if external.TrustTier() != TrustTierExternal {
		t.Error("an external row produced a managed client")
	}
	if external.negotiatesGleipnirExtensions() {
		t.Error("an external client negotiates io.gleipnir extensions")
	}

	if cap(external.callGate.sem) != 4 {
		t.Errorf("default concurrency = %d, want 4", cap(external.callGate.sem))
	}
	override := r.newClientForServer(db.McpServer{ID: "s3", Name: "noisy", Url: "http://z"})
	if cap(override.callGate.sem) != 1 {
		t.Errorf("per-server concurrency = %d, want the override of 1", cap(override.callGate.sem))
	}
}

// --- extension gating -------------------------------------------------------

// io.gleipnir/* is host-plane. An external server declaring it and being
// believed would make a URL an operator pasted in eligible to be asked to
// settle a human approval — something no operator designated it for. The
// declaration is dropped at the client, because "external extension opt-in is
// deferred" has to mean the path does not exist yet.
func TestExtensionNegotiation_IsReservedToManagedEndpoints(t *testing.T) {
	tests := []struct {
		name         string
		tier         TrustTier
		wantDeclared bool
	}{
		{name: "a managed endpoint's declaration is honoured", tier: TrustTierManaged, wantDeclared: true},
		{name: "an external server's declaration is dropped", tier: TrustTierExternal},
		{name: "an unset tier is treated as external", tier: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := NewFakeChannelServer()
			stub.Assurance = ChannelAssuranceAuthenticated
			srv := httptest.NewServer(stub)
			t.Cleanup(srv.Close)

			client := NewClient(srv.URL,
				WithProtocolVersion(ProtocolVersion20260728),
				WithTrustTier(tc.tier),
			)
			probe, err := client.ProbeProtocolVersion(context.Background())
			if err != nil {
				t.Fatalf("ProbeProtocolVersion: %v", err)
			}

			if probe.ChannelDeclared != tc.wantDeclared {
				t.Errorf("probe ChannelDeclared = %v, want %v", probe.ChannelDeclared, tc.wantDeclared)
			}
			_, declared := client.ChannelCapabilityOf()
			if declared != tc.wantDeclared {
				t.Errorf("client ChannelCapabilityOf declared = %v, want %v", declared, tc.wantDeclared)
			}

			// The era classification is untouched either way: dropping an
			// optional extension must never demote a definitively modern server
			// to the legacy transport.
			if probe.Era != EraModern {
				t.Errorf("era = %v, want modern regardless of tier", probe.Era)
			}
		})
	}
}
