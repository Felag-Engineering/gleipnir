package trigger

import (
	"testing"
	"time"
)

func TestRunManager(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, m *RunManager)
	}{
		{
			name: "register then cancel returns true and calls cancel func",
			run: func(t *testing.T, m *RunManager) {
				cancelled := false
				m.Register("run-1", func() { cancelled = true })
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
				m.Register("run-2", func() { cancelled = true })
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
					m.Register(id, func() { called[i] = true })
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
