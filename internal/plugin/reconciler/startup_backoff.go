package reconciler

import (
	"sort"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// startupFailurePrefix opens the status_detail of every generation that failed
// while trying to come up: its container could not be created or started, or
// it started and then failed its health gate. It is the marker
// planStartupHold reads to tell those apart from a generation that failed for
// any other reason — an operator stop, a removed instance, a token lost to a
// restart — none of which says anything about whether the NEXT attempt would
// start.
//
// The marker lives in the durable row rather than in memory on purpose: the
// backoff is then derived from generation history alone, so planning stays
// pure over observed state, a restart neither forgives a persistent failure
// nor causes a burst of fresh attempts, and the reason an operator needs is
// the same text the backoff is computed from.
const startupFailurePrefix = "startup failed: "

// startupBackoffBase and startupBackoffMax bound the wait before minting
// another generation after consecutive startup failures: exponential,
// doubling from the base and capped at the max.
//
// Without it a container that cannot start (a host missing a sysctl the
// runtime needs, a bad image, a refused create) got a fresh generation every
// pass — each one a minted and revoked token, a created and removed container,
// and an ERROR log line — forever, since generation numbers are never reused.
// There is deliberately no hard stop past the cap: a failure caused by the
// host environment is fixed on the host, and the instance should come up on
// its own at the next attempt rather than waiting for someone to notice it
// also needs a manual nudge. At the cap that is one attempt per
// startupBackoffMax, which bounds the churn without abandoning recovery.
const (
	startupBackoffBase = 10 * time.Second
	startupBackoffMax  = 10 * time.Minute
)

// startupHold is what planStartupHold derives from an instance's generation
// history: how many consecutive attempts at the current desired image and
// config failed to start, and the earliest instant another may be minted.
type startupHold struct {
	// Failures is the number of consecutive startup failures at the tail of
	// the history. Zero means nothing holds minting.
	Failures int

	// LastFailure is the newest failed generation's status_detail — the start
	// error itself, for the log line an operator reads.
	LastFailure string

	// LastGeneration is that generation's number.
	LastGeneration int64

	// Until is the earliest instant the next generation may be minted.
	Until time.Time
}

// heldAt reports whether minting is still held at now.
func (h startupHold) heldAt(now time.Time) bool {
	return h.Failures > 0 && now.Before(h.Until)
}

// planStartupHold derives the mint backoff for an instance from its generation
// history (every status, any order) and its desired row.
//
// It walks the history newest-first and counts consecutive startup failures,
// stopping at the first generation that is anything else — an active or
// retired generation means the image did start at some point, and a failure
// for a non-startup reason (an operator stop, say) is a fresh starting point.
// Only failures at the CURRENT desired image digest and config hash count: an
// operator who ships a fixed bundle should see it tried at once, not wait out
// a backoff earned by the broken one.
//
// An unparseable updated_at on the newest failure yields no hold. Every
// failure this package records writes a fresh RFC 3339 timestamp, so the next
// failure restores the hold — whereas holding on a timestamp nobody can read
// would have no end at all.
func planStartupHold(history []db.PluginContainerGeneration, desired db.PluginContainer) startupHold {
	sorted := make([]db.PluginContainerGeneration, len(history))
	copy(sorted, history)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Generation > sorted[j].Generation })

	var hold startupHold
	for _, gen := range sorted {
		if !isStartupFailure(gen) ||
			gen.ImageDigest != desired.ImageDigest || gen.ConfigHash != desired.ConfigHash {
			break
		}
		if hold.Failures == 0 {
			hold.LastFailure = *gen.StatusDetail
			hold.LastGeneration = gen.Generation
			failedAt, err := time.Parse(time.RFC3339Nano, gen.UpdatedAt)
			if err != nil {
				return startupHold{}
			}
			hold.Until = failedAt
		}
		hold.Failures++
	}
	if hold.Failures > 0 {
		hold.Until = hold.Until.Add(startupBackoff(hold.Failures))
	}
	return hold
}

// isStartupFailure reports whether a generation failed while trying to come up.
func isStartupFailure(gen db.PluginContainerGeneration) bool {
	return gen.Status == GenFailed && gen.StatusDetail != nil &&
		strings.HasPrefix(*gen.StatusDetail, startupFailurePrefix)
}

// startupBackoff is the wait after the given number of consecutive startup
// failures: startupBackoffBase doubled per failure past the first, capped at
// startupBackoffMax.
func startupBackoff(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	wait := startupBackoffBase
	for i := 1; i < failures; i++ {
		wait *= 2
		if wait >= startupBackoffMax {
			return startupBackoffMax
		}
	}
	return wait
}
