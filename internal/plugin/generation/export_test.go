package generation

// SetAfterPauseHookForTest installs fn as the post-pause synchronisation hook
// invoked by BeginDrain once an instance is committed to the paused state. It is
// the test-only seam that lets the external generation_test package observe the
// mid-drain paused window deterministically (issue #678).
//
// The hook lives on the Controller (not a package global), so each test's own
// controller is isolated — there is nothing to restore and no cross-test leak.
// Call this before issuing any BeginDrain on the controller; the launching
// go-statement then establishes happens-before for BeginDrain's read.
func (c *Controller) SetAfterPauseHookForTest(fn func(instanceID string)) {
	c.afterPauseHook = fn
}
