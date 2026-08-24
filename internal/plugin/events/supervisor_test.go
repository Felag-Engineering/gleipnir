package events_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// ── fakes ────────────────────────────────────────────────────────────────────

// fakeSupervisorQuerier satisfies events.PluginQuerier. instances/plugins are
// mutable under mu so tests (e.g. a scope change between connects) can update
// state the supervisor re-reads on every reconnect attempt.
type fakeSupervisorQuerier struct {
	mu        sync.Mutex
	instances map[string]db.PluginInstance
	plugins   map[string]db.Plugin
}

func newFakeSupervisorQuerier() *fakeSupervisorQuerier {
	return &fakeSupervisorQuerier{
		instances: make(map[string]db.PluginInstance),
		plugins:   make(map[string]db.Plugin),
	}
}

func (q *fakeSupervisorQuerier) setInstance(inst db.PluginInstance) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.instances[inst.ID] = inst
}

func (q *fakeSupervisorQuerier) setPlugin(p db.Plugin) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.plugins[p.ID] = p
}

func (q *fakeSupervisorQuerier) ListPluginsByStatus(context.Context, string) ([]db.Plugin, error) {
	return nil, nil
}
func (q *fakeSupervisorQuerier) ListPluginInstancesByPlugin(context.Context, string) ([]db.PluginInstance, error) {
	return nil, nil
}
func (q *fakeSupervisorQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if inst, ok := q.instances[id]; ok {
		return inst, nil
	}
	return db.PluginInstance{}, errors.New("instance not found: " + id)
}
func (q *fakeSupervisorQuerier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if p, ok := q.plugins[id]; ok {
		return p, nil
	}
	return db.Plugin{}, errors.New("plugin not found: " + id)
}

// buildEventManifestYAML produces a minimal manifest YAML declaring the given
// event kinds (attested by the plugin), mirroring the shape
// internal/plugin/trigger's own test helper builds.
func buildEventManifestYAML(t *testing.T, kinds ...string) string {
	t.Helper()
	m := &sdkmanifest.Manifest{
		SchemaVersion: "1",
		Name:          "test-plugin",
		Version:       "1.0.0",
	}
	for _, k := range kinds {
		m.EventKinds = append(m.EventKinds, sdkmanifest.EventKindDecl{Kind: k})
	}
	b, err := sdkmanifest.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(b)
}

// fakeStream is a fully test-controlled events.EventStream: it replays a
// fixed slice of events and then, when exhausted, either returns endErr (a
// terminal sentinel returned IMMEDIATELY — no timers, no wall-clock waiting)
// or blocks on ctx.Done() to model an idle-but-connected stream.
type fakeStream struct {
	mu     sync.Mutex
	events []mcp.CloudEvent
	idx    int
	endErr error
	closed bool
}

func (s *fakeStream) Next(ctx context.Context) (mcp.CloudEvent, error) {
	s.mu.Lock()
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		s.mu.Unlock()
		return ev, nil
	}
	endErr := s.endErr
	s.mu.Unlock()
	if endErr != nil {
		return mcp.CloudEvent{}, endErr
	}
	<-ctx.Done()
	return mcp.CloudEvent{}, ctx.Err()
}

func (s *fakeStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

// fakeOpener records every ListenEvents call and produces a stream from
// newStream, called with the 0-based attempt index for that instance.
type fakeOpener struct {
	newStream func(attempt int) *fakeStream

	mu    sync.Mutex
	calls []mcp.EventsListenParams
	// opened is signaled (non-blocking) after each ListenEvents call so tests
	// can synchronize on "a connect attempt happened" instead of polling.
	opened chan mcp.EventsListenParams
}

func newFakeOpener(newStream func(attempt int) *fakeStream) *fakeOpener {
	return &fakeOpener{newStream: newStream, opened: make(chan mcp.EventsListenParams, 64)}
}

func (o *fakeOpener) ListenEvents(_ context.Context, p mcp.EventsListenParams) (events.EventStream, error) {
	o.mu.Lock()
	attempt := len(o.calls)
	o.calls = append(o.calls, p)
	o.mu.Unlock()
	select {
	case o.opened <- p:
	default:
	}
	return o.newStream(attempt), nil
}

func (o *fakeOpener) callCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.calls)
}

func (o *fakeOpener) paramsAt(i int) mcp.EventsListenParams {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls[i]
}

// fakeResolver satisfies events.StreamResolver, returning a single fixed
// opener (or an error) for every instance.
type fakeResolver struct {
	opener events.StreamOpener
	err    error
}

func (r *fakeResolver) ResolveStream(context.Context, string) (events.StreamOpener, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.opener, nil
}

// fakeCapability satisfies events.CapabilityChecker with a mutable verdict.
type fakeCapability struct {
	mu     sync.Mutex
	serves bool
}

func (c *fakeCapability) setServes(v bool) {
	c.mu.Lock()
	c.serves = v
	c.mu.Unlock()
}

func (c *fakeCapability) Serves(string, caphealth.Capability) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serves
}

// fakeSink satisfies events.Sink. When block is non-nil, Handle waits for it
// to be closed before recording the event and returning — the seam the
// cursor-timing tests use to observe "not yet advanced while the sink is
// still running".
type fakeSink struct {
	block chan struct{}
	err   error

	mu      sync.Mutex
	handled []events.Event
	entered chan events.Event // signaled the instant Handle is invoked, before blocking
}

func newFakeSink() *fakeSink {
	return &fakeSink{entered: make(chan events.Event, 64)}
}

func (s *fakeSink) Handle(ctx context.Context, e events.Event) error {
	select {
	case s.entered <- e:
	default:
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.handled = append(s.handled, e)
	s.mu.Unlock()
	return s.err
}

func (s *fakeSink) received() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]events.Event, len(s.handled))
	copy(out, s.handled)
	return out
}

// fakeCursorStore satisfies events.Store with an in-memory map and a
// non-blocking "advanced" notification channel — the signal-don't-poll
// synchronization point for cursor-timing assertions.
type fakeCursorStore struct {
	mu       sync.Mutex
	rows     map[string]fakeCursorRow
	advanced chan struct{}
}

type fakeCursorRow struct {
	cursor string
	seq    uint64
}

func newFakeCursorStore() *fakeCursorStore {
	return &fakeCursorStore{rows: make(map[string]fakeCursorRow), advanced: make(chan struct{}, 64)}
}

func cursorKey(instanceID, scopeHash string) string { return instanceID + "|" + scopeHash }

func (s *fakeCursorStore) Load(_ context.Context, instanceID, scopeHash string) (string, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[cursorKey(instanceID, scopeHash)]
	return row.cursor, row.seq, nil
}

func (s *fakeCursorStore) Advance(_ context.Context, instanceID, scopeHash, cursor string, seq uint64) error {
	s.mu.Lock()
	s.rows[cursorKey(instanceID, scopeHash)] = fakeCursorRow{cursor: cursor, seq: seq}
	s.mu.Unlock()
	select {
	case s.advanced <- struct{}{}:
	default:
	}
	return nil
}

func (s *fakeCursorStore) Reset(_ context.Context, instanceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.rows {
		if strings.HasPrefix(k, instanceID+"|") {
			delete(s.rows, k)
		}
	}
	return nil
}

// getAny returns the row for instanceID regardless of scope hash. Every test
// in this file has at most one active (kinds, scope) combination per
// instance at the moment it calls this, so there is no ambiguity — and it
// avoids needing to reproduce the package-private scopeHash function in
// tests just to look a row up.
func (s *fakeCursorStore) getAny(instanceID string) (string, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, row := range s.rows {
		if strings.HasPrefix(k, instanceID+"|") {
			return row.cursor, row.seq
		}
	}
	return "", 0
}

// ── test helpers ─────────────────────────────────────────────────────────────

const testTimeout = 5 * time.Second

func waitOnChan[T any](t *testing.T, ch <-chan T, timeout time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func newTestSupervisor(t *testing.T, q *fakeSupervisorQuerier, resolver events.StreamResolver, capChecker events.CapabilityChecker, cursor events.Store, sink events.Sink, healthSetter func(context.Context, string, model.PluginHealthState, string)) *events.Supervisor {
	t.Helper()
	return events.NewSupervisor(events.Config{
		Querier:            q,
		TestStreamResolver: resolver,
		Capability:         capChecker,
		Cursor:             cursor,
		Sink:               sink,
		HealthSetter:       healthSetter,
		BackoffInitial:     time.Microsecond,
		BackoffMax:         5 * time.Millisecond,
		UnhealthyAfter:     3,
	})
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestSupervisor_CapabilityGate_WaitsThenOpens verifies that a capability
// reporting the event_source profile as not-serving prevents the stream from
// opening at all, and that flipping the verdict lets it open.
func TestSupervisor_CapabilityGate_WaitsThenOpens(t *testing.T) {
	t.Parallel()

	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{}`})
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: buildEventManifestYAML(t, "message")})

	opener := newFakeOpener(func(int) *fakeStream { return &fakeStream{} })
	capChecker := &fakeCapability{serves: false}
	sup := newTestSupervisor(t, q, &fakeResolver{opener: opener}, capChecker, newFakeCursorStore(), newFakeSink(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-1")
	t.Cleanup(sup.StopAll)

	// Negative assertion: give the gate several backoff ticks to (wrongly)
	// open a stream, bounded by a short deadline rather than a poll loop.
	select {
	case <-opener.opened:
		t.Fatal("stream opened while capability reported not-serving")
	case <-time.After(50 * time.Millisecond):
	}

	capChecker.setServes(true)
	waitOnChan(t, opener.opened, testTimeout, "stream to open once capability serves")
}

// TestSupervisor_EmptyEventKinds_Waits verifies that a plugin declaring no
// event kinds never opens a stream.
func TestSupervisor_EmptyEventKinds_Waits(t *testing.T) {
	t.Parallel()

	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{}`})
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: buildEventManifestYAML(t)}) // no kinds

	opener := newFakeOpener(func(int) *fakeStream { return &fakeStream{} })
	sup := newTestSupervisor(t, q, &fakeResolver{opener: opener}, nil, newFakeCursorStore(), newFakeSink(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-1")
	t.Cleanup(sup.StopAll)

	select {
	case <-opener.opened:
		t.Fatal("stream opened for a plugin declaring no event kinds")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestSupervisor_CursorAdvancesOnlyAfterHandleReturns verifies that a
// blocking sink is observed NOT to have advanced the cursor, and that
// unblocking it advances the cursor to the delivered event's sequence.
func TestSupervisor_CursorAdvancesOnlyAfterHandleReturns(t *testing.T) {
	t.Parallel()

	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{}`})
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: buildEventManifestYAML(t, "message")})

	ev := mcp.CloudEvent{SpecVersion: "1.0", Source: "src", Type: "message", ID: "e1", Sequence: 5}
	opener := newFakeOpener(func(int) *fakeStream {
		return &fakeStream{events: []mcp.CloudEvent{ev}}
	})

	sink := newFakeSink()
	sink.block = make(chan struct{})
	cursor := newFakeCursorStore()

	sup := newTestSupervisor(t, q, &fakeResolver{opener: opener}, nil, cursor, sink, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-1")
	t.Cleanup(sup.StopAll)

	waitOnChan(t, sink.entered, testTimeout, "sink to be entered")

	// The sink is still blocked: the cursor must not have advanced yet.
	if _, seq := cursor.getAny("inst-1"); seq != 0 {
		t.Fatalf("cursor advanced to seq %d before the blocking sink returned", seq)
	}

	close(sink.block)

	waitOnChan(t, cursor.advanced, testTimeout, "cursor to advance")
	if _, seq := cursor.getAny("inst-1"); seq != 5 {
		t.Errorf("cursor seq = %d, want 5", seq)
	}
}

// TestSupervisor_ErroringSinkStillAdvances verifies that a sink returning an
// error still results in the cursor being advanced — the dispatcher's own
// claim/rollback owns redelivery from there.
func TestSupervisor_ErroringSinkStillAdvances(t *testing.T) {
	t.Parallel()

	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{}`})
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: buildEventManifestYAML(t, "message")})

	ev := mcp.CloudEvent{SpecVersion: "1.0", Source: "src", Type: "message", ID: "e1", Sequence: 9}
	opener := newFakeOpener(func(int) *fakeStream {
		return &fakeStream{events: []mcp.CloudEvent{ev}}
	})

	sink := newFakeSink()
	sink.err = errors.New("sink boom")
	cursor := newFakeCursorStore()

	sup := newTestSupervisor(t, q, &fakeResolver{opener: opener}, nil, cursor, sink, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-1")
	t.Cleanup(sup.StopAll)

	waitOnChan(t, cursor.advanced, testTimeout, "cursor to advance despite sink error")
	if _, seq := cursor.getAny("inst-1"); seq != 9 {
		t.Errorf("cursor seq = %d, want 9", seq)
	}
	if got := sink.received(); len(got) != 1 || got[0].EventID != "e1" {
		t.Errorf("sink.received() = %v, want one event with ID e1 (the sink still ran despite returning an error)", got)
	}
}

// TestSupervisor_HeartbeatStarvation_MarksUnhealthy_ThenRecovers verifies the
// heartbeat sentinel is treated as a counted failure toward UnhealthyAfter,
// and that a subsequent successful event clears it — entirely without
// wall-clock waiting: the fake stream returns mcp.ErrEventsHeartbeatTimeout
// immediately rather than after any real timer, so every reconnect attempt
// resolves as fast as the scheduler allows.
func TestSupervisor_HeartbeatStarvation_MarksUnhealthy_ThenRecovers(t *testing.T) {
	t.Parallel()

	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{}`})
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: buildEventManifestYAML(t, "message")})

	const unhealthyAfter = 3
	recoveryEvent := mcp.CloudEvent{SpecVersion: "1.0", Source: "src", Type: "message", ID: "recovered", Sequence: 1}

	opener := newFakeOpener(func(attempt int) *fakeStream {
		if attempt < unhealthyAfter {
			return &fakeStream{endErr: mcp.ErrEventsHeartbeatTimeout}
		}
		return &fakeStream{events: []mcp.CloudEvent{recoveryEvent}}
	})

	health := make(chan model.PluginHealthState, 64)
	healthSetter := func(_ context.Context, _ string, state model.PluginHealthState, _ string) {
		select {
		case health <- state:
		default:
		}
	}

	sup := events.NewSupervisor(events.Config{
		Querier:            q,
		TestStreamResolver: &fakeResolver{opener: opener},
		Cursor:             newFakeCursorStore(),
		Sink:               newFakeSink(),
		HealthSetter:       healthSetter,
		BackoffInitial:     time.Microsecond,
		BackoffMax:         time.Microsecond,
		UnhealthyAfter:     unhealthyAfter,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-1")
	t.Cleanup(sup.StopAll)

	var sawUnhealthy, sawRecovery bool
	deadline := time.After(testTimeout)
	for !sawRecovery {
		select {
		case s := <-health:
			if s == model.PluginHealthStateUnhealthy {
				sawUnhealthy = true
			}
			if s == model.PluginHealthStateHealthy && sawUnhealthy {
				sawRecovery = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for unhealthy-then-recovery (sawUnhealthy=%v)", sawUnhealthy)
		}
	}
}

// TestSupervisor_CleanClose_ReconnectsFromCarriedCursor verifies that a
// clean server close is not counted as a failure and that a newer carried
// cursor is applied so the next connect resumes from it.
func TestSupervisor_CleanClose_ReconnectsFromCarriedCursor(t *testing.T) {
	t.Parallel()

	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{}`})
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: buildEventManifestYAML(t, "message")})

	firstEvent := mcp.CloudEvent{SpecVersion: "1.0", Source: "src", Type: "message", ID: "e1", Sequence: 3}
	closeErr := &mcp.EventsStreamClosed{Reason: "restart", Cursor: "7"}

	opener := newFakeOpener(func(attempt int) *fakeStream {
		if attempt == 0 {
			return &fakeStream{events: []mcp.CloudEvent{firstEvent}, endErr: closeErr}
		}
		// Second connect: block forever (nothing further to assert).
		return &fakeStream{}
	})

	sup := newTestSupervisor(t, q, &fakeResolver{opener: opener}, nil, newFakeCursorStore(), newFakeSink(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-1")
	t.Cleanup(sup.StopAll)

	waitOnChan(t, opener.opened, testTimeout, "first connect")
	waitOnChan(t, opener.opened, testTimeout, "reconnect after clean close")

	if opener.callCount() < 2 {
		t.Fatalf("expected at least 2 connect attempts, got %d", opener.callCount())
	}
	second := opener.paramsAt(1)
	if second.Cursor != "7" {
		t.Errorf("reconnect Cursor = %q, want %q (the clean close's carried cursor, newer than the delivered event's seq 3)", second.Cursor, "7")
	}
}

// TestSupervisor_ScopeChange_ResetsCursorOnReconnect verifies that a scope
// change between connects (via Restart, mirroring the operator save flow)
// resets the resume point to empty with no special code path — Store.Load
// under a fresh scopeHash reports "no cursor" by construction.
func TestSupervisor_ScopeChange_ResetsCursorOnReconnect(t *testing.T) {
	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{"a":1}`})
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: buildEventManifestYAML(t, "message")})

	firstEvent := mcp.CloudEvent{SpecVersion: "1.0", Source: "src", Type: "message", ID: "e1", Sequence: 4}
	opener := newFakeOpener(func(int) *fakeStream {
		return &fakeStream{events: []mcp.CloudEvent{firstEvent}}
	})

	sup := newTestSupervisor(t, q, &fakeResolver{opener: opener}, nil, newFakeCursorStore(), newFakeSink(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-1")
	t.Cleanup(sup.StopAll)

	waitOnChan(t, opener.opened, testTimeout, "first connect")
	first := opener.paramsAt(0)
	if first.Cursor != "" {
		t.Fatalf("first connect Cursor = %q, want empty", first.Cursor)
	}

	// Operator changes scope, then the admin handler would call Restart.
	// The first connect delivered one event and then blocks (no endErr), so
	// Restart's cancel is what produces the second connect attempt, under
	// the new scope.
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{"a":2}`})
	sup.Restart(ctx, "inst-1")

	second := waitOnChan(t, opener.opened, testTimeout, "reconnect under new scope")
	if second.Cursor != "" {
		t.Errorf("reconnect under new scope Cursor = %q, want empty (scope change must reset resume point)", second.Cursor)
	}
}

// TestSupervisor_StopAll_JoinsGoroutines verifies StopAll cancels and joins
// every running stream goroutine.
func TestSupervisor_StopAll_JoinsGoroutines(t *testing.T) {
	t.Parallel()

	q := newFakeSupervisorQuerier()
	for _, id := range []string{"inst-a", "inst-b", "inst-c"} {
		q.setInstance(db.PluginInstance{ID: id, PluginID: "plug-1", SubscriptionScopeJson: `{}`})
	}
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: buildEventManifestYAML(t, "message")})

	opener := newFakeOpener(func(int) *fakeStream { return &fakeStream{} }) // blocks on ctx.Done()
	sup := newTestSupervisor(t, q, &fakeResolver{opener: opener}, nil, newFakeCursorStore(), newFakeSink(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-a")
	sup.Start(ctx, "inst-b")
	sup.Start(ctx, "inst-c")

	waitOnChan(t, opener.opened, testTimeout, "at least one stream open")

	done := make(chan struct{})
	go func() {
		sup.StopAll()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("StopAll did not complete in time")
	}
}
