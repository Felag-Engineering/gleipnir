package dedup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// insertPluginAndInstance seeds the DB with a minimal plugin + instance row
// so plugin_event_dedup foreign-key constraints are satisfied.
func insertPluginAndInstance(t *testing.T, store *db.Store, pluginID, instanceID string) {
	t.Helper()
	_, err := store.DB().Exec(
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES (?, ?, '1.0.0', '{}', 'pubkey', 'active', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		pluginID, "plugin-"+pluginID,
	)
	if err != nil {
		t.Fatalf("insert plugin %s: %v", pluginID, err)
	}
	_, err = store.DB().Exec(
		`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, handshake_versions, health_state, version, created_at, updated_at)
		 VALUES (?, ?, ?, '{}', '{}', 'healthy', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		instanceID, pluginID, "instance-"+instanceID,
	)
	if err != nil {
		t.Fatalf("insert instance %s: %v", instanceID, err)
	}
}

// ── dbStore tests ─────────────────────────────────────────────────────────────

// TestDBStore_FirstSeenFalse_SecondSeenTrue verifies the core atomic
// record-and-check contract: the first Seen for a key is a miss (false, nil),
// and the second for the same key is a hit (true, nil).
// Clock mutation: do NOT t.Parallel().
func TestDBStore_FirstSeenFalse_SecondSeenTrue(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-1", "inst-1")

	fixedTime := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	s := dedup.NewDBStore(store.Queries())
	s.SetClockForTest(func() time.Time { return fixedTime })

	ctx := context.Background()
	k := dedup.Key{InstanceID: "inst-1", EventKind: "message", EventID: "evt-abc"}

	seen, err := s.Seen(ctx, k)
	if err != nil {
		t.Fatalf("first Seen: unexpected error: %v", err)
	}
	if seen {
		t.Error("first Seen: got true, want false (novel event)")
	}

	seen, err = s.Seen(ctx, k)
	if err != nil {
		t.Fatalf("second Seen: unexpected error: %v", err)
	}
	if !seen {
		t.Error("second Seen: got false, want true (duplicate)")
	}
}

// TestDBStore_SweepEvictsOldRows verifies that the sweeper removes rows
// older than the TTL window and that post-eviction the key is treated as novel
// again (returning false). Uses an injected clock: "old" is now-2h, "fresh"
// is now-30m. After sweeping with a floor at now-1h only the old row is gone.
// Clock mutation: do NOT t.Parallel().
func TestDBStore_SweepEvictsOldRows(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-2", "inst-2")

	ctx := context.Background()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	oldKey := dedup.Key{InstanceID: "inst-2", EventKind: "message", EventID: "old-evt"}
	freshKey := dedup.Key{InstanceID: "inst-2", EventKind: "message", EventID: "fresh-evt"}

	// Insert the old event with a clock set to now-2h.
	sOld := dedup.NewDBStore(store.Queries())
	sOld.SetClockForTest(func() time.Time { return now.Add(-2 * time.Hour) })
	if seen, err := sOld.Seen(ctx, oldKey); err != nil || seen {
		t.Fatalf("insert old: seen=%v err=%v", seen, err)
	}

	// Insert the fresh event with a clock set to now-30m.
	sFresh := dedup.NewDBStore(store.Queries())
	sFresh.SetClockForTest(func() time.Time { return now.Add(-30 * time.Minute) })
	if seen, err := sFresh.Seen(ctx, freshKey); err != nil || seen {
		t.Fatalf("insert fresh: seen=%v err=%v", seen, err)
	}

	// Sweep with a TTL of 1h from now — floor is now-1h, so only the old row is eligible.
	sweeper := dedup.NewSweeper(store.Queries(), time.Minute, time.Hour)
	sweeper.SetClockForTest(func() time.Time { return now })
	n, err := sweeper.SweepForTest(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d rows, want 1", n)
	}

	// After eviction the old key is novel again (false).
	sAfter := dedup.NewDBStore(store.Queries())
	sAfter.SetClockForTest(func() time.Time { return now })
	seen, err := sAfter.Seen(ctx, oldKey)
	if err != nil {
		t.Fatalf("post-sweep Seen(oldKey): %v", err)
	}
	if seen {
		t.Error("post-sweep Seen(oldKey): got true, want false (evicted)")
	}

	// The fresh key is still a duplicate.
	seen, err = sAfter.Seen(ctx, freshKey)
	if err != nil {
		t.Fatalf("post-sweep Seen(freshKey): %v", err)
	}
	if !seen {
		t.Error("post-sweep Seen(freshKey): got false, want true (not evicted)")
	}
}

// TestDBStore_FailOpen verifies that a store error returns (false, err) and
// never (true, err). This is the fail-open contract: callers (the dispatcher)
// proceed with dispatch when the store is unavailable.
// t.Parallel() is safe here because the fake querier is local.
func TestDBStore_FailOpen(t *testing.T) {
	t.Parallel()

	fakeErr := errors.New("sqlite: database is locked")
	q := &errQuerier{err: fakeErr}
	s := dedup.NewDBStore(q)

	seen, err := s.Seen(context.Background(), dedup.Key{
		InstanceID: "inst-1",
		EventKind:  "message",
		EventID:    "evt-1",
	})
	if seen {
		t.Error("fail-open violated: Seen returned true on error")
	}
	if err == nil {
		t.Fatal("expected non-nil error from errQuerier, got nil")
	}
	if !errors.Is(err, fakeErr) {
		t.Errorf("error = %v, want to wrap %v", err, fakeErr)
	}
}

// errQuerier is a fake Querier that always returns an error. Used to test the
// fail-open error path without a real database.
type errQuerier struct {
	err error
}

func (q *errQuerier) RecordEventIfNovel(_ context.Context, _ db.RecordEventIfNovelParams) (int64, error) {
	return 0, q.err
}

func (q *errQuerier) SweepEventDedup(_ context.Context, _ int64) (int64, error) {
	return 0, q.err
}
