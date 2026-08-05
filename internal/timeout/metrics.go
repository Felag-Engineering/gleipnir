package timeout

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
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

// feedbackTimeoutsTotal counts feedback requests that expired before an operator
// responded. Incremented right after the conditional claim succeeds, even if the
// run was already interrupted — the feedback request itself timed out regardless
// of subsequent run state.
var feedbackTimeoutsTotal = promauto.With(metrics.Registry()).NewCounter(
	prometheus.CounterOpts{
		Name: "gleipnir_feedback_timeouts_total",
		Help: "Count of feedback requests that expired before a response was received.",
	},
)

// toolInputTimeoutsTotal counts tool-initiated input requests resolved as
// timed-out by the scanner (ADR-055, spec §6.3). Separate from the feedback
// counter because the two measure different things: an agent asking a human,
// versus a tool asking one mid-call.
var toolInputTimeoutsTotal = promauto.With(metrics.Registry()).NewCounter(
	prometheus.CounterOpts{
		Name: "gleipnir_tool_input_timeouts_total",
		Help: "Tool-initiated input requests resolved as timed out.",
	},
)
