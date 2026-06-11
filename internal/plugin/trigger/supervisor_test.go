package trigger_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	plugintrigger "github.com/felag-engineering/gleipnir/internal/plugin/trigger"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
)

// ── fake trigger service (plugin side) ───────────────────────────────────────

// sequentialTriggerServer sends all events from eventsCh once Start is called,
// then returns nil (EOF for gRPC server streams).
type sequentialTriggerServer struct {
	triggerv1.UnimplementedTriggerServiceServer
	eventsCh <-chan *triggerv1.StartResponse
}

func (s *sequentialTriggerServer) Start(_ *triggerv1.StartRequest, stream grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	for resp := range s.eventsCh {
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
	return nil
}

// errorThenEventServer fails the first Start call (immediate EOF) and serves
// events from eventsCh on subsequent calls.
type errorThenEventServer struct {
	triggerv1.UnimplementedTriggerServiceServer
	mu       sync.Mutex
	calls    int
	eventsCh <-chan *triggerv1.StartResponse
}

func (s *errorThenEventServer) Start(_ *triggerv1.StartRequest, stream grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	s.mu.Lock()
	s.calls++
	isFirst := s.calls == 1
	s.mu.Unlock()

	if isFirst {
		return io.EOF // immediate EOF on first call → supervisor reconnects
	}
	for resp := range s.eventsCh {
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
	return nil
}

func (s *errorThenEventServer) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// blockingServer never sends events, blocking Recv until the stream context is
// cancelled.
type blockingServer struct {
	triggerv1.UnimplementedTriggerServiceServer
}

func (s *blockingServer) Start(_ *triggerv1.StartRequest, stream grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	<-stream.Context().Done()
	return stream.Context().Err()
}

// alwaysEOFServer immediately returns nil (EOF) from Start.
type alwaysEOFServer struct {
	triggerv1.UnimplementedTriggerServiceServer
	mu    sync.Mutex
	calls int
}

func (s *alwaysEOFServer) Start(_ *triggerv1.StartRequest, _ grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return nil
}

func (s *alwaysEOFServer) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// recoverServer fails the first failCount Start calls then sends events from ch.
type recoverServer struct {
	triggerv1.UnimplementedTriggerServiceServer
	mu        sync.Mutex
	calls     int
	failCount int
	ch        <-chan *triggerv1.StartResponse
}

func (s *recoverServer) Start(_ *triggerv1.StartRequest, stream grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	s.mu.Lock()
	s.calls++
	isFailure := s.calls <= s.failCount
	s.mu.Unlock()

	if isFailure {
		return nil // immediate EOF
	}
	for resp := range s.ch {
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
	return nil
}

// singleEventThenBlockServer sends one event then blocks until the stream
// context is cancelled, mimicking a long-lived connected plugin.  This prevents
// the reconnect loop from accumulating consecutive failures during tests that
// want to observe exactly what happens before the first event.
type singleEventThenBlockServer struct {
	triggerv1.UnimplementedTriggerServiceServer
	event *triggerv1.StartResponse
}

func (s *singleEventThenBlockServer) Start(_ *triggerv1.StartRequest, stream grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	if err := stream.Send(s.event); err != nil {
		return err
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

// ── fake InstanceLookup ───────────────────────────────────────────────────────

type fakeInstanceLookup struct {
	mu       sync.Mutex
	client   triggerv1.TriggerServiceClient
	pluginID string
}

func (l *fakeInstanceLookup) LookupInstance(_ string) (triggerv1.TriggerServiceClient, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.client, l.pluginID
}

// ── fake EventDispatcher ──────────────────────────────────────────────────────

type fakeEventDispatcher struct {
	mu     sync.Mutex
	events []plugintrigger.Event
}

func (d *fakeEventDispatcher) Handle(_ context.Context, evt plugintrigger.Event) error {
	d.mu.Lock()
	d.events = append(d.events, evt)
	d.mu.Unlock()
	return nil
}

func (d *fakeEventDispatcher) received() []plugintrigger.Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]plugintrigger.Event, len(d.events))
	copy(out, d.events)
	return out
}

// ── fake Querier for supervisor tests ─────────────────────────────────────────

type supQuerier struct {
	instByID map[string]db.PluginInstance
}

func (q *supQuerier) GetSubscribedActivePolicies(_ context.Context) ([]db.Policy, error) {
	return nil, nil
}
func (q *supQuerier) GetPluginByID(_ context.Context, _ string) (db.Plugin, error) {
	return db.Plugin{}, nil
}
func (q *supQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	if inst, ok := q.instByID[id]; ok {
		return inst, nil
	}
	// Return a non-empty scope by default so existing tests that do not care
	// about scope gating continue to reach the stream-open path.
	return db.PluginInstance{ID: id, SubscriptionScopeJson: `{"_default":true}`}, nil
}
func (q *supQuerier) ListPluginsByStatus(_ context.Context, _ string) ([]db.Plugin, error) {
	return nil, nil
}
func (q *supQuerier) ListPluginInstancesByPlugin(_ context.Context, _ string) ([]db.PluginInstance, error) {
	return nil, nil
}

// ── test helpers ─────────────────────────────────────────────────────────────

// startFakeTriggerServer starts an in-process gRPC server with the given
// TriggerServiceServer and returns a TriggerServiceClient pointing at it.
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

// newSupervisor builds a supervisor with test fakes for lookup and dispatcher.
func newSupervisor(lookup plugintrigger.InstanceLookup, dispatcher plugintrigger.EventDispatcher, healthSetter func(context.Context, string, model.PluginHealthState, string)) *plugintrigger.Supervisor {
	q := &supQuerier{}
	return plugintrigger.NewSupervisor(plugintrigger.Config{
		Querier:             q,
		HealthSetter:        healthSetter,
		BackoffInitial:      time.Microsecond, // near-instant for tests
		BackoffMax:          time.Millisecond,
		UnhealthyAfter:      3,
		TestInstanceLookup:  lookup,
		TestEventDispatcher: dispatcher,
	})
}

// waitFor polls fn until it returns true or timeout expires.
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

// ── tests ─────────────────────────────────────────────────────────────────────

// TestSupervisor_OrderPreservation verifies that N events from the stream reach
// the dispatcher in the same order they were sent.
func TestSupervisor_OrderPreservation(t *testing.T) {
	t.Parallel()

	const N = 10
	evCh := make(chan *triggerv1.StartResponse, N)
	for i := range N {
		evCh <- &triggerv1.StartResponse{
			EventId:     fmt.Sprintf("event-%d", i),
			EventKind:   "message",
			PayloadJson: "{}",
		}
	}
	close(evCh)

	client, stop := startFakeTriggerServer(t, &sequentialTriggerServer{eventsCh: evCh})
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}
	sup := newSupervisor(lookup, dispatcher, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sup.Start(ctx, "inst-1")

	waitFor(t, 5*time.Second, func() bool {
		return len(dispatcher.received()) >= N
	})

	got := dispatcher.received()
	if len(got) != N {
		t.Fatalf("expected %d events, got %d", N, len(got))
	}
	for i, evt := range got {
		want := fmt.Sprintf("event-%d", i)
		if evt.EventID != want {
			t.Errorf("position %d: got EventID=%q, want %q", i, evt.EventID, want)
		}
	}
}

// TestSupervisor_ReconnectsOnEOF verifies that a stream returning EOF causes
// the supervisor to reconnect and continue dispatching events.
func TestSupervisor_ReconnectsOnEOF(t *testing.T) {
	t.Parallel()

	// First stream: empty → immediate EOF. Second stream: one event.
	laterCh := make(chan *triggerv1.StartResponse, 1)
	laterCh <- &triggerv1.StartResponse{EventId: "after-reconnect", EventKind: "message"}
	close(laterCh)

	srv := &errorThenEventServer{eventsCh: laterCh}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}
	sup := newSupervisor(lookup, dispatcher, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sup.Start(ctx, "inst-1")

	waitFor(t, 8*time.Second, func() bool {
		got := dispatcher.received()
		return len(got) >= 1 && got[0].EventID == "after-reconnect"
	})

	if srv.startCount() < 2 {
		t.Errorf("expected at least 2 Start calls, got %d", srv.startCount())
	}
}

// TestSupervisor_ContextCancel verifies that cancelling the supervisor's root
// context (not the per-call Start ctx) causes the stream goroutine to exit,
// observable via StopAll returning. This exercises the real contract post-#401:
// the goroutine is parented off rootCtx, so only rootCtx cancellation (or an
// explicit Stop/StopAll) can drive exit (#401).
func TestSupervisor_ContextCancel(t *testing.T) {
	t.Parallel()

	srv := &blockingServer{}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}
	q := &supQuerier{}

	// Build the supervisor with an explicit, cancellable RootCtx so we can
	// verify that it is the rootCtx — not the per-call Start arg — that drives
	// goroutine exit (#401).
	rootCtx, rootCancel := context.WithCancel(context.Background())
	sup := plugintrigger.NewSupervisor(plugintrigger.Config{
		Querier:             q,
		BackoffInitial:      time.Microsecond,
		BackoffMax:          time.Millisecond,
		UnhealthyAfter:      3,
		TestInstanceLookup:  lookup,
		TestEventDispatcher: dispatcher,
		RootCtx:             rootCtx,
	})

	// Pass a separate, independent ctx to Start — it must not affect the
	// goroutine's lifetime under the new contract.
	sup.Start(context.Background(), "inst-1")

	// Give the goroutine time to reach stream.Recv before cancelling.
	time.Sleep(30 * time.Millisecond)

	// Cancel the root context, which is the actual parent of the stream goroutine.
	rootCancel()

	done := make(chan struct{})
	go func() {
		sup.StopAll()
		close(done)
	}()

	select {
	case <-done:
		// Clean exit — goroutine exited after rootCtx cancel.
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor goroutine did not exit within 5s after rootCtx cancel")
	}
}

// TestSupervisor_StopAll_JoinsWithoutRootCancel proves StopAll synchronously
// cancels and joins a live stream goroutine WITHOUT a prior rootCtx cancel.
//
// This is the exact contract the shutdown quiesce relies on (#500): main.go
// calls TriggerSupervisor.StopAll() before the run-drain wait while the root
// ctx may still be effectively live for in-flight work. StopAll must drive the
// goroutine to exit on its own (via the per-stream child ctx) and block until
// it has, so no further RunLauncher.Launch can land after StopAll returns.
func TestSupervisor_StopAll_JoinsWithoutRootCancel(t *testing.T) {
	t.Parallel()

	srv := &blockingServer{}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}
	q := &supQuerier{}

	// RootCtx stays live for the whole test — we never cancel it. StopAll alone
	// must drive the goroutine to exit.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sup := plugintrigger.NewSupervisor(plugintrigger.Config{
		Querier:             q,
		BackoffInitial:      time.Microsecond,
		BackoffMax:          time.Millisecond,
		UnhealthyAfter:      3,
		TestInstanceLookup:  lookup,
		TestEventDispatcher: dispatcher,
		RootCtx:             rootCtx,
	})

	sup.Start(context.Background(), "inst-1")

	// Let the goroutine reach stream.Recv (blocked in blockingServer).
	time.Sleep(30 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		sup.StopAll()
		close(done)
	}()

	select {
	case <-done:
		// StopAll returned only after the goroutine exited — quiesce works.
	case <-time.After(5 * time.Second):
		t.Fatal("StopAll did not join the stream goroutine within 5s (no rootCtx cancel)")
	}
}

// TestSupervisor_UnhealthyAfterConsecutiveFailures verifies that the health
// setter is called with Unhealthy after UnhealthyAfter consecutive EOF streams.
func TestSupervisor_UnhealthyAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()

	srv := &alwaysEOFServer{}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	var mu sync.Mutex
	var unhealthyCalled atomic.Bool

	healthSetter := func(_ context.Context, _ string, state model.PluginHealthState, _ string) {
		if state == model.PluginHealthStateUnhealthy {
			mu.Lock()
			unhealthyCalled.Store(true)
			mu.Unlock()
		}
	}

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}
	sup := newSupervisor(lookup, dispatcher, healthSetter)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sup.Start(ctx, "inst-1")

	waitFor(t, 8*time.Second, func() bool {
		return unhealthyCalled.Load()
	})

	if !unhealthyCalled.Load() {
		t.Fatal("HealthSetter never called with Unhealthy")
	}
}

// TestSupervisor_RecoveryAfterFailures verifies that after marking Unhealthy,
// a successful stream Recv resets the health to Healthy.
func TestSupervisor_RecoveryAfterFailures(t *testing.T) {
	t.Parallel()

	recoveryCh := make(chan *triggerv1.StartResponse, 1)
	srv := &recoverServer{failCount: 3, ch: recoveryCh}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	type healthEvent struct {
		state  model.PluginHealthState
		detail string
	}
	var mu sync.Mutex
	var healthEvents []healthEvent

	healthSetter := func(_ context.Context, _ string, state model.PluginHealthState, detail string) {
		mu.Lock()
		healthEvents = append(healthEvents, healthEvent{state, detail})
		mu.Unlock()
	}

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}
	sup := newSupervisor(lookup, dispatcher, healthSetter)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sup.Start(ctx, "inst-1")

	// Wait for Unhealthy to be reported.
	waitFor(t, 8*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range healthEvents {
			if e.state == model.PluginHealthStateUnhealthy {
				return true
			}
		}
		return false
	})

	// Now send a recovery event.
	recoveryCh <- &triggerv1.StartResponse{EventId: "recovery", EventKind: "message"}
	close(recoveryCh)

	// Wait for a recovery Healthy (one that comes after an Unhealthy event).
	waitFor(t, 8*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		var sawU bool
		for _, e := range healthEvents {
			if e.state == model.PluginHealthStateUnhealthy {
				sawU = true
			}
			if e.state == model.PluginHealthStateHealthy && sawU {
				return true
			}
		}
		return false
	})

	// Verify the recovery sequence: there must be an Unhealthy event
	// followed by a Healthy event. The initial stream-open Healthy (before
	// any failures) is expected and does not violate the ordering.
	mu.Lock()
	defer mu.Unlock()
	var sawUnhealthy, sawRecovery bool
	for _, e := range healthEvents {
		if e.state == model.PluginHealthStateUnhealthy {
			sawUnhealthy = true
		}
		if e.state == model.PluginHealthStateHealthy && sawUnhealthy {
			sawRecovery = true
		}
	}
	if !sawUnhealthy {
		t.Error("expected at least one Unhealthy event from consecutive failures")
	}
	if !sawRecovery {
		t.Error("expected a Healthy event after the Unhealthy event (recovery)")
	}
}

// ── Restart tests ─────────────────────────────────────────────────────────────

// captureStartServer records each StartRequest it receives and blocks until
// its context is cancelled. Each call adds to calls and scopes slices.
type captureStartServer struct {
	triggerv1.UnimplementedTriggerServiceServer
	mu     sync.Mutex
	calls  int
	scopes []string
}

func (s *captureStartServer) Start(req *triggerv1.StartRequest, stream grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	s.mu.Lock()
	s.calls++
	s.scopes = append(s.scopes, req.WatchScopeJson)
	s.mu.Unlock()
	<-stream.Context().Done()
	return stream.Context().Err()
}

func (s *captureStartServer) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *captureStartServer) capturedScopes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.scopes))
	copy(out, s.scopes)
	return out
}

// seqQuerier is a Querier that returns successive instances from instSeq on
// each call to GetPluginInstanceByID. Designed for Restart tests where the
// second Start call should see an updated SubscriptionScopeJson.
type seqQuerier struct {
	mu      sync.Mutex
	instSeq []db.PluginInstance
	callIdx int
}

func (q *seqQuerier) GetSubscribedActivePolicies(_ context.Context) ([]db.Policy, error) {
	return nil, nil
}
func (q *seqQuerier) GetPluginByID(_ context.Context, _ string) (db.Plugin, error) {
	return db.Plugin{}, nil
}
func (q *seqQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.instSeq) == 0 {
		return db.PluginInstance{ID: id}, nil
	}
	inst := q.instSeq[q.callIdx]
	if q.callIdx < len(q.instSeq)-1 {
		q.callIdx++
	}
	return inst, nil
}
func (q *seqQuerier) ListPluginsByStatus(_ context.Context, _ string) ([]db.Plugin, error) {
	return nil, nil
}
func (q *seqQuerier) ListPluginInstancesByPlugin(_ context.Context, _ string) ([]db.PluginInstance, error) {
	return nil, nil
}

// newSupervisorWithQuerier builds a Supervisor using the provided querier (allows
// custom Querier for Restart tests that need updated scope data).
func newSupervisorWithQuerier(querier plugintrigger.Querier, lookup plugintrigger.InstanceLookup, dispatcher plugintrigger.EventDispatcher) *plugintrigger.Supervisor {
	return plugintrigger.NewSupervisor(plugintrigger.Config{
		Querier:             querier,
		BackoffInitial:      time.Microsecond,
		BackoffMax:          time.Millisecond,
		UnhealthyAfter:      3,
		TestInstanceLookup:  lookup,
		TestEventDispatcher: dispatcher,
	})
}

// TestSupervisor_Restart_OpensNewStreamWithUpdatedScope verifies that Restart:
//  1. Cancels the existing stream goroutine.
//  2. Opens a new stream goroutine.
//  3. The second StartRequest carries the updated SubscriptionScopeJson from
//     the DB (not the original scope).
func TestSupervisor_Restart_OpensNewStreamWithUpdatedScope(t *testing.T) {
	// Not parallel: mutates a shared server's state per call ordering.
	srv := &captureStartServer{}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	const firstScope = `{"channels":["#general"]}`
	const secondScope = `{"channels":["#incidents","#ops"]}`

	querier := &seqQuerier{
		instSeq: []db.PluginInstance{
			{ID: "inst-1", SubscriptionScopeJson: firstScope},
			{ID: "inst-1", SubscriptionScopeJson: secondScope},
		},
	}

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}
	sup := newSupervisorWithQuerier(querier, lookup, dispatcher)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sup.Start(ctx, "inst-1")

	// Wait for first Start call.
	waitFor(t, 5*time.Second, func() bool { return srv.startCount() >= 1 })

	sup.Restart(ctx, "inst-1")

	// Wait for second Start call.
	waitFor(t, 5*time.Second, func() bool { return srv.startCount() >= 2 })

	scopes := srv.capturedScopes()
	if len(scopes) < 2 {
		t.Fatalf("expected at least 2 Start calls, got %d", len(scopes))
	}
	if scopes[0] != firstScope {
		t.Errorf("first StartRequest.WatchScopeJson = %q, want %q", scopes[0], firstScope)
	}
	if scopes[1] != secondScope {
		t.Errorf("second StartRequest.WatchScopeJson = %q, want %q", scopes[1], secondScope)
	}
}

// TestSupervisor_Restart_NotSupervised_IsNoOp verifies that Restart on an
// instance that was never started is a no-op and does not panic.
func TestSupervisor_Restart_NotSupervised_IsNoOp(t *testing.T) {
	t.Parallel()
	srv := &captureStartServer{}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	sup := newSupervisor(lookup, &fakeEventDispatcher{}, nil)

	ctx := context.Background()
	// Should not panic and should not open a stream.
	sup.Restart(ctx, "never-started")

	if srv.startCount() != 0 {
		t.Errorf("expected 0 Start calls after Restart of unsupervised instance, got %d", srv.startCount())
	}
}

// TestSupervisor_Restart_PerCallCtxCancel_DoesNotKillNewStream is the
// regression test for #401. It verifies that cancelling the per-call ctx
// passed to Restart does NOT kill the freshly-started stream goroutine.
//
// Pre-patch: Start(shortCtx, id) parented streamCtx off the dead shortCtx;
// the goroutine exited at the first ctx-check in streamLoop and srv.startCount
// never reached 2. Post-patch: Start ignores shortCtx and parents off rootCtx;
// the goroutine survives and the second Start call is observed.
//
// Not parallel — it observes goroutine survival across a tight time window.
func TestSupervisor_Restart_PerCallCtxCancel_DoesNotKillNewStream(t *testing.T) {
	srv := &captureStartServer{}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}

	// Non-empty scope so the scope gate passes and the stream actually opens.
	q := &scopeQuerier{scope: `{"channel":"#dev"}`}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sup := plugintrigger.NewSupervisor(plugintrigger.Config{
		Querier:             q,
		BackoffInitial:      time.Microsecond,
		BackoffMax:          time.Millisecond,
		UnhealthyAfter:      3,
		TestInstanceLookup:  lookup,
		TestEventDispatcher: dispatcher,
		RootCtx:             rootCtx,
	})
	t.Cleanup(sup.StopAll)

	// Seed the first stream.
	sup.Start(context.Background(), "inst-1")
	waitFor(t, 2*time.Second, func() bool { return srv.startCount() >= 1 })

	// Cancel the per-call ctx BEFORE calling Restart so that the old Start
	// implementation (which derived streamCtx from the caller's ctx) would
	// immediately kill the new goroutine at the first ctx.Err() check (#401).
	shortCtx, shortCancel := context.WithCancel(context.Background())
	shortCancel() // dead ctx passed to Restart

	sup.Restart(shortCtx, "inst-1")

	// The new stream goroutine must survive despite shortCtx being cancelled.
	// If it does, srv.startCount() reaches 2.
	waitFor(t, 5*time.Second, func() bool { return srv.startCount() >= 2 })
}

// TestSupervisor_StopRacesRestart_NoDeadlockNoDoubleStart verifies the
// lock-discipline contract: a concurrent Stop arriving during Restart must not
// cause a deadlock and must result in at most 1 new stream goroutine (Stop wins
// the race so the new Start either doesn't happen or is immediately cancelled).
func TestSupervisor_StopRacesRestart_NoDeadlockNoDoubleStart(t *testing.T) {
	srv := &captureStartServer{}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	sup := newSupervisor(lookup, &fakeEventDispatcher{}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start the instance so there is something to race against.
	sup.Start(ctx, "inst-race")
	waitFor(t, 5*time.Second, func() bool { return srv.startCount() >= 1 })

	// Fire Stop and Restart concurrently.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sup.Stop("inst-race")
	}()
	go func() {
		defer wg.Done()
		sup.Restart(ctx, "inst-race")
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// No deadlock.
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: Stop/Restart did not complete within 5s")
	}

	// After the race, the supervisor must not have > 2 Start calls total
	// (original + at most 1 from Restart). The important assertion is no
	// deadlock and no panic — exact call count depends on scheduler ordering.
	count := srv.startCount()
	if count > 2 {
		t.Errorf("Start called %d times, want ≤ 2 (original + at most 1 from Restart)", count)
	}
}

// TestSupervisor_StopAll verifies that StopAll waits for all goroutines to exit
// without leaking any goroutines.
func TestSupervisor_StopAll(t *testing.T) {
	t.Parallel()

	// Three instances on a blocking server.
	srv := &blockingServer{}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup := newSupervisor(lookup, dispatcher, nil)
	sup.Start(ctx, "inst-a")
	sup.Start(ctx, "inst-b")
	sup.Start(ctx, "inst-c")

	// Give goroutines time to reach blocking Recv.
	time.Sleep(30 * time.Millisecond)

	// StopAll must complete within a reasonable timeout.
	done := make(chan struct{})
	go func() {
		sup.StopAll()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StopAll did not complete within 5s")
	}
}

// ── Scope gate tests ──────────────────────────────────────────────────────────

// scopeQuerier returns instances whose SubscriptionScopeJson can be changed
// between calls via the mu-protected scope field, simulating an operator saving
// a new scope between the first and second stream attempts.
type scopeQuerier struct {
	mu    sync.Mutex
	scope string
}

func (q *scopeQuerier) setScope(s string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.scope = s
}

func (q *scopeQuerier) GetSubscribedActivePolicies(_ context.Context) ([]db.Policy, error) {
	return nil, nil
}
func (q *scopeQuerier) GetPluginByID(_ context.Context, _ string) (db.Plugin, error) {
	return db.Plugin{}, nil
}
func (q *scopeQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return db.PluginInstance{ID: id, SubscriptionScopeJson: q.scope}, nil
}
func (q *scopeQuerier) ListPluginsByStatus(_ context.Context, _ string) ([]db.Plugin, error) {
	return nil, nil
}
func (q *scopeQuerier) ListPluginInstancesByPlugin(_ context.Context, _ string) ([]db.PluginInstance, error) {
	return nil, nil
}

// TestSupervisor_EmptyScope_SkipsStart verifies that when subscription_scope_json
// is empty ("") or the zero-object "{}", the supervisor does NOT call
// TriggerService.Start — it silently waits and retries.
func TestSupervisor_EmptyScope_SkipsStart(t *testing.T) {
	t.Parallel()

	srv := &alwaysEOFServer{} // would count calls if Start were ever reached
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}

	for _, emptyScope := range []string{"", "{}"} {
		emptyScope := emptyScope
		t.Run("scope="+emptyScope, func(t *testing.T) {
			t.Parallel()

			q := &scopeQuerier{scope: emptyScope}
			sup := plugintrigger.NewSupervisor(plugintrigger.Config{
				Querier:             q,
				BackoffInitial:      time.Microsecond,
				BackoffMax:          5 * time.Millisecond, // short so the gate ticks quickly in tests
				UnhealthyAfter:      3,
				TestInstanceLookup:  lookup,
				TestEventDispatcher: dispatcher,
			})

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			sup.Start(ctx, "inst-empty-scope")

			// Wait for the context to expire — the scope gate should have prevented
			// any Start call reaching the plugin.
			<-ctx.Done()
			sup.StopAll()

			if n := srv.startCount(); n > 0 {
				t.Errorf("TriggerService.Start called %d time(s) with empty scope %q; expected 0", n, emptyScope)
			}
		})
	}
}

// TestSupervisor_NonEmptyScope_OpensStream verifies that a non-empty scope
// results in TriggerService.Start being called.
func TestSupervisor_NonEmptyScope_OpensStream(t *testing.T) {
	t.Parallel()

	evCh := make(chan *triggerv1.StartResponse, 1)
	evCh <- &triggerv1.StartResponse{EventId: "e1", EventKind: "msg", PayloadJson: "{}"}
	close(evCh)

	srv := &sequentialTriggerServer{eventsCh: evCh}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}

	q := &scopeQuerier{scope: `{"channels":["#general"]}`}
	sup := plugintrigger.NewSupervisor(plugintrigger.Config{
		Querier:             q,
		BackoffInitial:      time.Microsecond,
		BackoffMax:          5 * time.Millisecond,
		UnhealthyAfter:      3,
		TestInstanceLookup:  lookup,
		TestEventDispatcher: dispatcher,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sup.Start(ctx, "inst-with-scope")

	// The event should arrive at the dispatcher because the scope gate passes.
	waitFor(t, 3*time.Second, func() bool {
		return len(dispatcher.received()) >= 1
	})

	if len(dispatcher.received()) == 0 {
		t.Error("no events dispatched; expected at least 1 (scope gate should have passed)")
	}
}

// TestSupervisor_HealthyOnFirstRecv_NoPreFailure verifies that the supervisor
// calls HealthSetter(Healthy) on the first successful Recv even when the
// instance was never previously marked Unhealthy (clean first start with valid
// credentials). This covers the gap where the DB default is
// unhealthy/config_missing and the plugin itself only emits SetHealthState on
// failure paths — without this fix the instance would never transition to
// Healthy.
func TestSupervisor_HealthyOnFirstRecv_NoPreFailure(t *testing.T) {
	t.Parallel()

	evCh := make(chan *triggerv1.StartResponse, 1)
	evCh <- &triggerv1.StartResponse{EventId: "first-event", EventKind: "message", PayloadJson: "{}"}
	close(evCh)

	client, stop := startFakeTriggerServer(t, &sequentialTriggerServer{eventsCh: evCh})
	defer stop()

	type healthEvent struct {
		state  model.PluginHealthState
		detail string
	}
	var mu sync.Mutex
	var healthEvents []healthEvent

	// Cancel the supervisor as soon as the first Healthy event is seen. This
	// stops the reconnect loop before it accumulates enough consecutive failures
	// to trigger an Unhealthy call, keeping the test deterministic.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	healthSetter := func(_ context.Context, _ string, state model.PluginHealthState, detail string) {
		mu.Lock()
		healthEvents = append(healthEvents, healthEvent{state, detail})
		mu.Unlock()
		if state == model.PluginHealthStateHealthy {
			cancel() // stop the supervisor immediately on first Healthy
		}
	}

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}
	sup := newSupervisor(lookup, dispatcher, healthSetter)

	sup.Start(ctx, "inst-fresh")

	// Wait until at least one health event is recorded, then let Stop drain the
	// goroutine. The cancel in the healthSetter above fires on the first Healthy
	// call, so the goroutine exits shortly after we see the event.
	waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(healthEvents) > 0
	})

	// Stop blocks until the goroutine exits (drains doneCh), ensuring no more
	// health events are appended after we read the slice below.
	sup.Stop("inst-fresh")

	mu.Lock()
	defer mu.Unlock()

	// The Healthy event must have appeared, and it must be the first health event
	// recorded (no Unhealthy should precede it on a clean first start).
	if len(healthEvents) == 0 {
		t.Fatal("HealthSetter never called on clean first start")
	}
	if healthEvents[0].state != model.PluginHealthStateHealthy {
		t.Errorf("first HealthSetter call = %v, want Healthy", healthEvents[0].state)
	}
}

// TestSupervisor_Restart_EmptyToNonEmpty_TriggersStream verifies the
// Restart path: when scope transitions from empty → non-empty via Restart,
// the new goroutine picks up the updated scope and opens a stream.
//
// This is the correct handling of the subscription-scope PUT → Restart flow:
// the admin handler calls Restart after persisting the new scope, and the
// supervisor's Restart cancels the waiting goroutine and starts a fresh one
// that re-fetches the DB row.
func TestSupervisor_Restart_EmptyToNonEmpty_TriggersStream(t *testing.T) {
	// Not parallel: uses a shared alwaysEOFServer call count that Restart races.
	evCh := make(chan *triggerv1.StartResponse, 1)
	evCh <- &triggerv1.StartResponse{EventId: "after-scope-set", EventKind: "msg"}
	close(evCh)

	srv := &sequentialTriggerServer{eventsCh: evCh}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}

	q := &scopeQuerier{scope: ""} // start with empty scope

	sup := plugintrigger.NewSupervisor(plugintrigger.Config{
		Querier:             q,
		BackoffInitial:      time.Microsecond,
		BackoffMax:          10 * time.Millisecond,
		UnhealthyAfter:      3,
		TestInstanceLookup:  lookup,
		TestEventDispatcher: dispatcher,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sup.Start(ctx, "inst-scope-change")

	// Give the goroutine time to see the empty scope and enter the waiting loop.
	time.Sleep(30 * time.Millisecond)

	// Now simulate the operator saving a scope: update the querier and Restart.
	q.setScope(`{"channels":["#general"]}`)
	sup.Restart(ctx, "inst-scope-change")

	// The new goroutine should see the non-empty scope and open a stream,
	// delivering the event.
	waitFor(t, 5*time.Second, func() bool {
		return len(dispatcher.received()) >= 1
	})

	got := dispatcher.received()
	if len(got) == 0 {
		t.Fatal("no events dispatched after scope update and Restart")
	}
	if got[0].EventID != "after-scope-set" {
		t.Errorf("first event ID = %q, want %q", got[0].EventID, "after-scope-set")
	}
}

// ── Bug-fix regression tests ──────────────────────────────────────────────────

// nilThenClientLookup returns nil on the first N lookups (simulating a slow
// subprocess start), then returns the real client.  Used to verify that startup
// waits do not inflate consecutive toward the unhealthy threshold.
type nilThenClientLookup struct {
	mu         sync.Mutex
	nilCount   int
	callsSoFar int
	client     triggerv1.TriggerServiceClient
	pluginID   string
}

func (l *nilThenClientLookup) LookupInstance(_ string) (triggerv1.TriggerServiceClient, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.callsSoFar++
	if l.callsSoFar <= l.nilCount {
		return nil, ""
	}
	return l.client, l.pluginID
}

// TestSupervisor_StartupWaits_DoNotInflateConsecutive verifies that returning
// nil from LookupInstance (subprocess not yet running) does NOT increment the
// consecutive-failure counter.  Without the fix, enough nil lookups would push
// the backoff to BackoffMax and trip maybeMarkUnhealthy before any real stream
// failure occurred.
//
// Strategy: use UnhealthyAfter=3 and let the lookup return nil exactly 4 times
// (> threshold if consecutive were incremented).  The 5th lookup returns the
// real client, which opens a blocking stream (sends one event then blocks).
// We stop the supervisor after the event is dispatched to prevent subsequent
// reconnect failures from tripping the Unhealthy check.  If consecutive WERE
// incremented during the nil-lookup waits, Unhealthy would be set before the
// event dispatch.
func TestSupervisor_StartupWaits_DoNotInflateConsecutive(t *testing.T) {
	t.Parallel()

	// blockAfterEventServer sends events then blocks until context is cancelled.
	// Using blockingServer after one event prevents the reconnect loop from
	// accumulating post-event consecutive failures during the assertion window.
	evSrv := &singleEventThenBlockServer{
		event: &triggerv1.StartResponse{EventId: "post-startup", EventKind: "message"},
	}

	client, stop := startFakeTriggerServer(t, evSrv)
	defer stop()

	// 4 nil lookups before the client is available — more than UnhealthyAfter=3.
	lookup := &nilThenClientLookup{nilCount: 4, client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}

	var unhealthyCalled atomic.Bool
	healthSetter := func(_ context.Context, _ string, state model.PluginHealthState, _ string) {
		if state == model.PluginHealthStateUnhealthy {
			unhealthyCalled.Store(true)
		}
	}

	sup := newSupervisor(lookup, dispatcher, healthSetter)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sup.Start(ctx, "inst-slow-start")

	// Wait for the event — confirms the stream opened after startup waits.
	waitFor(t, 8*time.Second, func() bool {
		return len(dispatcher.received()) >= 1
	})

	// Stop the supervisor before the stream ends so the reconnect loop cannot
	// accumulate post-event failures that would trip Unhealthy.
	sup.Stop("inst-slow-start")

	if unhealthyCalled.Load() {
		t.Error("HealthSetter called with Unhealthy; consecutive must not be incremented for nil-lookup startup waits")
	}
	if len(dispatcher.received()) == 0 {
		t.Error("no events dispatched; stream should have opened after startup waits")
	}
}

// TestSupervisor_BackoffResets_OnSuccessfulStreamOpen verifies that consecutive
// is reset on a successful TriggerService.Start, not only on the first Recv.
//
// Scenario: the server EOFs immediately for the first N calls (racking up
// consecutive failures), then opens a real event stream.  Before the fix the
// backoff counter was only reset inside recvLoop on the first Recv; a stream
// that opened then immediately EOFed never reset consecutive, so the backoff
// climbed unboundedly across reconnects.  After the fix, consecutive is reset
// at the stream-open point, so recovery is immediate.
//
// Observable proxy: we count Start calls.  With the fix, after the initial
// failures the supervisor should reconnect quickly (low backoff due to reset)
// and reach the event-serving stream.  We assert the event is dispatched within
// a generous timeout; the exact call count is not asserted (scheduler-dependent).
func TestSupervisor_BackoffResets_OnSuccessfulStreamOpen(t *testing.T) {
	t.Parallel()

	evCh := make(chan *triggerv1.StartResponse, 1)
	evCh <- &triggerv1.StartResponse{EventId: "recovery-event", EventKind: "message"}
	close(evCh)

	// Fail the first 3 Start calls with immediate EOF, then serve the event.
	srv := &recoverServer{failCount: 3, ch: evCh}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}

	sup := newSupervisor(lookup, dispatcher, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sup.Start(ctx, "inst-backoff-reset")

	// Wait for the recovery event — confirms the stream opened after failures
	// and consecutive was reset so the next successful open is not delayed.
	waitFor(t, 8*time.Second, func() bool {
		return len(dispatcher.received()) >= 1
	})

	got := dispatcher.received()
	if len(got) == 0 {
		t.Fatal("no events dispatched; supervisor did not recover from initial EOF failures")
	}
	if got[0].EventID != "recovery-event" {
		t.Errorf("first event ID = %q, want recovery-event", got[0].EventID)
	}
}
