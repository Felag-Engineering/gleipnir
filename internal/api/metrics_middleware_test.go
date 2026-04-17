package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	dto "github.com/prometheus/client_model/go"
	"github.com/rapp992/gleipnir/internal/metrics"
)

// findMetricFamily searches the gathered metric families for one matching name.
func findMetricFamily(gathered []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, mf := range gathered {
		if mf.GetName() == name {
			return mf
		}
	}
	return nil
}

// findSample looks through a MetricFamily for the sample matching the given
// (method, route, code) label triplet. It returns nil if no match is found.
func findSample(mf *dto.MetricFamily, method, route, code string) *dto.Metric {
	for _, m := range mf.GetMetric() {
		labels := make(map[string]string, len(m.GetLabel()))
		for _, lp := range m.GetLabel() {
			labels[lp.GetName()] = lp.GetValue()
		}
		if labels[metrics.LabelMethod] == method &&
			labels[metrics.LabelRoute] == route &&
			labels[metrics.LabelCode] == code {
			return m
		}
	}
	return nil
}

// resetMetrics clears both package-scoped metric vecs so that each test starts
// from zero. Call this at the top of every test AND register it in t.Cleanup
// so parallel or sequentially-later tests are not polluted.
func resetMetrics() {
	httpRequestDuration.Reset()
	httpRequestsTotal.Reset()
}

// TestHttpMetrics_ObservesDurationAndCount verifies that a single request
// results in one counter increment and one histogram observation.
func TestHttpMetrics_ObservesDurationAndCount(t *testing.T) {
	resetMetrics()
	t.Cleanup(resetMetrics)

	r := chi.NewRouter()
	r.Use(httpMetrics)
	r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	gathered, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	// Verify counter.
	totalMF := findMetricFamily(gathered, "gleipnir_http_requests_total")
	if totalMF == nil {
		t.Fatal("gleipnir_http_requests_total not found in registry")
	}
	sample := findSample(totalMF, http.MethodGet, "/ping", "200")
	if sample == nil {
		t.Fatal("no counter series for {method=GET, route=/ping, code=200}")
	}
	if got := sample.GetCounter().GetValue(); got != 1 {
		t.Errorf("counter value = %v, want 1", got)
	}

	// Verify histogram.
	durMF := findMetricFamily(gathered, "gleipnir_http_request_duration_seconds")
	if durMF == nil {
		t.Fatal("gleipnir_http_request_duration_seconds not found in registry")
	}
	histSample := findSample(durMF, http.MethodGet, "/ping", "200")
	if histSample == nil {
		t.Fatal("no histogram series for {method=GET, route=/ping, code=200}")
	}
	if histSample.GetHistogram().GetSampleCount() != 1 {
		t.Errorf("histogram sample count = %d, want 1", histSample.GetHistogram().GetSampleCount())
	}
	if histSample.GetHistogram().GetSampleSum() <= 0 {
		t.Errorf("histogram sample sum = %v, want > 0", histSample.GetHistogram().GetSampleSum())
	}
}

// TestHttpMetrics_UsesRoutePatternForLabel verifies that high-cardinality path
// parameters do not leak into the route label — three distinct paths for the
// same route pattern produce a single series with count 3.
func TestHttpMetrics_UsesRoutePatternForLabel(t *testing.T) {
	resetMetrics()
	t.Cleanup(resetMetrics)

	r := chi.NewRouter()
	r.Use(httpMetrics)
	r.Get("/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for _, path := range []string{"/items/a", "/items/b", "/items/c"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
	}

	gathered, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	totalMF := findMetricFamily(gathered, "gleipnir_http_requests_total")
	if totalMF == nil {
		t.Fatal("gleipnir_http_requests_total not found")
	}

	// Expect exactly one series with the pattern label, not three separate series.
	sample := findSample(totalMF, http.MethodGet, "/items/{id}", "200")
	if sample == nil {
		t.Fatal("no counter series for {method=GET, route=/items/{id}, code=200}; raw paths may have leaked into labels")
	}
	if got := sample.GetCounter().GetValue(); got != 3 {
		t.Errorf("counter value = %v, want 3", got)
	}
}

// TestHttpMetrics_StatusCodesAreCaptured verifies that each status code a
// handler writes ends up as a distinct labelled series in the counter.
func TestHttpMetrics_StatusCodesAreCaptured(t *testing.T) {
	cases := []struct {
		name string
		code int
		want string
	}{
		{"200", http.StatusOK, "200"},
		{"404", http.StatusNotFound, "404"},
		{"500", http.StatusInternalServerError, "500"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMetrics()
			t.Cleanup(resetMetrics)

			code := tc.code
			r := chi.NewRouter()
			r.Use(httpMetrics)
			r.Get("/check", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			})

			req := httptest.NewRequest(http.MethodGet, "/check", nil)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)

			gathered, err := metrics.Registry().Gather()
			if err != nil {
				t.Fatalf("gather: %v", err)
			}

			totalMF := findMetricFamily(gathered, "gleipnir_http_requests_total")
			if totalMF == nil {
				t.Fatal("gleipnir_http_requests_total not found")
			}
			sample := findSample(totalMF, http.MethodGet, "/check", tc.want)
			if sample == nil {
				t.Fatalf("no counter series for {method=GET, route=/check, code=%s}", tc.want)
			}
		})
	}
}

// TestHttpMetrics_UnmatchedRouteFallback verifies that requests for paths with
// no matching chi route are labelled route="unmatched" rather than the raw
// path, keeping metric cardinality bounded.
func TestHttpMetrics_UnmatchedRouteFallback(t *testing.T) {
	resetMetrics()
	t.Cleanup(resetMetrics)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(httpMetrics)
	r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	gathered, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	totalMF := findMetricFamily(gathered, "gleipnir_http_requests_total")
	if totalMF == nil {
		t.Fatal("gleipnir_http_requests_total not found")
	}
	sample := findSample(totalMF, http.MethodGet, "unmatched", "404")
	if sample == nil {
		t.Fatal("no counter series for {method=GET, route=unmatched, code=404}; unmatched requests may have leaked raw paths into labels")
	}
}
