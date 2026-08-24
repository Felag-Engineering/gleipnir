package hostsvc

import (
	"sync"
	"time"
)

// emitEventRetiredBucket holds the audit-coalescing state for one plugin
// instance's refused EmitEvent calls.
type emitEventRetiredBucket struct {
	count     uint64    // refusals accumulated since the last audit flush
	lastFlush time.Time // zero value on creation, so the first refusal always flushes immediately
}

// emitEventRetiredCoalescer coalesces "emit_event_retired_profile" audit rows
// to at most one per auditFlushInterval per instance — same rationale and
// window as eventRateLimiter's "event_rate_limited" coalescing (spec §4.3
// "periodic"): a v2 event-source plugin that keeps calling the retired
// EmitEvent RPC must not drown the audit log with one row per call.
//
// Unlike eventRateLimiter this never allows the call through — EmitEvent is
// unconditionally refused for these callers — so there is no token bucket
// here, only the coalescing window.
type emitEventRetiredCoalescer struct {
	mu      sync.Mutex
	buckets map[string]*emitEventRetiredBucket // keyed by instance ID
}

// newEmitEventRetiredCoalescer constructs a coalescer with no pre-allocated
// buckets. Buckets are created lazily on first refusal from each instance.
func newEmitEventRetiredCoalescer() *emitEventRetiredCoalescer {
	return &emitEventRetiredCoalescer{
		buckets: map[string]*emitEventRetiredBucket{},
	}
}

// Note records one refusal for instanceID. It returns flush=true when the
// caller should write an audit row now, along with count — the number of
// refusals coalesced into this flush (including the current one). The
// zero-value lastFlush on a new bucket means the very first refusal for an
// instance always flushes, giving operators an immediate signal rather than
// waiting up to a minute.
func (c *emitEventRetiredCoalescer) Note(instanceID string) (flush bool, count uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	b, ok := c.buckets[instanceID]
	if !ok {
		b = &emitEventRetiredBucket{}
		c.buckets[instanceID] = b
	}
	b.count++

	// timeNow (declared in handlers_tier1.go) is the package's injectable
	// clock so tests can pin the coalescing window deterministically.
	now := timeNow()
	if now.Sub(b.lastFlush) >= auditFlushInterval {
		n := b.count
		b.count = 0
		b.lastFlush = now
		return true, n
	}
	return false, 0
}
