package run

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// noopApprovalCh returns a new cap-1 buffered channel suitable for passing to
// Register when tests don't exercise the approval path.
func noopApprovalCh() chan bool { return make(chan bool, 1) }

func TestRunManager(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, m *RunManager)
	}{
		{
			name: "register then cancel returns nil and calls cancel func",
			run: func(t *testing.T, m *RunManager) {
				cancelled := false
				m.Register("run-1", func() { cancelled = true }, noopApprovalCh())
				got := m.Cancel("run-1")
				if got != nil {
					t.Errorf("Cancel returned %v, want nil", got)
				}
				if !cancelled {
					t.Error("cancel func was not called")
				}
				// Goroutine's Deregister must still be able to exit cleanly.
				m.Deregister("run-1")
				waitWithTimeout(t, m, "after cancel+deregister")
			},
		},
		{
			name: "cancel unknown run returns ErrRunNotFound",
			run: func(t *testing.T, m *RunManager) {
				got := m.Cancel("unknown-run")
				if !errors.Is(got, ErrRunNotFound) {
					t.Errorf("Cancel returned %v, want ErrRunNotFound", got)
				}
			},
		},
		{
			name: "deregister calls cancel func then cancel returns ErrRunNotFound",
			run: func(t *testing.T, m *RunManager) {
				cancelled := false
				m.Register("run-2", func() { cancelled = true }, noopApprovalCh())
				m.Deregister("run-2")
				// Deregister calls the cancel func to clean up the context on
				// normal goroutine exit (before the goroutine's own defer cancel).
				if !cancelled {
					t.Error("cancel func was not called by Deregister")
				}
				// The entry has been removed, so Cancel must return ErrRunNotFound.
				got := m.Cancel("run-2")
				if !errors.Is(got, ErrRunNotFound) {
					t.Errorf("Cancel returned %v after Deregister, want ErrRunNotFound", got)
				}
				waitWithTimeout(t, m, "after deregister")
			},
		},
		{
			name: "CancelAll cancels all registered runs",
			run: func(t *testing.T, m *RunManager) {
				called := make([]bool, 3)
				ids := []string{"run-a", "run-b", "run-c"}
				for i, id := range ids {
					m.Register(id, func() { called[i] = true }, noopApprovalCh())
				}

				m.CancelAll()

				for i, id := range ids {
					if !called[i] {
						t.Errorf("cancel func for %s was not called", id)
					}
					// Cancel should return ErrRunNotFound — CancelAll already removed the entries.
					if m.Cancel(id) == nil {
						t.Errorf("Cancel(%s) returned nil after CancelAll, want ErrRunNotFound", id)
					}
				}

				// WaitGroup must drain only after the goroutines deregister.
				for _, id := range ids {
					m.Deregister(id)
				}
				waitWithTimeout(t, m, "after CancelAll+deregister")
			},
		},
		{
			name: "CancelAll on empty manager is a no-op",
			run: func(t *testing.T, m *RunManager) {
				// Must not panic.
				m.CancelAll()
				waitWithTimeout(t, m, "empty manager")
			},
		},
		{
			name: "Wait returns immediately when no runs registered",
			run: func(t *testing.T, m *RunManager) {
				waitWithTimeout(t, m, "no runs registered")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewRunManager()
			tc.run(t, m)
		})
	}
}

func TestSendApproval(t *testing.T) {
	t.Run("run not registered returns ErrRunNotFound", func(t *testing.T) {
		m := NewRunManager()
		got := m.SendApproval("unknown-run", true)
		if !errors.Is(got, ErrRunNotFound) {
			t.Errorf("SendApproval returned %v for unregistered run, want ErrRunNotFound", got)
		}
	})

	t.Run("registered with full buffer returns ErrNoReceiver", func(t *testing.T) {
		m := NewRunManager()
		// Cap-1 channel: the first send fills the buffer (returns nil).
		// The second send with a full buffer and no reader must return ErrNoReceiver.
		approvalCh := make(chan bool, 1)
		m.Register("run-blocked", func() {}, approvalCh)
		first := m.SendApproval("run-blocked", true)
		if first != nil {
			t.Errorf("first SendApproval returned %v, want nil (buffer was empty)", first)
		}
		got := m.SendApproval("run-blocked", true)
		if !errors.Is(got, ErrNoReceiver) {
			t.Errorf("second SendApproval returned %v with full buffer, want ErrNoReceiver", got)
		}
		m.Deregister("run-blocked")
		waitWithTimeout(t, m, "blocked run")
	})

	t.Run("approved delivered to waiting goroutine returns true", func(t *testing.T) {
		m := NewRunManager()
		ch := make(chan bool, 1) // cap 1: matches production channel from launcher.go
		m.Register("run-approve", func() {}, ch)

		received := make(chan bool, 1)
		ready := make(chan struct{})
		go func() {
			close(ready) // signal that this goroutine is about to block on ch
			received <- <-ch
		}()
		<-ready
		// Yield to give the goroutine a chance to actually block on ch.
		time.Sleep(time.Millisecond)

		got := m.SendApproval("run-approve", true)
		if got != nil {
			t.Errorf("SendApproval returned %v, want nil", got)
		}
		select {
		case val := <-received:
			if !val {
				t.Error("received false from channel, want true")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("goroutine did not receive approval within deadline")
		}
		m.Deregister("run-approve")
		waitWithTimeout(t, m, "approve run")
	})

	t.Run("denied delivered to waiting goroutine returns true", func(t *testing.T) {
		m := NewRunManager()
		ch := make(chan bool, 1) // cap 1: matches production channel from launcher.go
		m.Register("run-deny", func() {}, ch)

		received := make(chan bool, 1)
		ready := make(chan struct{})
		go func() {
			close(ready) // signal that this goroutine is about to block on ch
			received <- <-ch
		}()
		<-ready
		// Yield to give the goroutine a chance to actually block on ch.
		time.Sleep(time.Millisecond)

		got := m.SendApproval("run-deny", false)
		if got != nil {
			t.Errorf("SendApproval returned %v, want nil", got)
		}
		select {
		case val := <-received:
			if val {
				t.Error("received true from channel, want false")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("goroutine did not receive denial within deadline")
		}
		m.Deregister("run-deny")
		waitWithTimeout(t, m, "deny run")
	})

	t.Run("deregistered run returns ErrRunNotFound", func(t *testing.T) {
		m := NewRunManager()
		m.Register("run-dereg", func() {}, noopApprovalCh())
		m.Deregister("run-dereg")
		waitWithTimeout(t, m, "deregistered run")
		got := m.SendApproval("run-dereg", true)
		if !errors.Is(got, ErrRunNotFound) {
			t.Errorf("SendApproval returned %v after Deregister, want ErrRunNotFound", got)
		}
	})
}

func TestWaitForDeregistration(t *testing.T) {
	t.Run("returns true immediately when run is not active", func(t *testing.T) {
		m := NewRunManager()
		m.Register("run-gone", func() {}, noopApprovalCh())
		m.Deregister("run-gone")
		waitWithTimeout(t, m, "before WaitForDeregistration")

		got := m.WaitForDeregistration("run-gone", 2*time.Second)
		if !got {
			t.Error("WaitForDeregistration returned false for already-deregistered run, want true")
		}
	})

	t.Run("returns true when run deregisters within timeout", func(t *testing.T) {
		m := NewRunManager()
		m.Register("run-delayed", func() {}, noopApprovalCh())

		go func() {
			time.Sleep(100 * time.Millisecond)
			m.Deregister("run-delayed")
		}()

		got := m.WaitForDeregistration("run-delayed", 2*time.Second)
		if !got {
			t.Error("WaitForDeregistration returned false before timeout, want true")
		}
		waitWithTimeout(t, m, "after delayed deregister")
	})

	t.Run("returns false when timeout expires before deregister", func(t *testing.T) {
		m := NewRunManager()
		m.Register("run-stuck", func() {}, noopApprovalCh())

		got := m.WaitForDeregistration("run-stuck", 50*time.Millisecond)
		if got {
			t.Error("WaitForDeregistration returned true on timeout, want false")
		}
		// Clean up so the WaitGroup drains.
		m.Deregister("run-stuck")
		waitWithTimeout(t, m, "after stuck-run cleanup")
	})

	t.Run("multiple waiters all notified on deregister", func(t *testing.T) {
		m := NewRunManager()
		m.Register("run-multi", func() {}, noopApprovalCh())

		results := make([]bool, 3)
		var wg sync.WaitGroup
		for i := range results {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = m.WaitForDeregistration("run-multi", 2*time.Second)
			}()
		}

		// Give the goroutines time to register their waiter channels.
		time.Sleep(10 * time.Millisecond)
		m.Deregister("run-multi")

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("not all waiters were notified within deadline")
		}

		for i, got := range results {
			if !got {
				t.Errorf("waiter %d returned false, want true", i)
			}
		}
		waitWithTimeout(t, m, "after multi-waiter deregister")
	})
}

// TestSendApproval_BufferedDeliversAcrossGoroutineScheduling proves that the
// cap-1 buffer allows SendApproval to deliver a decision before the agent
// goroutine blocks on the channel. Without the buffer, the non-blocking select
// in SendApproval would drop the decision in this window.
func TestSendApproval_BufferedDeliversAcrossGoroutineScheduling(t *testing.T) {
	m := NewRunManager()
	ch := make(chan bool, 1) // cap 1: same as production
	m.Register("run-buf-approve", func() {}, ch)

	// Deliver the decision before any goroutine is reading the channel.
	// With an unbuffered channel this would return ErrNoReceiver; cap 1 must return nil.
	got := m.SendApproval("run-buf-approve", true)
	if got != nil {
		t.Fatalf("SendApproval returned %v before reader started, want nil (buffer should hold the value)", got)
	}

	// The value must be present in the buffer for the agent to receive later.
	select {
	case val := <-ch:
		if !val {
			t.Errorf("received %v from channel, want true", val)
		}
	default:
		t.Fatal("channel was empty after SendApproval returned true — buffer did not hold the value")
	}

	m.Deregister("run-buf-approve")
	waitWithTimeout(t, m, "buffered approval")
}

// waitWithTimeout calls m.Wait() in a goroutine and fails the test if it does
// not return within a short deadline. This prevents a buggy WaitGroup from
// hanging the entire test suite.
func waitWithTimeout(t *testing.T, m *RunManager, label string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		m.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Wait did not return within deadline (%s)", label)
	}
}

// fakeResolver is a test double for FeedbackResolver. It records the last call
// and returns the configured error.
type fakeResolver struct {
	err         error
	lastID      string
	lastBody    string
	calledCount int
}

func (f *fakeResolver) Resolve(requestID, body string) error {
	f.calledCount++
	f.lastID = requestID
	f.lastBody = body
	return f.err
}

// TestRegisterWithFeedbackResolver_AtomicRegistration verifies that immediately
// after RegisterWithFeedbackResolver returns, the resolver is observable via
// ResolveFeedback with no intermediate window where it is nil.
func TestRegisterWithFeedbackResolver_AtomicRegistration(t *testing.T) {
	m := NewRunManager()
	r := &fakeResolver{}

	m.RegisterWithFeedbackResolver("run-atomic", func() {}, noopApprovalCh(), r)

	err := m.ResolveFeedback("run-atomic", "req-1", "hello")
	if err != nil {
		t.Errorf("ResolveFeedback returned %v, want nil", err)
	}
	if r.calledCount != 1 {
		t.Errorf("resolver called %d times, want 1", r.calledCount)
	}
	if r.lastID != "req-1" {
		t.Errorf("lastID = %q, want %q", r.lastID, "req-1")
	}
	m.Deregister("run-atomic")
	waitWithTimeout(t, m, "atomic registration")
}

// TestRegisterFeedbackResolver_Unregistered_NoOp verifies that calling
// RegisterFeedbackResolver for a run that is not registered does nothing.
func TestRegisterFeedbackResolver_Unregistered_NoOp(t *testing.T) {
	m := NewRunManager()
	r := &fakeResolver{}

	// Should not panic or error.
	m.RegisterFeedbackResolver("no-such-run", r)

	err := m.ResolveFeedback("no-such-run", "req-1", "hello")
	if !errors.Is(err, ErrRunNotFound) {
		t.Errorf("ResolveFeedback returned %v, want ErrRunNotFound", err)
	}
	if r.calledCount != 0 {
		t.Errorf("resolver called %d times, want 0", r.calledCount)
	}
}

// TestResolveFeedback_RunNotFound verifies that ResolveFeedback returns
// ErrRunNotFound for a run that was never registered.
func TestResolveFeedback_RunNotFound(t *testing.T) {
	m := NewRunManager()
	err := m.ResolveFeedback("ghost-run", "req-1", "body")
	if !errors.Is(err, ErrRunNotFound) {
		t.Errorf("ResolveFeedback returned %v, want ErrRunNotFound", err)
	}
}

// TestResolveFeedback_DelegatesToResolver verifies that ResolveFeedback calls
// the registered resolver and propagates its return value.
func TestResolveFeedback_DelegatesToResolver(t *testing.T) {
	m := NewRunManager()
	sentinelErr := errors.New("resolver error")
	r := &fakeResolver{err: sentinelErr}

	m.Register("run-delegate", func() {}, noopApprovalCh())
	m.RegisterFeedbackResolver("run-delegate", r)

	err := m.ResolveFeedback("run-delegate", "req-42", "payload")
	if !errors.Is(err, sentinelErr) {
		t.Errorf("ResolveFeedback returned %v, want %v", err, sentinelErr)
	}
	if r.calledCount != 1 {
		t.Errorf("resolver called %d times, want 1", r.calledCount)
	}
	if r.lastID != "req-42" {
		t.Errorf("lastID = %q, want %q", r.lastID, "req-42")
	}
	if r.lastBody != "payload" {
		t.Errorf("lastBody = %q, want %q", r.lastBody, "payload")
	}
	m.Deregister("run-delegate")
	waitWithTimeout(t, m, "delegate run")
}

// TestResolveFeedback_AfterCancelAll_ReturnsErrRunNotFound verifies that
// CancelAll nils the feedbackResolver so that a subsequent ResolveFeedback
// returns ErrRunNotFound rather than racing with CancelAll.
func TestResolveFeedback_AfterCancelAll_ReturnsErrRunNotFound(t *testing.T) {
	m := NewRunManager()
	r := &fakeResolver{}

	m.Register("run-cancelall", func() {}, noopApprovalCh())
	m.RegisterFeedbackResolver("run-cancelall", r)

	m.CancelAll()

	err := m.ResolveFeedback("run-cancelall", "req-1", "body")
	if !errors.Is(err, ErrRunNotFound) {
		t.Errorf("ResolveFeedback after CancelAll returned %v, want ErrRunNotFound", err)
	}
	if r.calledCount != 0 {
		t.Errorf("resolver called %d times after CancelAll, want 0", r.calledCount)
	}

	// Drain the WaitGroup.
	m.Deregister("run-cancelall")
	waitWithTimeout(t, m, "cancelall run")
}
