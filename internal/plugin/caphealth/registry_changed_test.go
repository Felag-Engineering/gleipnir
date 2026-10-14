package caphealth

import (
	"testing"

	"github.com/felag-engineering/gleipnir/internal/model"
)

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestRegistry_Observed(t *testing.T) {
	r := NewRegistry()
	if r.Observed("i1") {
		t.Fatal("an instance nothing has probed reports observed")
	}
	r.SetLiveness("i1", Liveness{Detail: "down"})
	if !r.Observed("i1") {
		t.Error("an unhealthy verdict is still a verdict; Observed = false")
	}
	if r.Serves("i1", eventSource) {
		t.Error("an unreachable instance serves")
	}
	r.Forget("i1")
	if r.Observed("i1") {
		t.Error("Forget left the instance observed")
	}
}

func TestRegistry_Changed(t *testing.T) {
	healthyEntry := entry(eventSource, model.PluginHealthStateHealthy, "")
	faultEntry := entry(eventSource, model.PluginHealthStateUnhealthy, "drift")

	tests := []struct {
		name  string
		setup func(r *Registry)
		write func(r *Registry)
		fires bool
	}{
		{"first liveness", func(r *Registry) {}, func(r *Registry) { r.SetLiveness("i1", live()) }, true},
		{"repeat liveness", func(r *Registry) { r.SetLiveness("i1", live()) }, func(r *Registry) { r.SetLiveness("i1", live()) }, true},
		{"new capability entry", func(r *Registry) {}, func(r *Registry) { r.SetCapability("i1", healthyEntry) }, true},
		{"changed capability entry", func(r *Registry) { r.SetCapability("i1", healthyEntry) }, func(r *Registry) { r.SetCapability("i1", faultEntry) }, true},
		{"identical capability entry", func(r *Registry) { r.SetCapability("i1", healthyEntry) }, func(r *Registry) { r.SetCapability("i1", healthyEntry) }, false},
		{"applied self-report", func(r *Registry) {}, func(r *Registry) { r.SelfReportCapability("i1", faultEntry) }, true},
		{"refused self-report", func(r *Registry) { r.SelfReportCapability("i1", faultEntry) }, func(r *Registry) { r.SelfReportCapability("i1", healthyEntry) }, false},
		{"clear existing capability", func(r *Registry) { r.SetCapability("i1", faultEntry) }, func(r *Registry) { r.ClearCapability("i1", eventSource) }, true},
		{"clear missing capability", func(r *Registry) {}, func(r *Registry) { r.ClearCapability("i1", eventSource) }, false},
		{"clear self-reported", func(r *Registry) { r.SelfReportCapability("i1", faultEntry) }, func(r *Registry) { r.ClearSelfReported("i1") }, true},
		{"clear self-reported with none", func(r *Registry) { r.SetCapability("i1", faultEntry) }, func(r *Registry) { r.ClearSelfReported("i1") }, false},
		{"forget", func(r *Registry) { r.SetLiveness("i1", live()) }, func(r *Registry) { r.Forget("i1") }, true},
		{"write to another instance", func(r *Registry) {}, func(r *Registry) { r.SetLiveness("i2", live()) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			tt.setup(r)
			ch := r.Changed("i1")
			if closed(ch) {
				t.Fatal("Changed returned an already-closed channel")
			}
			tt.write(r)
			if got := closed(ch); got != tt.fires {
				t.Errorf("channel closed = %v, want %v", got, tt.fires)
			}
		})
	}
}

// The channel is one-shot: after it fires, Changed hands out a fresh one.
func TestRegistry_Changed_IsOneShot(t *testing.T) {
	r := NewRegistry()
	first := r.Changed("i1")
	if r.Changed("i1") != first {
		t.Error("two calls before a write returned different channels")
	}
	r.SetLiveness("i1", live())
	second := r.Changed("i1")
	if second == first || closed(second) {
		t.Error("Changed after a write did not return a fresh open channel")
	}
}
