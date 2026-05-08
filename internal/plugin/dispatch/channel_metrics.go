package dispatch

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// pluginRPCDurationSeconds measures the wall-clock time for each outbound
// ChannelService RPC, labelled by RPC name, plugin ID, and instance ID.
var pluginRPCDurationSeconds = promauto.With(metrics.Registry()).NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gleipnir_plugin_rpc_duration_seconds",
		Help:    "Latency for outbound ChannelService RPCs by rpc, plugin, and instance.",
		Buckets: metrics.BucketsFast,
	},
	[]string{"rpc", "plugin", "instance"},
)

// pluginRPCErrorsTotal counts ChannelService RPC failures by RPC name, plugin
// ID, and instance ID.
var pluginRPCErrorsTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_plugin_rpc_errors_total",
		Help: "ChannelService RPC failures by rpc, plugin, and instance.",
	},
	[]string{"rpc", "plugin", "instance"},
)

// observeRPC records the duration of a completed RPC call.
func observeRPC(rpc, pluginID, instanceID string, start time.Time) {
	pluginRPCDurationSeconds.WithLabelValues(rpc, pluginID, instanceID).Observe(time.Since(start).Seconds())
}

// incRPCError increments the error counter for a failed RPC call.
func incRPCError(rpc, pluginID, instanceID string) {
	pluginRPCErrorsTotal.WithLabelValues(rpc, pluginID, instanceID).Inc()
}
