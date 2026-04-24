package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rapp992/gleipnir/internal/infra/metrics"
)

// httpRequestDuration records HTTP request latency as a histogram, bucketed by
// method, route pattern, and status code. Registered on the custom Gleipnir
// registry at package init so it lands on /metrics — not the global default.
var httpRequestDuration = promauto.With(metrics.Registry()).NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gleipnir_http_request_duration_seconds",
		Help:    "HTTP request latency by method, route, and status code.",
		Buckets: metrics.BucketsFast,
	},
	[]string{metrics.LabelMethod, metrics.LabelRoute, metrics.LabelCode},
)

// httpRequestsTotal counts HTTP requests by method, route pattern, and status code.
var httpRequestsTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_http_requests_total",
		Help: "HTTP request count by method, route, and status code.",
	},
	[]string{metrics.LabelMethod, metrics.LabelRoute, metrics.LabelCode},
)

// httpMetrics is middleware that records Prometheus metrics for every HTTP
// request: a duration histogram and a request counter, both labelled by HTTP
// method, chi route pattern, and status code.
//
// The route pattern (e.g. "/items/{id}") is read AFTER next returns because
// chi's router fills RouteContext during request dispatch — reading it before
// next runs yields an empty string. Using the pattern rather than the raw path
// bounds label cardinality even for routes with high-entropy path parameters.
func httpMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()

		next.ServeHTTP(ww, r)

		elapsed := time.Since(start)

		// chi fills RoutePattern during routing, which happens inside next.
		// If nothing matched (404, pre-routing middleware short-circuit, etc.),
		// fall back to "unmatched" to keep cardinality bounded.
		pattern := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			pattern = rc.RoutePattern()
		}

		status := ww.Status()
		// net/http implicitly returns 200 when WriteHeader is never called.
		if status == 0 {
			status = http.StatusOK
		}
		code := strconv.Itoa(status)

		httpRequestDuration.WithLabelValues(r.Method, pattern, code).Observe(elapsed.Seconds())
		httpRequestsTotal.WithLabelValues(r.Method, pattern, code).Inc()
	})
}
