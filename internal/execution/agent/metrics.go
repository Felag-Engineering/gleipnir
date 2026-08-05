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

// elicitationBudgetExhausted counts tool calls abandoned because the run's
// per-run elicitation budget was spent (ADR-055, spec §6.2 cap 1). This is the
// operational record of an exhaustion: the run trace shows the agent-facing
// tool_result, and this counter shows an operator how often the cap is biting.
var elicitationBudgetExhausted = promauto.With(metrics.Registry()).NewCounter(
	prometheus.CounterOpts{
		Name: "gleipnir_elicitation_budget_exhausted_total",
		Help: "Tool calls abandoned because the run's elicitation budget was exhausted.",
	},
)

// inputAnswerReplays counts operator answers replayed automatically because a
// server re-asked the identical question after discarding its MRTR state
// (ADR-055, spec §6.5). Each increment is one human prompt that did NOT
// happen, which is the point of the mechanism; a rising rate also points at a
// server whose requestState TTL is too short for its own audience.
var inputAnswerReplays = promauto.With(metrics.Registry()).NewCounter(
	prometheus.CounterOpts{
		Name: "gleipnir_tool_input_answer_replays_total",
		Help: "Operator answers replayed automatically after a server re-asked the identical question.",
	},
)

// inputAnswerReplayMismatches counts the other branch: the server re-asked
// after an answer, but asked something different, so the operator was prompted
// again with the previous question attached. High relative to replays means
// servers are churning their questions mid-call rather than losing state.
var inputAnswerReplayMismatches = promauto.With(metrics.Registry()).NewCounter(
	prometheus.CounterOpts{
		Name: "gleipnir_tool_input_answer_replay_mismatches_total",
		Help: "Re-prompts issued because a server re-asked a different question after an answer.",
	},
)
