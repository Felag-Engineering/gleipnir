package resources

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// defaultInterval matches the v1.1 RSS sampler's cadence. Keeping it means the
// number an operator watches does not change its refresh rate at the cutover,
// only where it comes from.
const defaultInterval = 30 * time.Second

// StatsSource is the narrow runtime slice this collector needs.
type StatsSource interface {
	Stats(ctx context.Context, id container.ContainerID) (container.ContainerStats, error)
}

// Target is one container to sample.
type Target struct {
	InstanceID   string
	InstanceName string
	PluginID     string
	ContainerID  container.ContainerID
}

// TargetLister supplies the containers to sample each tick. Re-read every pass
// rather than cached, for the same reason the reconciler re-lists: instances
// come and go while the loop runs, and a cached list keeps sampling something
// that no longer exists.
type TargetLister interface {
	StatsTargets(ctx context.Context) ([]Target, error)
}

// Sample is one instance's reading, kept in memory for the admin aggregate.
type Sample struct {
	Target
	container.ContainerStats
	At time.Time
}

// timeNow is the package's injectable clock (CLAUDE.md "Testing time-dependent
// code"). Tests swap it via t.Cleanup and must not call t.Parallel.
var timeNow = func() time.Time { return time.Now() }

// Collector samples container stats and feeds both the Prometheus registry and
// the admin aggregate that replaces the RSS endpoint's numbers for
// containerized instances.
type Collector struct {
	source   StatsSource
	targets  TargetLister
	interval time.Duration

	mu      sync.RWMutex
	samples map[string]Sample

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

type CollectorConfig struct {
	Source   StatsSource
	Targets  TargetLister
	Interval time.Duration
}

func NewCollector(cfg CollectorConfig) (*Collector, error) {
	if cfg.Source == nil {
		return nil, errRequired("Source")
	}
	if cfg.Targets == nil {
		return nil, errRequired("Targets")
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Collector{
		source:   cfg.Source,
		targets:  cfg.Targets,
		interval: interval,
		samples:  make(map[string]Sample),
	}, nil
}

// SampleOnce takes one reading of every target.
//
// A per-container failure is skipped rather than fatal: an instance that is
// mid-restart has no stats to give, and one unreadable container must not stop
// the host learning about the others.
func (c *Collector) SampleOnce(ctx context.Context) error {
	targets, err := c.targets.StatsTargets(ctx)
	if err != nil {
		return err
	}

	live := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		stats, err := c.source.Stats(ctx, target.ContainerID)
		if err != nil {
			slog.DebugContext(ctx, "container stats unavailable",
				"instance_id", target.InstanceID, "err", err)
			continue
		}
		live[target.InstanceID] = struct{}{}

		c.mu.Lock()
		c.samples[target.InstanceID] = Sample{Target: target, ContainerStats: stats, At: timeNow()}
		c.mu.Unlock()

		memoryUsage.WithLabelValues(target.PluginID, target.InstanceName).Set(float64(stats.MemoryUsageBytes))
		memoryLimit.WithLabelValues(target.PluginID, target.InstanceName).Set(float64(stats.MemoryLimitBytes))
		cpuPercent.WithLabelValues(target.PluginID, target.InstanceName).Set(stats.CPUPercent)
	}

	// Drop instances that are gone. A stale gauge is worse than a missing one:
	// it reads as a running container using memory, which is exactly the thing
	// an operator would act on.
	c.mu.Lock()
	for id, sample := range c.samples {
		if _, ok := live[id]; ok {
			continue
		}
		delete(c.samples, id)
		memoryUsage.DeleteLabelValues(sample.PluginID, sample.InstanceName)
		memoryLimit.DeleteLabelValues(sample.PluginID, sample.InstanceName)
		cpuPercent.DeleteLabelValues(sample.PluginID, sample.InstanceName)
	}
	c.mu.Unlock()
	return nil
}

// Samples returns the most recent reading per instance.
func (c *Collector) Samples() []Sample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Sample, 0, len(c.samples))
	for _, s := range c.samples {
		out = append(out, s)
	}
	return out
}

// TotalMemoryBytes is the aggregate the admin header shows, replacing the RSS
// sum for containerized instances.
func (c *Collector) TotalMemoryBytes() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var total uint64
	for _, s := range c.samples {
		total += s.MemoryUsageBytes
	}
	return total
}

// Start runs a synchronous first sample and then the periodic loop, mirroring
// the reconciler so a caller can treat "Start returned" as "there is data".
func (c *Collector) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	if err := c.SampleOnce(runCtx); err != nil {
		cancel()
		return err
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := c.SampleOnce(runCtx); err != nil {
					slog.WarnContext(runCtx, "container stats pass failed", "err", err)
				}
			}
		}
	}()
	return nil
}

func (c *Collector) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *Collector) Wait() { c.wg.Wait() }
