package e2e_test

// generation_drain_e2e_test.go exercises the generation-drain guarantee over a
// real gRPC wire with the production interceptor chain.
//
// Coverage gap this fills: internal/plugin/generation/controller_test.go has 16
// tests covering Acquire/BeginDrain/force-cancel at the unit level (direct calls
// into the controller, no wire). internal/plugin/hostsvc/generation_interceptor_test.go
// tests the interceptor in-process (fabricated incoming context, no network).
// Neither test proves the full composed path:
//
//	client → network → token interceptor → generation interceptor → stub handler
//	     ← gRPC status ← network ←────────────────────────────────────────────
//
// This file adds two tests that exercise that path end-to-end:
//
//  1. TestHostRPC_GenerationDrain_WaitsForInflight — the natural drain path: an
//     in-flight RPC holds a refcount; BeginDrain cannot return drained=true until
//     the RPC completes (proven by ordering, not by reading the internal counter).
//
//  2. TestHostRPC_GenerationDrain_ForceCancelsStraggler — the force-cancel path:
//     a handler that blocks until ctx.Done() is force-cancelled by BeginDrain
//     after a short grace; the RPC returns codes.Canceled and BeginDrain returns
//     drained=false.

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// TestHostRPC_GenerationDrain_WaitsForInflight verifies the natural drain path:
// when an in-flight Host RPC holds a refcount on the current generation,
// BeginDrain cannot return drained=true until after that RPC completes.
//
// Proof by ordering (not by reading internal refcount state):
//
//  1. Launch RPC in a goroutine; wait until the handler signals it has entered.
//     At this point the generation interceptor has called Acquire and incremented
//     the refcount for generation N.
//  2. Launch BeginDrain in a goroutine. It enters the pause+wait loop because
//     refs[N] > 0.
//  3. Assert the RPC result channel is still empty while the handler is held
//     (proves BeginDrain is waiting, not that the RPC finished early).
//  4. Release the handler (close releaseHandler). The RPC completes → release()
//     decrements refs[N] to zero → BeginDrain observes zero → returns drained=true.
//
// The generation controller's controller.go:324-347 guarantees drained=true cannot
// be returned until the refcount reaches zero, so if the test observes drained=true
// AFTER close(releaseHandler), the ordering is proven without reading refs directly.
func TestHostRPC_GenerationDrain_WaitsForInflight(t *testing.T) {
	t.Parallel()

	const instanceID = "e2e-drain-natural-inst"

	reg := identity.New()
	ctrl := generation.New()
	ctrl.RegisterInstance(instanceID)

	token, err := reg.Issue(instanceID)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// handlerEntered is closed by onCall the moment the stub body begins executing.
	// Signalling via close (not send) is race-free: multiple concurrent readers
	// see the close atomically with no coordination needed.
	handlerEntered := make(chan struct{})

	// releaseHandler is closed by the test to let the handler return naturally.
	releaseHandler := make(chan struct{})

	stub := &stubHostServer{
		onCall: func(ctx context.Context) {
			// Signal that the interceptor has acquired a refcount and the handler
			// body is executing.
			close(handlerEntered)
			// Block until either the test releases us (natural path) or the context
			// is cancelled (force-cancel path — not used in this test, but the
			// select handles it cleanly so the goroutine never leaks on test failure).
			select {
			case <-releaseHandler:
			case <-ctx.Done():
			}
		},
	}

	client, stop := startHostServer(t, reg, ctrl, stub)
	defer stop()

	// --- Step 1: Launch RPC in a goroutine ---
	type rpcResult struct{ err error }
	rpcDone := make(chan rpcResult, 1)
	go func() {
		ctx := callCtxWithToken(token)
		_, err := client.GetRunContext(ctx, &hostv1.GetRunContextRequest{})
		rpcDone <- rpcResult{err: err}
	}()

	// --- Step 2: Wait for the handler to enter (5s generous deadline) ---
	// The 5s deadline is a CI-tolerance bound, not a real timing assertion;
	// the handler should signal within milliseconds.
	select {
	case <-handlerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not enter within 5s")
	}

	// --- Step 3: Launch BeginDrain in a goroutine ---
	type drainResult struct {
		newGen  uint64
		drained bool
		err     error
	}
	// drainStarted signals that the BeginDrain goroutine has entered the call
	// and is now in the pause state. We need this to guarantee the goroutine's
	// drain is in progress before we fire the concurrent second drain.
	drainStarted := make(chan struct{})
	drainDone := make(chan drainResult, 1)
	go func() {
		// Signal just before we call BeginDrain so the test knows we are committed.
		// There's a tiny window before BeginDrain acquires the lock, but it is
		// harmless: the inline concurrent call below either sees s.draining==false
		// (wins the race, becomes first drain) or ==true (second drain, gets error).
		// Either ordering produces a valid "drain already in progress" result for
		// whichever call is second. The check below accounts for this.
		close(drainStarted)
		newGen, drained, err := ctrl.BeginDrain(context.Background(), instanceID, 10*time.Second)
		drainDone <- drainResult{newGen: newGen, drained: drained, err: err}
	}()

	// Wait for the goroutine to be in-flight before firing the second drain.
	select {
	case <-drainStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("drain goroutine did not start within 5s")
	}

	// Give the goroutine a moment to actually call BeginDrain and enter the pause
	// state before we fire the concurrent second drain. A tiny sleep here is
	// acceptable because this is not a timing assertion: it only determines which
	// call wins the s.draining lock, not when the drain completes.
	time.Sleep(10 * time.Millisecond)

	// --- Step 4: Prove BeginDrain is still waiting ---
	// The drain cannot return until refs reach zero. The RPC is still holding a
	// refcount (handler has not returned yet), so drainDone must be empty.
	select {
	case r := <-drainDone:
		t.Fatalf("BeginDrain returned before handler was released: drained=%v newGen=%d err=%v", r.drained, r.newGen, r.err)
	default:
		// Expected: BeginDrain is blocked waiting for the in-flight refcount.
	}

	// Also check the concurrent-drain serialisation guarantee: a second BeginDrain
	// while one is already in progress must return "drain already in progress".
	// This is checked inline (before release) to avoid complicating the wire test
	// with extra goroutines. By now the goroutine's drain is in-progress
	// (10ms sleep above), so this call reliably sees s.draining==true.
	_, _, concurrentDrainErr := ctrl.BeginDrain(context.Background(), instanceID, time.Second)
	if concurrentDrainErr == nil {
		t.Error("second concurrent BeginDrain should have returned an error")
	} else if concurrentDrainErr.Error() != "generation: drain already in progress" {
		t.Errorf("second concurrent BeginDrain: want 'generation: drain already in progress', got %q", concurrentDrainErr)
	}

	// --- Step 5: Release the handler and wait for drain to complete ---
	close(releaseHandler)

	var dr drainResult
	select {
	case dr = <-drainDone:
	case <-time.After(10 * time.Second):
		t.Fatal("BeginDrain did not complete within 10s after handler release")
	}
	if dr.err != nil {
		t.Fatalf("BeginDrain returned error: %v", dr.err)
	}
	if !dr.drained {
		t.Error("BeginDrain: want drained=true (natural drain path), got false")
	}
	if dr.newGen != 2 {
		t.Errorf("BeginDrain new generation: want 2, got %d", dr.newGen)
	}

	// The RPC should have completed successfully (handler returned nil err).
	select {
	case r := <-rpcDone:
		if r.err != nil {
			t.Errorf("RPC returned error on natural drain path: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC did not complete within 5s after handler release")
	}
}

// TestHostRPC_GenerationDrain_ForceCancelsStraggler verifies the force-cancel
// path: a handler that blocks indefinitely on ctx.Done() is interrupted when
// BeginDrain's grace period elapses, and the RPC returns codes.Canceled.
//
// The only unavoidable wall-clock value is the 50ms BeginDrain grace. The
// ≥5× convention (CLAUDE.md) requires the completion deadline to be at least
// 5×50ms = 250ms; we use 10s (200×) to absorb CI scheduling jitter and account
// for the production forceCancelGrace=5s constant in controller.go:22.
func TestHostRPC_GenerationDrain_ForceCancelsStraggler(t *testing.T) {
	t.Parallel()

	const instanceID = "e2e-drain-force-cancel-inst"

	reg := identity.New()
	ctrl := generation.New()
	ctrl.RegisterInstance(instanceID)

	token, err := reg.Issue(instanceID)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// handlerEntered signals that the interceptor has acquired a refcount and the
	// handler is executing. The test must wait for this before calling BeginDrain
	// so it is guaranteed that refs[N] > 0 when the drain starts.
	handlerEntered := make(chan struct{})

	stub := &stubHostServer{
		onCall: func(ctx context.Context) {
			// Signal entry first, then block until the context is force-cancelled.
			// The force-cancel path is the only exit here; the test never sends on
			// releaseHandler so only ctx.Done() fires.
			close(handlerEntered)
			<-ctx.Done() // blocks until BeginDrain cancels the wrapped context
		},
	}

	client, stop := startHostServer(t, reg, ctrl, stub)
	defer stop()

	// --- Step 1: Launch RPC ---
	type rpcResult struct{ err error }
	rpcDone := make(chan rpcResult, 1)
	go func() {
		ctx := callCtxWithToken(token)
		_, err := client.GetRunContext(ctx, &hostv1.GetRunContextRequest{})
		rpcDone <- rpcResult{err: err}
	}()

	// --- Step 2: Wait for handler entry ---
	select {
	case <-handlerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not enter within 5s")
	}

	// --- Step 3: BeginDrain with a short grace to trigger force-cancel ---
	//
	// 50ms grace: short enough that the test finishes quickly, long enough that CI
	// scheduling jitter cannot make the drain complete before the refcount is seen.
	// After the grace the generation controller invokes entry.cancel(), which
	// cancels the wrapped ctx delivered to the handler. The handler's <-ctx.Done()
	// then unblocks and returns ctx.Err() (context.Canceled). The stub's
	// ctx.Err() check (hostrpc_harness_test.go) propagates that as an error over
	// the wire, which the gRPC runtime converts to codes.Canceled.
	const drainGrace = 50 * time.Millisecond

	type drainResult struct {
		newGen  uint64
		drained bool
		err     error
	}
	drainDone := make(chan drainResult, 1)
	go func() {
		newGen, drained, err := ctrl.BeginDrain(context.Background(), instanceID, drainGrace)
		drainDone <- drainResult{newGen: newGen, drained: drained, err: err}
	}()

	// --- Step 4: Assert force-cancel path ---
	//
	// 10s deadline = 200× the 50ms grace. Justification: after drainGrace elapses
	// the controller calls entry.cancel(), then waits up to forceCancelGrace=5s
	// (controller.go:22) for refs to reach zero. The total window is at most
	// drainGrace + forceCancelGrace = ~5.05s. Our 10s deadline is ≥2× that, and
	// ≥200× the stated 50ms grace per the CLAUDE.md ≥5× convention.
	const completionDeadline = 10 * time.Second

	var dr drainResult
	select {
	case dr = <-drainDone:
	case <-time.After(completionDeadline):
		t.Fatalf("BeginDrain did not complete within %s (forceCancelGrace is 5s, completionDeadline >> drainGrace)", completionDeadline)
	}

	if dr.err != nil {
		t.Fatalf("BeginDrain returned unexpected error: %v", dr.err)
	}
	// drained==false confirms the force-cancel path was taken (controller.go:350).
	if dr.drained {
		t.Error("BeginDrain: want drained=false (force-cancel path), got true")
	}
	// newGen must still be bumped even on the force-cancel path.
	if dr.newGen != 2 {
		t.Errorf("BeginDrain new generation: want 2, got %d", dr.newGen)
	}

	// The RPC must have returned a non-nil error with codes.Canceled.
	// Wait up to completionDeadline (already started above; remaining time is ≥9.9s).
	var rpcErr error
	select {
	case r := <-rpcDone:
		rpcErr = r.err
	case <-time.After(completionDeadline):
		t.Fatal("RPC did not return within completionDeadline after force-cancel")
	}

	if rpcErr == nil {
		t.Fatal("RPC should have returned an error after force-cancel, got nil")
	}
	st, ok := status.FromError(rpcErr)
	if !ok || st.Code() != codes.Canceled {
		t.Errorf("RPC error after force-cancel: want codes.Canceled, got %v", rpcErr)
	}
}
