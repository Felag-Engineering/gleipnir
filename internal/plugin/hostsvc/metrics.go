package hostsvc

import (
	"fmt"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// cardinalityCap is the maximum number of distinct values allowed per label key
// per metric. Exceeding this limit on any label causes the emission to be
// rejected with codes.ResourceExhausted (loud failure, per spec §8.1).
const cardinalityCap = 100

// gaugeEntry pairs a registered GaugeVec with the sorted set of user-supplied
// label keys it was registered with. This lets us detect inconsistent label
// keys across calls for the same metric name and return a clear error instead
// of panicking inside prometheus.GaugeVec.With.
type gaugeEntry struct {
	gv        *prometheus.GaugeVec
	labelKeys map[string]struct{} // user-supplied keys only (excludes auto-injected plugin/instance)
}

// pluginMetrics manages Prometheus GaugeVecs for plugin-emitted metrics.
//
// Design note: every plugin-emitted metric is registered as a GaugeVec because
// the EmitMetric proto carries only (name, value, labels) with no type
// discriminator. Counter and histogram semantics require declaration metadata
// that this RPC does not provide. Gauge is the safest universal choice; plugins
// needing monotonic counters require a dedicated follow-up RPC with explicit
// type support.
type pluginMetrics struct {
	mu sync.Mutex

	// gauges maps the fully-qualified metric name (gleipnir_plugin_<name>) to
	// its registered GaugeVec and the label-key set it was registered with.
	gauges map[string]gaugeEntry

	// cardinality tracks the set of observed values per (metric, labelKey).
	// It is checked before calling GaugeVec.With so we never register a new
	// label combination once the cap is reached.
	cardinality map[string]map[string]map[string]struct{} // metric → labelKey → valueSet
}

func newPluginMetrics() *pluginMetrics {
	return &pluginMetrics{
		gauges:      make(map[string]gaugeEntry),
		cardinality: make(map[string]map[string]map[string]struct{}),
	}
}

// reservedLabels are auto-injected by the host and must not appear in
// user-supplied label maps (collision would produce duplicate label keys in the
// GaugeVec, which Prometheus rejects at registration time).
var reservedLabels = map[string]bool{"plugin": true, "instance": true}

// set validates the metric name and labels, enforces cardinality, registers the
// GaugeVec on first use, then calls GaugeVec.With(merged).Set(value).
//
// pluginID and instanceID are the auto-injected label values.
// userLabels must not contain "plugin" or "instance" (caller validates first).
//
// Returns an error envelope code string on failure, or "" on success.
func (m *pluginMetrics) set(name string, value float64, userLabels map[string]string, pluginID, instanceID string) (errCode string, err error) {
	if strings.HasPrefix(name, "gleipnir_plugin_") {
		return "invalid_metric_name", fmt.Errorf("metric name must not include the gleipnir_plugin_ prefix (host adds it automatically)")
	}
	if name == "" {
		return "invalid_metric_name", fmt.Errorf("metric name must not be empty")
	}

	for k := range userLabels {
		if reservedLabels[k] {
			return "reserved_label", fmt.Errorf("label key %q is reserved by the host (auto-injected)", k)
		}
	}

	fullName := "gleipnir_plugin_" + name

	m.mu.Lock()
	defer m.mu.Unlock()

	// Cardinality pre-flight: check every user-supplied label before touching
	// Prometheus so we never register a new label combination once capped.
	mc := m.cardinality[fullName]
	for k, v := range userLabels {
		if mc == nil {
			break
		}
		ks := mc[k]
		if len(ks) >= cardinalityCap {
			if _, seen := ks[v]; !seen {
				return "cardinality_cap_exceeded", fmt.Errorf(
					"label %q for metric %q has reached the %d-value cardinality cap",
					k, fullName, cardinalityCap,
				)
			}
		}
	}

	entry, exists := m.gauges[fullName]
	if !exists {
		// Build the user-key set for future consistency checks.
		userKeySet := make(map[string]struct{}, len(userLabels))
		for k := range userLabels {
			userKeySet[k] = struct{}{}
		}

		// Build the full label key list: user keys + auto-injected plugin + instance.
		labelKeys := make([]string, 0, len(userLabels)+2)
		for k := range userLabels {
			labelKeys = append(labelKeys, k)
		}
		labelKeys = append(labelKeys, "plugin", "instance")

		opts := prometheus.GaugeOpts{
			Name: fullName,
			Help: "Plugin-emitted metric (gauge).",
		}
		candidate := prometheus.NewGaugeVec(opts, labelKeys)

		// The rest of the codebase registers collectors at package init via
		// promauto.With(). Plugin metrics arrive at runtime (per-call), so we
		// use the AlreadyRegisteredError idiom to make registration idempotent:
		// if another goroutine registered the same name first, reuse its
		// collector rather than returning an error.
		var gv *prometheus.GaugeVec
		if regErr := metrics.Registry().Register(candidate); regErr != nil {
			if are, ok := regErr.(prometheus.AlreadyRegisteredError); ok {
				gv = are.ExistingCollector.(*prometheus.GaugeVec)
			} else {
				return "metric_registration_failed", fmt.Errorf("register metric %q: %w", fullName, regErr)
			}
		} else {
			gv = candidate
		}
		entry = gaugeEntry{gv: gv, labelKeys: userKeySet}
		m.gauges[fullName] = entry
	} else {
		// Validate that the caller presents exactly the same user-supplied label
		// keys as the original registration. A mismatch would cause gv.With to
		// panic with "label name ... missing in label map".
		if len(userLabels) != len(entry.labelKeys) {
			return "inconsistent_label_keys", fmt.Errorf(
				"metric %q was registered with %d label key(s); got %d",
				fullName, len(entry.labelKeys), len(userLabels),
			)
		}
		for k := range userLabels {
			if _, ok := entry.labelKeys[k]; !ok {
				return "inconsistent_label_keys", fmt.Errorf(
					"metric %q: label key %q was not present at registration time",
					fullName, k,
				)
			}
		}
	}

	// Build the merged label map.
	merged := make(prometheus.Labels, len(userLabels)+2)
	for k, v := range userLabels {
		merged[k] = v
	}
	merged["plugin"] = pluginID
	merged["instance"] = instanceID

	// Record new label values in the cardinality tracker after the pre-flight
	// passes and before setting the gauge (still under m.mu).
	if mc == nil {
		mc = make(map[string]map[string]struct{})
		m.cardinality[fullName] = mc
	}
	for k, v := range userLabels {
		if mc[k] == nil {
			mc[k] = make(map[string]struct{})
		}
		mc[k][v] = struct{}{}
	}

	entry.gv.With(merged).Set(value)
	return "", nil
}
