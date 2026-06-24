package e2e_test

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
	plugintrigger "github.com/felag-engineering/gleipnir/internal/plugin/trigger"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// stubInstanceID is used as both the plugin_instances.id column value in
// seedSubstrateData and the argument to sup.Start(..., stubInstanceID). The
// same string in both places is what lets the supervisor's instance lookup
// resolve the correct DB row when dispatching events.
const stubInstanceID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"

// ── stub trigger server ───────────────────────────────────────────────────────

// stubTriggerServer delivers events from its channel then returns (EOF).
// Mirrors the sequentialTriggerServer pattern in supervisor_test.go:26-38.
type stubTriggerServer struct {
	triggerv1.UnimplementedTriggerServiceServer
	eventsCh <-chan *triggerv1.StartResponse
}

func (s *stubTriggerServer) Start(_ *triggerv1.StartRequest, stream grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	for resp := range s.eventsCh {
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
	return nil
}

// startFakeTriggerServer binds a gRPC server in-process and returns a client
// pointing at it. Mirrors supervisor_test.go:194-214; a local copy is needed
// because that helper is unexported and lives in a different package.
func startFakeTriggerServer(t *testing.T, srv triggerv1.TriggerServiceServer) (triggerv1.TriggerServiceClient, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	triggerv1.RegisterTriggerServiceServer(gs, srv)
	go gs.Serve(lis)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	client := triggerv1.NewTriggerServiceClient(conn)
	return client, func() {
		gs.Stop()
		conn.Close()
	}
}

// ── seed helper ───────────────────────────────────────────────────────────────

// seedSubstrateData opens a temporary SQLite store, applies migrations, and
// inserts the minimal set of rows needed for the composition test:
//   - one plugin with a manifest declaring event_kind "test_event" and a
//     binding_schema with a string field "channel"
//   - one plugin_instance with id=stubInstanceID referencing that plugin
//   - a matching policy subscribed to the instance, binding channel=C-MATCH
//   - a non-matching policy with the same source+kind but binding channel=C-NOPE
//
// Returns the *db.Queries so the caller can hand it to DispatcherConfig.Querier
// without re-constructing (both share the same underlying *sql.DB).
func seedSubstrateData(t *testing.T) (matchPolicyID, noMatchPolicyID string, q *db.Queries) {
	t.Helper()
	ctx := context.Background()

	store, err := db.Open(t.TempDir() + "/e2e.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	q = db.New(store.DB())

	// Build a manifest YAML with one event kind that has a string "channel"
	// binding schema. The dispatcher uses this schema to compile the binding.
	channelSchema := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "type"},
			{Kind: yaml.ScalarNode, Value: "object"},
			{Kind: yaml.ScalarNode, Value: "properties"},
			{
				Kind: yaml.MappingNode,
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "channel"},
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
		Name:          "stub-plugin",
		Version:       "1.0.0",
		Services:      sdkmanifest.Services{Trigger: "v1"},
		EventKinds: []sdkmanifest.EventKindDecl{
			{Kind: "test_event", BindingSchema: channelSchema},
		},
	}
	manifestYAML, err := sdkmanifest.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	pluginID := model.NewULID()

	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID:               pluginID,
		Name:             "stub-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: string(manifestYAML),
		TrustedPubkey:    "",
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}

	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID:           stubInstanceID,
		PluginID:     pluginID,
		InstanceName: "stub-instance",
		ConfigJson:   "{}",
		// Non-empty scope satisfies the supervisor's scope gate (skips streams
		// for unconfigured instances). The composition test's point is to drive
		// a stream end-to-end, so we explicitly opt in.
		SubscriptionScopeJson: `{"watch":"all"}`,
		HandshakeVersions:     "{}",
		HealthState:           "healthy",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}

	// policyYAML builds a minimal subscribed policy string.
	makePolicyYAML := func(channel string) string {
		p := map[string]any{
			"name": "stub-policy-" + channel,
			"trigger": map[string]any{
				"type":       "subscribed",
				"source":     "stub-instance",
				"event_kind": "test_event",
				"binding":    map[string]any{"channel": channel},
			},
			"agent": map[string]any{
				"task": "noop",
				"model": map[string]any{
					"provider": "anthropic",
					"name":     "claude-3-5-haiku-latest",
				},
			},
		}
		b, err := yaml.Marshal(p)
		if err != nil {
			t.Fatalf("yaml.Marshal policy: %v", err)
		}
		return string(b)
	}

	matchID := model.NewULID()
	if _, err := q.CreatePolicy(ctx, db.CreatePolicyParams{
		ID:          matchID,
		Name:        "stub-match",
		TriggerType: "subscribed",
		Yaml:        makePolicyYAML("C-MATCH"),
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("CreatePolicy match: %v", err)
	}

	noMatchID := model.NewULID()
	if _, err := q.CreatePolicy(ctx, db.CreatePolicyParams{
		ID:          noMatchID,
		Name:        "stub-no-match",
		TriggerType: "subscribed",
		Yaml:        makePolicyYAML("C-NOPE"),
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("CreatePolicy no-match: %v", err)
	}

	return matchID, noMatchID, q
}

// ── fake run launcher ─────────────────────────────────────────────────────────

// fakeRunLauncher records launched policy IDs. It satisfies plugintrigger.RunLauncher
// so the composition test never touches the real agent runtime.
type fakeRunLauncher struct {
	mu       sync.Mutex
	launched []string // policy IDs in launch order
}

func (l *fakeRunLauncher) LaunchWithConcurrency(_ context.Context, params run.LaunchParams) (run.LaunchResult, error) {
	l.mu.Lock()
	l.launched = append(l.launched, params.PolicyID)
	l.mu.Unlock()
	return run.LaunchResult{RunID: model.NewULID(), Outcome: run.OutcomeLaunched}, nil
}

func (l *fakeRunLauncher) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.launched))
	copy(out, l.launched)
	return out
}

// ── fake instance lookup ──────────────────────────────────────────────────────

// staticInstanceLookup returns the same client and pluginID for every lookup.
type staticInstanceLookup struct {
	client   triggerv1.TriggerServiceClient
	pluginID string
}

func (l *staticInstanceLookup) LookupInstance(_ string) (triggerv1.TriggerServiceClient, string) {
	return l.client, l.pluginID
}

// ── tracking publisher ────────────────────────────────────────────────────────

// trackingPublisher records every Publish call name and its raw JSON payload.
type trackingPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	name    string
	payload json.RawMessage
}

func (p *trackingPublisher) Publish(name string, data json.RawMessage) {
	p.mu.Lock()
	p.events = append(p.events, publishedEvent{name: name, payload: data})
	p.mu.Unlock()
}

// policyIDsFor returns the policy_id values from all events with the given name.
func (p *trackingPublisher) policyIDsFor(name string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []string
	for _, ev := range p.events {
		if ev.name != name {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(ev.payload, &m); err != nil {
			continue
		}
		ids = append(ids, m["policy_id"])
	}
	return ids
}

// ── poll helper ───────────────────────────────────────────────────────────────

// waitFor polls fn every 5ms until it returns true or deadline expires.
// Mirrors supervisor_test.go:231-241.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

// ── stubDedup ─────────────────────────────────────────────────────────────────

// stubDedup reports every event as already-seen so the dispatcher short-circuits
// before the policy scan. Used by TestSubstrate_DedupShortCircuits_NoLaunch.
type stubDedup struct {
	hits atomic.Int32
}

func (s *stubDedup) Seen(_ context.Context, _ dedup.Key) (bool, error) {
	s.hits.Add(1)
	return true, nil
}

// Unsee is a no-op: stubDedup always reports hits, so there is never a claim to
// roll back (#585).
func (s *stubDedup) Unsee(_ context.Context, _ dedup.Key) error { return nil }

// ── tests ─────────────────────────────────────────────────────────────────────

// TestSubstrate_HappyPath_FiresMatchingPolicyOnly wires the full trigger →
// dedup → binding → RunLauncher → dispatch path using real DB rows and an
// in-process gRPC stub plugin. The event carries channel=C-MATCH so only the
// matching policy fires; the C-NOPE policy must stay silent.
//
// This is also the negative-binding assertion: because Dispatcher.Handle is
// synchronous over all policies, once the matching policy has been launched
// the non-matching policy has already been evaluated. An exact-count check
// (len==1 and launched[0]==matchPolicyID) is therefore race-free.
func TestSubstrate_HappyPath_FiresMatchingPolicyOnly(t *testing.T) {
	t.Parallel()

	matchPolicyID, noMatchPolicyID, q := seedSubstrateData(t)

	launcher := &fakeRunLauncher{}
	pub := &trackingPublisher{}

	dispatcher := plugintrigger.NewDispatcher(plugintrigger.DispatcherConfig{
		Launcher:  launcher,
		Querier:   q,
		Dedup:     dedup.Noop{},
		Publisher: pub,
	})

	evCh := make(chan *triggerv1.StartResponse, 1)
	evCh <- &triggerv1.StartResponse{
		EventId:     "evt-1",
		EventKind:   "test_event",
		PayloadJson: `{"channel":"C-MATCH"}`,
	}
	close(evCh)

	client, stop := startFakeTriggerServer(t, &stubTriggerServer{eventsCh: evCh})
	defer stop()

	sup := plugintrigger.NewSupervisor(plugintrigger.Config{
		TestInstanceLookup:  &staticInstanceLookup{client: client, pluginID: "stub-plugin"},
		TestEventDispatcher: dispatcher,
		BackoffInitial:      time.Microsecond,
		BackoffMax:          time.Millisecond,
		UnhealthyAfter:      3,
		Querier:             q,
	})
	// StopAll prevents the stream goroutine from leaking past the test. Pre-patch
	// these goroutines were terminated by the deadline ctx expiring; post-patch
	// they are parented off context.Background() (the RootCtx default), so an
	// explicit cleanup is required (#401).
	t.Cleanup(sup.StopAll)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sup.Start(ctx, stubInstanceID)

	// Wait until the launch has been recorded AND both publisher events have
	// been emitted. Handle is synchronous over the full policy list, but the
	// non-matching policy may be evaluated before OR after the matching one
	// depending on DB ordering. Waiting for both events ensures Handle has
	// completed the full policy scan before we assert.
	waitFor(t, 5*time.Second, func() bool {
		launched := launcher.snapshot()
		matched := pub.policyIDsFor("plugin.event_matched")
		noMatch := pub.policyIDsFor("plugin.event_no_match")
		return len(launched) >= 1 && len(matched) >= 1 && len(noMatch) >= 1
	})

	launched := launcher.snapshot()
	if len(launched) != 1 {
		t.Errorf("launched policy count: want 1, got %d (%v)", len(launched), launched)
	} else if launched[0] != matchPolicyID {
		t.Errorf("launched policy: want %q (match), got %q", matchPolicyID, launched[0])
	}

	// plugin.event_matched must reference the matching policy.
	matched := pub.policyIDsFor("plugin.event_matched")
	if len(matched) != 1 || matched[0] != matchPolicyID {
		t.Errorf("plugin.event_matched policy_ids: want [%q], got %v", matchPolicyID, matched)
	}

	// plugin.event_no_match must reference the non-matching policy.
	noMatch := pub.policyIDsFor("plugin.event_no_match")
	if len(noMatch) != 1 || noMatch[0] != noMatchPolicyID {
		t.Errorf("plugin.event_no_match policy_ids: want [%q], got %v", noMatchPolicyID, noMatch)
	}
}

// TestSubstrate_DedupShortCircuits_NoLaunch verifies that a dedup hit prevents
// any policy from being evaluated or launched. The dispatcher exits before
// scanning policies when Seen returns true.
func TestSubstrate_DedupShortCircuits_NoLaunch(t *testing.T) {
	t.Parallel()

	_, _, q := seedSubstrateData(t)

	launcher := &fakeRunLauncher{}
	pub := &trackingPublisher{}
	dd := &stubDedup{}

	dispatcher := plugintrigger.NewDispatcher(plugintrigger.DispatcherConfig{
		Launcher:  launcher,
		Querier:   q,
		Dedup:     dd,
		Publisher: pub,
	})

	evCh := make(chan *triggerv1.StartResponse, 1)
	evCh <- &triggerv1.StartResponse{
		EventId:     "evt-dedup",
		EventKind:   "test_event",
		PayloadJson: `{"channel":"C-MATCH"}`,
	}
	close(evCh)

	client, stop := startFakeTriggerServer(t, &stubTriggerServer{eventsCh: evCh})
	defer stop()

	sup := plugintrigger.NewSupervisor(plugintrigger.Config{
		TestInstanceLookup:  &staticInstanceLookup{client: client, pluginID: "stub-plugin"},
		TestEventDispatcher: dispatcher,
		BackoffInitial:      time.Microsecond,
		BackoffMax:          time.Millisecond,
		UnhealthyAfter:      3,
		Querier:             q,
	})
	// StopAll prevents the stream goroutine from leaking past the test. Pre-patch
	// these goroutines were terminated by the deadline ctx expiring; post-patch
	// they are parented off context.Background() (the RootCtx default), so an
	// explicit cleanup is required (#401).
	t.Cleanup(sup.StopAll)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sup.Start(ctx, stubInstanceID)

	// Wait for the dedup store to be consulted, which means the event reached Handle.
	waitFor(t, 5*time.Second, func() bool {
		return dd.hits.Load() >= 1
	})

	// Give Handle a moment to complete (it returns immediately after dedup hit).
	time.Sleep(20 * time.Millisecond)

	if launched := launcher.snapshot(); len(launched) != 0 {
		t.Errorf("launched policy count: want 0 (dedup short-circuit), got %d (%v)", len(launched), launched)
	}
	if got := pub.policyIDsFor("plugin.event_matched"); len(got) != 0 {
		t.Errorf("plugin.event_matched: want 0 (dedup blocks policy scan), got %v", got)
	}
}

// Compile-time check: event.Publisher is satisfied by trackingPublisher.
var _ event.Publisher = (*trackingPublisher)(nil)
