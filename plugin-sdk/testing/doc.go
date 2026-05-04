// Package testing provides the fake host and test harness for Gleipnir plugin
// authors.
//
// # Import alias
//
// Because the package name "testing" collides with the stdlib "testing"
// package, import it with an alias in test files:
//
//	import plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
//
// # Minimal usage
//
//	host := plugintest.NewFakeHost(
//	    plugintest.WithInstanceConfigJSON(`{"greeting":"hi"}`),
//	    plugintest.WithRunContext(plugintest.RunContext{RunID: "r-1"}),
//	)
//	// wire host into a gRPC server, invoke your service, then:
//	host.AssertMetricEmitted(t, "echo_calls_total", map[string]string{"tool":"echo"})
//
// # Spec reference
//
// See docs/developer/plugin-system-spec.md §14.4 (testing harness).
//
// # No hostv1 import required
//
// Plugin authors never need to import gen/.../hostv1. All public types
// (AuditStep, Metric, Event, LogLine, RunContext, RunSummary, UserEntry,
// HealthState) are plain Go structs defined in this package.
//
// # Known fake-vs-real divergences
//
//   - No cardinality cap: production enforces a per-metric cardinality cap
//     (spec §8.1); the fake records every emission without limit.
//   - Production auto-injects plugin/instance labels (spec §12.2); assertion
//     helpers use subset label matching so tests remain forward-compatible
//     when new labels are added.
package testing
