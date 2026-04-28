package db

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

var dbQueryDurationSeconds = promauto.With(metrics.Registry()).NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gleipnir_db_query_duration_seconds",
		Help:    "SQLite query duration for hot-path queries, by query name.",
		Buckets: metrics.BucketsFast,
	},
	[]string{metrics.LabelQuery},
)
