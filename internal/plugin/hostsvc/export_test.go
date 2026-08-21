package hostsvc

import (
	"time"

	"github.com/felag-engineering/gleipnir/internal/plugin/pluginmetrics"
)

// SetTimeNowForTest swaps the package-level timeNow clock for tests in the
// hostsvc_test external package. It returns a restore function the caller
// must register with t.Cleanup.
//
// Use this from external tests to make rate-limit / audit-flush behavior
// deterministic. See CLAUDE.md "Testing rate-limited code" for the pattern.
func SetTimeNowForTest(fn func() time.Time) (restore func()) {
	orig := timeNow
	timeNow = fn
	return func() { timeNow = orig }
}

// Exported caps for use in external test package assertions. The values
// moved to internal/plugin/pluginmetrics with the guard itself (#877); these
// aliases keep this package's gRPC-level tests reading naturally until the
// whole package is removed at the cutover (#883).
const (
	MetricNameCap      = pluginmetrics.MetricNameCap
	MaxMetricNameBytes = pluginmetrics.MaxMetricNameBytes
	MaxLabelKeyBytes   = pluginmetrics.MaxLabelKeyBytes
	MaxLabelValueBytes = pluginmetrics.MaxLabelValueBytes
)
