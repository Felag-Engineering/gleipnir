package mcp

import (
	"fmt"
	"sync"

	"golang.org/x/time/rate"
)

// Elicitation rate-limit defaults (spec §6.2 cap 3). A token bucket, not a
// debounce: the spec rejects heuristics here because the harm being prevented
// is repetition fatigue-training approvers, and a debounce only spaces an
// attack out rather than capping it.
//
// The defaults are deliberately low. A legitimate server asks a human
// something occasionally; one asking more than a few times a minute is either
// broken or attacking the operator's attention.
const (
	defaultElicitationRatePerSec = 1.0
	defaultElicitationBurst      = 5
)

// ElicitationRateLimitError reports that a server exceeded its input_required
// rate limit. CallTool returns it in place of the decoded result: the call
// fails structurally and nothing is persisted or shown to an operator.
type ElicitationRateLimitError struct {
	ServerName string
}

func (e *ElicitationRateLimitError) Error() string {
	return fmt.Sprintf("mcp server %s exceeded its input_required rate limit", e.ServerName)
}

// elicitationLimiter is the per-server token bucket. One lives on each Client,
// and the Registry hands back the same Client for a given server for as long
// as its config is unchanged, so "per Client" is "per server" in practice.
//
// A client-cache eviction (a server's URL or auth headers changing) resets the
// bucket. That is acceptable: the operator changed the server, and the reset
// costs at most one burst.
type elicitationLimiter struct {
	mu      sync.Mutex
	limiter *rate.Limiter
}

// newElicitationLimiter builds a limiter from a (rate, burst) pair, applying
// the defaults for any non-positive value. As with ElicitationLimits.resolve, a
// bad value can only be corrected toward the default — never toward "no limit".
func newElicitationLimiter(ratePerSec float64, burst int) *elicitationLimiter {
	if ratePerSec <= 0 {
		ratePerSec = defaultElicitationRatePerSec
	}
	if burst <= 0 {
		burst = defaultElicitationBurst
	}
	return &elicitationLimiter{limiter: rate.NewLimiter(rate.Limit(ratePerSec), burst)}
}

// allow reports whether one more input_required result from this server is
// within the limit.
//
// AllowN with an explicit timestamp — rather than Allow(), which reads the
// wall clock internally — so refill follows the package's injectable timeNow
// clock and a test can freeze it for deterministic burst/refill assertions.
func (l *elicitationLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limiter.AllowN(timeNow(), 1)
}
