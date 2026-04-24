package timeout

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rapp992/gleipnir/internal/infra/metrics"
)

// approvalTimeoutsTotal counts approval requests that expired before an operator
// made a decision. Incremented right after the conditional claim succeeds, even
// if the run was already interrupted — the approval itself timed out regardless
// of subsequent run state.
var approvalTimeoutsTotal = promauto.With(metrics.Registry()).NewCounter(
	prometheus.CounterOpts{
		Name: "gleipnir_approval_timeouts_total",
		Help: "Count of approval requests that expired before a decision was made.",
	},
)
