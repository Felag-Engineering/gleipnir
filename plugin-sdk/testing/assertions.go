package testing

import (
	"fmt"
	"log/slog"
	"strings"
)

// TB is the subset of stdlib testing.TB used by assertion helpers. Both
// *testing.T and *testing.B satisfy this interface, so test authors do not
// need to import the stdlib "testing" package to call these helpers.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// AssertMetricEmitted fails the test unless at least one recorded metric has
// the given name AND every key-value pair in labels appears in the metric's
// label map. Extra labels on the recorded metric are allowed (subset match),
// because production auto-injects plugin/instance labels (spec §12.2).
func (f *FakeHost) AssertMetricEmitted(tb TB, name string, labels map[string]string) {
	tb.Helper()
	for _, m := range f.Metrics() {
		if m.Name != name {
			continue
		}
		if labelsMatch(m.Labels, labels) {
			return
		}
	}
	tb.Fatalf("AssertMetricEmitted: no metric %q with labels %v recorded; got: %v", name, labels, f.Metrics())
}

// AssertNoMetricEmitted fails the test if any recorded metric has the given name.
func (f *FakeHost) AssertNoMetricEmitted(tb TB, name string) {
	tb.Helper()
	count := 0
	for _, m := range f.Metrics() {
		if m.Name == name {
			count++
		}
	}
	if count > 0 {
		tb.Fatalf("AssertNoMetricEmitted: metric %q was emitted %d time(s) but should not have been", name, count)
	}
}

// AssertEventEmitted fails the test unless at least one recorded event has
// the given event kind.
func (f *FakeHost) AssertEventEmitted(tb TB, kind string) {
	tb.Helper()
	for _, e := range f.Events() {
		if e.EventKind == kind {
			return
		}
	}
	tb.Fatalf("AssertEventEmitted: no event with kind %q recorded; got: %v", kind, f.Events())
}

// AssertLogContains fails the test unless at least one recorded log line
// matches the given level exactly and contains substr in its message.
func (f *FakeHost) AssertLogContains(tb TB, level slog.Level, substr string) {
	tb.Helper()
	for _, l := range f.Logs() {
		if l.Level == level && strings.Contains(l.Msg, substr) {
			return
		}
	}
	tb.Fatalf("AssertLogContains: no log at level %v containing %q; got: %v", level, substr, f.Logs())
}

// AssertAuditStep fails the test unless at least one recorded audit step has
// the given step type and request ID.
func (f *FakeHost) AssertAuditStep(tb TB, stepType, requestID string) {
	tb.Helper()
	for _, s := range f.AuditSteps() {
		if s.StepType == stepType && s.RequestID == requestID {
			return
		}
	}
	tb.Fatalf("AssertAuditStep: no audit step with stepType=%q requestID=%q; got: %v", stepType, requestID, f.AuditSteps())
}

// AssertHealth fails the test unless the most recently reported health state
// equals want.
func (f *FakeHost) AssertHealth(tb TB, want HealthState) {
	tb.Helper()
	got, _, ok := f.Health()
	if !ok {
		tb.Fatalf("AssertHealth: no health state recorded")
		return
	}
	if got != want {
		tb.Fatalf("AssertHealth: want %v, got %v", healthStateName(want), healthStateName(got))
	}
}

// AssertRunHistoryRead fails the test unless RunHistoryCalls() equals n.
func (f *FakeHost) AssertRunHistoryRead(tb TB, n int) {
	tb.Helper()
	if got := f.RunHistoryCalls(); got != n {
		tb.Fatalf("AssertRunHistoryRead: want %d call(s), got %d", n, got)
	}
}

// AssertUserDirectoryRead fails the test unless UserDirectoryCalls() equals n.
func (f *FakeHost) AssertUserDirectoryRead(tb TB, n int) {
	tb.Helper()
	if got := f.UserDirectoryCalls(); got != n {
		tb.Fatalf("AssertUserDirectoryRead: want %d call(s), got %d", n, got)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// labelsMatch reports whether every key-value pair in want appears in got.
// Extra entries in got are allowed (subset match).
func labelsMatch(got, want map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// healthStateName returns a human-readable name for a HealthState constant.
func healthStateName(s HealthState) string {
	switch s {
	case HealthStateUnspecified:
		return "Unspecified"
	case HealthStateHealthy:
		return "Healthy"
	case HealthStateUnavailable:
		return "Unavailable"
	case HealthStateUnhealthy:
		return "Unhealthy"
	default:
		return fmt.Sprintf("HealthState(%d)", int32(s))
	}
}
