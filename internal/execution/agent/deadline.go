package agent

import "time"

// DeadlineSource names which clock decided a tool-initiated wait's deadline
// (ADR-055, mcp-realignment-spec.md §6.3). It is persisted alongside the
// deadline so a later failure can say WHY the wait ended, not just that it did
// — the difference between "no human answered in time" and "the server threw
// away its own state", which are different problems with different fixes.
type DeadlineSource string

const (
	// DeadlineSourcePolicy is Gleipnir's own feedback timeout. It is
	// authoritative for the human leg (§6.3 rule 1): the operator's time is
	// what this clock measures, and no server gets to extend it.
	DeadlineSourcePolicy DeadlineSource = "policy"

	// DeadlineSourceServerTTL is the server's task TTL. Server-side TTLs are
	// "weather" (§6.3 rule 2) — real, worth surfacing, but not authoritative.
	DeadlineSourceServerTTL DeadlineSource = "server_ttl"

	// DeadlineSourceRequestState is a TTL the server declares for its opaque
	// MRTR requestState blob.
	//
	// Nothing populates this today, and that is deliberate rather than
	// unfinished: internal/mcp treats requestState as opaque and never parses
	// it, so Gleipnir cannot discover a TTL hiding inside it. The source exists
	// because §6.3's precedence rule has three clocks, and a rule implemented
	// for two of them is a rule that gets re-derived incorrectly when the third
	// arrives. When a server declares that TTL out-of-band, it enters here.
	DeadlineSourceRequestState DeadlineSource = "request_state"
)

// Valid reports whether s is a known source. The tool_input_requests
// deadline_source column has a matching CHECK constraint.
func (s DeadlineSource) Valid() bool {
	switch s {
	case DeadlineSourcePolicy, DeadlineSourceServerTTL, DeadlineSourceRequestState:
		return true
	}
	return false
}

// DeadlineInputs carries the clocks that can govern one tool-initiated wait.
// A zero time means that clock does not apply.
type DeadlineInputs struct {
	// Now is the reference instant. Callers pass an explicit value rather than
	// letting this package read the wall clock, so the computation is pure and
	// testable.
	Now time.Time

	// PolicyTimeout is how long Gleipnir will wait for a human. Must be
	// positive — every wait carries a deadline, and a caller that has not
	// resolved a timeout has not finished resolving the policy.
	PolicyTimeout time.Duration

	// ServerTaskTTL is when the server says its task expires. Zero if the wait
	// is not backed by a task, or the server declared no TTL.
	ServerTaskTTL time.Time

	// RequestStateTTL is when the server says its opaque requestState expires.
	// Zero unless a server declared it out-of-band — see
	// DeadlineSourceRequestState.
	RequestStateTTL time.Time
}

// EffectiveDeadline computes the deadline that actually governs a wait and the
// clock it came from: the minimum of the applicable clocks (§6.3 rule 3,
// "audiences display the effective deadline").
//
// Taking the minimum is not the same as treating the clocks as equals. The
// policy timeout is authoritative for the human leg — it is the promise made to
// the operator, and it always applies. A server clock can only ever make the
// wait SHORTER, by telling us the answer will be worthless after some point
// because the server will have discarded the state it needs to accept it.
// Waiting past that moment would collect an answer nobody can use.
//
// Ties go to the policy: when two clocks land on the same instant, the run
// failed because Gleipnir's own timeout elapsed, and reporting a server TTL
// would point the operator at someone else's system for a deadline that was
// ours.
//
// A server clock already in the past does not produce a deadline in the past.
// That would expire the wait before an operator could possibly see it, turning
// a stale server into an instant, invisible failure. It is clamped to Now, so
// the very next scan resolves it with the server-TTL source recorded — the
// failure is immediate but explained.
func EffectiveDeadline(in DeadlineInputs) (time.Time, DeadlineSource) {
	deadline := in.Now.Add(in.PolicyTimeout)
	source := DeadlineSourcePolicy

	// Strictly-earlier comparisons keep the tie with the policy clock.
	if !in.ServerTaskTTL.IsZero() && in.ServerTaskTTL.Before(deadline) {
		deadline = in.ServerTaskTTL
		source = DeadlineSourceServerTTL
	}
	if !in.RequestStateTTL.IsZero() && in.RequestStateTTL.Before(deadline) {
		deadline = in.RequestStateTTL
		source = DeadlineSourceRequestState
	}

	if deadline.Before(in.Now) {
		deadline = in.Now
	}
	return deadline, source
}

// timeoutMessage explains an expired wait in terms of the clock that ended it.
// "Nobody answered in time" and "the server threw away the state needed to
// accept an answer" are different problems with different fixes, and an
// operator reading a failed run should not have to guess which one happened.
func timeoutMessage(toolName string, source DeadlineSource, policyTimeout time.Duration) string {
	switch source {
	case DeadlineSourceServerTTL:
		return "tool input timeout: the server's task for " + toolName +
			" expired before an operator answered; the server, not Gleipnir, ended this wait"
	case DeadlineSourceRequestState:
		return "tool input timeout: the server's request state for " + toolName +
			" expired before an operator answered; the server, not Gleipnir, ended this wait"
	default:
		return "tool input timeout: operator did not answer " + toolName + " within " + policyTimeout.String()
	}
}

// timeNow is the agent package's injectable clock for deadline computation
// (CLAUDE.md "Testing time-dependent code"). Tests swap it via t.Cleanup and
// must not call t.Parallel() while it is swapped.
var timeNow = func() time.Time { return time.Now() }
