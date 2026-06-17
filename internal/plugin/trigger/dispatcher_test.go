package trigger_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
	plugintrigger "github.com/felag-engineering/gleipnir/internal/plugin/trigger"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type fakeQuerier struct {
	policies  []db.Policy
	polErr    error
	instances map[string]db.PluginInstance // id → instance
	instErr   error
	plugins   map[string]db.Plugin // id → plugin
	plugErr   error
}

func (q *fakeQuerier) GetSubscribedActivePolicies(_ context.Context) ([]db.Policy, error) {
	return q.policies, q.polErr
}

func (q *fakeQuerier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	if q.plugErr != nil {
		return db.Plugin{}, q.plugErr
	}
	if p, ok := q.plugins[id]; ok {
		return p, nil
	}
	return db.Plugin{}, errors.New("plugin not found: " + id)
}

func (q *fakeQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	if q.instErr != nil {
		return db.PluginInstance{}, q.instErr
	}
	if inst, ok := q.instances[id]; ok {
		return inst, nil
	}
	return db.PluginInstance{}, errors.New("instance not found: " + id)
}

func (q *fakeQuerier) ListPluginsByStatus(_ context.Context, _ string) ([]db.Plugin, error) {
	return nil, nil
}

func (q *fakeQuerier) ListPluginInstancesByPlugin(_ context.Context, _ string) ([]db.PluginInstance, error) {
	return nil, nil
}

// fakeDedupStore counts Seen calls and returns seen=true for registered keys.
type fakeDedupStore struct {
	mu      sync.Mutex
	seenSet map[dedup.Key]bool
	calls   int
}

func (s *fakeDedupStore) Seen(_ context.Context, k dedup.Key) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.seenSet[k], nil
}

func (s *fakeDedupStore) markSeen(k dedup.Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seenSet == nil {
		s.seenSet = make(map[dedup.Key]bool)
	}
	s.seenSet[k] = true
}

// fakePublisher records Publish calls.
type fakePublisher struct {
	mu     sync.Mutex
	events []string
}

func (p *fakePublisher) Publish(name string, _ json.RawMessage) {
	p.mu.Lock()
	p.events = append(p.events, name)
	p.mu.Unlock()
}

func (p *fakePublisher) published() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.events))
	copy(out, p.events)
	return out
}

// fakeLauncher satisfies plugintrigger.RunLauncher and records LaunchWithConcurrency calls.
type fakeLauncher struct {
	mu     sync.Mutex
	calls  []run.LaunchParams
	result run.LaunchResult // outcome to return on success
	err    error            // e.g. run.ErrConcurrencyQueueFull
}

var _ plugintrigger.RunLauncher = (*fakeLauncher)(nil)

func (l *fakeLauncher) LaunchWithConcurrency(_ context.Context, p run.LaunchParams) (run.LaunchResult, error) {
	l.mu.Lock()
	l.calls = append(l.calls, p)
	l.mu.Unlock()
	return l.result, l.err
}

func (l *fakeLauncher) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

// buildManifestYAML produces a minimal manifest YAML with one event kind and
// an optional binding schema yaml.Node.
func buildManifestYAML(t *testing.T, eventKind string, bindingSchema *yaml.Node) string {
	t.Helper()
	m := &sdkmanifest.Manifest{
		SchemaVersion: "1",
		Name:          "test-plugin",
		Version:       "1.0.0",
		Services:      sdkmanifest.Services{Trigger: "v1"},
	}
	m.EventKinds = []sdkmanifest.EventKindDecl{
		{Kind: eventKind, BindingSchema: bindingSchema},
	}
	b, err := sdkmanifest.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(b)
}

// policyYAML builds a minimal subscribed policy YAML with the given source,
// event kind, and optional binding.
func policyYAML(source, eventKind string, binding map[string]any) string {
	trig := map[string]any{
		"type":       "subscribed",
		"source":     source,
		"event_kind": eventKind,
	}
	if len(binding) > 0 {
		trig["binding"] = binding
	}
	p := map[string]any{
		"name":    "test-policy",
		"trigger": trig,
		"agent": map[string]any{
			"task":  "do stuff",
			"model": map[string]any{"provider": "anthropic", "name": "claude-3-5-haiku-latest"},
		},
	}
	b, err := yaml.Marshal(p)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// newDispatcher wires a Dispatcher with the given querier, dedup store, and
// launcher.
func newDispatcher(q plugintrigger.Querier, d dedup.Store, launcher *fakeLauncher, pub event.Publisher) *plugintrigger.Dispatcher {
	return plugintrigger.NewDispatcher(plugintrigger.DispatcherConfig{
		Launcher:  launcher,
		Querier:   q,
		Dedup:     d,
		Publisher: pub,
		Logger:    nil, // uses slog.Default()
	})
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestDispatcher_DedupHit verifies that a dedup hit short-circuits dispatch
// before any policy scan or Launch call.
func TestDispatcher_DedupHit(t *testing.T) {
	t.Parallel()

	dedupStore := &fakeDedupStore{}
	k := dedup.Key{InstanceID: "inst-1", EventKind: "message", EventID: "event-abc"}
	dedupStore.markSeen(k)

	q := &fakeQuerier{
		policies: []db.Policy{{ID: "pol-1", Yaml: policyYAML("my-instance", "message", nil), UpdatedAt: "t1"}},
	}
	launcher := &fakeLauncher{}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedupStore, launcher, pub)

	evt := plugintrigger.Event{
		InstanceID:  "inst-1",
		EventKind:   "message",
		EventID:     "event-abc",
		PayloadJSON: []byte(`{}`),
		ObservedAt:  time.Now(),
	}
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if launcher.callCount() != 0 {
		t.Errorf("expected 0 launches on dedup hit, got %d", launcher.callCount())
	}
}

// TestDispatcher_DedupMiss_MatchingPolicyLaunches verifies that a dedup miss
// with a matching policy results in a Launch call.
func TestDispatcher_DedupMiss_MatchingPolicyLaunches(t *testing.T) {
	t.Parallel()

	q := &fakeQuerier{
		policies: []db.Policy{{
			ID:        "pol-1",
			Yaml:      policyYAML("my-instance", "message", nil),
			UpdatedAt: "t1",
		}},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "my-instance"},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: buildManifestYAML(t, "message", nil)},
		},
	}
	launcher := &fakeLauncher{}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	evt := plugintrigger.Event{
		InstanceID:  "inst-1",
		PluginID:    "plug-1",
		EventKind:   "message",
		EventID:     "event-1",
		PayloadJSON: []byte(`{"text":"hello"}`),
		ObservedAt:  time.Now(),
	}
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if launcher.callCount() != 1 {
		t.Errorf("expected 1 launch, got %d", launcher.callCount())
	}
}

// TestDispatcher_BindingNoMatch verifies that a non-matching binding does not
// result in a Launch call.
func TestDispatcher_BindingNoMatch(t *testing.T) {
	t.Parallel()

	// Build a binding schema with a string "channel" field.
	schema := buildStringSchema("channel")

	q := &fakeQuerier{
		policies: []db.Policy{{
			ID:        "pol-1",
			Yaml:      policyYAML("my-instance", "message", map[string]any{"channel": "#incidents"}),
			UpdatedAt: "t1",
		}},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "my-instance"},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: buildManifestYAML(t, "message", schema)},
		},
	}
	launcher := &fakeLauncher{}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	// Payload has channel=#general, not #incidents → no match.
	evt := plugintrigger.Event{
		InstanceID:  "inst-1",
		EventKind:   "message",
		EventID:     "event-2",
		PayloadJSON: []byte(`{"channel":"#general","text":"hi"}`),
		ObservedAt:  time.Now(),
	}
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if launcher.callCount() != 0 {
		t.Errorf("expected 0 launches on non-matching binding, got %d", launcher.callCount())
	}
	// plugin.event_no_match should have been published.
	published := pub.published()
	if !contains(published, "plugin.event_no_match") {
		t.Errorf("expected plugin.event_no_match in %v", published)
	}
}

// TestDispatcher_BadPolicyYAML_OtherPoliciesStillMatch verifies that a
// malformed policy YAML is skipped while other policies proceed normally.
func TestDispatcher_BadPolicyYAML_OtherPoliciesStillMatch(t *testing.T) {
	t.Parallel()

	q := &fakeQuerier{
		policies: []db.Policy{
			{ID: "bad", Yaml: "!!invalid yaml: {{{", UpdatedAt: "t1"},
			{ID: "good", Yaml: policyYAML("my-instance", "message", nil), UpdatedAt: "t1"},
		},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "my-instance"},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: buildManifestYAML(t, "message", nil)},
		},
	}
	launcher := &fakeLauncher{}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	evt := plugintrigger.Event{
		InstanceID:  "inst-1",
		EventKind:   "message",
		EventID:     "event-3",
		PayloadJSON: []byte(`{}`),
		ObservedAt:  time.Now(),
	}
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// The good policy should still launch.
	if launcher.callCount() != 1 {
		t.Errorf("expected 1 launch (bad policy skipped), got %d", launcher.callCount())
	}
}

// TestDispatcher_LaunchError_StreamStaysAlive verifies that a launcher error
// does not propagate out of Handle.
func TestDispatcher_LaunchError_StreamStaysAlive(t *testing.T) {
	t.Parallel()

	q := &fakeQuerier{
		policies: []db.Policy{{
			ID:        "pol-1",
			Yaml:      policyYAML("my-instance", "message", nil),
			UpdatedAt: "t1",
		}},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "my-instance"},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: buildManifestYAML(t, "message", nil)},
		},
	}
	launcher := &fakeLauncher{err: errors.New("launch broke")}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	evt := plugintrigger.Event{
		InstanceID:  "inst-1",
		EventKind:   "message",
		EventID:     "event-4",
		PayloadJSON: []byte(`{}`),
		ObservedAt:  time.Now(),
	}
	// Handle must not return an error even though Launch failed.
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle returned unexpected error: %v", err)
	}
}

// TestDispatcher_CacheInvalidation verifies that changing updated_at causes
// the policy to be re-parsed on the next Handle call.
func TestDispatcher_CacheInvalidation(t *testing.T) {
	t.Parallel()

	q := &fakeQuerier{
		policies: []db.Policy{{
			ID:        "pol-1",
			Yaml:      policyYAML("my-instance", "message", nil),
			UpdatedAt: "t1",
		}},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "my-instance"},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: buildManifestYAML(t, "message", nil)},
		},
	}

	// Instrument parse indirectly: each parse of a matching policy triggers a
	// Launch call, so launch count reflects the number of unique cache entries.
	launcher := &fakeLauncher{}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	// First Handle call → policy parsed and cached.
	evt := plugintrigger.Event{
		InstanceID: "inst-1", EventKind: "message", EventID: "event-5",
		PayloadJSON: []byte(`{}`), ObservedAt: time.Now(),
	}
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle 1: %v", err)
	}

	// Same event with same updated_at → cache hit.
	evt.EventID = "event-6"
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle 2: %v", err)
	}

	// Change updated_at → cache miss → re-parse.
	q.policies[0].UpdatedAt = "t2"
	evt.EventID = "event-7"
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle 3: %v", err)
	}

	// All three should have launched (one per event).
	if launcher.callCount() != 3 {
		t.Errorf("expected 3 launches, got %d", launcher.callCount())
	}
}

// TestDispatcher_ConcurrentEvents verifies that concurrent Handle calls for
// different (instance, kind) pairs do not deadlock or corrupt state.
func TestDispatcher_ConcurrentEvents(t *testing.T) {
	t.Parallel()

	q := &fakeQuerier{
		policies: []db.Policy{{
			ID:        "pol-1",
			Yaml:      policyYAML("my-instance", "message", nil),
			UpdatedAt: "t1",
		}},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "my-instance"},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: buildManifestYAML(t, "message", nil)},
		},
	}
	launcher := &fakeLauncher{}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	const goroutines = 20
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload, _ := json.Marshal(map[string]int{"n": i})
			evt := plugintrigger.Event{
				InstanceID:  "inst-1",
				EventKind:   "message",
				EventID:     fmt.Sprintf("event-%d", i),
				PayloadJSON: payload,
				ObservedAt:  time.Now(),
			}
			if err := d.Handle(context.Background(), evt); err != nil {
				t.Errorf("goroutine %d: Handle: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if launcher.callCount() != goroutines {
		t.Errorf("expected %d launches, got %d", goroutines, launcher.callCount())
	}
}

// TestDispatcher_ManifestParsedAsYAML verifies that the manifest snapshot is
// decoded using manifest.Unmarshal (YAML path), not JSON.
func TestDispatcher_ManifestParsedAsYAML(t *testing.T) {
	t.Parallel()

	// Build a real manifest and marshal it as YAML.
	manifestYAML := buildManifestYAML(t, "message", nil)

	q := &fakeQuerier{
		policies: []db.Policy{{
			ID:        "pol-1",
			Yaml:      policyYAML("my-instance", "message", nil),
			UpdatedAt: "t1",
		}},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "my-instance"},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: manifestYAML},
		},
	}
	launcher := &fakeLauncher{}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	evt := plugintrigger.Event{
		InstanceID:  "inst-1",
		EventKind:   "message",
		EventID:     "event-manifest",
		PayloadJSON: []byte(`{}`),
		ObservedAt:  time.Now(),
	}
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if launcher.callCount() != 1 {
		t.Errorf("expected 1 launch, got %d", launcher.callCount())
	}
}

// TestDispatcher_ConcurrencyCap_SkipBlocksFire verifies that when
// LaunchWithConcurrency returns OutcomeSkipped, Handle returns nil and
// plugin.event_matched was still published (binding evaluation precedes launch).
func TestDispatcher_ConcurrencyCap_SkipBlocksFire(t *testing.T) {
	t.Parallel()

	q := newBasicQuerier(t, "my-instance", "message")
	launcher := &fakeLauncher{result: run.LaunchResult{Outcome: run.OutcomeSkipped}}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	evt := plugintrigger.Event{
		InstanceID:  "inst-1",
		PluginID:    "plug-1",
		EventKind:   "message",
		EventID:     "event-skip",
		PayloadJSON: []byte(`{}`),
		ObservedAt:  time.Now(),
	}
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if launcher.callCount() != 1 {
		t.Errorf("expected 1 LaunchWithConcurrency call, got %d", launcher.callCount())
	}
	// plugin.event_matched is published before the launch attempt.
	if !contains(pub.published(), "plugin.event_matched") {
		t.Errorf("expected plugin.event_matched in %v", pub.published())
	}
}

// TestDispatcher_ConcurrencyCap_QueueFullDrops verifies that when
// LaunchWithConcurrency returns ErrConcurrencyQueueFull, exactly one
// LaunchWithConcurrency call is made carrying the parsed queue_depth, and
// Handle returns nil.
func TestDispatcher_ConcurrencyCap_QueueFullDrops(t *testing.T) {
	t.Parallel()

	const wantQueueDepth = 3

	// Inline a YAML with concurrency: queue and queue_depth: 3 under the agent block.
	yaml := `
name: test-policy
trigger:
  type: subscribed
  source: my-instance
  event_kind: message
agent:
  task: do stuff
  model:
    provider: anthropic
    name: claude-3-5-haiku-latest
  concurrency: queue
  queue_depth: 3
`
	q := &fakeQuerier{
		policies: []db.Policy{{
			ID:        "pol-queue",
			Yaml:      yaml,
			UpdatedAt: "t1",
		}},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "my-instance"},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: buildManifestYAML(t, "message", nil)},
		},
	}
	launcher := &fakeLauncher{err: run.ErrConcurrencyQueueFull}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	evt := plugintrigger.Event{
		InstanceID:  "inst-1",
		PluginID:    "plug-1",
		EventKind:   "message",
		EventID:     "event-queuefull",
		PayloadJSON: []byte(`{}`),
		ObservedAt:  time.Now(),
	}
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if launcher.callCount() != 1 {
		t.Errorf("expected 1 LaunchWithConcurrency call, got %d", launcher.callCount())
	}
	call := launcher.calls[0]
	if call.ParsedPolicy.Agent.QueueDepth != wantQueueDepth {
		t.Errorf("queue depth = %d, want %d", call.ParsedPolicy.Agent.QueueDepth, wantQueueDepth)
	}
}

// TestDispatcher_HappyPath_PayloadPropagation verifies that a matching event's
// payload is propagated byte-identical as TriggerPayload in the LaunchParams,
// and that other launch fields are set correctly.
func TestDispatcher_HappyPath_PayloadPropagation(t *testing.T) {
	t.Parallel()

	q := newBasicQuerier(t, "my-instance", "message")
	launcher := &fakeLauncher{}
	pub := &fakePublisher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, pub)

	payloadJSON := []byte(`{"channel":"#general","text":"hello"}`)
	evt := plugintrigger.Event{
		InstanceID:  "inst-1",
		PluginID:    "plug-1",
		EventKind:   "message",
		EventID:     "event-payload",
		PayloadJSON: payloadJSON,
		ObservedAt:  time.Now(),
	}
	if err := d.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if launcher.callCount() != 1 {
		t.Fatalf("expected 1 launch, got %d", launcher.callCount())
	}
	lp := launcher.calls[0]
	if lp.PolicyID != "pol-1" {
		t.Errorf("PolicyID = %q, want %q", lp.PolicyID, "pol-1")
	}
	if lp.TriggerType != model.TriggerTypeSubscribed {
		t.Errorf("TriggerType = %v, want %v", lp.TriggerType, model.TriggerTypeSubscribed)
	}
	if lp.TriggerPayload != string(payloadJSON) {
		t.Errorf("TriggerPayload = %q, want %q", lp.TriggerPayload, string(payloadJSON))
	}
	if lp.ParsedPolicy == nil {
		t.Error("ParsedPolicy is nil, want non-nil")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// newBasicQuerier returns a fakeQuerier wired with a single matching policy,
// instance, and plugin for the given source (instance name) and event kind.
// The policy ID is "pol-1".
func newBasicQuerier(t *testing.T, source, eventKind string) *fakeQuerier {
	t.Helper()
	return &fakeQuerier{
		policies: []db.Policy{{
			ID:        "pol-1",
			Yaml:      policyYAML(source, eventKind, nil),
			UpdatedAt: "t1",
		}},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: source},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: buildManifestYAML(t, eventKind, nil)},
		},
	}
}

// buildStringSchema creates a minimal YAML schema node for a single string
// property named key with no format.
func buildStringSchema(key string) *yaml.Node {
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
	return schema
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
