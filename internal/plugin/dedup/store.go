package dedup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// dedupTTL is the fixed rolling-window duration for event dedup. It matches
// the 1-hour window advertised in the proto contract (spec §4.3). Only the
// sweep cadence is configurable via GLEIPNIR_PLUGIN_DEDUP_SWEEP_INTERVAL.
const dedupTTL = time.Hour

// package-level Prometheus collectors registered once at import time via
// promauto.With(metrics.Registry()). This mirrors internal/plugin/hostsvc/
// event_ratelimit.go:17 and ensures tests cannot trigger double-registration
// panics even when multiple test binaries share the registry. The `plugin`
// label is omitted: Key carries only InstanceID (not PluginID), so we cannot
// include it without an extra DB round-trip per event — bounded cardinality
// is ensured by the per-instance/event_kind labels.
var (
	dedupDuplicates = promauto.With(metrics.Registry()).NewCounterVec(
		prometheus.CounterOpts{
			Name: "gleipnir_plugin_event_dedup_duplicates_total",
			Help: "Events dropped by the rolling-window dedup store (already seen).",
		},
		[]string{metrics.LabelInstance, "event_kind"},
	)

	dedupSweepDeleted = promauto.With(metrics.Registry()).NewCounter(
		prometheus.CounterOpts{
			Name: "gleipnir_plugin_event_dedup_sweep_deleted_total",
			Help: "Total dedup rows evicted by the background sweeper.",
		},
	)
)

// Querier is the narrow DB interface used by dbStore and Sweeper. Using an
// interface (not *db.Queries directly) keeps both types testable with fakes.
type Querier interface {
	RecordEventIfNovel(ctx context.Context, arg db.RecordEventIfNovelParams) (int64, error)
	SweepEventDedup(ctx context.Context, floor int64) (int64, error)
	DeleteEventDedup(ctx context.Context, arg db.DeleteEventDedupParams) error
}

// dbStore is a SQLite-backed Store implementation.
type dbStore struct {
	q          Querier
	clock      func() time.Time
	duplicates *prometheus.CounterVec
}

// NewDBStore returns a Store backed by SQLite. The clock defaults to
// time.Now; inject a fake via dbStore.clock for tests.
func NewDBStore(q Querier) *dbStore {
	return &dbStore{
		q:          q,
		clock:      time.Now,
		duplicates: dedupDuplicates,
	}
}

// SetClockForTest replaces the clock on this store. Tests that call this must
// not use t.Parallel() because the clock is a shared mutable field.
func (s *dbStore) SetClockForTest(fn func() time.Time) {
	s.clock = fn
}

// Seen atomically records the key and reports whether it was already present.
// Returns (false, nil) on a novel event, (true, nil) on a duplicate, or
// (false, err) when the store is unavailable — fail-open so a degraded store
// never silently drops events.
func (s *dbStore) Seen(ctx context.Context, k Key) (bool, error) {
	rows, err := s.q.RecordEventIfNovel(ctx, db.RecordEventIfNovelParams{
		PluginInstanceID: k.InstanceID,
		EventKind:        k.EventKind,
		EventID:          k.EventID,
		CreatedAtMs:      s.clock().UTC().UnixMilli(),
	})
	if err != nil {
		// Return (false, err) — caller (dispatcher) treats this as a miss and
		// proceeds with dispatch (fail-open contract).
		return false, fmt.Errorf("dedup record: %w", err)
	}
	if rows == 0 {
		// rows_affected == 0 means the INSERT hit ON CONFLICT — key already present.
		s.duplicates.WithLabelValues(k.InstanceID, k.EventKind).Inc()
		return true, nil
	}
	return false, nil
}

// Unsee deletes the dedup row for k, rolling back a claim taken by a prior Seen
// that returned (false, nil). It is idempotent: a DELETE affecting zero rows is
// not an error (the row may have already aged out at the TTL, or a concurrent
// sweep may have evicted it). See the Store.Unsee contract (#585).
func (s *dbStore) Unsee(ctx context.Context, k Key) error {
	if err := s.q.DeleteEventDedup(ctx, db.DeleteEventDedupParams{
		PluginInstanceID: k.InstanceID,
		EventKind:        k.EventKind,
		EventID:          k.EventID,
	}); err != nil {
		return fmt.Errorf("dedup unsee: %w", err)
	}
	return nil
}

// Sweeper runs a background loop that evicts dedup rows older than ttl by
// comparing their host-assigned created_at_ms against the current clock.
type Sweeper struct {
	q        Querier
	clock    func() time.Time
	interval time.Duration
	ttl      time.Duration
	deleted  prometheus.Counter
	log      *slog.Logger
}

// NewSweeper constructs a Sweeper. interval is the cadence of sweep ticks;
// ttl is the rolling-window duration (always dedupTTL in production).
func NewSweeper(q Querier, interval, ttl time.Duration) *Sweeper {
	return &Sweeper{
		q:        q,
		clock:    time.Now,
		interval: interval,
		ttl:      ttl,
		deleted:  dedupSweepDeleted,
		log:      slog.Default(),
	}
}

// SetClockForTest replaces the clock on this sweeper. Tests that call this
// must not use t.Parallel() because the clock is a shared mutable field.
func (sw *Sweeper) SetClockForTest(fn func() time.Time) {
	sw.clock = fn
}

// SweepForTest exposes the internal sweep method for unit tests so they can
// trigger a sweep without waiting for a ticker.
func (sw *Sweeper) SweepForTest(ctx context.Context) (int64, error) {
	return sw.sweep(ctx)
}

// floor returns the Unix-millisecond cutoff below which rows are eligible for
// eviction. Rows with created_at_ms < floor() have aged past the TTL window.
func (sw *Sweeper) floor() int64 {
	return sw.clock().Add(-sw.ttl).UTC().UnixMilli()
}

// sweep deletes rows older than the TTL window and returns the number of
// rows evicted. The count is logged at Info only when rows were deleted so
// steady-state logs stay quiet.
func (sw *Sweeper) sweep(ctx context.Context) (int64, error) {
	n, err := sw.q.SweepEventDedup(ctx, sw.floor())
	if err != nil {
		return 0, fmt.Errorf("dedup sweep: %w", err)
	}
	if n > 0 {
		sw.deleted.Add(float64(n))
		sw.log.InfoContext(ctx, "dedup sweeper evicted rows", "count", n)
	}
	return n, nil
}

// Start runs the sweep loop until ctx is cancelled. Call as a goroutine.
// Mirrors the oauth DBNonceStore.StartJanitor pattern.
func (sw *Sweeper) Start(ctx context.Context) {
	ticker := time.NewTicker(sw.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := sw.sweep(ctx); err != nil {
				sw.log.WarnContext(ctx, "dedup sweep error", "err", err)
			}
		}
	}
}
