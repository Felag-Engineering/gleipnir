package trigger

import (
	"testing"
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
			name: "register then deregister then cancel returns false",
			run: func(t *testing.T, m *RunManager) {
				cancelled := false
				m.Register("run-2", func() { cancelled = true })
				m.Deregister("run-2")
				got := m.Cancel("run-2")
				if got {
					t.Error("Cancel returned true after Deregister, want false")
				}
				if cancelled {
					t.Error("cancel func was called after Deregister")
				}
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
