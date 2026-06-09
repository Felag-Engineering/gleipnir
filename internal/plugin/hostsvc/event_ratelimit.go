package hostsvc

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// eventDroppedCounter tracks events that were silently dropped at the host-side
// EmitEvent ingress because the per-instance rate limit was exceeded.
// Registered statically at package init — same pattern as internal/db/metrics.go.
var eventDroppedCounter = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_plugin_event_dropped_total",
		Help: "Events dropped at the host-side EmitEvent ingress.",
	},
	[]string{metrics.LabelPlugin, metrics.LabelInstance, metrics.LabelReason},
)

const (
	// defaultEventsPerSec is the sustained token-bucket fill rate per instance.
	// TODO(#222): read from plugin_instances.config_json once a stable
	// instance-config schema exists (spec §4.3 "configurable on instance").
	defaultEventsPerSec = 100.0

	// defaultEventsBurst is the maximum instantaneous burst allowed per instance.
	defaultEventsBurst = 200

	// auditFlushInterval is the minimum time between "event_rate_limited" audit
	// rows for the same instance. Coalescing prevents the audit log from being
	// drowned by per-event rows when a plugin misbehaves (spec §4.3 "periodic").
	auditFlushInterval = 1 * time.Minute
)

// instanceBucket holds the rate limiter and audit-coalescing state for a single
// plugin instance.
type instanceBucket struct {
	limiter   *rate.Limiter
	pluginID  string
	dropped   uint64    // drops accumulated since the last audit flush
	lastFlush time.Time // zero value on creation, so the first drop always flushes immediately
}

// eventRateLimiter enforces a per-instance token-bucket rate limit on incoming
// plugin events and coalesces audit events to at most one per minute per instance.
type eventRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*instanceBucket // keyed by instance ID
}

// newEventRateLimiter constructs an eventRateLimiter with no pre-allocated buckets.
// Buckets are created lazily on first event from each instance.
func newEventRateLimiter() *eventRateLimiter {
	return &eventRateLimiter{
		buckets: map[string]*instanceBucket{},
	}
}

// Allow checks whether the next event from (pluginID, instanceID) is within the
// rate limit.
//
// It returns:
//   - allowed=true when the event is within the limit.
//   - allowed=false, flushCount=0 when the event is dropped but the audit
//     coalescing window has not elapsed yet.
//   - allowed=false, flushCount>0 when the event is dropped AND the coalescing
//     window has elapsed; flushCount is the total number of drops accumulated
//     since the previous audit flush (the caller should write one audit row).
//
// The zero value of lastFlush means the very first drop always triggers a flush,
// giving operators an immediate signal the first time a plugin misbehaves instead
// of waiting up to 60 seconds.
func (rl *eventRateLimiter) Allow(pluginID, instanceID string) (allowed bool, flushCount uint64) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[instanceID]
	if !ok {
		b = &instanceBucket{
			limiter:  rate.NewLimiter(rate.Limit(defaultEventsPerSec), defaultEventsBurst),
			pluginID: pluginID,
		}
		rl.buckets[instanceID] = b
	}

	// AllowN with an explicit timestamp (rather than Allow(), which calls
	// time.Now() internally) so token refill follows the package's injectable
	// timeNow clock. Tests freeze timeNow to get deterministic drop counts.
	now := timeNow()
	if b.limiter.AllowN(now, 1) {
		return true, 0
	}

	// Event is over the limit — accumulate and check whether the flush window
	// has elapsed. Because lastFlush is the zero time.Time on a new bucket,
	// the very first drop satisfies this condition (now - zero >> 1 min).
	b.dropped++
	if now.Sub(b.lastFlush) >= auditFlushInterval {
		count := b.dropped
		b.dropped = 0
		b.lastFlush = now
		return false, count
	}

	return false, 0
}
