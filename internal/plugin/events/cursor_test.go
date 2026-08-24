package events_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// insertPluginAndInstance seeds the DB with a minimal plugin + instance row
// so plugin_event_cursors foreign-key constraints are satisfied. Mirrors
// internal/plugin/dedup/store_test.go's helper of the same name.
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

// TestDBStore_LoadFreshInstanceReturnsEmpty verifies that Load on an instance
// with no stored cursor reports "no cursor yet" as (empty, 0, nil) rather
// than an error.
func TestDBStore_LoadFreshInstanceReturnsEmpty(t *testing.T) {
	t.Parallel()
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-fresh", "inst-fresh")

	s := events.NewDBStore(store.Queries())
	cursor, seq, err := s.Load(context.Background(), "inst-fresh", "scope-a")
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cursor != "" || seq != 0 {
		t.Errorf("Load = (%q, %d), want (\"\", 0)", cursor, seq)
	}
}

// TestDBStore_AdvanceThenLoadRoundTrips verifies the basic write-through
// contract: an Advance is visible to a subsequent Load under the same scope.
func TestDBStore_AdvanceThenLoadRoundTrips(t *testing.T) {
	t.Parallel()
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-roundtrip", "inst-roundtrip")

	ctx := context.Background()
	s := events.NewDBStore(store.Queries())
	s.SetClockForTest(func() time.Time { return time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC) })

	if err := s.Advance(ctx, "inst-roundtrip", "scope-a", "cursor-1", 10); err != nil {
		t.Fatalf("Advance: unexpected error: %v", err)
	}

	cursor, seq, err := s.Load(ctx, "inst-roundtrip", "scope-a")
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cursor != "cursor-1" || seq != 10 {
		t.Errorf("Load = (%q, %d), want (\"cursor-1\", 10)", cursor, seq)
	}
}

// TestDBStore_LoadScopeMismatchReturnsEmptyAndLeavesRowIntact verifies that a
// cursor earned under a different scope is never handed back to a caller
// asking under a new scope — it must read as "no cursor" — but the stored
// row is left untouched so it remains available for diagnosis.
func TestDBStore_LoadScopeMismatchReturnsEmptyAndLeavesRowIntact(t *testing.T) {
	t.Parallel()
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-scope", "inst-scope")

	ctx := context.Background()
	s := events.NewDBStore(store.Queries())
	s.SetClockForTest(func() time.Time { return time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC) })

	if err := s.Advance(ctx, "inst-scope", "scope-old", "cursor-old", 5); err != nil {
		t.Fatalf("Advance: unexpected error: %v", err)
	}

	cursor, seq, err := s.Load(ctx, "inst-scope", "scope-new")
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cursor != "" || seq != 0 {
		t.Errorf("Load under mismatched scope = (%q, %d), want (\"\", 0)", cursor, seq)
	}

	// The stored row must be unchanged — a diagnostic read under the
	// original scope still sees it.
	cursor, seq, err = s.Load(ctx, "inst-scope", "scope-old")
	if err != nil {
		t.Fatalf("Load under original scope: unexpected error: %v", err)
	}
	if cursor != "cursor-old" || seq != 5 {
		t.Errorf("Load under original scope = (%q, %d), want (\"cursor-old\", 5) — mismatch must not have mutated the row", cursor, seq)
	}
}

// TestDBStore_AdvanceRefusesBackwardsSeq verifies that Advance rejects a
// sequence at or below the stored value with an error naming both — a
// server buffer replaying an already-acked sequence is a server bug the
// store must not paper over.
func TestDBStore_AdvanceRefusesBackwardsSeq(t *testing.T) {
	t.Parallel()
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-backwards", "inst-backwards")

	ctx := context.Background()
	s := events.NewDBStore(store.Queries())
	s.SetClockForTest(func() time.Time { return time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC) })

	if err := s.Advance(ctx, "inst-backwards", "scope-a", "cursor-10", 10); err != nil {
		t.Fatalf("initial Advance: unexpected error: %v", err)
	}

	// Equal seq is refused.
	err := s.Advance(ctx, "inst-backwards", "scope-a", "cursor-10-again", 10)
	if err == nil {
		t.Fatal("Advance with seq == stored seq: want error, got nil")
	}
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("Advance error = %q, want it to name both seq values (10)", err.Error())
	}

	// Lower seq is refused.
	err = s.Advance(ctx, "inst-backwards", "scope-a", "cursor-5", 5)
	if err == nil {
		t.Fatal("Advance with seq < stored seq: want error, got nil")
	}
	if !strings.Contains(err.Error(), "5") || !strings.Contains(err.Error(), "10") {
		t.Errorf("Advance error = %q, want it to name both 5 and 10", err.Error())
	}

	// The stored cursor must be unchanged by the refused Advance calls.
	cursor, seq, loadErr := s.Load(ctx, "inst-backwards", "scope-a")
	if loadErr != nil {
		t.Fatalf("Load: unexpected error: %v", loadErr)
	}
	if cursor != "cursor-10" || seq != 10 {
		t.Errorf("Load after refused Advance = (%q, %d), want (\"cursor-10\", 10)", cursor, seq)
	}
}

// TestDBStore_AdvanceAcceptsStrictlyGreaterSeq verifies the success path of
// the monotonicity check.
func TestDBStore_AdvanceAcceptsStrictlyGreaterSeq(t *testing.T) {
	t.Parallel()
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-forward", "inst-forward")

	ctx := context.Background()
	s := events.NewDBStore(store.Queries())
	s.SetClockForTest(func() time.Time { return time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC) })

	if err := s.Advance(ctx, "inst-forward", "scope-a", "cursor-1", 1); err != nil {
		t.Fatalf("Advance seq=1: unexpected error: %v", err)
	}
	if err := s.Advance(ctx, "inst-forward", "scope-a", "cursor-2", 2); err != nil {
		t.Fatalf("Advance seq=2: unexpected error: %v", err)
	}

	cursor, seq, err := s.Load(ctx, "inst-forward", "scope-a")
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cursor != "cursor-2" || seq != 2 {
		t.Errorf("Load = (%q, %d), want (\"cursor-2\", 2)", cursor, seq)
	}
}

// TestDBStore_ResetClearsCursor verifies that Reset removes the stored row so
// a subsequent Load reports "no cursor" and a subsequent Advance is not
// bound by the old sequence's monotonicity floor.
func TestDBStore_ResetClearsCursor(t *testing.T) {
	t.Parallel()
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-reset", "inst-reset")

	ctx := context.Background()
	s := events.NewDBStore(store.Queries())
	s.SetClockForTest(func() time.Time { return time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC) })

	if err := s.Advance(ctx, "inst-reset", "scope-old", "cursor-99", 99); err != nil {
		t.Fatalf("Advance: unexpected error: %v", err)
	}
	if err := s.Reset(ctx, "inst-reset"); err != nil {
		t.Fatalf("Reset: unexpected error: %v", err)
	}

	cursor, seq, err := s.Load(ctx, "inst-reset", "scope-old")
	if err != nil {
		t.Fatalf("Load after Reset: unexpected error: %v", err)
	}
	if cursor != "" || seq != 0 {
		t.Errorf("Load after Reset = (%q, %d), want (\"\", 0)", cursor, seq)
	}

	// A lower seq than the pre-reset value is now accepted, since Reset
	// cleared the monotonicity floor.
	if err := s.Advance(ctx, "inst-reset", "scope-new", "cursor-1", 1); err != nil {
		t.Errorf("Advance after Reset with seq=1: unexpected error: %v", err)
	}
}

// TestDBStore_InstanceDeleteCascades verifies ON DELETE CASCADE: deleting the
// owning plugin_instances row removes the cursor row with it.
func TestDBStore_InstanceDeleteCascades(t *testing.T) {
	t.Parallel()
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-cascade", "inst-cascade")

	ctx := context.Background()
	s := events.NewDBStore(store.Queries())
	s.SetClockForTest(func() time.Time { return time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC) })

	if err := s.Advance(ctx, "inst-cascade", "scope-a", "cursor-1", 1); err != nil {
		t.Fatalf("Advance: unexpected error: %v", err)
	}

	if _, err := store.DB().Exec(`DELETE FROM plugin_instances WHERE id = ?`, "inst-cascade"); err != nil {
		t.Fatalf("delete plugin_instances row: %v", err)
	}

	var count int
	if err := store.DB().QueryRow(
		`SELECT COUNT(*) FROM plugin_event_cursors WHERE plugin_instance_id = ?`, "inst-cascade",
	).Scan(&count); err != nil {
		t.Fatalf("count plugin_event_cursors rows: %v", err)
	}
	if count != 0 {
		t.Errorf("plugin_event_cursors rows for deleted instance = %d, want 0 (ON DELETE CASCADE)", count)
	}
}

// TestDBStore_ConcurrentAdvanceForTwoInstances exercises Advance from two
// goroutines writing to two different instances concurrently. Run with
// -race: each instance's row is independent, so there must be no data race
// in the store itself.
func TestDBStore_ConcurrentAdvanceForTwoInstances(t *testing.T) {
	t.Parallel()
	store := testutil.NewTestStore(t)
	insertPluginAndInstance(t, store, "plug-conc-a", "inst-conc-a")
	insertPluginAndInstance(t, store, "plug-conc-b", "inst-conc-b")

	ctx := context.Background()
	s := events.NewDBStore(store.Queries())

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	advance := func(instanceID string, n uint64) {
		defer wg.Done()
		for i := uint64(1); i <= n; i++ {
			if err := s.Advance(ctx, instanceID, "scope-a", "cursor", i); err != nil {
				errs <- err
				return
			}
		}
	}

	wg.Add(2)
	go advance("inst-conc-a", 20)
	go advance("inst-conc-b", 20)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Advance: unexpected error: %v", err)
	}

	for _, instanceID := range []string{"inst-conc-a", "inst-conc-b"} {
		_, seq, err := s.Load(ctx, instanceID, "scope-a")
		if err != nil {
			t.Fatalf("Load(%s): unexpected error: %v", instanceID, err)
		}
		if seq != 20 {
			t.Errorf("Load(%s) seq = %d, want 20", instanceID, seq)
		}
	}
}

// TestNoop verifies the no-op Store never persists anything: Load always
// reports "no cursor" and Advance/Reset never error.
func TestNoop(t *testing.T) {
	t.Parallel()
	var s events.Noop
	ctx := context.Background()

	cursor, seq, err := s.Load(ctx, "any-instance", "any-scope")
	if err != nil || cursor != "" || seq != 0 {
		t.Errorf("Noop.Load = (%q, %d, %v), want (\"\", 0, nil)", cursor, seq, err)
	}
	if err := s.Advance(ctx, "any-instance", "any-scope", "cursor", 1); err != nil {
		t.Errorf("Noop.Advance: unexpected error: %v", err)
	}
	if err := s.Reset(ctx, "any-instance"); err != nil {
		t.Errorf("Noop.Reset: unexpected error: %v", err)
	}
}
