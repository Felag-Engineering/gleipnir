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

var sseEventsDroppedTotal = promauto.With(metrics.Registry()).NewCounter(
	prometheus.CounterOpts{
		Name: "gleipnir_sse_events_dropped_total",
		Help: "SSE events dropped because a subscriber's channel buffer was full. " +
			"Clients recover via Last-Event-ID replay on reconnect; a sustained " +
			"non-zero rate indicates buffers are undersized for event throughput.",
	},
)
