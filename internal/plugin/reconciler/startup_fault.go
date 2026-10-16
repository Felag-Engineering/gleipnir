package reconciler

import (
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// StartupFault is the operator-facing account of an instance whose containers
// keep failing to start: the reason, how many attempts in a row have failed,
// and when the next one is due.
//
// It is a READ of generation history, not a health verdict, and that is the
// point of keeping it off the health chip. Instance health has one writer, the
// capability prober, and a startup failure is not something the prober can
// observe — a container that never came up is never a probe target. More
// importantly, a failing upgrade must not narrow routing: during a rotation the
// old generation keeps serving while new ones fail to start, so the fault is
// reported next to the health state rather than folded into it. Serving says
// which case this is.
type StartupFault struct {
	// Failures is the number of consecutive startup failures at the current
	// desired image and config — the same count the mint backoff is derived
	// from.
	Failures int

	// LastError is the newest failure's cause, without the startupFailurePrefix
	// marker: the create, start or health-gate error itself.
	LastError string

	// LastGeneration is the newest failed generation's number.
	LastGeneration int64

	// LastFailedAt is when that generation failed.
	LastFailedAt time.Time

	// NextAttemptAt is the earliest instant the reconciler will mint another
	// generation. It may be in the past: the hold has expired and the next pass
	// will try again.
	NextAttemptAt time.Time

	// AttemptInProgress reports a newer generation that is still on its way up
	// (pending, starting, or healthy and not yet switched to). It does not hide
	// the fault: the failures it may be about to repeat are still the latest
	// evidence of whether this instance can start.
	AttemptInProgress bool

	// ServingGeneration is the generation still serving traffic — active, or
	// draining mid-switch — or zero when nothing is. Non-zero means this is a
	// failed upgrade and the instance is up on its previous generation.
	ServingGeneration int64
}

// DescribeStartupFault derives an instance's startup fault from its full
// generation history and its desired row. ok is false when there is nothing to
// report.
//
// It applies planStartupHold's rule exactly, so the operator sees the same
// count and deadline the reconciler is acting on. The one difference is that a
// generation still in flight is set aside before counting rather than ending
// the run: planStartupHold never sees one (it runs just before a mint, when
// nothing is in flight), but a reader can, and letting it break the run would
// make the fault vanish for the length of every retry and reappear when the
// retry failed.
//
// The fault clears on its own once a generation reaches active, because an
// active generation ends the run of consecutive failures, and once the desired
// image or config changes, because only failures at the current desired pair
// count. A stopped instance has none: nothing will be attempted, so "next
// attempt at" would be false, and the stop's own failure detail ends the run
// for any generation that was live.
func DescribeStartupFault(history []db.PluginContainerGeneration, desired db.PluginContainer) (StartupFault, bool) {
	if desired.DesiredState == DesiredStopped {
		return StartupFault{}, false
	}
	var fault StartupFault
	settled := make([]db.PluginContainerGeneration, 0, len(history))
	for _, gen := range history {
		switch gen.Status {
		case GenPending, GenStarting, GenHealthy:
			fault.AttemptInProgress = true
			continue
		case GenActive, GenDraining:
			if gen.Generation > fault.ServingGeneration {
				fault.ServingGeneration = gen.Generation
			}
		}
		settled = append(settled, gen)
	}

	hold := planStartupHold(settled, desired)
	if hold.Failures == 0 {
		return StartupFault{}, false
	}
	fault.Failures = hold.Failures
	fault.LastError = strings.TrimPrefix(hold.LastFailure, startupFailurePrefix)
	fault.LastGeneration = hold.LastGeneration
	fault.LastFailedAt = hold.LastFailedAt
	fault.NextAttemptAt = hold.Until
	return fault, true
}
