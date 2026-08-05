package logcapture

import (
	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// Capture counters. The drop counter is the one that matters: a truncated log
// that says nothing about being truncated is worse than no log, because it
// reads as the container having gone quiet.
var (
	capturedLines = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gleipnir_plugin_container_log_lines_total",
		Help: "Plugin container output lines captured by the host, by stream.",
	}, []string{"stream"})

	droppedLines = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gleipnir_plugin_container_log_dropped_total",
		Help: "Plugin container output lines dropped by the host's capture rate limit.",
	})
)

func init() {
	metrics.Registry().MustRegister(capturedLines, droppedLines)
}
