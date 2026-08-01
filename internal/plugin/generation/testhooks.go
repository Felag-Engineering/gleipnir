package generation

// SetAfterPauseHookForTest installs fn as the post-pause synchronisation hook
// invoked by BeginDrain the moment an instance is committed to the paused
// state (see Controller.afterPauseHook in controller.go). BeginDrain does not
// return until *after* it un-pauses, so without this hook there is no public
// signal for the mid-drain paused window (issue #678); tests that need to
// race a concurrent Acquire, RPC, or BeginDrain call against a drain in
// progress synchronise on this hook instead of a sleep (CLAUDE.md
// "Signal-don't-poll").
//
// This lives in a plain (non-_test.go) file rather than export_test.go so
// that tests OUTSIDE this package — e.g. internal/plugin/hostsvc and
// internal/plugin/e2e — can install it too. A `_test.go` file, even one that
// exports package-private state for an external test package in the same
// directory, is only compiled when testing this package itself; it is
// invisible to other packages that merely import generation.
//
// The hook lives on the Controller (not a package-level variable), so each
// test's own controller is isolated and there is nothing to restore between
// tests. That said, callers MUST NOT run the calling test with t.Parallel()
// while a hook is installed: afterPauseHook is an unsynchronized field read
// by BeginDrain, and the point of this seam is to let a single test
// deterministically order its own goroutines around one BeginDrain call —
// mixing that with t.Parallel() reintroduces exactly the kind of scheduling
// assumption this hook exists to eliminate.
//
// SetAfterPauseHookForTest must never be called from production code —
// afterPauseHook is always nil on every real Controller.
func (c *Controller) SetAfterPauseHookForTest(fn func(instanceID string)) {
	c.afterPauseHook = fn
}
