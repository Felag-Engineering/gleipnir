// Package metrics owns the custom Prometheus registry and shared building
// blocks (bucket presets, label-key and enum constants) that all domain
// packages in Gleipnir use for instrumentation.
//
// This is a leaf package with no internal imports — it depends only on the
// standard library and github.com/prometheus/client_golang.
//
// All Gleipnir metrics use the "gleipnir_" naming prefix. Domain packages
// register their own collectors by calling promauto.With(metrics.Registry())
// so that every collector ends up on the same custom registry rather than
// the global default.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registry is the package-private custom Prometheus registry. It is
// intentionally isolated from prometheus.DefaultRegisterer so that test
// packages cannot accidentally inherit collectors registered elsewhere, and
// so the /metrics handler exposes exactly what Gleipnir registers.
var registry = prometheus.NewRegistry()

func init() {
	// Register the Go runtime collector (go_goroutines, go_gc_duration_seconds,
	// etc.) on startup. A panic here surfaces a collector conflict
	// deterministically at program start, which is preferable to a silent
	// registration failure discovered later.
	registry.MustRegister(collectors.NewGoCollector())
}

// Registry returns the custom Prometheus registry. Domain packages call
// promauto.With(metrics.Registry()) to register their own collectors.
//
// The concrete *prometheus.Registry type is returned (rather than the
// Registerer or Gatherer interface) so callers can use it as both — promauto
// accepts Registerer, and tests need Gatherer. Returning the concrete type
// avoids forcing two separate accessors.
func Registry() *prometheus.Registry {
	return registry
}

// Handler returns an http.Handler that serves the Prometheus text exposition
// format from the custom registry. Mount this on /metrics in the chi router.
//
// Passing Registry in HandlerOpts enables the handler to expose its own
// promhttp_metric_handler_errors_total series via the same registry, which
// is the standard client_golang idiom for self-describing handler metrics.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{Registry: registry})
}

// BucketsFast defines histogram bucket boundaries for low-latency operations
// such as MCP tool calls and database queries (sub-10s expected range).
var BucketsFast = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// BucketsSlow defines histogram bucket boundaries for high-latency operations
// such as LLM calls and end-to-end run duration (sub-600s expected range).
var BucketsSlow = []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}

// Label key constants are the canonical Prometheus label names used across all
// Gleipnir collectors. Defining them here keeps the naming scheme authoritative
// in one place and prevents typos at call sites.
const (
	LabelErrorType   = "error_type"
	LabelDirection   = "direction"
	LabelProvider    = "provider"
	LabelModel       = "model"
	LabelServer      = "server"
	LabelTool        = "tool"
	LabelQuery       = "query"
	LabelMethod      = "method"
	LabelRoute       = "route"
	LabelCode        = "code"
	LabelState       = "state"
	LabelStatus      = "status"
	LabelStepType    = "step_type"
	LabelTriggerType = "trigger_type"
	LabelFrom        = "from"
	LabelTo          = "to"
)

// error_type label values enumerate the categories of errors recorded in
// Gleipnir metrics. The set is fixed by the metrics spec to keep cardinality
// bounded — do not add values without updating the spec.
const (
	ErrorTypeTimeout     = "timeout"
	ErrorTypeConnection  = "connection"
	ErrorTypeRateLimit   = "rate_limit"
	ErrorTypeAuth        = "auth"
	ErrorTypeServerError = "server_error"
	ErrorTypeProtocol    = "protocol"
)

// direction label values distinguish inbound from outbound token flows in LLM
// usage metrics.
const (
	DirectionInput  = "input"
	DirectionOutput = "output"
)
