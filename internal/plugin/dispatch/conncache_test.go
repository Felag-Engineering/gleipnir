package dispatch

// This file uses the internal test package (package dispatch, not dispatch_test)
// so it can access the unexported connCache type directly, which is needed for
// the call-counter and race assertions the plan requires.

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// plainConnFactory returns a ConnFactory backed by an in-process bufconn
// listener.  No gRPC services are registered — we only need the conn to be
// dialable for the lifecycle tests; no RPCs are made.
func plainConnFactory(t *testing.T) (ConnFactory, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()

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
		srv.Stop()
		lis.Close()
	}
	return factory, cleanup
}

// newIdentityCache creates a connCache[grpc.ClientConnInterface] using the
// identity function as newClient.  T = grpc.ClientConnInterface lets the test
// compare returned values without a type assertion while still exercising the
// full cache lifecycle (dial, double-check, closeAll).
//
// Interface equality holds for pointer-equal concrete values: two variables of
// type grpc.ClientConnInterface that wrap the same *grpc.ClientConn compare
// equal with ==, so conn1 != conn2 comparisons in the tests are valid.
func newIdentityCache(factory ConnFactory) *connCache[grpc.ClientConnInterface] {
	return newConnCache(factory, func(c grpc.ClientConnInterface) grpc.ClientConnInterface { return c })
}

// TestConnCache_CachingDedup verifies that two sequential getOrConnect calls
// for the same instance name return the identical *grpc.ClientConn and that
// the factory is invoked exactly once.
func TestConnCache_CachingDedup(t *testing.T) {
	factory, cleanup := plainConnFactory(t)
	defer cleanup()

	var callCount atomic.Int32
	countingFactory := func(name string) (*grpc.ClientConn, error) {
		callCount.Add(1)
		return factory(name)
	}

	cache := newIdentityCache(countingFactory)
	t.Cleanup(func() { _ = cache.closeAll() })

	conn1, err := cache.getOrConnect("inst-a")
	if err != nil {
		t.Fatalf("first getOrConnect: %v", err)
	}
	conn2, err := cache.getOrConnect("inst-a")
	if err != nil {
		t.Fatalf("second getOrConnect: %v", err)
	}
	if conn1 != conn2 {
		t.Error("two getOrConnect calls for the same instance should return the same conn pointer")
	}
	if got := callCount.Load(); got != 1 {
		t.Errorf("factory called %d times, want 1", got)
	}
}

// TestConnCache_LostRaceLoserClose deterministically exercises the double-check
// branch.  A blocking factory holds all goroutines past the fast-path miss until
// released simultaneously; every goroutine dials.  After release, exactly one
// entry survives in the cache (the others close their losers) and a subsequent
// single getOrConnect must not trigger a new dial.
//
// We assert:
//   - factory.callCount == goroutine count (every loser dialed)
//   - all goroutines return the same conn pointer (the winner is cached for all)
//   - a post-race getOrConnect does not increment callCount
func TestConnCache_LostRaceLoserClose(t *testing.T) {
	factory, cleanup := plainConnFactory(t)
	defer cleanup()

	const goroutines = 5
	var callCount atomic.Int32
	// gate is closed once all goroutines have passed the fast-path check and are
	// waiting in the factory; releasing it causes them all to dial simultaneously.
	gate := make(chan struct{})
	// arrived is a barrier: each goroutine writes once after entering the factory.
	// Buffered at the goroutine count so the write never blocks regardless of
	// scheduling. The main goroutine drains exactly `goroutines` values before
	// closing the gate, guaranteeing every goroutine is past the fast-path miss
	// and blocked in the factory — a deterministic exercise of the loser-close
	// path. This is the signal-don't-poll idiom from pool_test.go; a spin-wait
	// here would be both flaky and able to release the gate before all goroutines
	// arrived (making the dial count non-deterministic).
	arrived := make(chan struct{}, goroutines)

	blockingFactory := func(name string) (*grpc.ClientConn, error) {
		callCount.Add(1)
		arrived <- struct{}{} // signal arrival; buffered so this never blocks
		<-gate                // block until released
		return factory(name)
	}

	cache := newIdentityCache(blockingFactory)
	t.Cleanup(func() { _ = cache.closeAll() })

	var wg sync.WaitGroup
	results := make([]grpc.ClientConnInterface, goroutines)
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = cache.getOrConnect("inst-race")
		}()
	}

	// Wait until all goroutines are inside the blocking factory (past the
	// fast-path check) before releasing. Drain exactly `goroutines` arrivals;
	// the deadline is a generous CI-tolerance bound, not a real timing assertion.
	for i := 0; i < goroutines; i++ {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for goroutines to enter the blocking factory")
		}
	}
	close(gate) // release all goroutines simultaneously
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d returned error: %v", i, err)
		}
	}

	// All goroutines must return the same surviving conn (interface comparison).
	survivor := results[0]
	for i, c := range results {
		if c != survivor {
			t.Errorf("goroutine %d got different conn value; winner should be cached for all", i)
		}
	}

	// Every goroutine dialed (callCount == goroutines).
	if got := callCount.Load(); got != goroutines {
		t.Errorf("factory called %d times, want %d (each loser must have dialed)", got, goroutines)
	}

	// A subsequent getOrConnect must reuse the cached entry without a new dial.
	_, err := cache.getOrConnect("inst-race")
	if err != nil {
		t.Fatalf("post-race getOrConnect: %v", err)
	}
	if got := callCount.Load(); got != goroutines {
		t.Errorf("factory called %d times after post-race call, want %d (survivor cached)", got, goroutines)
	}
}

// TestConnCache_ConnectError verifies that a factory error is wrapped with the
// instance name in the message, preserving the exact format consumers rely on.
func TestConnCache_ConnectError(t *testing.T) {
	sentinel := errors.New("dial refused")
	failFactory := func(_ string) (*grpc.ClientConn, error) {
		return nil, sentinel
	}

	cache := newIdentityCache(failFactory)
	t.Cleanup(func() { _ = cache.closeAll() })

	_, err := cache.getOrConnect("inst-err")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(err, sentinel) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), "inst-err") {
		t.Errorf("error message %q does not contain instance name %q", err.Error(), "inst-err")
	}
}

// TestConnCache_CloseAllAndRedial verifies that closeAll resets the cache to a
// fresh empty map, causing subsequent getOrConnect calls to re-dial.
//
// Functional check: call-counter increments prove the cache was cleared, not
// conn.GetState()==Shutdown (which is async and would flake).
func TestConnCache_CloseAllAndRedial(t *testing.T) {
	factory, cleanup := plainConnFactory(t)
	defer cleanup()

	var callCount atomic.Int32
	countingFactory := func(name string) (*grpc.ClientConn, error) {
		callCount.Add(1)
		return factory(name)
	}

	cache := newIdentityCache(countingFactory)

	// Dial 3 distinct instances.
	for _, name := range []string{"inst-1", "inst-2", "inst-3"} {
		if _, err := cache.getOrConnect(name); err != nil {
			t.Fatalf("getOrConnect(%q): %v", name, err)
		}
	}
	if got := callCount.Load(); got != 3 {
		t.Errorf("factory called %d times before close, want 3", got)
	}

	if err := cache.closeAll(); err != nil {
		t.Errorf("closeAll: %v", err)
	}

	// Re-dial all 3; each must trigger a new factory call because closeAll
	// reset the cache to an empty map.
	for _, name := range []string{"inst-1", "inst-2", "inst-3"} {
		if _, err := cache.getOrConnect(name); err != nil {
			t.Fatalf("post-close getOrConnect(%q): %v", name, err)
		}
	}
	if got := callCount.Load(); got != 6 {
		t.Errorf("factory called %d times after close+redial, want 6 (3+3)", got)
	}

	// Clean up the newly opened connections.
	if err := cache.closeAll(); err != nil {
		t.Errorf("final closeAll: %v", err)
	}
}

// TestConnCache_DoubleCloseSafety verifies that calling closeAll twice does not
// panic and that the second call returns nil (nothing left to close).
func TestConnCache_DoubleCloseSafety(t *testing.T) {
	factory, cleanup := plainConnFactory(t)
	defer cleanup()

	cache := newIdentityCache(factory)

	if _, err := cache.getOrConnect("inst-x"); err != nil {
		t.Fatalf("getOrConnect: %v", err)
	}

	if err := cache.closeAll(); err != nil {
		t.Errorf("first closeAll: %v", err)
	}
	// Second close must be a no-op — the map was reset to empty by the first call.
	if err := cache.closeAll(); err != nil {
		t.Errorf("second closeAll returned error: %v", err)
	}
}
