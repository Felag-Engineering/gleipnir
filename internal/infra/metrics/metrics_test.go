package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rapp992/gleipnir/internal/infra/metrics"
)

// TestRegistry_GoCollectorRegistered verifies that the Go runtime collector
// was registered during package init — confirming no panic occurred and that
// the registry is in a usable state.
func TestRegistry_GoCollectorRegistered(t *testing.T) {
	families, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("Registry().Gather() error: %v", err)
	}

	for _, mf := range families {
		if strings.HasPrefix(mf.GetName(), "go_") {
			return // found at least one go_* metric family — pass
		}
	}
	t.Error("expected at least one metric family with name starting with 'go_', found none")
}

// TestRegistry_CustomCollectorRegistration verifies that a domain collector
// can be registered on the custom registry and that a duplicate registration
// returns AlreadyRegisteredError rather than panicking.
func TestRegistry_CustomCollectorRegistration(t *testing.T) {
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gleipnir_test_total",
		Help: "Counter used only by TestRegistry_CustomCollectorRegistration.",
	})

	// t.Cleanup is used (not defer) so that the unregister runs even when an
	// assertion below calls t.Fatal — defer would not run in that case because
	// t.Fatal calls runtime.Goexit.
	t.Cleanup(func() { metrics.Registry().Unregister(c) })

	if err := metrics.Registry().Register(c); err != nil {
		t.Fatalf("first Register() call returned unexpected error: %v", err)
	}

	// A second registration of the same collector must return AlreadyRegisteredError.
	err := metrics.Registry().Register(c)
	if err == nil {
		t.Fatal("expected AlreadyRegisteredError on duplicate Register(), got nil")
	}
	if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
		t.Fatalf("expected prometheus.AlreadyRegisteredError, got %T: %v", err, err)
	}
}

// TestHandler_ExpositionFormat verifies that Handler() returns a valid
// Prometheus text exposition response.
func TestHandler_ExpositionFormat(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	// No Accept header is set, so the handler negotiates the Prometheus text
	// exposition format (version=0.0.4) rather than OpenMetrics. This is
	// intentional — text exposition is the broadly-compatible default, and
	// asserting on its Content-Type gives a stable contract for tests.
	rec := httptest.NewRecorder()

	metrics.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/plain; version=0.0.4") {
		t.Errorf("expected Content-Type starting with 'text/plain; version=0.0.4', got %q", contentType)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "go_goroutines") {
		t.Errorf("expected body to contain 'go_goroutines' (runtime collector output), but it did not\nbody excerpt: %.500s", body)
	}
}

// TestBucketPresets_Monotonic verifies that both bucket preset slices are
// strictly increasing. Prometheus requires sorted, unique bucket boundaries —
// this test catches accidental edits to the preset values.
func TestBucketPresets_Monotonic(t *testing.T) {
	cases := []struct {
		name    string
		buckets []float64
	}{
		{"BucketsFast", metrics.BucketsFast},
		{"BucketsSlow", metrics.BucketsSlow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.buckets) == 0 {
				t.Fatal("bucket slice must not be empty")
			}
			for i := 1; i < len(tc.buckets); i++ {
				if tc.buckets[i] <= tc.buckets[i-1] {
					t.Errorf("bucket[%d]=%v is not greater than bucket[%d]=%v (slice must be strictly increasing)",
						i, tc.buckets[i], i-1, tc.buckets[i-1])
				}
			}
		})
	}
}

// TestBucketPresets_Values guards against drift by asserting the exact slice
// contents match the spec values.
func TestBucketPresets_Values(t *testing.T) {
	wantFast := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	wantSlow := []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}

	assertSliceEqual(t, "BucketsFast", metrics.BucketsFast, wantFast)
	assertSliceEqual(t, "BucketsSlow", metrics.BucketsSlow, wantSlow)
}

func assertSliceEqual(t *testing.T, name string, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: len=%d, want %d", name, len(got), len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %v, want %v", name, i, got[i], want[i])
		}
	}
}
