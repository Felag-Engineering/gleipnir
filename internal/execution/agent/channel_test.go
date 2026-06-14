// Package agent — channel_test.go covers the inAppChannel waiter map in
// isolation: RegisterWaiter / Resolve / UnregisterWaiter, the production
// primitives FeedbackHandler.Wait drives. The audit-step and run-transition
// behavior that wraps these primitives is covered through the real handler in
// feedback_test.go (e.g. TestFeedbackHandler_Wait_ResponseReceived); these tests
// deliberately exercise the map mechanics alone so a concurrency regression in
// Resolve surfaces here rather than buried in a handler test.
package agent

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// newInAppChannelForTest seeds a minimal store + state machine so newInAppChannel
// has its non-nil dependencies, then returns a ready-to-use inAppChannel. The
// waiter-map primitives under test (RegisterWaiter/Resolve/UnregisterWaiter)
// touch neither the DB nor the AuditWriter, so tests drive them directly.
func newInAppChannelForTest(t *testing.T, runID string) *inAppChannel {
	t.Helper()
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, runID, "p1", model.RunStatusRunning)

	sm := NewRunStateMachine(runID, model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { w.Close() }) //nolint:errcheck

	return newInAppChannel(w, sm)
}

// TestInAppChannel_Resolve_DeliversToWaiter verifies the happy path: a registered
// waiter receives the operator response delivered by Resolve.
func TestInAppChannel_Resolve_DeliversToWaiter(t *testing.T) {
	inApp := newInAppChannelForTest(t, "run1")

	ch := inApp.RegisterWaiter("fb-1")

	if err := inApp.Resolve("fb-1", "operator response"); err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}

	select {
	case resp := <-ch:
		if resp.text != "operator response" {
			t.Errorf("response = %q, want %q", resp.text, "operator response")
		}
	case <-time.After(time.Second):
		t.Fatal("Resolve did not deliver to the waiter within 1s")
	}
}

// TestInAppChannel_Resolve_UnknownRequestID verifies that Resolve on a fresh
// channel with no registered waiter returns ErrUnknownRequestID.
func TestInAppChannel_Resolve_UnknownRequestID(t *testing.T) {
	inApp := newInAppChannelForTest(t, "run1")

	err := inApp.Resolve("nonexistent-id", "body")
	if !errors.Is(err, ErrUnknownRequestID) {
		t.Errorf("Resolve = %v, want ErrUnknownRequestID", err)
	}
}

// TestInAppChannel_Resolve_AfterRelease_ReturnsUnknown verifies that once a
// waiter is released (the production analog: FeedbackHandler.Wait returns via
// timeout or ctx-cancel and its deferred release runs), a subsequent Resolve
// call yields ErrUnknownRequestID rather than sending to a leaked channel.
func TestInAppChannel_Resolve_AfterRelease_ReturnsUnknown(t *testing.T) {
	inApp := newInAppChannelForTest(t, "run1")

	_ = inApp.RegisterWaiter("fb-timeout")
	inApp.UnregisterWaiter("fb-timeout")

	err := inApp.Resolve("fb-timeout", "late response")
	if !errors.Is(err, ErrUnknownRequestID) {
		t.Errorf("Resolve after release = %v, want ErrUnknownRequestID", err)
	}
}

// TestInAppChannel_Resolve_DoubleCall verifies that the first Resolve returns nil
// and the second returns ErrUnknownRequestID (delete-under-lock prevents double
// delivery).
func TestInAppChannel_Resolve_DoubleCall(t *testing.T) {
	inApp := newInAppChannelForTest(t, "run1")

	ch := inApp.RegisterWaiter("fb-double")

	if err := inApp.Resolve("fb-double", "first"); err != nil {
		t.Fatalf("first Resolve: unexpected error: %v", err)
	}
	<-ch // drain the delivered response so the channel state mirrors production

	err := inApp.Resolve("fb-double", "second")
	if !errors.Is(err, ErrUnknownRequestID) {
		t.Errorf("second Resolve = %v, want ErrUnknownRequestID", err)
	}
}

// TestInAppChannel_Resolve_Concurrent registers 50 waiters with unique
// feedback_ids, then resolves all 50 from 50 concurrent goroutines. All responses
// must be delivered correctly and the waiter map must be empty afterwards. The
// test is -race clean.
func TestInAppChannel_Resolve_Concurrent(t *testing.T) {
	const N = 50
	inApp := newInAppChannelForTest(t, "run1")

	chans := make([]<-chan inAppResponse, N)
	for i := 0; i < N; i++ {
		chans[i] = inApp.RegisterWaiter(fmt.Sprintf("fb-%d", i))
	}

	var resolveWg sync.WaitGroup
	resolveWg.Add(N)
	resolveErrs := make([]error, N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer resolveWg.Done()
			resolveErrs[i] = inApp.Resolve(fmt.Sprintf("fb-%d", i), fmt.Sprintf("response-%d", i))
		}()
	}
	resolveWg.Wait()

	for i, err := range resolveErrs {
		if err != nil {
			t.Errorf("Resolve[%d]: unexpected error: %v", i, err)
		}
	}

	// Every waiter must have received its own response.
	for i := 0; i < N; i++ {
		select {
		case resp := <-chans[i]:
			if want := fmt.Sprintf("response-%d", i); resp.text != want {
				t.Errorf("waiter[%d] received %q, want %q", i, resp.text, want)
			}
		case <-time.After(time.Second):
			t.Errorf("waiter[%d] received no response within 1s", i)
		}
	}

	// The waiter map must be empty after delivery.
	inApp.mu.Lock()
	remaining := len(inApp.waiters)
	inApp.mu.Unlock()
	if remaining != 0 {
		t.Errorf("inApp.waiters has %d entries, want 0", remaining)
	}
}
