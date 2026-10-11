package adminv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle/adminv2"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// Compile-time proof that the v2 adapters satisfy the admin interfaces and
// that the real collaborators satisfy the adapters' narrow inputs.
var (
	_ admin.PluginProcessManager = (*adminv2.ProcessManager)(nil)
	_ admin.TriggerRestarter     = (*adminv2.TriggerRestarter)(nil)
	_ admin.InflightCounter      = (*adminv2.InflightCounter)(nil)
	_ admin.ToolConnEvictor      = adminv2.ToolConnEvictor{}
	_ admin.RSSAggregator        = (*adminv2.RSSAggregator)(nil)
	_ admin.InstanceProvisioner  = (*lifecycle.DesiredState)(nil)

	_ adminv2.EventSupervisor      = (*events.Supervisor)(nil)
	_ adminv2.EndpointDeregistrar  = (*mcp.ManagedRegistrar)(nil)
	_ adminv2.InFlightSource       = (*mcp.Registry)(nil)
	_ adminv2.SampleSource         = (*resources.Collector)(nil)
	_ adminv2.InstanceByNameReader = (*db.Queries)(nil)
	_ adminv2.ManagedServerReader  = (*db.Queries)(nil)
)

// ─── fakes ───────────────────────────────────────────────────────────────────

type fakeKicker struct {
	mu sync.Mutex
	n  int
}

func (k *fakeKicker) Kick() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.n++
}

func (k *fakeKicker) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.n
}

type fakeRegistrar struct {
	mu          sync.Mutex
	deregisters []string
	err         error
}

func (r *fakeRegistrar) Deregister(_ context.Context, instanceID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deregisters = append(r.deregisters, instanceID)
	return r.err
}

type fakeSupervisor struct {
	mu    sync.Mutex
	calls []string
}

func (s *fakeSupervisor) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *fakeSupervisor) Start(_ context.Context, id string)   { s.record("start:" + id) }
func (s *fakeSupervisor) Restart(_ context.Context, id string) { s.record("restart:" + id) }
func (s *fakeSupervisor) Stop(id string)                       { s.record("stop:" + id) }

func (s *fakeSupervisor) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

type fakeInFlight map[string]int

func (f fakeInFlight) InFlight(serverID string) int { return f[serverID] }

// ─── seeding ─────────────────────────────────────────────────────────────────

func seedNamedInstance(t *testing.T, store *db.Store, instanceID, pluginID, name, health string) {
	t.Helper()
	now := fixedClock().UTC().Format(time.RFC3339Nano)
	if _, err := store.Queries().CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID:                    instanceID,
		PluginID:              pluginID,
		InstanceName:          name,
		ConfigJson:            "{}",
		SubscriptionScopeJson: "{}",
		HandshakeVersions:     "{}",
		HealthState:           health,
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("seed instance %s: %v", instanceID, err)
	}
}

// seedManagedEndpoint registers the instance's managed MCP server row under
// serverID, which is what InflightCounter resolves an instance name to.
func seedManagedEndpoint(t *testing.T, store *db.Store, serverID, instanceID string) {
	t.Helper()
	if _, err := store.Queries().CreateManagedMCPServer(context.Background(), db.CreateManagedMCPServerParams{
		ID:               serverID,
		Name:             "managed-" + instanceID,
		Url:              "http://10.83.0.2:8080/mcp",
		CreatedAt:        fixedClock().UTC().Format(time.RFC3339Nano),
		PluginInstanceID: &instanceID,
	}); err != nil {
		t.Fatalf("seed managed endpoint: %v", err)
	}
}

func desiredStateOf(t *testing.T, store *db.Store, instanceID string) string {
	t.Helper()
	row, err := store.Queries().GetPluginContainerByInstance(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("get plugin_containers row for %s: %v", instanceID, err)
	}
	return row.DesiredState
}

// ─── admin harness ───────────────────────────────────────────────────────────

type adminHarness struct {
	store      *db.Store
	kicker     *fakeKicker
	registrar  *fakeRegistrar
	supervisor *fakeSupervisor
	inFlight   fakeInFlight
	handler    *admin.PluginHandler
}

// newAdminHarness wires the REAL admin handler with the v2 adapters injected
// the way the v2 assembly will, over a real store and the real DesiredState.
func newAdminHarness(t *testing.T, samples adminv2.SampleSource) *adminHarness {
	t.Helper()
	store := testutil.NewTestStore(t)
	h := &adminHarness{
		store:      store,
		kicker:     &fakeKicker{},
		registrar:  &fakeRegistrar{},
		supervisor: &fakeSupervisor{},
		inFlight:   fakeInFlight{},
	}

	desired := lifecycle.NewDesiredState(fixedClock)
	procMgr := adminv2.NewProcessManager(store.Queries(), desired, h.kicker, h.registrar)
	trigger := adminv2.NewTriggerRestarter(h.supervisor)

	lc := admin.NewInstanceLifecycle(admin.InstanceLifecycleDeps{
		Q:           store.Queries(),
		Store:       store,
		Clock:       fixedClock,
		ProcMgr:     procMgr,
		Trigger:     trigger,
		Inflight:    adminv2.NewInflightCounter(store.Queries(), store.Queries(), h.inFlight),
		Evictor:     adminv2.ToolConnEvictor{},
		Provisioner: desired,
	})
	deps := admin.PluginHandlerDeps{
		Q:              store.Queries(),
		Clock:          fixedClock,
		ProcessManager: procMgr,
		Lifecycle:      lc,
		Config:         admin.NewInstanceConfig(admin.InstanceConfigDeps{Q: store.Queries(), Trigger: trigger}),
		Store:          store,
		Provisioner:    desired,
	}
	if samples != nil {
		deps.RSSAggregator = adminv2.NewRSSAggregator(samples)
	}
	h.handler = admin.NewPluginHandler(deps)
	return h
}

func (h *adminHarness) serve(method, path, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/plugins/{id}/instances/{iid}/deactivate", h.handler.DeactivateInstance)
	r.Post("/plugins/{id}/instances/{iid}/activate", h.handler.ActivateInstance)
	r.Delete("/plugins/{id}/instances/{iid}", h.handler.DeleteInstance)
	r.Put("/plugins/{id}/instances/{iid}/subscription-scope", h.handler.PutSubscriptionScope)
	r.Get("/plugins/rss", h.handler.GetPluginRSS)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// ─── admin handler over the v2 adapters ──────────────────────────────────────

func TestAdminHandler_InstanceLifecycle_OverDesiredState(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		pathSuffix    string
		health        string
		desiredBefore string
		inFlight      int
		wantStatus    int
		wantDesired   string
		wantKick      bool
		wantRowGone   bool
	}{
		{
			name: "deactivate stops the container and kicks the reconciler", method: http.MethodPost, pathSuffix: "/deactivate",
			health: "healthy", desiredBefore: lifecycle.DesiredStateRunning,
			wantStatus: http.StatusOK, wantDesired: lifecycle.DesiredStateStopped, wantKick: true,
		},
		{
			name: "activate runs the container and kicks the reconciler", method: http.MethodPost, pathSuffix: "/activate",
			health: "inactive", desiredBefore: lifecycle.DesiredStateStopped,
			wantStatus: http.StatusOK, wantDesired: lifecycle.DesiredStateRunning, wantKick: true,
		},
		{
			name: "deactivate with in-flight calls is refused and touches nothing", method: http.MethodPost, pathSuffix: "/deactivate",
			health: "healthy", desiredBefore: lifecycle.DesiredStateRunning, inFlight: 2,
			wantStatus: http.StatusConflict, wantDesired: lifecycle.DesiredStateRunning,
		},
		{
			name: "delete with in-flight calls is refused with 409", method: http.MethodDelete, pathSuffix: "",
			health: "healthy", desiredBefore: lifecycle.DesiredStateRunning, inFlight: 1,
			wantStatus: http.StatusConflict, wantDesired: lifecycle.DesiredStateRunning,
		},
		{
			name: "delete with no in-flight calls removes the desired-state row", method: http.MethodDelete, pathSuffix: "",
			health: "healthy", desiredBefore: lifecycle.DesiredStateRunning,
			wantStatus: http.StatusNoContent, wantKick: true, wantRowGone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newAdminHarness(t, nil)
			seedV2Plugin(t, h.store, "plugin-1", digestOf("admin-lifecycle"), "active")
			seedNamedInstance(t, h.store, "inst-1", "plugin-1", "prod", tt.health)
			seedManagedEndpoint(t, h.store, "srv-1", "inst-1")
			h.inFlight["srv-1"] = tt.inFlight

			if _, err := lifecycle.NewDesiredState(fixedClock).CreateForInstance(context.Background(), h.store.Queries(), "inst-1"); err != nil {
				t.Fatalf("CreateForInstance: %v", err)
			}
			if tt.desiredBefore != lifecycle.DesiredStateRunning {
				if err := lifecycle.NewDesiredState(fixedClock).SetDesired(context.Background(), h.store.Queries(), "inst-1", tt.desiredBefore); err != nil {
					t.Fatalf("seed desired state: %v", err)
				}
			}

			rec := h.serve(tt.method, "/plugins/plugin-1/instances/inst-1"+tt.pathSuffix, "")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusConflict && !strings.Contains(rec.Body.String(), "in-flight") {
				t.Errorf("409 body = %s, want it to mention in-flight calls", rec.Body.String())
			}

			if tt.wantRowGone {
				if _, err := h.store.Queries().GetPluginContainerByInstance(context.Background(), "inst-1"); err == nil {
					t.Error("plugin_containers row still exists after delete")
				}
			} else if got := desiredStateOf(t, h.store, "inst-1"); got != tt.wantDesired {
				t.Errorf("desired_state = %q, want %q", got, tt.wantDesired)
			}

			if gotKick := h.kicker.count() > 0; gotKick != tt.wantKick {
				t.Errorf("reconciler kicked = %v (%d kicks), want %v", gotKick, h.kicker.count(), tt.wantKick)
			}
		})
	}
}

// ─── ProcessManager ──────────────────────────────────────────────────────────

func TestProcessManager_StartByPluginID_SkipsDeactivatedInstances(t *testing.T) {
	ctx := context.Background()
	h := newAdminHarness(t, nil)
	seedV2Plugin(t, h.store, "plugin-1", digestOf("start-by-plugin"), "active")
	seedNamedInstance(t, h.store, "inst-on", "plugin-1", "on", "unhealthy")
	seedNamedInstance(t, h.store, "inst-off", "plugin-1", "off", "inactive")

	desired := lifecycle.NewDesiredState(fixedClock)
	for _, id := range []string{"inst-on", "inst-off"} {
		if _, err := desired.CreateForInstance(ctx, h.store.Queries(), id); err != nil {
			t.Fatalf("CreateForInstance(%s): %v", id, err)
		}
		if err := desired.SetDesired(ctx, h.store.Queries(), id, lifecycle.DesiredStateStopped); err != nil {
			t.Fatalf("SetDesired(%s): %v", id, err)
		}
	}

	pm := adminv2.NewProcessManager(h.store.Queries(), desired, h.kicker, h.registrar)
	if err := pm.StartByPluginID(ctx, "plugin-1"); err != nil {
		t.Fatalf("StartByPluginID: %v", err)
	}

	if got := desiredStateOf(t, h.store, "inst-on"); got != lifecycle.DesiredStateRunning {
		t.Errorf("inst-on desired_state = %q, want running", got)
	}
	if got := desiredStateOf(t, h.store, "inst-off"); got != lifecycle.DesiredStateStopped {
		t.Errorf("inst-off desired_state = %q, want it left stopped", got)
	}
	if h.kicker.count() != 1 {
		t.Errorf("kicks = %d, want 1", h.kicker.count())
	}
}

func TestProcessManager_StartByPluginID_InactivePluginIsAnError(t *testing.T) {
	ctx := context.Background()
	h := newAdminHarness(t, nil)
	seedV2Plugin(t, h.store, "plugin-1", digestOf("start-inactive-plugin"), "active")
	seedNamedInstance(t, h.store, "inst-1", "plugin-1", "prod", "unhealthy")
	desired := lifecycle.NewDesiredState(fixedClock)
	if _, err := desired.CreateForInstance(ctx, h.store.Queries(), "inst-1"); err != nil {
		t.Fatalf("CreateForInstance: %v", err)
	}
	if _, err := h.store.DB().ExecContext(ctx, `UPDATE plugins SET status = 'removed' WHERE id = 'plugin-1'`); err != nil {
		t.Fatalf("remove plugin: %v", err)
	}

	pm := adminv2.NewProcessManager(h.store.Queries(), desired, h.kicker, h.registrar)
	err := pm.StartByPluginID(ctx, "plugin-1")
	if !errors.Is(err, lifecycle.ErrPluginNotActive) {
		t.Fatalf("err = %v, want ErrPluginNotActive", err)
	}
}

func TestProcessManager_StopByPluginID_RemovesRowsAndDeregisters(t *testing.T) {
	ctx := context.Background()
	h := newAdminHarness(t, nil)
	seedV2Plugin(t, h.store, "plugin-1", digestOf("stop-by-plugin"), "active")
	desired := lifecycle.NewDesiredState(fixedClock)
	for _, id := range []string{"inst-a", "inst-b"} {
		seedNamedInstance(t, h.store, id, "plugin-1", id, "healthy")
		if _, err := desired.CreateForInstance(ctx, h.store.Queries(), id); err != nil {
			t.Fatalf("CreateForInstance(%s): %v", id, err)
		}
	}

	pm := adminv2.NewProcessManager(h.store.Queries(), desired, h.kicker, h.registrar)
	if err := pm.StopByPluginID(ctx, "plugin-1"); err != nil {
		t.Fatalf("StopByPluginID: %v", err)
	}

	for _, id := range []string{"inst-a", "inst-b"} {
		if _, err := h.store.Queries().GetPluginContainerByInstance(ctx, id); err == nil {
			t.Errorf("plugin_containers row for %s still exists", id)
		}
	}
	if len(h.registrar.deregisters) != 2 {
		t.Errorf("deregistered = %v, want both instances", h.registrar.deregisters)
	}
	if h.kicker.count() != 1 {
		t.Errorf("kicks = %d, want 1", h.kicker.count())
	}
}

func TestProcessManager_Stop_WithoutContainerRowIsNoOp(t *testing.T) {
	h := newAdminHarness(t, nil)
	seedV2Plugin(t, h.store, "plugin-1", digestOf("stop-no-row"), "active")
	seedNamedInstance(t, h.store, "inst-1", "plugin-1", "prod", "healthy")

	pm := adminv2.NewProcessManager(h.store.Queries(), lifecycle.NewDesiredState(fixedClock), h.kicker, h.registrar)
	if err := pm.Stop(context.Background(), "inst-1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// ─── TriggerRestarter ────────────────────────────────────────────────────────

func TestTriggerRestarter_DelegatesToSupervisor(t *testing.T) {
	sup := &fakeSupervisor{}
	tr := adminv2.NewTriggerRestarter(sup)
	tr.Start(context.Background(), "inst-1")
	tr.Restart(context.Background(), "inst-1")
	tr.Stop("inst-1")

	want := []string{"start:inst-1", "restart:inst-1", "stop:inst-1"}
	got := sup.recorded()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("supervisor calls = %v, want %v", got, want)
	}
}

const eventsManifestWithScope = `schema_version: v1
name: events-plugin
version: 1.0.0
services:
  trigger: v1
auth:
  mode: instance_credentials
  strategy: none
event_kinds:
  - kind: message
subscription_schema:
  type: object
  additionalProperties: false
  required:
    - channels
  properties:
    channels:
      type: array
      items:
        type: string
`

type listenOpener struct {
	mu     sync.Mutex
	params []mcp.EventsListenParams
	opened chan struct{}
	// first is the event the first connect delivers before idling.
	first mcp.CloudEvent
}

func (o *listenOpener) ListenEvents(_ context.Context, p mcp.EventsListenParams) (events.EventStream, error) {
	o.mu.Lock()
	attempt := len(o.params)
	o.params = append(o.params, p)
	o.mu.Unlock()
	o.opened <- struct{}{}
	if attempt == 0 {
		return &idleStream{events: []mcp.CloudEvent{o.first}}, nil
	}
	return &idleStream{}, nil
}

func (o *listenOpener) paramsAt(i int) mcp.EventsListenParams {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.params[i]
}

type idleStream struct {
	events []mcp.CloudEvent
	idx    int
}

func (s *idleStream) Next(ctx context.Context) (mcp.CloudEvent, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	<-ctx.Done()
	return mcp.CloudEvent{}, ctx.Err()
}

func (s *idleStream) Close() error { return nil }

type staticResolver struct{ opener events.StreamOpener }

func (r staticResolver) ResolveStream(context.Context, string) (events.StreamOpener, error) {
	return r.opener, nil
}

// signalingCursorStore wraps the real DB cursor store and signals each Advance,
// the synchronization point for "the first event's cursor is durable".
type signalingCursorStore struct {
	events.Store
	advanced chan struct{}
}

func (s *signalingCursorStore) Advance(ctx context.Context, instanceID, scopeHash, cursor string, seq uint64) error {
	err := s.Store.Advance(ctx, instanceID, scopeHash, cursor, seq)
	s.advanced <- struct{}{}
	return err
}

type discardSink struct{}

func (discardSink) Handle(context.Context, events.Event) error { return nil }

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestScopePut_ThroughV2Restarter_ReconnectsWithFreshCursor drives a scope PUT
// through the real admin handler and the v2 restarter into a real events
// supervisor over a real cursor store. The reconnect under the new scope must
// carry the new scope and no cursor, while the old scope's cursor was real.
func TestScopePut_ThroughV2Restarter_ReconnectsWithFreshCursor(t *testing.T) {
	h := newAdminHarness(t, nil)
	ctx := context.Background()
	now := fixedClock().UTC().Format(time.RFC3339Nano)
	if _, err := h.store.Queries().CreatePlugin(ctx, db.CreatePluginParams{
		ID: "plugin-1", Name: "events-plugin", PluginVersion: "1.0.0", ManifestSnapshot: eventsManifestWithScope,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	seedNamedInstance(t, h.store, "inst-1", "plugin-1", "prod", "healthy")
	if _, err := h.store.DB().ExecContext(ctx,
		`UPDATE plugin_instances SET subscription_scope_json = '{"channels":["a"]}' WHERE id = 'inst-1'`); err != nil {
		t.Fatalf("seed scope: %v", err)
	}

	opener := &listenOpener{
		opened: make(chan struct{}, 8),
		first:  mcp.CloudEvent{SpecVersion: "1.0", Source: "src", Type: "message", ID: "e1", Sequence: 4},
	}
	cursors := &signalingCursorStore{Store: events.NewDBStore(h.store.Queries()), advanced: make(chan struct{}, 8)}
	sup := events.NewSupervisor(events.Config{
		Querier:            h.store.Queries(),
		TestStreamResolver: staticResolver{opener: opener},
		Cursor:             cursors,
		Sink:               discardSink{},
		BackoffInitial:     time.Microsecond,
		BackoffMax:         5 * time.Millisecond,
	})
	t.Cleanup(sup.StopAll)

	// Rebuild the handler's restarter over the real supervisor.
	trigger := adminv2.NewTriggerRestarter(sup)
	handler := admin.NewPluginHandler(admin.PluginHandlerDeps{
		Q:      h.store.Queries(),
		Clock:  fixedClock,
		Config: admin.NewInstanceConfig(admin.InstanceConfigDeps{Q: h.store.Queries(), Trigger: trigger}),
	})
	h.handler = handler

	sup.Start(ctx, "inst-1")
	waitFor(t, opener.opened, "first connect")
	waitFor(t, cursors.advanced, "first event's cursor to be stored")
	if got := opener.paramsAt(0); got.Cursor != "" {
		t.Fatalf("first connect Cursor = %q, want empty", got.Cursor)
	}

	rec := h.serve(http.MethodPut, "/plugins/plugin-1/instances/inst-1/subscription-scope",
		`{"scope":{"channels":["b"]},"expected_version":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("scope PUT status = %d, body: %s", rec.Code, rec.Body.String())
	}

	waitFor(t, opener.opened, "reconnect under the new scope")
	second := opener.paramsAt(1)
	if second.Cursor != "" {
		t.Errorf("reconnect Cursor = %q, want empty: a scope change must not resume the old scope's cursor", second.Cursor)
	}
	var scope map[string][]string
	if err := json.Unmarshal(second.Scope, &scope); err != nil {
		t.Fatalf("decode reconnect scope %q: %v", second.Scope, err)
	}
	if len(scope["channels"]) != 1 || scope["channels"][0] != "b" {
		t.Errorf("reconnect scope = %s, want channels [b]", second.Scope)
	}
}

// ─── InflightCounter ─────────────────────────────────────────────────────────

func TestInflightCounter(t *testing.T) {
	h := newAdminHarness(t, nil)
	seedV2Plugin(t, h.store, "plugin-1", digestOf("inflight"), "active")
	seedNamedInstance(t, h.store, "inst-1", "plugin-1", "prod", "healthy")
	seedNamedInstance(t, h.store, "inst-2", "plugin-1", "no-endpoint", "healthy")
	seedManagedEndpoint(t, h.store, "srv-1", "inst-1")

	counter := adminv2.NewInflightCounter(h.store.Queries(), h.store.Queries(), fakeInFlight{"srv-1": 3})

	tests := []struct {
		name         string
		instanceName string
		want         int
	}{
		{"instance with an endpoint reports the gate's count", "prod", 3},
		{"instance without a managed endpoint has no calls", "no-endpoint", 0},
		{"unknown instance has no calls", "ghost", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := counter.InflightCountByInstance(tt.instanceName); got != tt.want {
				t.Errorf("InflightCountByInstance(%q) = %d, want %d", tt.instanceName, got, tt.want)
			}
		})
	}
}

// ─── RSS ─────────────────────────────────────────────────────────────────────

type fakeSamples []resources.Sample

func (f fakeSamples) Samples() []resources.Sample { return f }

// v1ShapedAggregator stands in for the v1 *process.RSSSampler adapter.
type v1ShapedAggregator struct{ samples []admin.RSSSample }

func (a v1ShapedAggregator) Aggregate() (uint64, int, []admin.RSSSample) {
	var total uint64
	for _, s := range a.samples {
		total += s.Bytes
	}
	return total, len(a.samples), a.samples
}

func rssJSONShape(t *testing.T, agg admin.RSSAggregator) map[string]any {
	t.Helper()
	handler := admin.NewPluginHandler(admin.PluginHandlerDeps{RSSAggregator: agg})
	rec := httptest.NewRecorder()
	handler.GetPluginRSS(rec, httptest.NewRequest(http.MethodGet, "/plugins/rss", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode rss body: %v", err)
	}
	return envelope.Data
}

func keysOf(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func TestRSSAggregator_SameJSONShapeAsV1(t *testing.T) {
	sampledAt := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	samples := fakeSamples{
		{Target: resources.Target{InstanceID: "i-small", InstanceName: "small", PluginID: "p"}, At: sampledAt},
		{Target: resources.Target{InstanceID: "i-big", InstanceName: "big", PluginID: "p"}, At: sampledAt},
	}
	samples[0].MemoryUsageBytes = 100
	samples[1].MemoryUsageBytes = 900
	v2 := adminv2.NewRSSAggregator(samples)

	v1 := v1ShapedAggregator{samples: []admin.RSSSample{
		{InstanceID: "i-big", InstanceName: "big", PluginID: "p", Bytes: 900, SampledAt: sampledAt},
		{InstanceID: "i-small", InstanceName: "small", PluginID: "p", Bytes: 100, SampledAt: sampledAt},
	}}

	got := rssJSONShape(t, v2)
	want := rssJSONShape(t, v1)

	if g, w := keysOf(got), keysOf(want); len(g) != len(w) {
		t.Fatalf("top-level keys = %v, want %v", g, w)
	} else {
		for k := range w {
			if !g[k] {
				t.Errorf("v2 response is missing top-level key %q", k)
			}
		}
	}

	gotInstances := got["instances"].([]any)
	wantInstances := want["instances"].([]any)
	if len(gotInstances) != len(wantInstances) {
		t.Fatalf("instances = %d, want %d", len(gotInstances), len(wantInstances))
	}
	g0 := gotInstances[0].(map[string]any)
	w0 := wantInstances[0].(map[string]any)
	for k := range w0 {
		if _, ok := g0[k]; !ok {
			t.Errorf("v2 instance entry is missing key %q", k)
		}
	}
	if len(g0) != len(w0) {
		t.Errorf("v2 instance keys = %v, want exactly %v", keysOf(g0), keysOf(w0))
	}

	// Same values, not merely the same keys: ordered by memory descending, with
	// the stats read's timestamp.
	if g0["instance_id"] != "i-big" || g0["rss_bytes"] != float64(900) {
		t.Errorf("first instance = %v, want the largest first", g0)
	}
	if g0["sampled_at"] != w0["sampled_at"] {
		t.Errorf("sampled_at = %v, want %v", g0["sampled_at"], w0["sampled_at"])
	}
	if got["total_bytes"] != float64(1000) || got["instance_count"] != float64(2) {
		t.Errorf("total/count = %v/%v, want 1000/2", got["total_bytes"], got["instance_count"])
	}
}
