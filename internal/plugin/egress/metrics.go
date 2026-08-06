package egress

import (
	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// Egress counters. Denials carry the reason as a label and allows do not:
// "how often is this plugin reaching for something it was never granted" is the
// question an operator asks, and it is answerable only if the refusals are
// broken out. The reason vocabulary is closed (DenyReason), so the label
// cardinality is bounded by construction.
var (
	egressDenied = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gleipnir_plugin_egress_denied_total",
		Help: "Plugin egress connections refused by the host proxy, by reason.",
	}, []string{"reason"})

	egressAllowed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gleipnir_plugin_egress_allowed_total",
		Help: "Plugin egress connections permitted by the host proxy.",
	})
)

func init() {
	metrics.Registry().MustRegister(egressDenied, egressAllowed)
}
