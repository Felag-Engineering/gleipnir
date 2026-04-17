package agent_test

import (
	"strings"
	"testing"

	// Import packages that register their collectors at init time so the
	// smoke test covers all seven metric families in a single Describe pass.
	_ "github.com/rapp992/gleipnir/internal/agent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rapp992/gleipnir/internal/metrics"
	_ "github.com/rapp992/gleipnir/internal/runstate"
	_ "github.com/rapp992/gleipnir/internal/timeout"
)

// TestMetricFamiliesRegistered verifies that all seven Priority-1 metric
// families are present in the custom registry. Uses Describe() rather than
// Gather() because Vec metrics (GaugeVec, CounterVec, HistogramVec) produce
// no samples until a label combination is first observed — Gather() would miss
// them. Describe() enumerates every registered collector's descriptors,
// including those with no observations, giving a complete view of the registry.
func TestMetricFamiliesRegistered(t *testing.T) {
	want := map[string]bool{
		"gleipnir_runs_active":                 false,
		"gleipnir_runs_total":                  false,
		"gleipnir_run_duration_seconds":        false,
		"gleipnir_run_steps_total":             false,
		"gleipnir_audit_queue_depth":           false,
		"gleipnir_run_state_transitions_total": false,
		"gleipnir_approval_timeouts_total":     false,
	}

	ch := make(chan *prometheus.Desc, 1024)
	metrics.Registry().Describe(ch)
	close(ch)

	for d := range ch {
		desc := d.String()
		for name := range want {
			// prometheus.Desc.String() format: Desc{fqName: "name", help: "...", ...}
			if strings.Contains(desc, `"`+name+`"`) {
				want[name] = true
			}
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("metric family %q not registered", name)
		}
	}
}
