package state

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// pluginHealthTransitionsTotal counts successful plugin health state machine
// transitions. Called only on the success path, after the DB write commits, so
// the counter accurately reflects durable transitions.
var pluginHealthTransitionsTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_plugin_health_transitions_total",
		Help: "Count of plugin instance health state machine transitions.",
	},
	[]string{metrics.LabelFrom, metrics.LabelTo},
)

// RecordTransition increments the plugin health transition counter for from→to.
func RecordTransition(from, to model.PluginHealthState) {
	pluginHealthTransitionsTotal.WithLabelValues(string(from), string(to)).Inc()
}
