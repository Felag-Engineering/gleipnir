package dispatch

// SetQueueSlotClaimedHookForTest installs fn as the queue-slot-claim signal
// and returns a restore func. Tests that install a hook must not run with
// t.Parallel() (package-level state; see CLAUDE.md "Testing time-dependent
// code" rule 2 — the same constraint applies to any shared test hook).
func SetQueueSlotClaimedHookForTest(fn func()) (restore func()) {
	prev := testHookQueueSlotClaimed
	testHookQueueSlotClaimed = fn
	return func() { testHookQueueSlotClaimed = prev }
}
