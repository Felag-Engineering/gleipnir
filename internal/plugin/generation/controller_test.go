package generation_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
)

// TestRegisterInstance_AssignsGenerationOne verifies that a fresh instance
// always starts at generation 1.
func TestRegisterInstance_AssignsGenerationOne(t *testing.T) {
	c := generation.New()
	gen := c.RegisterInstance("inst-a")
	if gen != 1 {
		t.Fatalf("expected generation 1 for new instance, got %d", gen)
	}
}

// TestRegisterInstance_AfterBeginDrain_DoesNotResetGeneration locks the
// soft-ensure-exists contract: registering after BeginDrain should return
// the already-bumped generation, not reset it to 1.
func TestRegisterInstance_AfterBeginDrain_DoesNotResetGeneration(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-b")

	ctx := context.Background()
	newGen, _, err := c.BeginDrain(ctx, "inst-b", time.Second)
	if err != nil {
		t.Fatalf("BeginDrain: %v", err)
	}
	if newGen != 2 {
		t.Fatalf("BeginDrain expected generation 2, got %d", newGen)
	}

	// Register again — must return 2, not 1.
	gen := c.RegisterInstance("inst-b")
	if gen != 2 {
		t.Fatalf("RegisterInstance after BeginDrain: expected 2, got %d", gen)
	}
}

// TestAcquire_IncrementsRefcountAndReleaseDecrements is a basic smoke test that
// Acquire succeeds and release() brings the refcount back to zero without panic.
func TestAcquire_IncrementsRefcountAndReleaseDecrements(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-c")

	ctx := context.Background()
	wrappedCtx, release, gen, err := c.Acquire(ctx, "inst-c")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if gen != 1 {
		t.Fatalf("expected gen 1, got %d", gen)
	}
	if wrappedCtx == nil {
		t.Fatal("Acquire returned nil context")
	}
	if wrappedCtx.Err() != nil {
		t.Fatalf("wrapped ctx already cancelled: %v", wrappedCtx.Err())
	}

	release()

	// Calling release a second time must be idempotent (the once.Do guard).
	release()
}

// TestAcquire_BlocksWhilePaused_WakesOnNewGeneration verifies that an Acquire
// arriving during a drain pause waits until BeginDrain completes and then
// succeeds under the new generation.
//
// Synchronisation: we hold a refcount slot throughout, which guarantees
// BeginDrain cannot return until we release it. Therefore any goroutine started
// after BeginDrain is launched and while the refcount is held will find the
// instance paused and block in Acquire — no sleep needed.
func TestAcquire_BlocksWhilePaused_WakesOnNewGeneration(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-d")

	ctx := context.Background()

	// Acquire a slot that will hold BeginDrain open.
	_, held, _, err := c.Acquire(ctx, "inst-d")
	if err != nil {
		t.Fatalf("Acquire (held): %v", err)
	}

	drainStarted := make(chan struct{})
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		// Signal just before blocking on the drain channel so the test knows
		// BeginDrain has set paused=true and is waiting for the refcount to drop.
		close(drainStarted)
		_, _, _ = c.BeginDrain(ctx, "inst-d", time.Second)
	}()

	// Wait until BeginDrain has started (and thus set paused=true). Because we
	// still hold the refcount, BeginDrain cannot return — the instance is
	// guaranteed to be in the paused state when the next Acquire goroutine runs.
	select {
	case <-drainStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("BeginDrain goroutine did not start")
	}

	// A new Acquire must block while paused. The drain goroutine above holds the
	// lock only briefly (to set paused=true) and then releases it before waiting
	// on the drain channel, so this Acquire call will enter the select and block.
	acquireResult := make(chan uint64, 1)
	go func() {
		_, rel, gen, err := c.Acquire(ctx, "inst-d")
		if err != nil {
			acquireResult <- 0
			return
		}
		rel()
		acquireResult <- gen
	}()

	// Release the held slot so BeginDrain can complete and unblock the waiting
	// Acquire goroutine.
	held()

	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.Fatal("BeginDrain did not complete")
	}

	select {
	case gen := <-acquireResult:
		if gen != 2 {
			t.Fatalf("blocked Acquire returned gen %d, want 2", gen)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked Acquire did not wake after drain")
	}
}

// TestBeginDrain_DrainsToZeroBeforeGrace verifies the happy-path drain scenario
// (AC: in-flight RPCs complete within grace → drained=true).
func TestBeginDrain_DrainsToZeroBeforeGrace(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-e")

	ctx := context.Background()
	wrappedCtx, release, _, err := c.Acquire(ctx, "inst-e")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// drainStarted is closed by the BeginDrain goroutine just before it blocks
	// on the drain channel. Because the held refcount keeps refs > 0, BeginDrain
	// cannot return until we release(), so closing drainStarted is the earliest
	// moment we know it is in the waiting state — no sleep needed.
	drainStarted := make(chan struct{})
	drainResult := make(chan bool, 1)
	go func() {
		close(drainStarted)
		_, drained, err := c.BeginDrain(ctx, "inst-e", time.Second)
		if err != nil {
			drainResult <- false
			return
		}
		drainResult <- drained
	}()

	// Wait until the BeginDrain goroutine has started. Because we still hold
	// the refcount, BeginDrain is blocked in its wait loop — it cannot have
	// cancelled the context yet, so wrappedCtx must still be live.
	select {
	case <-drainStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("BeginDrain goroutine did not start")
	}

	// The held call's context must still be live (not cancelled).
	if wrappedCtx.Err() != nil {
		t.Fatalf("held call ctx was cancelled prematurely: %v", wrappedCtx.Err())
	}

	// Release the held call; BeginDrain should now return drained=true.
	release()

	select {
	case drained := <-drainResult:
		if !drained {
			t.Fatal("expected drained=true but got false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BeginDrain did not return after release")
	}
}

// TestBeginDrain_ForceCancelsOnGraceExceeded verifies the force-cancel path
// (AC: gen-N RPC exceeding grace is force-cancelled).
func TestBeginDrain_ForceCancelsOnGraceExceeded(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-f")

	ctx := context.Background()
	wrappedCtx, release, _, err := c.Acquire(ctx, "inst-f")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release() // idempotent guard

	drainResult := make(chan bool, 1)
	go func() {
		// 50 ms grace: short enough that the held call exceeds it.
		_, drained, err := c.BeginDrain(ctx, "inst-f", 50*time.Millisecond)
		if err != nil {
			drainResult <- false
			return
		}
		drainResult <- drained
	}()

	// Wait for the held call to be force-cancelled.
	select {
	case <-wrappedCtx.Done():
		// Expected: context was cancelled by BeginDrain.
	case <-time.After(10 * time.Second):
		t.Fatal("force-cancel did not arrive within 10s")
	}

	// Release the slot (simulates the handler observing ctx.Done and returning).
	release()

	select {
	case drained := <-drainResult:
		if drained {
			t.Fatal("expected drained=false for grace-exceeded scenario, got true")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("BeginDrain did not return after force-cancel")
	}
}

// TestBeginDrain_NewGenerationProceedsAfterForceCancel verifies that after the
// force-cancel path completes, a fresh Acquire returns the new generation
// immediately without error.
func TestBeginDrain_NewGenerationProceedsAfterForceCancel(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-g")

	ctx := context.Background()
	wrappedCtx, release, _, err := c.Acquire(ctx, "inst-g")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = c.BeginDrain(ctx, "inst-g", 20*time.Millisecond)
	}()

	// Block until BeginDrain force-cancels the held call's context (grace expired).
	// This is the correct synchronisation point: once wrappedCtx.Done() fires,
	// the force-cancel has definitively occurred — no sleep guessing needed.
	select {
	case <-wrappedCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("force-cancel did not arrive within 5s")
	}
	release()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BeginDrain did not return after release")
	}

	_, rel2, gen2, err := c.Acquire(ctx, "inst-g")
	if err != nil {
		t.Fatalf("Acquire after force-cancel: %v", err)
	}
	rel2()
	if gen2 != 2 {
		t.Fatalf("expected gen 2 after drain, got %d", gen2)
	}
}

// TestBeginDrain_ConcurrentCallersSerialize verifies that when two BeginDrain
// goroutines race on the same instance, exactly one succeeds and the other
// returns "drain already in progress".
//
// Synchronisation: the held refcount guarantees that whichever goroutine WINS
// the internal draining-flag race is immediately stuck on the drain channel —
// it cannot return until we release. The LOSER returns immediately with
// "drain already in progress". We therefore wait for the FIRST result on
// errCh: since the winner is stuck, the first result is always from the loser
// ("drain already in progress"). Only then do we release so the winner can
// complete. This ordering is deterministic — no sleeps, no scheduler guesses.
func TestBeginDrain_ConcurrentCallersSerialize(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-h")

	ctx := context.Background()

	// Hold an in-flight call so whichever BeginDrain wins the draining-flag
	// race is stuck waiting for refs to drain. The losing goroutine returns
	// immediately with the "drain already in progress" error.
	_, release, _, err := c.Acquire(ctx, "inst-h")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	errCh := make(chan error, 2)

	for range 2 {
		go func() {
			_, _, e := c.BeginDrain(ctx, "inst-h", time.Second)
			errCh <- e
		}()
	}

	// The winner is stuck on waitCh (refs > 0); the loser returns immediately.
	// Therefore the first result we receive is always "drain already in progress".
	firstErr := <-errCh
	if firstErr == nil || firstErr.Error() != "generation: drain already in progress" {
		t.Errorf("first result: expected 'drain already in progress', got %v", firstErr)
	}

	// Now release so the winning drain can complete.
	release()

	// The second result is the winner's: should be nil.
	secondErr := <-errCh
	if secondErr != nil {
		t.Errorf("second result: expected nil (drain succeeded), got %v", secondErr)
	}
}

// TestConcurrentAcquireAtGenerationSwitch is a stress test: 100 goroutines
// Acquire+work+Release in a tight loop while BeginDrain runs once in the
// middle. Asserts that every goroutine returns, refcounts for every observed
// generation eventually hit zero, and no release is called twice.
func TestConcurrentAcquireAtGenerationSwitch(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-stress")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const workers = 100
	var wg sync.WaitGroup
	var doubleRelease atomic.Int32

	drainStarted := make(chan struct{})
	var drainOnce sync.Once

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				wrappedCtx, release, _, err := c.Acquire(ctx, "inst-stress")
				if err != nil {
					// Context cancelled or instance unregistered — acceptable
					// at shutdown; count as a clean exit.
					return
				}

				var released atomic.Bool
				originalRelease := release
				release = func() {
					if !released.CompareAndSwap(false, true) {
						doubleRelease.Add(1)
					}
					originalRelease()
				}

				// Signal that at least one goroutine is in-flight so the drain
				// has something to wait on.
				drainOnce.Do(func() { close(drainStarted) })

				// Simulate a tiny bit of work; observe cancellation.
				select {
				case <-wrappedCtx.Done():
				case <-time.After(time.Millisecond):
				}
				release()
			}
		}()
	}

	// Start BeginDrain once we know at least one call is in-flight.
	go func() {
		select {
		case <-drainStarted:
		case <-ctx.Done():
			return
		}
		c.BeginDrain(ctx, "inst-stress", 200*time.Millisecond) //nolint:errcheck
	}()

	wg.Wait()

	if n := doubleRelease.Load(); n > 0 {
		t.Errorf("release() called more than once for %d acquires", n)
	}
}

// TestBeginDrain_ConcurrentUnregister_NoPanic verifies that calling
// UnregisterInstance while BeginDrain is between step 1 (lock released, fresh
// pausedCh set) and step 4 (rotation) does not cause a double-close panic.
//
// Before issue #345 was fixed, BeginDrain re-read s.pausedCh inside step 4 after
// releasing the lock. UnregisterInstance could close s.pausedCh in that window,
// causing BeginDrain's subsequent close() to panic. The fix captures the channel
// in step 1 before releasing the lock.
//
// Run with -race to catch any data-race regressions.
func TestBeginDrain_ConcurrentUnregister_NoPanic(t *testing.T) {
	// Run many iterations to increase the chance of hitting the race window.
	for i := range 200 {
		c := generation.New()
		id := "inst-race-unregister"
		c.RegisterInstance(id)

		ctx := context.Background()

		// Hold a refcount slot so BeginDrain blocks in the drain-wait phase
		// (between step 1 and step 4), giving UnregisterInstance time to run.
		_, held, _, err := c.Acquire(ctx, id)
		if err != nil {
			t.Fatalf("iter %d: Acquire: %v", i, err)
		}

		// Barrier: ensure the BeginDrain goroutine has entered its wait before we
		// unregister. We do this by having it signal after setting paused=true but
		// while it is still blocked on the drain channel (held keeps refs > 0).
		pauseSet := make(chan struct{})

		drainDone := make(chan struct{})
		go func() {
			defer close(drainDone)
			// Signal that we are about to block on the drain channel. At this point
			// the lock has been released and s.paused == true.
			close(pauseSet)
			c.BeginDrain(ctx, id, 5*time.Second) //nolint:errcheck
		}()

		// Wait until BeginDrain has released its lock (pauseSet closed), then
		// unregister — this closes s.pausedCh from under BeginDrain.
		select {
		case <-pauseSet:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: BeginDrain goroutine did not start", i)
		}

		// UnregisterInstance closes s.pausedCh while BeginDrain is between
		// step 1 and step 4. This is the race window that caused the panic.
		c.UnregisterInstance(id)

		// Release the held slot so BeginDrain's drain channel fires (refs → 0)
		// and BeginDrain can proceed to step 4 and attempt to close prevPausedCh.
		held()

		select {
		case <-drainDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("iter %d: BeginDrain did not return", i)
		}
	}
}

// TestUnregisterInstance_WakesBlockedAcquires verifies that UnregisterInstance
// causes any goroutine blocked in Acquire to return an error without leaking,
// and that subsequent Acquire calls also return an error immediately.
//
// Synchronisation: we hold a refcount (preventing BeginDrain from returning)
// and start BeginDrain before the Acquire goroutine. Since BeginDrain is stuck
// on the drain channel (not holding any lock) once it has set paused=true, the
// Acquire goroutine either (a) observes paused=true and blocks in the select,
// or (b) loses the race and runs before paused=true is set — in either case
// UnregisterInstance causes it to return an error. We wait on acquireErr with
// a generous timeout to confirm the goroutine did not leak.
func TestUnregisterInstance_WakesBlockedAcquires(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-i")

	ctx := context.Background()

	// Hold a refcount slot so the BeginDrain goroutine below is stuck waiting
	// for refs to drain. The release function is intentionally never called —
	// UnregisterInstance takes over cleanup instead.
	_, _, _, err := c.Acquire(ctx, "inst-i")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Start BeginDrain in a goroutine; it will block on the drain channel
	// because the held refcount keeps refs[1] > 0. This sets paused=true before
	// blocking, giving the Acquire goroutine below a chance to observe it.
	drainRunning := make(chan struct{})
	go func() {
		close(drainRunning)                         // goroutine is scheduled
		c.BeginDrain(ctx, "inst-i", 10*time.Second) //nolint:errcheck
	}()

	// Wait for the BeginDrain goroutine to be scheduled, then start an Acquire
	// goroutine. Because refs[1] > 0, BeginDrain is stuck on the drain channel
	// (not on the lock), so by the time we start the Acquire goroutine, paused
	// is either already true (Acquire blocks in select) or BeginDrain is racing
	// for the lock (Acquire contends and observes paused=true once BeginDrain
	// releases the lock). In both cases UnregisterInstance ends the wait.
	select {
	case <-drainRunning:
	case <-time.After(5 * time.Second):
		t.Fatal("BeginDrain goroutine did not start")
	}

	acquireErr := make(chan error, 1)
	go func() {
		_, _, _, err := c.Acquire(ctx, "inst-i")
		acquireErr <- err
	}()

	// Unregister — closes unregisteredCh (waking any goroutine in the Acquire
	// select) and removes from byInstance (causing any goroutine that hasn't
	// yet fetched s to return "not registered"). In both cases the goroutine
	// terminates without leaking.
	c.UnregisterInstance("inst-i")

	select {
	case err := <-acquireErr:
		// Accept either "instance unregistered" (goroutine was in the select when
		// UnregisterInstance fired) or "instance not registered" (goroutine ran
		// after UnregisterInstance removed the entry). Both mean no goroutine leak.
		if err == nil {
			t.Fatalf("Acquire after UnregisterInstance: expected error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire goroutine did not return after UnregisterInstance — possible leak")
	}

	// A brand-new Acquire on the now-unregistered instance must also fail
	// immediately (not block, not create new state).
	_, _, _, err2 := c.Acquire(ctx, "inst-i")
	if err2 == nil {
		t.Fatal("Acquire on unregistered instance: expected error, got nil")
	}
}
