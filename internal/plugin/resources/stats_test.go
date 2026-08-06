package resources

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// --- fixtures ---------------------------------------------------------------

type fakeStats struct {
	mu      sync.Mutex
	byID    map[container.ContainerID]container.ContainerStats
	errs    map[container.ContainerID]error
	queries int
}

func (f *fakeStats) Stats(_ context.Context, id container.ContainerID) (container.ContainerStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	if err, ok := f.errs[id]; ok {
		return container.ContainerStats{}, err
	}
	return f.byID[id], nil
}

type fakeTargets struct {
	mu      sync.Mutex
	targets []Target
	err     error
}

func (f *fakeTargets) StatsTargets(context.Context) ([]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.targets, f.err
}

func (f *fakeTargets) set(targets ...Target) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = targets
}

func target(id, name string) Target {
	return Target{InstanceID: id, InstanceName: name, PluginID: "plug-1", ContainerID: container.ContainerID("c-" + id)}
}

// --- sampling ---------------------------------------------------------------

func TestCollector_SamplesEveryTarget(t *testing.T) {
	source := &fakeStats{byID: map[container.ContainerID]container.ContainerStats{
		"c-inst-1": {MemoryUsageBytes: 100 << 20, MemoryLimitBytes: 256 << 20, CPUPercent: 12.5},
		"c-inst-2": {MemoryUsageBytes: 50 << 20, MemoryLimitBytes: 256 << 20, CPUPercent: 3},
	}}
	targets := &fakeTargets{}
	targets.set(target("inst-1", "alpha"), target("inst-2", "beta"))

	c, err := NewCollector(CollectorConfig{Source: source, Targets: targets})
	if err != nil {
		t.Fatalf("NewCollector: %v", err)
	}
	if err := c.SampleOnce(context.Background()); err != nil {
		t.Fatalf("SampleOnce: %v", err)
	}

	if got := len(c.Samples()); got != 2 {
		t.Fatalf("samples = %d, want 2", got)
	}
	// This aggregate is what the admin header shows, replacing the RSS sum for
	// containerized instances.
	if got := c.TotalMemoryBytes(); got != 150<<20 {
		t.Errorf("total = %d, want %d", got, 150<<20)
	}
}

// One unreadable container must not stop the host learning about the others —
// an instance mid-restart simply has no stats to give.
func TestCollector_PerContainerFailureIsSkipped(t *testing.T) {
	source := &fakeStats{
		byID: map[container.ContainerID]container.ContainerStats{"c-inst-2": {MemoryUsageBytes: 42}},
		errs: map[container.ContainerID]error{"c-inst-1": errors.New("no such container")},
	}
	targets := &fakeTargets{}
	targets.set(target("inst-1", "alpha"), target("inst-2", "beta"))

	c, _ := NewCollector(CollectorConfig{Source: source, Targets: targets})
	if err := c.SampleOnce(context.Background()); err != nil {
		t.Fatalf("SampleOnce: %v", err)
	}

	samples := c.Samples()
	if len(samples) != 1 || samples[0].InstanceID != "inst-2" {
		t.Errorf("samples = %+v, want only the readable instance", samples)
	}
}

// A stale gauge is worse than a missing one: it reads as a running container
// using memory, which is exactly the thing an operator would act on.
func TestCollector_DropsInstancesThatAreGone(t *testing.T) {
	source := &fakeStats{byID: map[container.ContainerID]container.ContainerStats{
		"c-inst-1": {MemoryUsageBytes: 10},
		"c-inst-2": {MemoryUsageBytes: 20},
	}}
	targets := &fakeTargets{}
	targets.set(target("inst-1", "alpha"), target("inst-2", "beta"))

	c, _ := NewCollector(CollectorConfig{Source: source, Targets: targets})
	if err := c.SampleOnce(context.Background()); err != nil {
		t.Fatalf("SampleOnce: %v", err)
	}
	if len(c.Samples()) != 2 {
		t.Fatalf("setup: want 2 samples")
	}

	targets.set(target("inst-1", "alpha"))
	if err := c.SampleOnce(context.Background()); err != nil {
		t.Fatalf("SampleOnce: %v", err)
	}

	samples := c.Samples()
	if len(samples) != 1 || samples[0].InstanceID != "inst-1" {
		t.Errorf("samples = %+v, want only the surviving instance", samples)
	}
	if got := c.TotalMemoryBytes(); got != 10 {
		t.Errorf("total = %d, want only the surviving instance's 10", got)
	}
}

// The target list is re-read every pass rather than cached, for the same reason
// the reconciler re-lists: instances come and go while the loop runs.
func TestCollector_RereadsTargetsEachPass(t *testing.T) {
	source := &fakeStats{byID: map[container.ContainerID]container.ContainerStats{
		"c-inst-1": {}, "c-inst-2": {},
	}}
	targets := &fakeTargets{}
	targets.set(target("inst-1", "alpha"))

	c, _ := NewCollector(CollectorConfig{Source: source, Targets: targets})
	_ = c.SampleOnce(context.Background())

	targets.set(target("inst-1", "alpha"), target("inst-2", "beta"))
	_ = c.SampleOnce(context.Background())

	if got := len(c.Samples()); got != 2 {
		t.Errorf("samples = %d after the target list grew, want 2", got)
	}
}

func TestCollector_ListFailureIsReported(t *testing.T) {
	c, _ := NewCollector(CollectorConfig{
		Source:  &fakeStats{},
		Targets: &fakeTargets{err: errors.New("db down")},
	})
	if err := c.SampleOnce(context.Background()); err == nil {
		t.Error("SampleOnce succeeded with an unreadable target list")
	}
}

func TestCollector_SampleCarriesTheClock(t *testing.T) {
	frozen := time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return frozen }
	t.Cleanup(func() { timeNow = func() time.Time { return time.Now() } })

	targets := &fakeTargets{}
	targets.set(target("inst-1", "alpha"))
	c, _ := NewCollector(CollectorConfig{
		Source:  &fakeStats{byID: map[container.ContainerID]container.ContainerStats{"c-inst-1": {}}},
		Targets: targets,
	})
	_ = c.SampleOnce(context.Background())

	if got := c.Samples()[0].At; !got.Equal(frozen) {
		t.Errorf("sample time = %s, want %s", got, frozen)
	}
}

func TestNewCollector_RequiresItsDependencies(t *testing.T) {
	if _, err := NewCollector(CollectorConfig{Targets: &fakeTargets{}}); err == nil {
		t.Error("NewCollector accepted a config with no Source")
	}
	if _, err := NewCollector(CollectorConfig{Source: &fakeStats{}}); err == nil {
		t.Error("NewCollector accepted a config with no Targets")
	}
}

// --- OOM --------------------------------------------------------------------

type recordingReporter struct {
	faults []string
	audits []int64
}

func (r *recordingReporter) CapabilityFault(_ context.Context, _, detail string) {
	r.faults = append(r.faults, detail)
}

func (r *recordingReporter) Audited(_ context.Context, _ string, limitBytes int64) {
	r.audits = append(r.audits, limitBytes)
}

// An OOM kill is the cap doing its job, and both destinations matter: the
// health fault narrows routing away from an instance that cannot serve, the
// audit event is what an operator reads to find out why.
func TestObserveExit_OOMProducesBothFaultAndAudit(t *testing.T) {
	reporter := &recordingReporter{}
	info := container.ContainerInfo{ID: "c1", State: container.ContainerStateExited, OOMKilled: true, ExitCode: 137}

	if !ObserveExit(context.Background(), reporter, "inst-1", info, 256<<20) {
		t.Fatal("ObserveExit did not report an OOM kill")
	}
	if len(reporter.faults) != 1 || len(reporter.audits) != 1 {
		t.Fatalf("faults=%v audits=%v, want one of each", reporter.faults, reporter.audits)
	}
	// The detail names the limit because the fix is almost always "raise it",
	// and a detail saying only "out of memory" sends an operator to read the
	// plugin's code when the answer is a number in their own configuration.
	if !strings.Contains(reporter.faults[0], "256 MiB") {
		t.Errorf("detail = %q, want it to name the limit", reporter.faults[0])
	}
	if reporter.audits[0] != 256<<20 {
		t.Errorf("audited limit = %d", reporter.audits[0])
	}
}

// A plugin that crashes on its own is a different fault with a different fix.
// Conflating the two would send an operator to raise a memory limit that was
// never the problem.
func TestObserveExit_OrdinaryCrashIsNotAnOOM(t *testing.T) {
	reporter := &recordingReporter{}
	info := container.ContainerInfo{ID: "c1", State: container.ContainerStateExited, ExitCode: 1}

	if ObserveExit(context.Background(), reporter, "inst-1", info, 256<<20) {
		t.Error("a non-zero exit was reported as an OOM kill")
	}
	if len(reporter.faults) != 0 || len(reporter.audits) != 0 {
		t.Errorf("a plain crash produced faults=%v audits=%v", reporter.faults, reporter.audits)
	}
}

func TestObserveExit_NilReporterIsSafe(t *testing.T) {
	info := container.ContainerInfo{OOMKilled: true}
	if !ObserveExit(context.Background(), nil, "inst-1", info, 0) {
		t.Error("ObserveExit with a nil reporter did not report the kill")
	}
}

func TestOOMDetail_WithoutAKnownLimit(t *testing.T) {
	if got := OOMDetail(0); strings.Contains(got, "MiB") {
		t.Errorf("detail = %q, want no fabricated limit when none is known", got)
	}
}
