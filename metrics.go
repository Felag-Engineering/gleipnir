package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// operatorAPIRefusedTotal counts connections the netguard.Listener wrapping
// the operator API listener refused, by reason. netguard is a strict leaf
// package with no internal imports (it must stay importable by any future
// v2 assembly without dragging the metrics registry in with it), so this
// metric is defined here and wired to the guard via netguard.WithOnRefuse
// rather than netguard registering it itself.
var operatorAPIRefusedTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_operator_api_refused_total",
		Help: "Connections refused by the operator API network guard, by reason.",
	},
	[]string{metrics.LabelReason},
)
