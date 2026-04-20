package sse

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rapp992/gleipnir/internal/metrics"
)

var sseConnectionsActive = promauto.With(metrics.Registry()).NewGauge(
	prometheus.GaugeOpts{
		Name: "gleipnir_sse_connections_active",
		Help: "Currently-connected SSE subscribers.",
	},
)
