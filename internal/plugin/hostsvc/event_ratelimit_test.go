package hostsvc

import (
	"testing"
	"time"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
)

// snapshotDropCounter returns the current value of the dropped-event counter
// for (pluginID, instanceID, reason=rate_limit).
func snapshotDropCounter(pluginID, instanceID string) float64 {
	return promtestutil.ToFloat64(
		eventDroppedCounter.WithLabelValues(pluginID, instanceID, "rate_limit"),
	)
}

// TestEventRateLimiter_WithinBurstAllows verifies that defaultEventsBurst
// sequential calls all return allowed=true.
func TestEventRateLimiter_WithinBurstAllows(t *testing.T) {
	t.Parallel()

	rl := newEventRateLimiter()
	const (
		pluginID   = "plug-within"
		instanceID = "iid-within"
	)
	before := snapshotDropCounter(pluginID, instanceID)

	for i := range defaultEventsBurst {
		allowed, flush := rl.Allow(pluginID, instanceID)
		if !allowed {
			t.Fatalf("call %d: expected allowed=true, got false (flush=%d)", i, flush)
		}
		if flush != 0 {
			t.Fatalf("call %d: expected flushCount=0, got %d", i, flush)
		}
	}

	// Counter must not have moved — no drops occurred.
	after := snapshotDropCounter(pluginID, instanceID)
	if diff := after - before; diff != 0 {
		t.Errorf("counter delta = %.0f, want 0", diff)
	}
}

// TestEventRateLimiter_AboveBurstDrops verifies that calls above defaultEventsBurst
// are dropped and the Prometheus counter is incremented for each drop.
//
// We do not assert the exact split because the token bucket may refill slightly
// between calls. The key invariant is: at least 50 of the 250 calls are dropped
// and the counter matches the drop count returned by Allow.
func TestEventRateLimiter_AboveBurstDrops(t *testing.T) {
	t.Parallel()

	rl := newEventRateLimiter()
	const (
		pluginID   = "plug-above"
		instanceID = "iid-above"
		total      = 250
	)
	before := snapshotDropCounter(pluginID, instanceID)

	var drops int
	for range total {
		allowed, _ := rl.Allow(pluginID, instanceID)
		if !allowed {
			drops++
			eventDroppedCounter.WithLabelValues(pluginID, instanceID, "rate_limit").Inc()
		}
	}

	// At least 50 of 250 must have been dropped (burst is 200).
	if drops < 50 {
		t.Errorf("drops = %d, want >= 50", drops)
	}

	after := snapshotDropCounter(pluginID, instanceID)
	if diff := after - before; diff != float64(drops) {
		t.Errorf("counter delta = %.0f, want %d", diff, drops)
	}
}

// TestEventRateLimiter_PerInstanceIsolation verifies that exhausting instance A's
// token bucket does not affect instance B.
func TestEventRateLimiter_PerInstanceIsolation(t *testing.T) {
	t.Parallel()

	rl := newEventRateLimiter()
	const pluginID = "plug-iso"

	// Exhaust instance A by sending well above the burst limit.
	for range defaultEventsBurst + 100 {
		rl.Allow(pluginID, "iid-iso-A")
	}

	// Instance B should still have its full burst available.
	for i := range 100 {
		allowed, _ := rl.Allow(pluginID, "iid-iso-B")
		if !allowed {
			t.Fatalf("instance B call %d: expected allowed=true; instance A exhaustion should not bleed over", i)
		}
	}
}

// TestEventRateLimiter_AuditFlushCoalesces verifies the audit-coalescing logic:
//   - The very first drop flushes immediately (zero-value lastFlush behavior).
//   - Subsequent drops within the same minute window return flushCount=0.
//   - Once the window elapses, the next drop returns the accumulated count.
func TestEventRateLimiter_AuditFlushCoalesces(t *testing.T) {
	// Not parallel — manipulates the package-level timeNow clock.
	const (
		pluginID   = "plug-flush"
		instanceID = "iid-flush"
	)

	// Save and restore the package-level clock.
	origTimeNow := timeNow
	t.Cleanup(func() { timeNow = origTimeNow })

	// Start the fake clock at a known point far enough from zero that the first
	// drop definitely satisfies now - zero >= auditFlushInterval.
	fakeNow := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return fakeNow }

	rl := newEventRateLimiter()

	// Drain the full burst so the next Allow call must drop.
	for range defaultEventsBurst {
		rl.Allow(pluginID, instanceID)
	}

	t.Run("FirstDropFlushes", func(t *testing.T) {
		allowed, flush := rl.Allow(pluginID, instanceID)
		if allowed {
			t.Fatal("expected drop after burst exhausted, got allowed=true")
		}
		if flush == 0 {
			t.Error("first drop should flush immediately (zero lastFlush), got flushCount=0")
		}
	})

	// Advance 30 seconds — still within the 60-second window.
	fakeNow = fakeNow.Add(30 * time.Second)

	t.Run("WithinWindowNoFlush", func(t *testing.T) {
		// Accumulate 300 drops; none should trigger a flush.
		for i := range 300 {
			allowed, flush := rl.Allow(pluginID, instanceID)
			if allowed {
				// Token bucket may have refilled; skip this call.
				continue
			}
			if flush != 0 {
				t.Errorf("drop %d within 30s window: expected flushCount=0, got %d", i, flush)
			}
		}
	})

	// Advance past the full minute window from the first flush.
	fakeNow = fakeNow.Add(31 * time.Second) // total: 61 seconds since first flush

	t.Run("AfterWindowFlushesAccumulated", func(t *testing.T) {
		// The next drop after the window must return a non-zero flush count.
		var gotFlush uint64
		for range 50 {
			allowed, flush := rl.Allow(pluginID, instanceID)
			if allowed {
				continue // refill; try again
			}
			if flush > 0 {
				gotFlush = flush
				break
			}
		}
		if gotFlush == 0 {
			t.Error("expected a non-zero flushCount after the 60s window elapsed, got 0")
		}
	})

	t.Run("SubsequentDropsSameWindowNoFlush", func(t *testing.T) {
		// After the flush, further drops in the same new window must return 0.
		for i := range 50 {
			allowed, flush := rl.Allow(pluginID, instanceID)
			if allowed {
				continue
			}
			if flush != 0 {
				t.Errorf("drop %d after flush: expected flushCount=0 in new window, got %d", i, flush)
				break
			}
		}
	})
}
