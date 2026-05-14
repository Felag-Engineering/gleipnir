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
	// Not parallel — sibling tests in this file mutate the shared timeNow clock.
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
// Runs under a frozen clock so the limiter cannot refill mid-loop — the split
// is therefore exactly burst-allowed and (total - burst)-dropped.
func TestEventRateLimiter_AboveBurstDrops(t *testing.T) {
	// Not parallel — mutates the package-level timeNow clock.
	origTimeNow := timeNow
	t.Cleanup(func() { timeNow = origTimeNow })
	timeNow = func() time.Time { return time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC) }

	rl := newEventRateLimiter()
	const (
		pluginID   = "plug-above"
		instanceID = "iid-above"
		total      = 250
		wantDrops  = total - defaultEventsBurst
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

	if drops != wantDrops {
		t.Errorf("drops = %d, want %d", drops, wantDrops)
	}

	after := snapshotDropCounter(pluginID, instanceID)
	if diff := after - before; diff != float64(drops) {
		t.Errorf("counter delta = %.0f, want %d", diff, drops)
	}
}

// TestEventRateLimiter_PerInstanceIsolation verifies that exhausting instance A's
// token bucket does not affect instance B.
func TestEventRateLimiter_PerInstanceIsolation(t *testing.T) {
	// Not parallel — sibling tests in this file mutate the shared timeNow clock.
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

	// drainBurst empties the limiter's bucket at the current fake clock so the
	// next Allow call is guaranteed to drop. This is necessary because advancing
	// the fake clock refills the rate.Limiter just like it would in production —
	// to test audit coalescing in isolation, we drain first then probe.
	drainBurst := func() {
		for range defaultEventsBurst {
			rl.Allow(pluginID, instanceID)
		}
	}

	// Advance 30 seconds — still within the 60-second audit window. The
	// limiter also refilled to its full burst during this jump, so drain first.
	fakeNow = fakeNow.Add(30 * time.Second)
	drainBurst()

	t.Run("WithinWindowNoFlush", func(t *testing.T) {
		allowed, flush := rl.Allow(pluginID, instanceID)
		if allowed {
			t.Fatal("expected drop after drain, got allowed=true")
		}
		if flush != 0 {
			t.Errorf("within 30s window: expected flushCount=0, got %d", flush)
		}
	})

	// Advance past the full minute window from the first flush.
	// Total: 61s since first flush at 12:00:00.
	fakeNow = fakeNow.Add(31 * time.Second)
	drainBurst()

	t.Run("AfterWindowFlushesAccumulated", func(t *testing.T) {
		allowed, flush := rl.Allow(pluginID, instanceID)
		if allowed {
			t.Fatal("expected drop after drain, got allowed=true")
		}
		if flush == 0 {
			t.Error("expected a non-zero flushCount after the 60s window elapsed, got 0")
		}
	})

	t.Run("SubsequentDropsSameWindowNoFlush", func(t *testing.T) {
		// Bucket is already drained from the previous subtest's setup; the
		// limiter has had no clock advance, so the next call still drops.
		allowed, flush := rl.Allow(pluginID, instanceID)
		if allowed {
			t.Fatal("expected drop in same window, got allowed=true")
		}
		if flush != 0 {
			t.Errorf("drop after flush: expected flushCount=0 in new window, got %d", flush)
		}
	})
}
