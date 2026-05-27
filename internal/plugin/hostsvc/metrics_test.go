package hostsvc_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// newMetricsServer is a helper that builds a hostsvc.Server wired for
// EmitMetric tests. Each test must use a distinct pluginID and instanceID to
// avoid Prometheus name collisions across tests that run in the same process.
func newMetricsServer(t *testing.T, pluginID, instanceID string) *hostsvc.Server {
	t.Helper()
	q := &fakeQuerier{
		instance: db.PluginInstance{ID: instanceID, PluginID: pluginID},
	}
	binder := &fakeInstanceBinder{id: instanceID, ok: true}
	return hostsvc.NewServer(q, testEncryptionKey, &fakeResolver{}, binder, &fakePublisher{}, nil)
}

// TestEmitMetric_MetricNameCap verifies that a plugin instance may not register
// more than MetricNameCap distinct metric names. The (MetricNameCap+1)th
// distinct name must be rejected with codes.ResourceExhausted and error code
// "metric_name_cap_exceeded" in the status message.
func TestEmitMetric_MetricNameCap(t *testing.T) {
	srv := newMetricsServer(t, "plug-namecap", "iid-namecap")

	// Emit MetricNameCap distinct names — all must succeed.
	for i := 0; i < hostsvc.MetricNameCap; i++ {
		name := fmt.Sprintf("namecap_metric_%04d", i)
		_, err := srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
			Name:  name,
			Value: float64(i),
		})
		if err != nil {
			t.Fatalf("emission %d (name=%q): unexpected error: %v", i, name, err)
		}
	}

	// The (MetricNameCap+1)th distinct name must be rejected.
	_, err := srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
		Name:  "namecap_metric_overflow",
		Value: 999,
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.ResourceExhausted {
		t.Fatalf("overflow name: expected codes.ResourceExhausted, got %v", err)
	}
	if !strings.Contains(st.Message(), "metric_name_cap_exceeded") {
		t.Errorf("status message = %q, want it to contain %q", st.Message(), "metric_name_cap_exceeded")
	}
}

// TestEmitMetric_MetricNameCap_RepeatedNameNotCounted verifies that repeatedly
// emitting the same metric name does not consume additional name-cap slots.
func TestEmitMetric_MetricNameCap_RepeatedNameNotCounted(t *testing.T) {
	srv := newMetricsServer(t, "plug-namecap-rep", "iid-namecap-rep")

	// Emit one name 10 times.
	for i := 0; i < 10; i++ {
		_, err := srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
			Name:  "repeated_metric",
			Value: float64(i),
		})
		if err != nil {
			t.Fatalf("repeated emission %d: unexpected error: %v", i, err)
		}
	}

	// (MetricNameCap - 1) additional distinct names must still succeed because
	// the repeated name only consumed one slot.
	for i := 0; i < hostsvc.MetricNameCap-1; i++ {
		name := fmt.Sprintf("namecap_rep_extra_%04d", i)
		_, err := srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
			Name:  name,
			Value: float64(i),
		})
		if err != nil {
			t.Fatalf("extra emission %d (name=%q): unexpected error: %v", i, name, err)
		}
	}
}

// TestEmitMetric_MetricNameLengthCap verifies that metric names longer than
// MaxMetricNameBytes are rejected with codes.InvalidArgument.
func TestEmitMetric_MetricNameLengthCap(t *testing.T) {
	srv := newMetricsServer(t, "plug-namelen", "iid-namelen")

	// Exactly at the limit must succeed.
	atLimit := strings.Repeat("a", hostsvc.MaxMetricNameBytes)
	_, err := srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
		Name:  atLimit,
		Value: 1,
	})
	if err != nil {
		t.Fatalf("name at limit (%d bytes): unexpected error: %v", hostsvc.MaxMetricNameBytes, err)
	}

	// One byte over the limit must be rejected.
	overLimit := strings.Repeat("b", hostsvc.MaxMetricNameBytes+1)
	_, err = srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
		Name:  overLimit,
		Value: 2,
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("name over limit: expected codes.InvalidArgument, got %v", err)
	}
	if !strings.Contains(st.Message(), "invalid_metric_name") {
		t.Errorf("status message = %q, want it to contain %q", st.Message(), "invalid_metric_name")
	}
}

// TestEmitMetric_LabelKeyLengthCap verifies that label keys longer than
// MaxLabelKeyBytes are rejected with codes.InvalidArgument.
func TestEmitMetric_LabelKeyLengthCap(t *testing.T) {
	srv := newMetricsServer(t, "plug-labelkey", "iid-labelkey")

	// Key exactly at the limit must succeed.
	atLimit := strings.Repeat("k", hostsvc.MaxLabelKeyBytes)
	_, err := srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
		Name:   "label_key_cap_metric",
		Value:  1,
		Labels: map[string]string{atLimit: "v"},
	})
	if err != nil {
		t.Fatalf("label key at limit (%d bytes): unexpected error: %v", hostsvc.MaxLabelKeyBytes, err)
	}

	// Key one byte over the limit must be rejected.
	overLimit := strings.Repeat("j", hostsvc.MaxLabelKeyBytes+1)
	_, err = srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
		Name:   "label_key_cap_metric_2",
		Value:  2,
		Labels: map[string]string{overLimit: "v"},
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("label key over limit: expected codes.InvalidArgument, got %v", err)
	}
	if !strings.Contains(st.Message(), "invalid_label") {
		t.Errorf("status message = %q, want it to contain %q", st.Message(), "invalid_label")
	}
}

// TestEmitMetric_LabelValueLengthCap verifies that label values longer than
// MaxLabelValueBytes are rejected with codes.InvalidArgument.
func TestEmitMetric_LabelValueLengthCap(t *testing.T) {
	srv := newMetricsServer(t, "plug-labelval", "iid-labelval")

	// Value exactly at the limit must succeed.
	atLimit := strings.Repeat("v", hostsvc.MaxLabelValueBytes)
	_, err := srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
		Name:   "label_val_cap_metric",
		Value:  1,
		Labels: map[string]string{"env": atLimit},
	})
	if err != nil {
		t.Fatalf("label value at limit (%d bytes): unexpected error: %v", hostsvc.MaxLabelValueBytes, err)
	}

	// Value one byte over the limit must be rejected.
	overLimit := strings.Repeat("w", hostsvc.MaxLabelValueBytes+1)
	_, err = srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
		Name:   "label_val_cap_metric_2",
		Value:  2,
		Labels: map[string]string{"env": overLimit},
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("label value over limit: expected codes.InvalidArgument, got %v", err)
	}
	if !strings.Contains(st.Message(), "invalid_label") {
		t.Errorf("status message = %q, want it to contain %q", st.Message(), "invalid_label")
	}
}

// TestEmitMetric_LabelKeySortingDeterminism verifies that two EmitMetric calls
// for the same metric name with the same set of label keys — but constructed in
// a way that could produce different map iteration orders — do not produce an
// AlreadyRegisteredError or inconsistent_label_keys rejection. This is the
// regression test for the non-deterministic label ordering bug.
//
// The test calls EmitMetric several times with a five-key label map. Because Go
// map iteration order is randomised per run, repeatedly registering with a naive
// range-over-map would fail. With sort.Strings applied before GaugeVec
// registration the order is stable and subsequent calls succeed.
func TestEmitMetric_LabelKeySortingDeterminism(t *testing.T) {
	srv := newMetricsServer(t, "plug-sort", "iid-sort")

	// Five label keys chosen to exercise different sort orderings.
	labels := map[string]string{
		"zebra":   "z",
		"alpha":   "a",
		"mango":   "m",
		"bravo":   "b",
		"charlie": "c",
	}

	// Emit the same metric name 20 times. Any non-determinism in label-key
	// ordering would surface as inconsistent_label_keys or panic on the second
	// registration attempt.
	for i := 0; i < 20; i++ {
		// Rebuild the map on every iteration to encourage different iteration
		// orders (Go randomises per map allocation).
		freshLabels := map[string]string{
			"zebra":   "z",
			"alpha":   "a",
			"mango":   "m",
			"bravo":   "b",
			"charlie": "c",
		}
		_, err := srv.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
			Name:   "sorted_label_metric",
			Value:  float64(i),
			Labels: freshLabels,
		})
		if err != nil {
			t.Fatalf("iteration %d: unexpected error (label key ordering bug?): %v", i, err)
		}
	}
	_ = labels // used only for documentation
}
