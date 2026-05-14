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
	return db.PluginInstance{ID: id}, nil
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

// TestSupervisor_ContextCancel verifies that cancelling the outer context
// causes the stream goroutine to exit, observable via StopAll returning.
func TestSupervisor_ContextCancel(t *testing.T) {
	t.Parallel()

	srv := &blockingServer{}
	client, stop := startFakeTriggerServer(t, srv)
	defer stop()

	lookup := &fakeInstanceLookup{client: client, pluginID: "test-plugin"}
	dispatcher := &fakeEventDispatcher{}

	ctx, cancel := context.WithCancel(context.Background())
	sup := newSupervisor(lookup, dispatcher, nil)
	sup.Start(ctx, "inst-1")

	// Give the goroutine time to reach stream.Recv before cancelling.
	time.Sleep(30 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		sup.StopAll()
		close(done)
	}()

	select {
	case <-done:
		// Clean exit — goroutine exited after ctx cancel.
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor goroutine did not exit within 5s after ctx cancel")
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

	// Wait for Healthy to be reported.
	waitFor(t, 8*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range healthEvents {
			if e.state == model.PluginHealthStateHealthy {
				return true
			}
		}
		return false
	})

	// Verify ordering: Unhealthy must appear before Healthy.
	mu.Lock()
	defer mu.Unlock()
	var sawUnhealthy bool
	for _, e := range healthEvents {
		if e.state == model.PluginHealthStateUnhealthy {
			sawUnhealthy = true
		}
		if e.state == model.PluginHealthStateHealthy && !sawUnhealthy {
			t.Error("saw Healthy before Unhealthy — wrong order")
		}
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
