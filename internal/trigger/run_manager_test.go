package trigger

import (
	"testing"
	"time"
)

// noopApprovalCh returns a new unbuffered channel suitable for passing to
// Register when tests don't exercise the approval path.
func noopApprovalCh() chan bool { return make(chan bool) }

// noopFeedbackCh returns a new unbuffered channel suitable for passing to
// Register when tests don't exercise the feedback path.
func noopFeedbackCh() chan string { return make(chan string) }

func TestRunManager(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, m *RunManager)
	}{
		{
			name: "register then cancel returns true and calls cancel func",
			run: func(t *testing.T, m *RunManager) {
				cancelled := false
				m.Register("run-1", func() { cancelled = true }, noopApprovalCh(), noopFeedbackCh())
				got := m.Cancel("run-1")
				if !got {
					t.Error("Cancel returned false, want true")
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
			name: "cancel unknown run returns false",
			run: func(t *testing.T, m *RunManager) {
				got := m.Cancel("unknown-run")
				if got {
					t.Error("Cancel returned true for unknown run, want false")
				}
			},
		},
		{
			name: "deregister calls cancel func then cancel returns false",
			run: func(t *testing.T, m *RunManager) {
				cancelled := false
				m.Register("run-2", func() { cancelled = true }, noopApprovalCh(), noopFeedbackCh())
				m.Deregister("run-2")
				// Deregister calls the cancel func to clean up the context on
				// normal goroutine exit (before the goroutine's own defer cancel).
				if !cancelled {
					t.Error("cancel func was not called by Deregister")
				}
				// The entry has been removed, so Cancel should be a no-op.
				got := m.Cancel("run-2")
				if got {
					t.Error("Cancel returned true after Deregister, want false")
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
					m.Register(id, func() { called[i] = true }, noopApprovalCh(), noopFeedbackCh())
				}

				m.CancelAll()

				for i, id := range ids {
					if !called[i] {
						t.Errorf("cancel func for %s was not called", id)
					}
					// Cancel should return false — CancelAll already removed the entries.
					if m.Cancel(id) {
						t.Errorf("Cancel(%s) returned true after CancelAll, want false", id)
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
	t.Run("run not registered returns false", func(t *testing.T) {
		m := NewRunManager()
		got := m.SendApproval("unknown-run", true)
		if got {
			t.Error("SendApproval returned true for unregistered run, want false")
		}
	})

	t.Run("registered but nobody reading returns false", func(t *testing.T) {
		m := NewRunManager()
		m.Register("run-blocked", func() {}, noopApprovalCh(), noopFeedbackCh())
		// Channel is unbuffered and nobody is reading — non-blocking send must fail.
		got := m.SendApproval("run-blocked", true)
		if got {
			t.Error("SendApproval returned true with no reader, want false")
		}
		m.Deregister("run-blocked")
		waitWithTimeout(t, m, "blocked run")
	})

	t.Run("approved delivered to waiting goroutine returns true", func(t *testing.T) {
		m := NewRunManager()
		ch := make(chan bool)
		m.Register("run-approve", func() {}, ch, noopFeedbackCh())

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
		if !got {
			t.Error("SendApproval returned false, want true")
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
		ch := make(chan bool)
		m.Register("run-deny", func() {}, ch, noopFeedbackCh())

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
		if !got {
			t.Error("SendApproval returned false, want true")
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

	t.Run("deregistered run returns false", func(t *testing.T) {
		m := NewRunManager()
		m.Register("run-dereg", func() {}, noopApprovalCh(), noopFeedbackCh())
		m.Deregister("run-dereg")
		waitWithTimeout(t, m, "deregistered run")
		got := m.SendApproval("run-dereg", true)
		if got {
			t.Error("SendApproval returned true after Deregister, want false")
		}
	})
}

func TestSendFeedback(t *testing.T) {
	t.Run("run not registered returns false", func(t *testing.T) {
		m := NewRunManager()
		got := m.SendFeedback("unknown-run", "hello")
		if got {
			t.Error("SendFeedback returned true for unregistered run, want false")
		}
	})

	t.Run("registered but nobody reading returns false", func(t *testing.T) {
		m := NewRunManager()
		m.Register("run-blocked", func() {}, noopApprovalCh(), noopFeedbackCh())
		// Channel is unbuffered and nobody is reading — non-blocking send must fail.
		got := m.SendFeedback("run-blocked", "some response")
		if got {
			t.Error("SendFeedback returned true with no reader, want false")
		}
		m.Deregister("run-blocked")
		waitWithTimeout(t, m, "blocked run")
	})

	t.Run("response delivered to waiting goroutine returns true", func(t *testing.T) {
		m := NewRunManager()
		ch := make(chan string)
		m.Register("run-feedback", func() {}, noopApprovalCh(), ch)

		received := make(chan string, 1)
		ready := make(chan struct{})
		go func() {
			close(ready)
			received <- <-ch
		}()
		<-ready
		time.Sleep(time.Millisecond)

		got := m.SendFeedback("run-feedback", "yes, proceed")
		if !got {
			t.Error("SendFeedback returned false, want true")
		}
		select {
		case val := <-received:
			if val != "yes, proceed" {
				t.Errorf("received %q, want %q", val, "yes, proceed")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("goroutine did not receive feedback within deadline")
		}
		m.Deregister("run-feedback")
		waitWithTimeout(t, m, "feedback run")
	})

	t.Run("deregistered run returns false", func(t *testing.T) {
		m := NewRunManager()
		m.Register("run-dereg-fb", func() {}, noopApprovalCh(), noopFeedbackCh())
		m.Deregister("run-dereg-fb")
		waitWithTimeout(t, m, "deregistered run")
		got := m.SendFeedback("run-dereg-fb", "hello")
		if got {
			t.Error("SendFeedback returned true after Deregister, want false")
		}
	})
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
