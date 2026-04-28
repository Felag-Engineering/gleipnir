package agent

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// runsActive tracks in-flight runs by state. Inc/Dec happen exclusively inside
// RunStateMachine.Transition — see state.go for the three-branch gauge logic.
var runsActive = promauto.With(metrics.Registry()).NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gleipnir_runs_active",
		Help: "Runs currently in-flight, by state.",
	},
	[]string{metrics.LabelState},
)

// runsTotal counts completed runs by trigger type and terminal status.
var runsTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_runs_total",
		Help: "Cumulative runs by trigger type and terminal status.",
	},
	[]string{metrics.LabelTriggerType, metrics.LabelStatus},
)

// runDurationSeconds measures end-to-end run duration by trigger type and terminal status.
var runDurationSeconds = promauto.With(metrics.Registry()).NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gleipnir_run_duration_seconds",
		Help:    "End-to-end run duration by trigger type and terminal status.",
		Buckets: metrics.BucketsSlow,
	},
	[]string{metrics.LabelTriggerType, metrics.LabelStatus},
)

// runStepsTotal counts steps written to the audit trail by step type.
var runStepsTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_run_steps_total",
		Help: "Run steps written to the audit trail, by step type.",
	},
	[]string{metrics.LabelStepType},
)

// auditQueueDepth tracks the number of steps currently queued in the AuditWriter
// but not yet durably written. Inc on enqueue, Dec on dequeue (see audit.go).
var auditQueueDepth = promauto.With(metrics.Registry()).NewGauge(
	prometheus.GaugeOpts{
		Name: "gleipnir_audit_queue_depth",
		Help: "Depth of the AuditWriter enqueue queue (SQLite write backpressure).",
	},
)
