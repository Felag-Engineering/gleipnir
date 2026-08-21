package pluginmetrics

import (
	"fmt"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// These tests exercise the guard directly, where it now lives. The gRPC-level
// tests in internal/plugin/hostsvc keep covering the same behaviour through
// the EmitMetric RPC until that package is removed at the cutover (#883);
// the host endpoint's host/emit_metric tests cover the HTTP path. Metric
// names are unique per test because the guard registers into the shared
// process-wide registry.

// gatherLabels returns the label pairs of the first series of the named
// metric family, or nil when the family does not exist.
func gatherLabels(t *testing.T, fullName string) map[string]string {
	t.Helper()
	families, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, fam := range families {
		if fam.GetName() == fullName && len(fam.GetMetric()) > 0 {
			out := make(map[string]string)
			for _, lp := range fam.GetMetric()[0].GetLabel() {
				out[lp.GetName()] = lp.GetValue()
			}
			return out
		}
	}
	return nil
}

func TestSet_ForcePrefixAndAutoLabels(t *testing.T) {
	m := New()
	if code, err := m.Set("prefix_probe_total", 7, map[string]string{"queue": "inbound"}, "plug-a", "inst-a"); err != nil {
		t.Fatalf("Set: %s: %v", code, err)
	}

	labels := gatherLabels(t, "gleipnir_plugin_prefix_probe_total")
	if labels == nil {
		t.Fatal("metric not registered under the forced gleipnir_plugin_ prefix")
	}
	if labels[metrics.LabelPlugin] != "plug-a" || labels[metrics.LabelInstance] != "inst-a" {
		t.Errorf("auto-injected labels = %v, want plugin=plug-a instance=inst-a", labels)
	}
	if labels["queue"] != "inbound" {
		t.Errorf("user label queue = %q, want inbound", labels["queue"])
	}

	// A caller pre-applying the prefix is rejected — double-prefixing would
	// split one logical metric across two names.
	if code, _ := m.Set("gleipnir_plugin_prefix_probe_total", 1, nil, "plug-a", "inst-a"); code != "invalid_metric_name" {
		t.Errorf("pre-prefixed name: code = %q, want invalid_metric_name", code)
	}
}

func TestSet_ReservedLabelsRejected(t *testing.T) {
	m := New()
	for _, reserved := range []string{metrics.LabelPlugin, metrics.LabelInstance} {
		code, err := m.Set("reserved_probe", 1, map[string]string{reserved: "spoof"}, "plug-b", "inst-b")
		if code != "reserved_label" || err == nil {
			t.Errorf("label %q: code = %q err = %v, want reserved_label rejection", reserved, code, err)
		}
	}
}

func TestSet_CardinalityCap(t *testing.T) {
	m := New()
	for i := 0; i < CardinalityCap; i++ {
		code, err := m.Set("cardcap_probe", 1, map[string]string{"shard": fmt.Sprintf("s%04d", i)}, "plug-c", "inst-c")
		if err != nil {
			t.Fatalf("value %d: %s: %v", i, code, err)
		}
	}
	// Value CardinalityCap+1 is a NEW distinct value past the cap: rejected.
	code, err := m.Set("cardcap_probe", 1, map[string]string{"shard": "overflow"}, "plug-c", "inst-c")
	if code != "cardinality_cap_exceeded" || err == nil {
		t.Fatalf("overflow value: code = %q err = %v, want cardinality_cap_exceeded", code, err)
	}
	// An ALREADY-SEEN value keeps working at the cap — the cap bounds
	// distinct values, it does not freeze the metric.
	if code, err := m.Set("cardcap_probe", 2, map[string]string{"shard": "s0000"}, "plug-c", "inst-c"); err != nil {
		t.Fatalf("seen value at cap: %s: %v", code, err)
	}
}

func TestSet_InconsistentLabelKeysRejected(t *testing.T) {
	m := New()
	if code, err := m.Set("labelset_probe", 1, map[string]string{"a": "1", "b": "2"}, "plug-d", "inst-d"); err != nil {
		t.Fatalf("register: %s: %v", code, err)
	}

	cases := []struct {
		name   string
		labels map[string]string
	}{
		{name: "missing key", labels: map[string]string{"a": "1"}},
		{name: "extra key", labels: map[string]string{"a": "1", "b": "2", "c": "3"}},
		{name: "renamed key", labels: map[string]string{"a": "1", "z": "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, err := m.Set("labelset_probe", 1, tc.labels, "plug-d", "inst-d")
			if code != "inconsistent_label_keys" || err == nil {
				t.Errorf("code = %q err = %v, want inconsistent_label_keys — a mismatch must reject, not panic in GaugeVec.With", code, err)
			}
		})
	}
}

func TestSet_ValidationCaps(t *testing.T) {
	m := New()
	if code, _ := m.Set("", 1, nil, "p", "i"); code != "invalid_metric_name" {
		t.Errorf("empty name: code = %q", code)
	}
	if code, _ := m.Set(strings.Repeat("n", MaxMetricNameBytes+1), 1, nil, "p", "i"); code != "invalid_metric_name" {
		t.Errorf("oversize name: code = %q", code)
	}
	if code, _ := m.Set("caps_probe", 1, map[string]string{strings.Repeat("k", MaxLabelKeyBytes+1): "v"}, "p", "i"); code != "invalid_label" {
		t.Errorf("oversize key: code = %q", code)
	}
	if code, _ := m.Set("caps_probe", 1, map[string]string{"k": strings.Repeat("v", MaxLabelValueBytes+1)}, "p", "i"); code != "invalid_label" {
		t.Errorf("oversize value: code = %q", code)
	}
}
