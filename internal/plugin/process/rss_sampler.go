package process

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// pluginRSSGauge is a Prometheus gauge that tracks each plugin subprocess's
// resident set size in bytes. Registered at package init via promauto so there
// is exactly one registration regardless of how many RSSSampler instances are
// constructed — double registration would panic.
var pluginRSSGauge = promauto.With(metrics.Registry()).NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gleipnir_plugin_process_rss_bytes",
		Help: "Resident set size of plugin subprocesses in bytes, sampled every 30s.",
	},
	[]string{metrics.LabelPlugin, metrics.LabelInstance},
)

// RSSSample holds one RSS reading for a single plugin instance.
type RSSSample struct {
	InstanceID   string
	InstanceName string
	PluginID     string
	Bytes        uint64
	SampledAt    time.Time
}

// RSSSampler samples /proc/<pid>/statm for every running plugin subprocess on
// a fixed interval, stores the latest reading per instance, and exposes an
// Aggregate() method for the admin REST endpoint.
//
// Construct with NewRSSSampler and start the background goroutine with Start.
type RSSSampler struct {
	// snapshotFn returns the current set of running instances. Called on every
	// tick so the sampler always works with the live process map.
	snapshotFn func() map[string]InstanceInfo

	mu      sync.RWMutex
	samples map[string]RSSSample // keyed by instance ID

	// readRSS is injectable so tests can run platform-independently without
	// spawning real processes or requiring Linux /proc.
	readRSS func(int) (uint64, error)

	// timeNow is injectable per codebase convention so tests can set a fixed
	// timestamp and assert on SampledAt without wall-clock races.
	timeNow func() time.Time
}

// NewRSSSampler constructs a sampler that calls snapshotFn on every tick to
// discover running instances. The caller must call Start to begin sampling.
func NewRSSSampler(snapshotFn func() map[string]InstanceInfo) *RSSSampler {
	return &RSSSampler{
		snapshotFn: snapshotFn,
		samples:    make(map[string]RSSSample),
		readRSS:    ReadRSS,
		timeNow:    time.Now,
	}
}

// Start launches the background sampling goroutine. It ticks every interval,
// reads RSS for each running instance, updates the sample map and Prometheus
// gauge, and removes stale entries (instances that are no longer in the
// snapshot). The goroutine exits when ctx is cancelled.
//
// Callers should pass a context that is cancelled on server shutdown. The
// ticker is stopped on cancellation to prevent resource leaks.
func (s *RSSSampler) Start(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sample()
			}
		}
	}()
}

// sample performs one sampling round: reads RSS for every instance in the
// snapshot, updates stored samples and Prometheus gauges, and removes entries
// for instances that are no longer running.
func (s *RSSSampler) sample() {
	snapshot := s.snapshotFn()
	now := s.timeNow()

	newSamples := make(map[string]RSSSample, len(snapshot))
	for instanceID, info := range snapshot {
		bytes, err := s.readRSS(info.Pid)
		if err != nil {
			// Process may have exited between the snapshot and the read. Skip
			// silently — the entry will be absent from newSamples and pruned below.
			continue
		}
		newSamples[instanceID] = RSSSample{
			InstanceID:   instanceID,
			InstanceName: info.InstanceName,
			PluginID:     info.PluginID,
			Bytes:        bytes,
			SampledAt:    now,
		}
		pluginRSSGauge.With(prometheus.Labels{
			metrics.LabelPlugin:   info.PluginID,
			metrics.LabelInstance: info.InstanceName,
		}).Set(float64(bytes))
	}

	s.mu.Lock()
	// Remove gauge entries for instances no longer in the snapshot.
	for instanceID, old := range s.samples {
		if _, stillRunning := newSamples[instanceID]; !stillRunning {
			pluginRSSGauge.DeleteLabelValues(old.PluginID, old.InstanceName)
		}
	}
	s.samples = newSamples
	s.mu.Unlock()
}

// Aggregate returns the sum of all sampled RSS values, the number of sampled
// instances, and a copy of the per-instance samples sorted by RSS descending.
//
// When no instances are running the return values are (0, 0, nil).
func (s *RSSSampler) Aggregate() (totalBytes uint64, count int, perInstance []RSSSample) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.samples) == 0 {
		return 0, 0, nil
	}

	out := make([]RSSSample, 0, len(s.samples))
	for _, sample := range s.samples {
		totalBytes += sample.Bytes
		out = append(out, sample)
	}

	// Sort descending by RSS so the largest consumers appear first.
	sort.Slice(out, func(i, j int) bool {
		return out[i].Bytes > out[j].Bytes
	})

	return totalBytes, len(out), out
}
