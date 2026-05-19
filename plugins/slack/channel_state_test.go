package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestCorrelationMap_PutTake asserts that put stores a correlation and take
// retrieves and deletes it atomically.
func TestCorrelationMap_PutTake(t *testing.T) {
	m := &correlationMap{
		data:   make(map[string]correlation),
		stopCh: make(chan struct{}),
	}

	c := correlation{
		channel: "C01CHANNEL",
		ts:      "1700000000.000001",
		runID:   "run-1",
		addedAt: time.Now(),
	}
	m.put("req-1", c)

	got, ok := m.take("req-1")
	if !ok {
		t.Fatal("take: expected ok=true, got false")
	}
	if got.channel != c.channel {
		t.Errorf("channel: want %q, got %q", c.channel, got.channel)
	}
	if got.ts != c.ts {
		t.Errorf("ts: want %q, got %q", c.ts, got.ts)
	}

	// Second take must return ok=false (atomically deleted).
	_, ok = m.take("req-1")
	if ok {
		t.Error("second take: expected ok=false (already deleted), got true")
	}

	// Take on unknown ID returns false.
	_, ok = m.take("does-not-exist")
	if ok {
		t.Error("take unknown: expected ok=false, got true")
	}
}

// TestCorrelationMap_Sweep asserts that sweep evicts entries older than ttl and
// leaves newer ones intact.
func TestCorrelationMap_Sweep(t *testing.T) {
	m := &correlationMap{
		data:   make(map[string]correlation),
		stopCh: make(chan struct{}),
	}

	now := time.Now()
	ttl := 10 * time.Second

	// Old entry (should be evicted).
	m.put("old", correlation{addedAt: now.Add(-15 * time.Second)})
	// New entry (should survive).
	m.put("new", correlation{addedAt: now.Add(-5 * time.Second)})

	evicted := m.sweep(now, ttl)
	if evicted != 1 {
		t.Errorf("sweep: want 1 evicted, got %d", evicted)
	}

	_, ok := m.take("old")
	if ok {
		t.Error("old entry: expected evicted (ok=false), still present")
	}
	_, ok = m.take("new")
	if !ok {
		t.Error("new entry: expected present (ok=true), was evicted")
	}
}

// TestCorrelationMap_Concurrent exercises put/take from 1000 goroutines to
// detect data races under -race.
func TestCorrelationMap_Concurrent(t *testing.T) {
	m := &correlationMap{
		data:   make(map[string]correlation),
		stopCh: make(chan struct{}),
	}

	const goroutines = 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("req-%d", i)
			m.put(key, correlation{addedAt: time.Now()})
			m.take(key)
		}()
	}

	wg.Wait()
	// If we get here without data-race detector firing, the test passes.
}
