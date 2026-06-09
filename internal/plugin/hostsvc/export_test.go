package hostsvc

import "time"

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

// Exported caps for use in external test package assertions.
const (
	MetricNameCap      = metricNameCap
	MaxMetricNameBytes = maxMetricNameBytes
	MaxLabelKeyBytes   = maxLabelKeyBytes
	MaxLabelValueBytes = maxLabelValueBytes
)
