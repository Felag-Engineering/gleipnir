package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
	plugintrigger "github.com/felag-engineering/gleipnir/internal/plugin/trigger"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// buildEventManifestYAMLWithBindingSchema is buildEventManifestYAML widened
// with a binding_schema on the given kind, declaring a single string
// property named key — the shape TestPipeline_MatchingBindingLaunches_
// NonMatchingDoesNot needs so its policies' binding compiles against
// something.
func buildEventManifestYAMLWithBindingSchema(t *testing.T, kind, key string) string {
	t.Helper()
	schema := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "type"},
			{Kind: yaml.ScalarNode, Value: "object"},
			{Kind: yaml.ScalarNode, Value: "properties"},
			{
				Kind: yaml.MappingNode,
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: key},
					{
						Kind: yaml.MappingNode,
						Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Value: "type"},
							{Kind: yaml.ScalarNode, Value: "string"},
						},
					},
				},
			},
		},
	}
	m := &sdkmanifest.Manifest{
		SchemaVersion: "1",
		Name:          "test-plugin",
		Version:       "1.0.0",
		EventKinds:    []sdkmanifest.EventKindDecl{{Kind: kind, BindingSchema: schema}},
	}
	b, err := sdkmanifest.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(b)
}

// This file exercises the full ingestion pipeline spec §5 commits to:
// events/listen client (internal/mcp, driven against mcp.FakeEventsServer) →
// dedup → GetSubscribedActivePolicies → binding compile/evaluate →
// RunLauncher.LaunchWithConcurrency, all reached through
// trigger.ListenSinkAdapter wrapping a real *trigger.Dispatcher — the same
// production adapter and dispatcher the wired supervisor would use.
//
// Only the RunLauncher and the Dispatcher's policy Querier are fakes (DoD:
// "fake Querier, fake RunLauncher, dedup.Noop" — dedup here is the real
// DB-backed store so the duplicate-drop case is exercised for real). Plugin,
// plugin_instances, and the managed mcp_servers row are all real DB rows,
// resolved through the real mcp.Registry the way production does.

var pipelineFrozen = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

// pipelineFixture seeds a real store with one plugin instance whose managed
// mcp_servers row points at fake, mirroring discoverprobe_test.go's
// probeFixture — real queries plus the real ManagedRegistrar, so client
// resolution is exercised for real.
func pipelineFixture(t *testing.T, fake *mcp.FakeEventsServer, manifestYAML string) (*db.Store, *mcp.Registry, string) {
	t.Helper()
	s := testutil.NewTestStore(t)

	const pluginID = "pl-pipeline"
	const instanceID = "inst-pipeline"
	if _, err := s.DB().Exec(
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES (?, 'test-plugin', '1.0.0', ?, 'pubkey', 'active', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		pluginID, manifestYAML,
	); err != nil {
		t.Fatalf("insert plugin: %v", err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, subscription_scope_json, handshake_versions, health_state, version, created_at, updated_at)
		 VALUES (?, ?, 'instance-pipeline', '{}', '{}', '{}', 'healthy', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		instanceID, pluginID,
	); err != nil {
		t.Fatalf("insert plugin instance: %v", err)
	}

	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	registrar, err := mcp.NewManagedRegistrar(s.Queries(), func() string { return "srv-pipeline" }, func() time.Time { return pipelineFrozen })
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	if _, err := registrar.Register(context.Background(), mcp.ManagedEndpoint{
		InstanceID:   instanceID,
		InstanceName: "instance-pipeline",
		URL:          srv.URL,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	return s, mcp.NewRegistry(s.Queries()), instanceID
}

// fakeDispatcherQuerier satisfies plugintrigger.Querier with in-memory
// policies, mirroring internal/plugin/trigger's own dispatcher_test.go fake
// — duplicated rather than imported because it is unexported there.
type fakeDispatcherQuerier struct {
	mu       sync.Mutex
	policies []db.Policy
	plugin   db.Plugin
	instance db.PluginInstance
}

func (q *fakeDispatcherQuerier) GetSubscribedActivePolicies(context.Context) ([]db.Policy, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.policies, nil
}
func (q *fakeDispatcherQuerier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	if id != q.plugin.ID {
		return db.Plugin{}, errors.New("plugin not found: " + id)
	}
	return q.plugin, nil
}
func (q *fakeDispatcherQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	if id != q.instance.ID {
		return db.PluginInstance{}, errors.New("instance not found: " + id)
	}
	return q.instance, nil
}
func (q *fakeDispatcherQuerier) ListPluginsByStatus(context.Context, string) ([]db.Plugin, error) {
	return nil, nil
}
func (q *fakeDispatcherQuerier) ListPluginInstancesByPlugin(context.Context, string) ([]db.PluginInstance, error) {
	return nil, nil
}

// fakePipelineLauncher satisfies plugintrigger.RunLauncher, recording every
// launch and signaling on a channel so tests synchronize without polling.
type fakePipelineLauncher struct {
	mu       sync.Mutex
	calls    []run.LaunchParams
	result   run.LaunchResult
	err      error
	launched chan run.LaunchParams
}

func newFakePipelineLauncher() *fakePipelineLauncher {
	return &fakePipelineLauncher{
		result:   run.LaunchResult{Outcome: run.OutcomeLaunched},
		launched: make(chan run.LaunchParams, 64),
	}
}

func (l *fakePipelineLauncher) LaunchWithConcurrency(_ context.Context, p run.LaunchParams) (run.LaunchResult, error) {
	l.mu.Lock()
	l.calls = append(l.calls, p)
	l.mu.Unlock()
	select {
	case l.launched <- p:
	default:
	}
	return l.result, l.err
}

func (l *fakePipelineLauncher) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

// fakePipelinePublisher satisfies event.Publisher, discarding every event.
type fakePipelinePublisher struct{}

func (fakePipelinePublisher) Publish(string, json.RawMessage) {}

var _ event.Publisher = fakePipelinePublisher{}

// pipelinePolicyYAML builds a minimal subscribed policy bound to source +
// event kind, with an optional binding.
func pipelinePolicyYAML(t *testing.T, name, source, eventKind string, binding map[string]any) string {
	t.Helper()
	trig := map[string]any{
		"type":       "subscribed",
		"source":     source,
		"event_kind": eventKind,
	}
	if len(binding) > 0 {
		trig["binding"] = binding
	}
	p := map[string]any{
		"name":    name,
		"trigger": trig,
		"agent": map[string]any{
			"task":  "do stuff",
			"model": map[string]any{"provider": "anthropic", "name": "claude-3-5-haiku-latest"},
		},
	}
	b, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("marshal policy yaml: %v", err)
	}
	return string(b)
}

// TestPipeline_MatchingBindingLaunches_NonMatchingDoesNot drives the full
// events/listen → dedup → policy scan → binding evaluate → launch pipeline
// against a real mcp.FakeEventsServer, asserting a matching policy launches
// and a non-matching one does not.
func TestPipeline_MatchingBindingLaunches_NonMatchingDoesNot(t *testing.T) {
	t.Parallel()

	fake := mcp.NewFakeEventsServer()
	fake.HeartbeatMs = 50
	fake.ListenEvents = []mcp.CloudEvent{
		{SpecVersion: "1.0", Source: "src", Type: "message", ID: "evt-match", Sequence: 1, Data: json.RawMessage(`{"channel":"#incidents"}`)},
	}

	manifestYAML := buildEventManifestYAMLWithBindingSchema(t, "message", "channel")
	store, registry, instanceID := pipelineFixture(t, fake, manifestYAML)

	dispatcherQuerier := &fakeDispatcherQuerier{
		plugin:   db.Plugin{ID: "pl-pipeline", ManifestSnapshot: manifestYAML},
		instance: db.PluginInstance{ID: instanceID, PluginID: "pl-pipeline", InstanceName: "instance-pipeline"},
		policies: []db.Policy{
			{ID: "pol-match", Yaml: pipelinePolicyYAML(t, "matches", "instance-pipeline", "message", map[string]any{"channel": "#incidents"}), UpdatedAt: "t1"},
			{ID: "pol-nomatch", Yaml: pipelinePolicyYAML(t, "no-match", "instance-pipeline", "message", map[string]any{"channel": "#general"}), UpdatedAt: "t1"},
		},
	}

	launcher := newFakePipelineLauncher()
	dispatcher := plugintrigger.NewDispatcher(plugintrigger.DispatcherConfig{
		Launcher:  launcher,
		Querier:   dispatcherQuerier,
		Dedup:     dedup.NewDBStore(store.Queries()),
		Publisher: fakePipelinePublisher{},
	})
	sink := plugintrigger.NewListenSinkAdapter(dispatcher)

	sup := events.NewSupervisor(events.Config{
		Querier:        store.Queries(),
		Servers:        store.Queries(),
		Clients:        registry,
		Cursor:         events.NewDBStore(store.Queries()),
		Sink:           sink,
		BackoffInitial: time.Millisecond,
		BackoffMax:     10 * time.Millisecond,
		UnhealthyAfter: 5,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, instanceID)
	t.Cleanup(sup.StopAll)

	params := waitOnChan(t, launcher.launched, 10*time.Second, "matching policy to launch")
	if params.PolicyID != "pol-match" {
		t.Errorf("launched PolicyID = %q, want %q", params.PolicyID, "pol-match")
	}

	// Give the non-matching policy a chance to (wrongly) launch — bounded
	// negative assertion, not a poll loop.
	select {
	case p := <-launcher.launched:
		t.Fatalf("unexpected second launch for policy %q", p.PolicyID)
	case <-time.After(200 * time.Millisecond):
	}

	if launcher.callCount() != 1 {
		t.Errorf("launch count = %d, want 1 (only the matching policy)", launcher.callCount())
	}
}

// TestPipeline_DuplicateEventID_DroppedByDedup verifies that redelivering the
// same event id within a stream (the extension's at-least-once contract,
// doc §7.3 — a server may redeliver, and this client never assumes
// at-most-once) results in exactly one launch: the real DB-backed dedup
// store absorbs the duplicate before the policy scan runs a second time.
func TestPipeline_DuplicateEventID_DroppedByDedup(t *testing.T) {
	t.Parallel()

	fake := mcp.NewFakeEventsServer()
	fake.HeartbeatMs = 50
	fake.ListenEvents = []mcp.CloudEvent{
		{SpecVersion: "1.0", Source: "src", Type: "message", ID: "evt-dup", Sequence: 1, Data: json.RawMessage(`{}`)},
		// Same event id redelivered under a later sequence — the store's
		// monotonic-seq check is satisfied (seq 1 then 2), but the
		// dispatcher's dedup key is (instance, kind, EVENT ID), which is
		// unchanged, so this must still be dropped.
		{SpecVersion: "1.0", Source: "src", Type: "message", ID: "evt-dup", Sequence: 2, Data: json.RawMessage(`{}`)},
	}

	manifestYAML := buildEventManifestYAML(t, "message")
	store, registry, instanceID := pipelineFixture(t, fake, manifestYAML)

	dispatcherQuerier := &fakeDispatcherQuerier{
		plugin:   db.Plugin{ID: "pl-pipeline", ManifestSnapshot: manifestYAML},
		instance: db.PluginInstance{ID: instanceID, PluginID: "pl-pipeline", InstanceName: "instance-pipeline"},
		policies: []db.Policy{
			{ID: "pol-1", Yaml: pipelinePolicyYAML(t, "p1", "instance-pipeline", "message", nil), UpdatedAt: "t1"},
		},
	}

	launcher := newFakePipelineLauncher()
	dispatcher := plugintrigger.NewDispatcher(plugintrigger.DispatcherConfig{
		Launcher:  launcher,
		Querier:   dispatcherQuerier,
		Dedup:     dedup.NewDBStore(store.Queries()),
		Publisher: fakePipelinePublisher{},
	})
	sink := plugintrigger.NewListenSinkAdapter(dispatcher)

	sup := events.NewSupervisor(events.Config{
		Querier:        store.Queries(),
		Servers:        store.Queries(),
		Clients:        registry,
		Cursor:         events.NewDBStore(store.Queries()),
		Sink:           sink,
		BackoffInitial: time.Millisecond,
		BackoffMax:     10 * time.Millisecond,
		UnhealthyAfter: 5,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, instanceID)
	t.Cleanup(sup.StopAll)

	waitOnChan(t, launcher.launched, 10*time.Second, "first delivery to launch")

	// Give the redelivered duplicate a chance to (wrongly) launch a second
	// time — a bounded negative assertion, not a poll loop.
	select {
	case p := <-launcher.launched:
		t.Fatalf("unexpected second launch for redelivered event (policy %q) — dedup should have dropped it", p.PolicyID)
	case <-time.After(300 * time.Millisecond):
	}

	if launcher.callCount() != 1 {
		t.Errorf("launch count = %d, want 1 (redelivery must be deduped)", launcher.callCount())
	}
}
