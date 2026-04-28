package runstate

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// runStateTransitionsTotal counts successful run state machine transitions.
// Two call sites: RunStateMachine.Transition (internal/agent/state.go) for the
// normal agent-driven path, and TransitionRunFailed (runstate.go) for the
// scanner/orphan-driven failure path. Both paths call RecordTransition only
// after the DB write commits.
var runStateTransitionsTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_run_state_transitions_total",
		Help: "Count of run state machine transitions.",
	},
	[]string{metrics.LabelFrom, metrics.LabelTo},
)

// RecordTransition increments the run state transition counter for from→to.
// Called only on the success path, after the DB write has committed, so the
// counter accurately reflects durable transitions.
func RecordTransition(from, to model.RunStatus) {
	runStateTransitionsTotal.WithLabelValues(string(from), string(to)).Inc()
}
