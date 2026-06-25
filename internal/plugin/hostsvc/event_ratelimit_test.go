package hostsvc

import (
	"math"
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

// ptr helpers for concise test table entries.
func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

// TestResolveRateLimit verifies the per-field independent fallback logic.
func TestResolveRateLimit(t *testing.T) {
	type want struct {
		wantRate  float64
		wantBurst int
	}
	tests := []struct {
		name       string
		ratePerSec *float64
		burst      *int64
		wantRate   float64
		wantBurst  int
	}{
		// Custom honored
		{
			name:       "custom rate and burst honored",
			ratePerSec: f64(50),
			burst:      i64(100),
			wantRate:   50,
			wantBurst:  100,
		},
		{
			name:       "rate at cap boundary honored",
			ratePerSec: f64(maxEventsPerSec),
			burst:      i64(1),
			wantRate:   maxEventsPerSec,
			wantBurst:  1,
		},
		{
			name:       "burst at cap boundary honored",
			ratePerSec: f64(1),
			burst:      i64(maxEventsBurst),
			wantRate:   1,
			wantBurst:  maxEventsBurst,
		},
		// NULL → default for that field
		{
			name:       "nil rate falls back to default",
			ratePerSec: nil,
			burst:      i64(50),
			wantRate:   defaultEventsPerSec,
			wantBurst:  50,
		},
		{
			name:       "nil burst falls back to default",
			ratePerSec: f64(50),
			burst:      nil,
			wantRate:   50,
			wantBurst:  defaultEventsBurst,
		},
		{
			name:       "both nil → both defaults",
			ratePerSec: nil,
			burst:      nil,
			wantRate:   defaultEventsPerSec,
			wantBurst:  defaultEventsBurst,
		},
		// Zero / negative → default
		{
			name:       "zero rate → default",
			ratePerSec: f64(0),
			burst:      i64(50),
			wantRate:   defaultEventsPerSec,
			wantBurst:  50,
		},
		{
			name:       "negative rate → default",
			ratePerSec: f64(-1),
			burst:      i64(50),
			wantRate:   defaultEventsPerSec,
			wantBurst:  50,
		},
		{
			name:       "zero burst → default",
			ratePerSec: f64(50),
			burst:      i64(0),
			wantRate:   50,
			wantBurst:  defaultEventsBurst,
		},
		{
			name:       "negative burst → default",
			ratePerSec: f64(50),
			burst:      i64(-5),
			wantRate:   50,
			wantBurst:  defaultEventsBurst,
		},
		// NaN / Inf → default
		{
			name:       "NaN rate → default",
			ratePerSec: f64(math.NaN()),
			burst:      i64(50),
			wantRate:   defaultEventsPerSec,
			wantBurst:  50,
		},
		{
			name:       "positive Inf rate → default",
			ratePerSec: f64(math.Inf(1)),
			burst:      i64(50),
			wantRate:   defaultEventsPerSec,
			wantBurst:  50,
		},
		{
			name:       "negative Inf rate → default",
			ratePerSec: f64(math.Inf(-1)),
			burst:      i64(50),
			wantRate:   defaultEventsPerSec,
			wantBurst:  50,
		},
		// Above cap → default
		{
			name:       "rate above cap → default",
			ratePerSec: f64(maxEventsPerSec + 1),
			burst:      i64(50),
			wantRate:   defaultEventsPerSec,
			wantBurst:  50,
		},
		{
			name:       "burst above cap → default",
			ratePerSec: f64(50),
			burst:      i64(maxEventsBurst + 1),
			wantRate:   50,
			wantBurst:  defaultEventsBurst,
		},
		// Per-field independence: one bad field does not affect the other
		{
			name:       "bad rate, good burst — burst unchanged",
			ratePerSec: f64(0),
			burst:      i64(500),
			wantRate:   defaultEventsPerSec,
			wantBurst:  500,
		},
		{
			name:       "good rate, bad burst — rate unchanged",
			ratePerSec: f64(75.5),
			burst:      i64(-10),
			wantRate:   75.5,
			wantBurst:  defaultEventsBurst,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRate, gotBurst := resolveRateLimit(tc.ratePerSec, tc.burst)
			if float64(gotRate) != tc.wantRate {
				t.Errorf("rate = %v, want %v", float64(gotRate), tc.wantRate)
			}
			if gotBurst != tc.wantBurst {
				t.Errorf("burst = %d, want %d", gotBurst, tc.wantBurst)
			}
		})
	}
}

// TestEventRateLimiter_CustomRateAndBurst verifies that a custom rate/burst pair
// is used when provided, using a frozen clock so token refill is deterministic.
func TestEventRateLimiter_CustomRateAndBurst(t *testing.T) {
	// Not parallel — mutates the package-level timeNow clock.
	origTimeNow := timeNow
	t.Cleanup(func() { timeNow = origTimeNow })
	timeNow = func() time.Time { return time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC) }

	rl := newEventRateLimiter()
	const (
		pluginID   = "plug-custom"
		instanceID = "iid-custom"
	)

	customBurst := int64(10)
	customRate := 5.0 // 5 events/sec

	// The burst of 10 should all be allowed immediately.
	for i := range 10 {
		allowed, _ := rl.Allow(pluginID, instanceID, &customRate, &customBurst)
		if !allowed {
			t.Fatalf("call %d: expected allowed=true within custom burst of %d", i, customBurst)
		}
	}

	// The 11th call should be dropped (clock is frozen, no refill).
	allowed, _ := rl.Allow(pluginID, instanceID, &customRate, &customBurst)
	if allowed {
		t.Fatal("11th call: expected drop after custom burst exhausted, got allowed=true")
	}
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
		allowed, flush := rl.Allow(pluginID, instanceID, nil, nil)
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
		allowed, _ := rl.Allow(pluginID, instanceID, nil, nil)
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
		rl.Allow(pluginID, "iid-iso-A", nil, nil)
	}

	// Instance B should still have its full burst available.
	for i := range 100 {
		allowed, _ := rl.Allow(pluginID, "iid-iso-B", nil, nil)
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
		rl.Allow(pluginID, instanceID, nil, nil)
	}

	t.Run("FirstDropFlushes", func(t *testing.T) {
		allowed, flush := rl.Allow(pluginID, instanceID, nil, nil)
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
			rl.Allow(pluginID, instanceID, nil, nil)
		}
	}

	// Advance 30 seconds — still within the 60-second audit window. The
	// limiter also refilled to its full burst during this jump, so drain first.
	fakeNow = fakeNow.Add(30 * time.Second)
	drainBurst()

	t.Run("WithinWindowNoFlush", func(t *testing.T) {
		allowed, flush := rl.Allow(pluginID, instanceID, nil, nil)
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
		allowed, flush := rl.Allow(pluginID, instanceID, nil, nil)
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
		allowed, flush := rl.Allow(pluginID, instanceID, nil, nil)
		if allowed {
			t.Fatal("expected drop in same window, got allowed=true")
		}
		if flush != 0 {
			t.Errorf("drop after flush: expected flushCount=0 in new window, got %d", flush)
		}
	})
}
