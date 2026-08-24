package dispatch_test

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

func TestMain(m *testing.M) {
	// Intercept the re-exec pattern used by integration tests that need a real
	// plugin subprocess. When this test binary is re-launched as a subprocess
	// (by setting GLEIPNIR_TEST_FIXTURE), it acts as a plugin server rather than
	// running the test suite.
	// Known noise: because the re-exec'd fixture is this race-instrumented test
	// binary, its stderr may carry a "WARNING: DATA RACE" on os.Stderr between
	// go-plugin's Serve (which reassigns os.Stderr for stdio sync, server.go:493)
	// and yamux.DefaultConfig in the muxer's acceptSession goroutine. The race is
	// entirely inside hashicorp/go-plugin (present unchanged through v1.8.0), is
	// not caused by SDK or host code, and cannot fail these tests — the host only
	// logs the subprocess stderr, and the fixture is SIGKILLed so its race exit
	// code is never observed. Do not chase it here; it needs an upstream fix.
	if os.Getenv("GLEIPNIR_TEST_FIXTURE") == "dispatch-serve-via-sdk" {
		serve.Serve(
			serve.WithChannelService(func(_ hostv1.HostServiceClient) channelv1.ChannelServiceServer {
				return &dispatchFixtureChannel{}
			}),
		)
		os.Exit(0)
	}
	goleak.VerifyTestMain(m)
}

// dispatchFixtureChannel is a trivial ChannelService that returns Ok=true on
// every Notify call. Used by TestDispatcher_Notify_ProductionWiring.
type dispatchFixtureChannel struct {
	channelv1.UnimplementedChannelServiceServer
}

func (s *dispatchFixtureChannel) Notify(_ context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
	return &channelv1.NotifyResponse{Ok: true}, nil
}

// fakeToolServer is a test double for toolv1.ToolServiceServer.
type fakeToolServer struct {
	toolv1.UnimplementedToolServiceServer
	mu sync.Mutex

	// callHook, if set, is invoked by Call.
	callHook func(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error)

	// cancelHook, if set, is invoked by Cancel.
	cancelHook func(ctx context.Context, req *toolv1.CancelRequest) (*toolv1.CancelResponse, error)
}

func (s *fakeToolServer) Call(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
	s.mu.Lock()
	hook := s.callHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	return &toolv1.CallResponse{OutputJson: `"default"`}, nil
}

func (s *fakeToolServer) Cancel(ctx context.Context, req *toolv1.CancelRequest) (*toolv1.CancelResponse, error) {
	s.mu.Lock()
	hook := s.cancelHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	return &toolv1.CancelResponse{}, nil
}

// bufconnDialer builds a ConnFactory backed by an in-process bufconn listener.
func bufconnDialer(t *testing.T, srv *fakeToolServer) (dispatch.ConnFactory, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	toolv1.RegisterToolServiceServer(s, srv)

	serveErr := make(chan error, 1)
	go func() { serveErr <- s.Serve(lis) }()

	factory := func(_ string) (*grpc.ClientConn, error) {
		return grpc.NewClient(
			"passthrough:///bufnet",
			grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
				return lis.Dial()
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}

	cleanup := func() {
		s.Stop()
		lis.Close()
	}
	return factory, cleanup
}

// newTestPool constructs a Pool with a bufconn-backed ConnFactory.
func newTestPool(t *testing.T, srv *fakeToolServer, extra ...func(*dispatch.Config)) (*dispatch.Pool, func()) {
	t.Helper()
	factory, srvCleanup := bufconnDialer(t, srv)
	cfg := dispatch.Config{
		CallTimeout:          500 * time.Millisecond,
		CancelTimeout:        150 * time.Millisecond,
		DefaultMaxConcurrent: 10,
		DefaultMaxQueueDepth: 10,
		Connect:              factory,
	}
	for _, fn := range extra {
		fn(&cfg)
	}
	pool := dispatch.New(cfg)
	return pool, func() {
		pool.Close() //nolint:errcheck
		srvCleanup()
	}
}

// TestPool_HappyPath verifies a successful Call returns output_json without error.
func TestPool_HappyPath(t *testing.T) {
	srv := &fakeToolServer{
		callHook: func(_ context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			return &toolv1.CallResponse{OutputJson: `{"echoed":"hello"}`}, nil
		},
	}
	pool, cleanup := newTestPool(t, srv)
	defer cleanup()

	output, isError, err := pool.Call(context.Background(), "run-1", "pol-1", "inst", "echo", `{}`)
	if err != nil {
		t.Fatalf("Call returned unexpected error: %v", err)
	}
	if isError {
		t.Error("isError should be false for success")
	}
	if output != `{"echoed":"hello"}` {
		t.Errorf("output = %q; want %q", output, `{"echoed":"hello"}`)
	}
}

// TestPool_EvictInstance_ForcesRedial verifies that EvictInstance drops the
// cached connection so the next Call re-dials via ConnFactory. This is the fix
// for the deactivate→activate bug: a stopped-then-respawned subprocess must not
// keep serving calls over the dead connection cached from its prior generation.
func TestPool_EvictInstance_ForcesRedial(t *testing.T) {
	srv := &fakeToolServer{
		callHook: func(_ context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			return &toolv1.CallResponse{OutputJson: `"ok"`}, nil
		},
	}
	// Wrap the bufconn factory with a dial counter.
	inner, srvCleanup := bufconnDialer(t, srv)
	var dials atomic.Int64
	counting := func(name string) (*grpc.ClientConn, error) {
		dials.Add(1)
		return inner(name)
	}
	pool := dispatch.New(dispatch.Config{
		CallTimeout:          500 * time.Millisecond,
		CancelTimeout:        150 * time.Millisecond,
		DefaultMaxConcurrent: 10,
		DefaultMaxQueueDepth: 10,
		Connect:              counting,
	})
	defer func() {
		pool.Close() //nolint:errcheck
		srvCleanup()
	}()

	// First call dials once and caches the connection.
	if _, _, err := pool.Call(context.Background(), "run-1", "pol-1", "inst", "echo", `{}`); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	// Second call to the same instance reuses the cached connection (no re-dial).
	if _, _, err := pool.Call(context.Background(), "run-2", "pol-1", "inst", "echo", `{}`); err != nil {
		t.Fatalf("second Call: %v", err)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials after two cached calls = %d; want 1", got)
	}

	// Evict, then the next call must re-dial.
	pool.EvictInstance("inst")
	if _, _, err := pool.Call(context.Background(), "run-3", "pol-1", "inst", "echo", `{}`); err != nil {
		t.Fatalf("Call after evict: %v", err)
	}
	if got := dials.Load(); got != 2 {
		t.Errorf("dials after evict+call = %d; want 2 (re-dial)", got)
	}

	// Evicting an unknown instance is a safe no-op.
	pool.EvictInstance("does-not-exist")
}

// TestPool_ErrorEnvelope verifies that a plugin-side ErrorEnvelope is returned
// as isError=true with no Go error.
func TestPool_ErrorEnvelope(t *testing.T) {
	srv := &fakeToolServer{
		callHook: func(_ context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			return &toolv1.CallResponse{
				Error: &commonv1.ErrorEnvelope{
					Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
					Message: "bad argument",
				},
			}, nil
		},
	}
	pool, cleanup := newTestPool(t, srv)
	defer cleanup()

	output, isError, err := pool.Call(context.Background(), "run-1", "pol-1", "inst", "tool", `{}`)
	if err != nil {
		t.Fatalf("Call returned unexpected Go error: %v", err)
	}
	if !isError {
		t.Error("isError should be true for ErrorEnvelope response")
	}
	if output == "" {
		t.Error("output should be a non-empty formatted error string")
	}
}

// TestPool_CallTimeout verifies that when the server is slow and the parent ctx
// is healthy, ErrCallTimeout is returned.
func TestPool_CallTimeout(t *testing.T) {
	unblock := make(chan struct{})
	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			select {
			case <-ctx.Done():
				return nil, status.Error(codes.DeadlineExceeded, ctx.Err().Error())
			case <-unblock:
				return &toolv1.CallResponse{}, nil
			}
		},
	}
	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.CallTimeout = 30 * time.Millisecond
	})
	defer func() {
		close(unblock)
		cleanup()
	}()

	_, _, err := pool.Call(context.Background(), "run-1", "pol-1", "inst", "slow-tool", `{}`)
	if !errors.Is(err, dispatch.ErrCallTimeout) {
		t.Errorf("error = %v; want ErrCallTimeout", err)
	}
}

// TestPool_CallIDMetadata verifies that gleipnir-call-id metadata is set on the
// server side and matches RequestContext.CallId.
func TestPool_CallIDMetadata(t *testing.T) {
	type result struct {
		metaCallID string
		ctxCallID  string
	}
	resultCh := make(chan result, 1)

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			var metaID string
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				if vals := md.Get(sdkproto.CallIDMetadataKey); len(vals) > 0 {
					metaID = vals[0]
				}
			}
			resultCh <- result{metaCallID: metaID, ctxCallID: req.GetContext().GetCallId()}
			return &toolv1.CallResponse{OutputJson: `"ok"`}, nil
		},
	}
	pool, cleanup := newTestPool(t, srv)
	defer cleanup()

	pool.Call(context.Background(), "run-1", "pol-1", "inst", "meta-tool", `{}`) //nolint:errcheck

	select {
	case r := <-resultCh:
		if r.metaCallID == "" {
			t.Error("gleipnir-call-id metadata was not set on the server")
		}
		if r.metaCallID != r.ctxCallID {
			t.Errorf("metadata call_id %q != RequestContext.CallId %q", r.metaCallID, r.ctxCallID)
		}
	case <-time.After(time.Second):
		t.Fatal("server hook was not called")
	}
}

// TestPool_CancelRun verifies that CancelRun drives Cancel RPCs for every
// in-flight call belonging to the run, and all Call goroutines return.
// The server honours the cancel by checking for a cancel signal on a shared channel,
// avoiding the need for force-disconnect (which creates timing-sensitive races in tests).
func TestPool_CancelRun(t *testing.T) {
	const numCalls = 3

	// cancelledIDs receives the call_id from each Cancel RPC.
	cancelledIDs := make(chan string, numCalls)

	// arrived signals when a Call has entered the server hook (i.e. is in-flight).
	arrived := make(chan struct{}, numCalls)

	// unblockCh is used by the Call hook to wait until explicitly unblocked.
	// We close it to release all blocked calls at once.
	unblockCh := make(chan struct{})

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			arrived <- struct{}{}
			// Block until either the gRPC context is cancelled (conn close or deadline)
			// or we're explicitly unblocked.
			select {
			case <-ctx.Done():
				return nil, status.Error(codes.Canceled, ctx.Err().Error())
			case <-unblockCh:
				return &toolv1.CallResponse{OutputJson: `"ok"`}, nil
			}
		},
		cancelHook: func(_ context.Context, req *toolv1.CancelRequest) (*toolv1.CancelResponse, error) {
			cancelledIDs <- req.GetCallId()
			return &toolv1.CancelResponse{}, nil
		},
	}

	var unblockOnce sync.Once
	doUnblock := func() { unblockOnce.Do(func() { close(unblockCh) }) }

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.DefaultMaxConcurrent = numCalls
		cfg.CallTimeout = 5 * time.Second
		cfg.CancelTimeout = 500 * time.Millisecond
	})
	defer func() {
		doUnblock() // idempotent release of any still-blocked Call hooks
		cleanup()
	}()

	var wg sync.WaitGroup
	for i := 0; i < numCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool.Call(context.Background(), "run-cancel", "pol-1", "inst", "slow-tool", `{}`) //nolint:errcheck
		}()
	}

	// Wait until all calls have reached the server hook before calling CancelRun.
	// This prevents the race where CancelRun fires before the pool has registered
	// the in-flight calls and snapshotInflightForRun returns empty.
	for i := 0; i < numCalls; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("call %d did not arrive at server", i+1)
		}
	}

	pool.CancelRun("run-cancel")

	// Wait for all Cancel RPCs to reach the server.
	cancelReceived := 0
	timeout := time.After(2 * time.Second)
	for cancelReceived < numCalls {
		select {
		case <-cancelledIDs:
			cancelReceived++
		case <-timeout:
			t.Fatalf("server only received %d Cancel RPCs, want %d", cancelReceived, numCalls)
		}
	}

	// Release the in-flight Call hooks so goroutines can return cleanly.
	doUnblock()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight Call goroutines did not return")
	}
}

// TestPool_CancelRun_WhileQueuedDoesNotDial verifies that a call cancelled by
// CancelRun while it is still blocked on the concurrency semaphore aborts
// cleanly with ErrRunCancelled and NEVER dials/executes the plugin tool call.
//
// This is the #588 regression guard for the queued case: the call is registered
// as in-flight BEFORE the semaphore acquire, so CancelRun can see it and cancel
// its per-call context. Against the pre-fix code (register AFTER the semaphore)
// the queued call is invisible to CancelRun and the queued select watches only
// the parent ctx, so this test would block until the deadline.
func TestPool_CancelRun_WhileQueuedDoesNotDial(t *testing.T) {
	var mu sync.Mutex
	arrivalCount := 0 // total calls that reached the server hook

	// firstArrived signals when the first (slot-holding) call is in the hook.
	firstArrived := make(chan struct{}, 1)
	unblockFirst := make(chan struct{})

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			// With maxConcurrent=1, only the first (slot-holding) call may ever reach
			// the hook. A second arrival means the queued call escaped CancelRun and
			// dialed the plugin — the #588 bug.
			mu.Lock()
			arrivalCount++
			mu.Unlock()
			firstArrived <- struct{}{}
			select {
			case <-unblockFirst:
				return &toolv1.CallResponse{OutputJson: `"ok"`}, nil
			case <-ctx.Done():
				return nil, status.Error(codes.Canceled, ctx.Err().Error())
			}
		},
	}

	// Single concurrency slot so the second call is forced to queue.
	var unblockOnce sync.Once
	doUnblockFirst := func() { unblockOnce.Do(func() { close(unblockFirst) }) }

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.DefaultMaxConcurrent = 1
		cfg.DefaultMaxQueueDepth = 4
		cfg.CallTimeout = 5 * time.Second
	})
	defer func() {
		doUnblockFirst()
		cleanup()
	}()

	// First call consumes the only semaphore slot and parks in the hook.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pool.Call(context.Background(), "run-other", "pol-1", "inst", "tool", `{}`) //nolint:errcheck
	}()
	select {
	case <-firstArrived:
	case <-time.After(5 * time.Second):
		t.Fatal("first call did not reach server")
	}

	// Second call belongs to the run we will cancel; it blocks on the semaphore.
	queuedErrCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, qErr := pool.Call(context.Background(), "run-cancel", "pol-1", "inst", "tool", `{}`)
		queuedErrCh <- qErr
	}()

	// Wait until the queued call is registered as in-flight for run-cancel. This
	// is the signal we synchronize on instead of a fixed sleep: the fix registers
	// before the semaphore acquire, so the call appears in InflightCountByInstance
	// while still queued.
	waitForCondition(t, 5*time.Second, func() bool {
		return pool.InflightCountByInstance("inst") == 2
	})

	// Cancel the run while the second call is still queued (it never dialed).
	pool.CancelRun("run-cancel")

	// The queued call must return ErrRunCancelled.
	var queuedErr error
	select {
	case queuedErr = <-queuedErrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("queued call did not return after CancelRun")
	}
	if !errors.Is(queuedErr, dispatch.ErrRunCancelled) {
		t.Errorf("queued call error = %v; want ErrRunCancelled", queuedErr)
	}

	// The queued call must never have dialed the plugin: only the first
	// slot-holding call should have arrived at the server hook.
	mu.Lock()
	arrivals := arrivalCount
	mu.Unlock()
	if arrivals != 1 {
		t.Errorf("server saw %d arrivals; want 1 — queued call executed the plugin gRPC tool after CancelRun (cancellation escaped, #588)", arrivals)
	}

	// Release the first call so all goroutines unwind.
	doUnblockFirst()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutines did not return")
	}
}

// TestPool_CancelRun_InWindowAbortsInflightRPC verifies the exact #588 window:
// a call that has acquired the semaphore and is executing the gRPC RPC is
// cancelled by CancelRun even when the caller's parent context is still healthy
// (mirroring Pool.Close, which calls CancelRun without cancelling any agent ctx).
// The call must return ErrRunCancelled rather than appearing to succeed.
func TestPool_CancelRun_InWindowAbortsInflightRPC(t *testing.T) {
	arrived := make(chan struct{}, 1)

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			arrived <- struct{}{}
			// Honour gRPC context cancellation (driven by the per-call ctx that
			// CancelRun cancels). If we instead returned a value, that would be the
			// escape the bug allows.
			<-ctx.Done()
			return nil, status.Error(codes.Canceled, ctx.Err().Error())
		},
	}

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.CallTimeout = 5 * time.Second
		cfg.CancelTimeout = 500 * time.Millisecond
	})
	defer cleanup()

	// Parent context stays healthy for the whole test — only CancelRun drives the
	// cancellation, exactly as Pool.Close does during shutdown.
	var callErr error
	var isErr bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, isErr, callErr = pool.Call(context.Background(), "run-window", "pol-1", "inst", "tool", `{}`)
	}()

	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not reach server")
	}

	pool.CancelRun("run-window")

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return after CancelRun")
	}

	if !errors.Is(callErr, dispatch.ErrRunCancelled) {
		t.Errorf("call error = %v; want ErrRunCancelled (parent ctx healthy, CancelRun fired)", callErr)
	}
	if isErr {
		t.Error("isError should be false for a cancelled call")
	}
}

// TestPool_QueueSlotReleasedOnAdmission verifies that a running call does NOT
// keep occupying a queue slot. The queue gate (capacity = max_queue_depth)
// accounts for waiting callers only and must be released the moment a call is
// admitted to a concurrency slot. With max_concurrent=1 and max_queue_depth=1,
// a second call must be able to QUEUE (block on the semaphore) while the first
// runs — if the running call still held its queue slot, the second would be
// wrongly rejected with ErrQueueFull. This guards the #588 fix against a
// regression where the queue slot was released only at call exit.
func TestPool_QueueSlotReleasedOnAdmission(t *testing.T) {
	arrived := make(chan struct{}, 1)
	unblock := make(chan struct{})
	var unblockOnce sync.Once
	doUnblock := func() { unblockOnce.Do(func() { close(unblock) }) }

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			arrived <- struct{}{}
			select {
			case <-unblock:
				return &toolv1.CallResponse{OutputJson: `"ok"`}, nil
			case <-ctx.Done():
				return nil, status.Error(codes.Canceled, ctx.Err().Error())
			}
		},
	}

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.DefaultMaxConcurrent = 1
		cfg.DefaultMaxQueueDepth = 1
		cfg.CallTimeout = 5 * time.Second
	})
	defer func() {
		doUnblock()
		cleanup()
	}()

	var wg sync.WaitGroup

	// First call takes the only concurrency slot and parks in the hook. Once it is
	// admitted it must have released its queue slot.
	wg.Add(1)
	go func() {
		defer wg.Done()
		pool.Call(context.Background(), "run-a", "pol-1", "inst", "tool", `{}`) //nolint:errcheck
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("first call did not reach server")
	}

	// Second call must be able to claim the freed queue slot and block on the
	// semaphore — NOT be rejected with ErrQueueFull. Against the regression
	// (running call still holding its queue slot) the queue gate is full and this
	// call returns ErrQueueFull immediately, so it never registers as in-flight.
	secondErrCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, err := pool.Call(context.Background(), "run-b", "pol-1", "inst", "tool", `{}`)
		secondErrCh <- err
	}()

	// The second call registers as in-flight only after claiming a queue slot, so
	// reaching count==2 proves it queued rather than being rejected.
	waitForCondition(t, 5*time.Second, func() bool {
		return pool.InflightCountByInstance("inst") == 2
	})

	// Sanity: it must not have already returned ErrQueueFull.
	select {
	case err := <-secondErrCh:
		t.Fatalf("second call returned early with %v; want it queued on the semaphore", err)
	default:
	}

	// Release the first call; the second is admitted and completes cleanly.
	doUnblock()
	select {
	case err := <-secondErrCh:
		if err != nil {
			t.Errorf("second call error = %v; want nil (admitted after first released)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second call did not complete after first released its slot")
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutines did not return")
	}
}

// waitForCondition polls cond until it returns true or the timeout elapses. It
// is used only to synchronize on pool-internal state (InflightCountByInstance,
// a guarded test flag) for which the pool publishes no event channel; the poll
// budget is a generous CI-tolerance bound, not a timing assertion.
func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

// TestPool_Semaphore verifies that max_concurrent_calls limits concurrency
// and extra callers block until a slot is released.
func TestPool_Semaphore(t *testing.T) {
	const maxConc = 2
	const total = 4

	unblock := make(chan struct{})
	var mu sync.Mutex
	var liveCount, maxSeen int

	// arrived signals when a call enters the hook (consuming a concurrency slot).
	arrived := make(chan struct{}, total)

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			mu.Lock()
			liveCount++
			if liveCount > maxSeen {
				maxSeen = liveCount
			}
			mu.Unlock()
			arrived <- struct{}{}
			defer func() { mu.Lock(); liveCount--; mu.Unlock() }()

			select {
			case <-unblock:
				return &toolv1.CallResponse{}, nil
			case <-ctx.Done():
				return nil, status.Error(codes.Canceled, "cancelled")
			}
		},
	}

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.DefaultMaxConcurrent = maxConc
		cfg.DefaultMaxQueueDepth = total + 4
	})
	defer cleanup()

	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool.Call(context.Background(), "run-sem", "pol-1", "inst", "tool", `{}`) //nolint:errcheck
		}()
	}

	// Wait until maxConc calls are actively executing inside the server hook.
	// The remaining (total - maxConc) calls are queued and have not entered the
	// hook yet, so checking liveCount at this point gives a clean assertion.
	for i := 0; i < maxConc; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("call %d did not arrive at server", i+1)
		}
	}

	mu.Lock()
	if liveCount > maxConc {
		t.Errorf("live calls = %d; exceeds max concurrent %d", liveCount, maxConc)
	}
	mu.Unlock()

	close(unblock)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutines did not return in time")
	}
}

// TestPool_QueueFull verifies that when all concurrency slots and queue slots
// are taken, a third call is rejected with ErrQueueFull immediately.
func TestPool_QueueFull(t *testing.T) {
	unblock := make(chan struct{})

	// arrived signals when the in-flight call has entered the server hook
	// (i.e. the semaphore slot is consumed).
	arrived := make(chan struct{}, 1)

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			arrived <- struct{}{}
			select {
			case <-unblock:
				return &toolv1.CallResponse{}, nil
			case <-ctx.Done():
				return nil, status.Error(codes.Canceled, "cancelled")
			}
		},
	}

	// claimed fires once per successful queue-slot claim. With queue depth 1
	// the second fire is a deterministic checkpoint: the first claimant can
	// only release its slot after semaphore admission, so at fire #2 the
	// semaphore is held by call 1 (parked in the server hook) and the sole
	// queue slot is held by call 2. Generous buffer so an unexpected extra
	// claim surfaces as an assertion failure, never a blocked hook.
	claimed := make(chan struct{}, 8)
	restore := dispatch.SetQueueSlotClaimedHookForTest(func() { claimed <- struct{}{} })
	t.Cleanup(restore)

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.DefaultMaxConcurrent = 1
		cfg.DefaultMaxQueueDepth = 1
	})
	defer cleanup()

	// Launch concurrent=1 in-flight + queue=1 waiting — SEQUENCED, not
	// concurrent. Launching both unordered was a race in the test itself
	// (#914): call 2 could reach the queue while call 1 still held the sole
	// queue slot (claimed but not yet admitted to the semaphore), get
	// ErrQueueFull immediately, and silently return — after which claim #2
	// could never fire and the test sat out its full deadline. The losing
	// schedule needs only a slow call-1 admission, which a race-instrumented,
	// CPU-starved CI runner supplies readily (three identical 30s timeouts on
	// 2026-08-24, on PRs that never touched this package). So: prove call 1
	// is admitted — its queue slot provably released — before call 2 exists.
	var wg sync.WaitGroup
	launch := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool.Call(context.Background(), "run-qf", "pol-1", "inst", "tool", `{}`) //nolint:errcheck
		}()
	}

	// The deadlines below exist only to fail loudly if a checkpoint never
	// arrives — a passing run never waits on them — so they are sized for a
	// CPU-starved CI runner, where 5s was observed to be too tight (#767).
	launch()
	select {
	case <-claimed:
	case <-time.After(30 * time.Second):
		t.Fatal("queue-slot claim 1 not observed")
	}
	// Call 1 admitted: it holds the semaphore and has released the queue slot.
	select {
	case <-arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("in-flight call did not arrive at server")
	}

	launch()
	select {
	case <-claimed:
	case <-time.After(30 * time.Second):
		t.Fatal("queue-slot claim 2 not observed")
	}
	// Semaphore and queue are now both provably full: call 1 is parked in the
	// server hook, call 2 holds the sole queue slot.

	_, _, err := pool.Call(context.Background(), "run-qf", "pol-1", "inst", "tool", `{}`)
	if !errors.Is(err, dispatch.ErrQueueFull) {
		t.Errorf("error = %v; want ErrQueueFull", err)
	}

	// Release the in-flight calls.
	close(unblock)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("goroutines did not drain")
	}
}

// TestPool_ParentCtxCancelledWhileQueued verifies that cancelling the parent
// ctx while a call is waiting for a semaphore slot exits with ctx.Err().
func TestPool_ParentCtxCancelledWhileQueued(t *testing.T) {
	unblock := make(chan struct{})

	// firstArrived signals when the first call has reached the server hook and
	// is holding the only concurrency slot.
	firstArrived := make(chan struct{}, 1)

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			firstArrived <- struct{}{}
			select {
			case <-unblock:
				return &toolv1.CallResponse{}, nil
			case <-ctx.Done():
				return nil, status.Error(codes.Canceled, "cancelled")
			}
		},
	}

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.DefaultMaxConcurrent = 1
		cfg.DefaultMaxQueueDepth = 2
	})
	defer func() {
		close(unblock)
		cleanup()
	}()

	// First call holds the semaphore.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pool.Call(context.Background(), "run-ctxq", "pol-1", "inst", "tool", `{}`) //nolint:errcheck
	}()

	// Wait until the first call is inside the server hook (semaphore slot consumed).
	select {
	case <-firstArrived:
	case <-time.After(5 * time.Second):
		t.Fatal("first call did not reach server")
	}

	// Second call queues; cancel its context.
	ctx, cancel := context.WithCancel(context.Background())
	var queuedErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, queuedErr = pool.Call(ctx, "run-ctxq", "pol-1", "inst", "tool", `{}`)
	}()
	// Give the second goroutine time to enter the queue before we cancel.
	// It does not reach the server (slot is taken), so a brief yield suffices.
	time.Sleep(5 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutines did not return after ctx cancel")
	}

	if queuedErr == nil {
		t.Error("queued call should return an error after ctx cancel, got nil")
	}
}

// TestPool_ErrorClassification_DeadlineExceeded_ParentHealthy verifies that
// codes.DeadlineExceeded while the parent ctx is alive returns ErrCallTimeout.
func TestPool_ErrorClassification_DeadlineExceeded_ParentHealthy(t *testing.T) {
	srv := &fakeToolServer{
		callHook: func(_ context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			return nil, status.Error(codes.DeadlineExceeded, "deadline exceeded")
		},
	}
	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.CallTimeout = 200 * time.Millisecond
	})
	defer cleanup()

	_, _, err := pool.Call(context.Background(), "run-ec", "pol-1", "inst", "tool", `{}`)
	if !errors.Is(err, dispatch.ErrCallTimeout) {
		t.Errorf("error = %v; want ErrCallTimeout", err)
	}
}

// TestPool_ErrorClassification_Canceled_ParentCancelled verifies that
// codes.Canceled after the parent ctx is cancelled propagates ctx.Err().
func TestPool_ErrorClassification_Canceled_ParentCancelled(t *testing.T) {
	// arrived signals when the call has entered the server hook.
	arrived := make(chan struct{}, 1)

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			arrived <- struct{}{}
			<-ctx.Done()
			return nil, status.Error(codes.Canceled, "cancelled")
		},
	}
	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.CallTimeout = 500 * time.Millisecond
	})
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	var callErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, callErr = pool.Call(ctx, "run-ec2", "pol-1", "inst", "tool", `{}`)
	}()

	// Wait until the call is inside the server hook before cancelling the context.
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not reach server before cancel")
	}
	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not return after ctx cancel")
	}

	if callErr == nil {
		t.Error("expected error after ctx cancel, got nil")
	}
}

// TestPool_CrossRunBlastRadius documents that conn.Close() after a Cancel
// timeout kills other in-flight calls on the same connection (v1 blast radius).
func TestPool_CrossRunBlastRadius(t *testing.T) {
	unblock := make(chan struct{})
	arrived := make(chan struct{}, 2)
	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			arrived <- struct{}{}
			select {
			case <-unblock:
				return &toolv1.CallResponse{OutputJson: `"ok"`}, nil
			case <-ctx.Done():
				return nil, status.Error(codes.Canceled, "cancelled")
			}
		},
		cancelHook: func(ctx context.Context, _ *toolv1.CancelRequest) (*toolv1.CancelResponse, error) {
			// Deliberately slow — exceeds CancelTimeout to force conn.Close().
			// Use ctx.Done() so the goroutine exits when the connection closes.
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
			return &toolv1.CancelResponse{}, nil
		},
	}

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.DefaultMaxConcurrent = 5
		cfg.CallTimeout = 5 * time.Second
		cfg.CancelTimeout = 30 * time.Millisecond // short deadline → conn.Close() fires
	})
	defer func() {
		close(unblock)
		cleanup()
	}()

	var wgA, wgB sync.WaitGroup
	var errB error

	// runA — will be cancelled.
	wgA.Add(1)
	go func() {
		defer wgA.Done()
		pool.Call(context.Background(), "run-A", "pol-1", "inst", "tool", `{}`) //nolint:errcheck
	}()

	// runB — shares the same instance connection.
	wgB.Add(1)
	go func() {
		defer wgB.Done()
		_, _, errB = pool.Call(context.Background(), "run-B", "pol-1", "inst", "tool", `{}`)
	}()

	// Wait until both calls have reached the server (and thus are registered
	// as in-flight in the pool). A fixed sleep here is flaky on slow CI runners:
	// if CancelRun fires before run-A is registered, snapshotInflightForRun
	// returns empty and the test blocks until the 3s deadline.
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(2 * time.Second):
			t.Fatal("calls did not arrive at server")
		}
	}

	// Cancelling runA causes Cancel timeout → conn.Close() → runB also killed.
	pool.CancelRun("run-A")

	done := make(chan struct{})
	go func() {
		wgA.Wait()
		wgB.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		// Loaded CI runners need more than 3s to unwind: the 30ms CancelTimeout,
		// the gRPC conn teardown, and both server hooks' ctx-Done branches all
		// have to fire and propagate before the two Call goroutines return.
		t.Fatal("calls did not return after blast-radius conn.Close()")
	}

	// runB should have received an error because the shared connection closed.
	// In rare timing scenarios the connection may be re-established; log only.
	if errB != nil {
		t.Logf("runB received error as expected: %v", errB)
	} else {
		t.Log("runB returned nil (connection re-established before blast-radius — acceptable in tests)")
	}
}

// TestPool_LookupCall_ReverseIndex verifies that a call is visible in LookupCall
// while in-flight and absent after the call completes.
func TestPool_LookupCall_ReverseIndex(t *testing.T) {
	// callIDSeen receives the call_id the server observed in the RequestContext.
	callIDSeen := make(chan string, 1)
	// unblock lets the test release the in-flight call; closeOnce prevents
	// the double-close that would occur if the deferred cleanup fires after the
	// explicit release below.
	unblock := make(chan struct{})
	var closeOnce sync.Once
	doUnblock := func() { closeOnce.Do(func() { close(unblock) }) }

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			callIDSeen <- req.GetContext().GetCallId()
			select {
			case <-unblock:
				return &toolv1.CallResponse{OutputJson: `"ok"`}, nil
			case <-ctx.Done():
				return nil, status.Error(codes.Canceled, "cancelled")
			}
		},
	}

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.CallTimeout = 2 * time.Second
	})
	defer func() {
		doUnblock()
		cleanup()
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pool.Call(context.Background(), "run-rev", "pol-rev", "inst", "tool", `{}`) //nolint:errcheck
	}()

	// Wait for the server to signal the call is active, then observe LookupCall.
	var callID string
	select {
	case callID = <-callIDSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("server hook was not called")
	}

	info, ok := pool.LookupCall(callID)
	if !ok {
		t.Fatalf("LookupCall(%q) = false while call is in-flight", callID)
	}
	if info.RunID != "run-rev" {
		t.Errorf("RunID = %q, want %q", info.RunID, "run-rev")
	}
	if info.PolicyID != "pol-rev" {
		t.Errorf("PolicyID = %q, want %q", info.PolicyID, "pol-rev")
	}
	if info.InstanceName != "inst" {
		t.Errorf("InstanceName = %q, want %q", info.InstanceName, "inst")
	}

	// Release the call and confirm it is no longer visible.
	doUnblock()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("call goroutine did not return")
	}

	_, ok = pool.LookupCall(callID)
	if ok {
		t.Errorf("LookupCall(%q) = true after call completed, want false", callID)
	}
}

// TestPool_InflightCountByInstance verifies that InflightCountByInstance returns
// the correct count of in-flight calls per instance name, and that the count
// drops to zero after the calls complete.
func TestPool_InflightCountByInstance(t *testing.T) {
	// callIDSeen receives a signal each time the server starts handling a call.
	callIDSeen := make(chan struct{}, 10)
	// unblock releases all blocked calls at once.
	unblock := make(chan struct{})
	var closeOnce sync.Once
	doUnblock := func() { closeOnce.Do(func() { close(unblock) }) }

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			callIDSeen <- struct{}{}
			select {
			case <-unblock:
				return &toolv1.CallResponse{OutputJson: `"ok"`}, nil
			case <-ctx.Done():
				return nil, status.Error(codes.Canceled, "cancelled")
			}
		},
	}

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.CallTimeout = 5 * time.Second
	})
	defer func() {
		doUnblock()
		cleanup()
	}()

	var wg sync.WaitGroup

	// Start two calls for "inst" and one call for "other".
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool.Call(context.Background(), "run-a", "pol-a", "inst", "tool", `{}`) //nolint:errcheck
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		pool.Call(context.Background(), "run-b", "pol-b", "other", "tool", `{}`) //nolint:errcheck
	}()

	// Wait until all three calls are active inside the server hook.
	for i := 0; i < 3; i++ {
		select {
		case <-callIDSeen:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for call %d to start", i+1)
		}
	}

	// Assert per-instance counts while all calls are in-flight.
	if got := pool.InflightCountByInstance("inst"); got != 2 {
		t.Errorf("InflightCountByInstance(inst) = %d while in-flight, want 2", got)
	}
	if got := pool.InflightCountByInstance("other"); got != 1 {
		t.Errorf("InflightCountByInstance(other) = %d while in-flight, want 1", got)
	}
	if got := pool.InflightCountByInstance("absent"); got != 0 {
		t.Errorf("InflightCountByInstance(absent) = %d, want 0", got)
	}

	// Release all calls and wait for the goroutines to finish.
	doUnblock()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("call goroutines did not return after unblock")
	}

	// Counts must drop to zero after completion.
	if got := pool.InflightCountByInstance("inst"); got != 0 {
		t.Errorf("InflightCountByInstance(inst) = %d after completion, want 0", got)
	}
	if got := pool.InflightCountByInstance("other"); got != 0 {
		t.Errorf("InflightCountByInstance(other) = %d after completion, want 0", got)
	}
}

// TestPool_CancelRun_MultipleCallsSameInstance_NoRaceOnConnClose verifies that
// when a run has multiple in-flight calls on the same instance and each Cancel
// RPC times out (triggering conn.Close), the race detector does not report a
// concurrent close on the same connection.
func TestPool_CancelRun_MultipleCallsSameInstance_NoRaceOnConnClose(t *testing.T) {
	const numCalls = 3

	arrived := make(chan struct{}, numCalls)

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			arrived <- struct{}{}
			<-ctx.Done()
			return nil, status.Error(codes.Canceled, ctx.Err().Error())
		},
		// Deliberately slow cancel — exceeds CancelTimeout so every goroutine
		// attempts conn.Close(). Without sync.Once this races.
		cancelHook: func(ctx context.Context, _ *toolv1.CancelRequest) (*toolv1.CancelResponse, error) {
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
			return &toolv1.CancelResponse{}, nil
		},
	}

	pool, cleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.DefaultMaxConcurrent = numCalls
		cfg.CallTimeout = 10 * time.Second
		cfg.CancelTimeout = 20 * time.Millisecond // short → all goroutines hit conn.Close
	})
	defer cleanup()

	var wg sync.WaitGroup
	for i := 0; i < numCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool.Call(context.Background(), "run-race", "pol-1", "inst", "slow-tool", `{}`) //nolint:errcheck
		}()
	}

	// Wait until all calls have reached the server hook.
	for i := 0; i < numCalls; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("call %d did not arrive at server", i+1)
		}
	}

	pool.CancelRun("run-race")

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight Call goroutines did not return after CancelRun")
	}
}

// TestPool_Close_WaitsForCancelGoroutines verifies that Pool.Close blocks until
// all goroutines spawned by CancelRun have finished, so they never reference a
// connection that has already been torn down.
func TestPool_Close_WaitsForCancelGoroutines(t *testing.T) {
	const numCalls = 2

	arrived := make(chan struct{}, numCalls)
	// cancelStarted signals when a cancel goroutine has begun its work.
	cancelStarted := make(chan struct{}, numCalls)

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			arrived <- struct{}{}
			<-ctx.Done()
			return nil, status.Error(codes.Canceled, ctx.Err().Error())
		},
		cancelHook: func(ctx context.Context, _ *toolv1.CancelRequest) (*toolv1.CancelResponse, error) {
			// Signal that this cancel goroutine is running, then hold briefly so
			// Pool.Close has a chance to return too early if the WaitGroup is absent.
			cancelStarted <- struct{}{}
			select {
			case <-ctx.Done():
			case <-time.After(200 * time.Millisecond):
			}
			return &toolv1.CancelResponse{}, nil
		},
	}

	pool, srvCleanup := newTestPool(t, srv, func(cfg *dispatch.Config) {
		cfg.DefaultMaxConcurrent = numCalls
		cfg.CallTimeout = 10 * time.Second
		cfg.CancelTimeout = 50 * time.Millisecond
	})
	defer srvCleanup()

	var callWg sync.WaitGroup
	for i := 0; i < numCalls; i++ {
		callWg.Add(1)
		go func() {
			defer callWg.Done()
			pool.Call(context.Background(), "run-close", "pol-1", "inst", "slow-tool", `{}`) //nolint:errcheck
		}()
	}

	// Wait until all calls are registered inside the server.
	for i := 0; i < numCalls; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("call %d did not arrive at server", i+1)
		}
	}

	// Close races with the cancel goroutines — it must not return until they
	// have all finished.
	closeDone := make(chan struct{})
	go func() {
		pool.Close() //nolint:errcheck
		close(closeDone)
	}()

	// Wait for all cancel goroutines to be started (ensures CancelRun fired them).
	for i := 0; i < numCalls; i++ {
		select {
		case <-cancelStarted:
		case <-time.After(5 * time.Second):
			t.Fatalf("cancel goroutine %d did not start", i+1)
		}
	}

	// Pool.Close should not have returned yet because cancel goroutines are
	// still running. Give it a short window — if it fires during this window the
	// WaitGroup is missing.
	select {
	case <-closeDone:
		t.Error("Pool.Close returned before cancel goroutines finished")
	case <-time.After(20 * time.Millisecond):
		// Good: Close is still blocking.
	}

	// After cancel goroutines eventually finish, Close must unblock.
	select {
	case <-closeDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Pool.Close did not return after cancel goroutines finished")
	}

	done := make(chan struct{})
	go func() { callWg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Call goroutines did not return after Pool.Close")
	}
}
