package api

import (
	"context"
	"fmt"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
)

// StatsService computes dashboard aggregate statistics from the DB.
type StatsService struct {
	store *db.Store
}

// NewStatsService creates a StatsService backed by the given store.
func NewStatsService(store *db.Store) *StatsService {
	return &StatsService{store: store}
}

// DashboardStats holds the four counters returned by GET /api/v1/stats.
type DashboardStats struct {
	ActiveRuns       int64 `json:"active_runs"`
	PendingApprovals int64 `json:"pending_approvals"`
	PolicyCount      int64 `json:"policy_count"`
	TokensLast24h    int64 `json:"tokens_last_24h"`
}

// Compute queries the DB for all four dashboard aggregates and returns them.
func (s *StatsService) Compute(ctx context.Context) (DashboardStats, error) {
	activeRuns, err := s.store.CountActiveRuns(ctx)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("count active runs: %w", err)
	}

	pending, err := s.store.ListPendingApprovalRequests(ctx)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("list pending approvals: %w", err)
	}

	policies, err := s.store.ListPolicies(ctx)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("list policies: %w", err)
	}

	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	rawTokens, err := s.store.SumTokensLast24Hours(ctx, since)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("sum tokens: %w", err)
	}

	// COALESCE(SUM(...), 0) returns interface{} from the sqlc-generated query
	// because SQLite's type system makes the result type ambiguous. Convert
	// explicitly: the value is always an integer, but the driver may return
	// int64 or []byte depending on whether any rows matched.
	var tokens int64
	switch v := rawTokens.(type) {
	case int64:
		tokens = v
	case []byte:
		fmt.Sscanf(string(v), "%d", &tokens)
	}

	return DashboardStats{
		ActiveRuns:       activeRuns,
		PendingApprovals: int64(len(pending)),
		PolicyCount:      int64(len(policies)),
		TokensLast24h:    tokens,
	}, nil
}
