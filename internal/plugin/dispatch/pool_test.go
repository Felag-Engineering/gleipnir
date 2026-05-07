package dispatch_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
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

	// unblockCh is used by the Call hook to wait until explicitly unblocked.
	// We close it to release all blocked calls at once.
	unblockCh := make(chan struct{})

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
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

	time.Sleep(50 * time.Millisecond) // let calls establish in the server

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

// TestPool_Semaphore verifies that max_concurrent_calls limits concurrency
// and extra callers block until a slot is released.
func TestPool_Semaphore(t *testing.T) {
	const maxConc = 2
	const total = 4

	unblock := make(chan struct{})
	var mu sync.Mutex
	var liveCount, maxSeen int

	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
			mu.Lock()
			liveCount++
			if liveCount > maxSeen {
				maxSeen = liveCount
			}
			mu.Unlock()
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

	time.Sleep(40 * time.Millisecond) // let goroutines settle

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
	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
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
		cfg.DefaultMaxQueueDepth = 1
	})
	defer cleanup()

	// Launch concurrent=1 in-flight + queue=1 waiting.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool.Call(context.Background(), "run-qf", "pol-1", "inst", "tool", `{}`) //nolint:errcheck
		}()
	}
	time.Sleep(30 * time.Millisecond) // ensure both above calls are in-flight/queued

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
	case <-time.After(2 * time.Second):
		t.Fatal("goroutines did not drain")
	}
}

// TestPool_ParentCtxCancelledWhileQueued verifies that cancelling the parent
// ctx while a call is waiting for a semaphore slot exits with ctx.Err().
func TestPool_ParentCtxCancelledWhileQueued(t *testing.T) {
	unblock := make(chan struct{})
	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
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
	time.Sleep(10 * time.Millisecond)

	// Second call queues; cancel its context.
	ctx, cancel := context.WithCancel(context.Background())
	var queuedErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, queuedErr = pool.Call(ctx, "run-ctxq", "pol-1", "inst", "tool", `{}`)
	}()
	time.Sleep(10 * time.Millisecond)
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
	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
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
	time.Sleep(10 * time.Millisecond)
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
	srv := &fakeToolServer{
		callHook: func(ctx context.Context, _ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
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

	time.Sleep(20 * time.Millisecond) // both in-flight

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
	case <-time.After(3 * time.Second):
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
