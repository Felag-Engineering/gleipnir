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
//	h := plugintest.NewToolHarness(t,
//	    func(hc hostv1.HostServiceClient) tool.Service { return NewToolService(hc) },
//	    plugintest.WithInstanceConfigJSON(`{"greeting":"hi"}`),
//	)
//	out, err := h.Call(ctx, "echo", []byte(`{"message":"hi"}`))
//	h.Host.AssertMetricEmitted(t, "echo_calls_total", map[string]string{"tool": "echo"})
//
// NewToolHarness (and the analogous NewChannelHarness / NewTriggerHarness)
// wires the author's service against a live FakeHost over in-process bufconn
// gRPC, registers a single t.Cleanup that tears everything down, and returns
// a typed client plus the FakeHost for assertions.
//
// # Host callbacks in harness tests
//
// Harness gRPC contexts carry no gleipnir-call-id header, so serve.WithCallContext
// is a graceful no-op — it leaves the context unchanged rather than injecting a
// call ID. Host RPCs (EmitMetric, Log, …) still reach the FakeHost over the live
// connection, so AssertMetricEmitted / AssertLogContains assertions pass normally.
//
// # Raw wire exception
//
// plugin-sdk/cmd/gleipnir-plugin/cmd/internal/runfixture deliberately uses the
// real go-plugin subprocess transport and is intentionally NOT migrated to the
// harness. It exists to exercise the full plugin subprocess launch path (the
// binary, not the service logic) and must remain on the raw wire.
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
