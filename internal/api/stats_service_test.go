package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
)

// insertTestRunWithTime inserts a run row with a specific created_at timestamp and token cost.
func insertTestRunWithTime(t *testing.T, s *db.Store, id, policyID, status, createdAt string, tokenCost int64) {
	t.Helper()
	_, err := s.DB().ExecContext(context.Background(),
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at, token_cost)
		 VALUES (?, ?, ?, 'webhook', '{}', ?, ?, ?)`,
		id, policyID, status, createdAt, createdAt, tokenCost,
	)
	if err != nil {
		t.Fatalf("insertTestRunWithTime %s: %v", id, err)
	}
}

func TestStatsServiceCompute(t *testing.T) {
	ctx := context.Background()

	t.Run("empty db returns all zeros", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		svc := api.NewStatsService(store)
		stats, err := svc.Compute(ctx)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		if stats.ActiveRuns != 0 || stats.PendingApprovals != 0 ||
			stats.PolicyCount != 0 || stats.TokensLast24h != 0 {
			t.Errorf("expected all zeros, got %+v", stats)
		}
	})

	t.Run("counts policies correctly", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "p1", "pol1", "")
		insertTestPolicy(t, store, "p2", "pol2", "")

		svc := api.NewStatsService(store)
		stats, err := svc.Compute(ctx)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		if stats.PolicyCount != 2 {
			t.Errorf("PolicyCount = %d, want 2", stats.PolicyCount)
		}
	})

	t.Run("counts active runs correctly", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "p1", "pol1", "")
		insertTestRun(t, store, "r-running", "p1", "running")
		insertTestRun(t, store, "r-pending", "p1", "pending")
		insertTestRun(t, store, "r-waiting", "p1", "waiting_for_approval")
		insertTestRun(t, store, "r-complete", "p1", "complete")

		svc := api.NewStatsService(store)
		stats, err := svc.Compute(ctx)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		if stats.ActiveRuns != 3 {
			t.Errorf("ActiveRuns = %d, want 3", stats.ActiveRuns)
		}
	})

	t.Run("tokens_last_24h sums recent runs only", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "p1", "pol1", "")

		now := time.Now().UTC()
		recent := now.Add(-1 * time.Hour).Format(time.RFC3339Nano)
		old := "2020-01-01T00:00:00Z"

		insertTestRunWithTime(t, store, "r-recent", "p1", "complete", recent, 2000)
		insertTestRunWithTime(t, store, "r-old", "p1", "complete", old, 9999)

		svc := api.NewStatsService(store)
		stats, err := svc.Compute(ctx)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		if stats.TokensLast24h != 2000 {
			t.Errorf("TokensLast24h = %d, want 2000 (old run should be excluded)", stats.TokensLast24h)
		}
	})

	t.Run("tokens_last_24h sums multiple recent runs", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "p1", "pol1", "")

		now := time.Now().UTC()
		recent := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
		insertTestRunWithTime(t, store, "r1", "p1", "complete", recent, 300)
		insertTestRunWithTime(t, store, "r2", "p1", "complete", recent, 700)

		svc := api.NewStatsService(store)
		stats, err := svc.Compute(ctx)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		if stats.TokensLast24h != 1000 {
			t.Errorf("TokensLast24h = %d, want 1000", stats.TokensLast24h)
		}
	})
}
