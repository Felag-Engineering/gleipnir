// Package e2e owns the host-side plugin substrate composition test. It wires
// together every host-side plugin-substrate package
// (internal/plugin/trigger, internal/plugin/dedup, internal/plugin/binding,
// internal/plugin/dispatch plus internal/execution/run) and proves they compose
// correctly using a generic in-process stub plugin. Slack-specific behavior is
// validated separately by the Playwright spec under tests/playwright/.
//
// All test files use the external test package name "e2e_test" so no
// production code can import this package. It runs in CI on every PR as the
// per-PR regression gate (see issue #237 §"Deviation from spec §14.7").
package e2e
