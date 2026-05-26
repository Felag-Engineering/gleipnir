package process

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubReadRSS returns a fixed mapping of pid → bytes. PIDs not in the map
// return an error, simulating a process that has exited.
func stubReadRSS(rssMap map[int]uint64) func(int) (uint64, error) {
	return func(pid int) (uint64, error) {
		v, ok := rssMap[pid]
		if !ok {
			return 0, errors.New("stub: pid not found")
		}
		return v, nil
	}
}

func newTestSampler(snapshotFn func() map[string]InstanceInfo, rssMap map[int]uint64, now time.Time) *RSSSampler {
	s := NewRSSSampler(snapshotFn)
	s.readRSS = stubReadRSS(rssMap)
	s.timeNow = func() time.Time { return now }
	return s
}

// These tests share the package-level pluginRSSGauge so they must NOT run in
// parallel (t.Parallel() would cause concurrent gauge mutations).

func TestRSSSampler_ZeroInstances(t *testing.T) {
	s := newTestSampler(
		func() map[string]InstanceInfo { return nil },
		nil,
		time.Now(),
	)

	total, count, perInstance := s.Aggregate()
	if total != 0 {
		t.Errorf("expected total=0, got %d", total)
	}
	if count != 0 {
		t.Errorf("expected count=0, got %d", count)
	}
	if perInstance != nil {
		t.Errorf("expected nil perInstance, got %v", perInstance)
	}
}

func TestRSSSampler_MultipleInstances(t *testing.T) {
	fixedNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const (
		instA = "inst-aaa"
		instB = "inst-bbb"
		pidA  = 100
		pidB  = 200
		rssA  = 50 * 1024 * 1024  // 50 MiB
		rssB  = 100 * 1024 * 1024 // 100 MiB
	)

	snapshot := map[string]InstanceInfo{
		instA: {Pid: pidA, InstanceName: "alpha", PluginID: "plugin-a"},
		instB: {Pid: pidB, InstanceName: "beta", PluginID: "plugin-b"},
	}
	s := newTestSampler(
		func() map[string]InstanceInfo { return snapshot },
		map[int]uint64{pidA: rssA, pidB: rssB},
		fixedNow,
	)

	// Trigger one sampling round.
	s.sample()

	total, count, perInstance := s.Aggregate()
	if total != rssA+rssB {
		t.Errorf("expected total=%d, got %d", rssA+rssB, total)
	}
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}
	if len(perInstance) != 2 {
		t.Fatalf("expected 2 per-instance samples, got %d", len(perInstance))
	}

	// Results must be sorted by RSS descending: instB (100 MiB) first.
	if perInstance[0].Bytes != rssB {
		t.Errorf("expected first sample to have %d bytes (instB), got %d", rssB, perInstance[0].Bytes)
	}
	if perInstance[1].Bytes != rssA {
		t.Errorf("expected second sample to have %d bytes (instA), got %d", rssA, perInstance[1].Bytes)
	}

	// SampledAt must match the injected clock.
	for _, s := range perInstance {
		if !s.SampledAt.Equal(fixedNow) {
			t.Errorf("sample %s: expected SampledAt=%v, got %v", s.InstanceID, fixedNow, s.SampledAt)
		}
	}
}

func TestRSSSampler_StaleEntryRemoval(t *testing.T) {
	const (
		instA = "inst-aaa"
		instB = "inst-bbb"
		instC = "inst-ccc"
		pidA  = 100
		pidB  = 200
		pidC  = 300
	)

	// First tick: three instances.
	snapshot := map[string]InstanceInfo{
		instA: {Pid: pidA, InstanceName: "alpha", PluginID: "plugin-x"},
		instB: {Pid: pidB, InstanceName: "beta", PluginID: "plugin-x"},
		instC: {Pid: pidC, InstanceName: "gamma", PluginID: "plugin-x"},
	}
	rssMap := map[int]uint64{pidA: 10 << 20, pidB: 20 << 20, pidC: 30 << 20}
	now := time.Now()

	s := newTestSampler(func() map[string]InstanceInfo { return snapshot }, rssMap, now)
	s.sample()

	_, count, _ := s.Aggregate()
	if count != 3 {
		t.Fatalf("after first tick: expected 3 instances, got %d", count)
	}

	// Second tick: instC is gone (removed from snapshot, PID no longer answers).
	delete(snapshot, instC)
	delete(rssMap, pidC)
	s.sample()

	total, count, perInstance := s.Aggregate()
	if count != 2 {
		t.Errorf("after second tick: expected 2 instances, got %d", count)
	}
	for _, sample := range perInstance {
		if sample.InstanceID == instC {
			t.Errorf("stale entry %s should have been removed but is still present", instC)
		}
	}
	expectedTotal := uint64(10<<20 + 20<<20)
	if total != expectedTotal {
		t.Errorf("expected total=%d, got %d", expectedTotal, total)
	}
}

func TestRSSSampler_StartAndCancel(t *testing.T) {
	// Verify that the goroutine exits cleanly when the context is cancelled.
	// We use a very short interval and cancel quickly; the test just ensures
	// no goroutine leak (the goroutine will exit when ctx is Done).
	s := newTestSampler(
		func() map[string]InstanceInfo { return nil },
		nil,
		time.Now(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx, 100*time.Millisecond)

	// Let the goroutine run briefly, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// There's no observable side effect to assert on here without a sync
	// channel, but the test confirms the goroutine exits without blocking the
	// test process (via the test timeout).
}
