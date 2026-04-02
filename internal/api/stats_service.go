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

// DashboardStats holds the counters returned by GET /api/v1/stats.
type DashboardStats struct {
	ActiveRuns       int64 `json:"active_runs"`
	PendingApprovals int64 `json:"pending_approvals"`
	PendingFeedback  int64 `json:"pending_feedback"`
	PolicyCount      int64 `json:"policy_count"`
	TokensLast24h    int64 `json:"tokens_last_24h"`
}

// Compute queries the DB for all four dashboard aggregates and returns them.
func (s *StatsService) Compute(ctx context.Context) (DashboardStats, error) {
	activeRuns, err := s.store.CountActiveRuns(ctx)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("count active runs: %w", err)
	}

	pendingApprovals, err := s.store.CountPendingApprovalRequests(ctx)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("count pending approvals: %w", err)
	}

	pendingFeedback, err := s.store.CountPendingFeedbackRequests(ctx)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("count pending feedback: %w", err)
	}

	policyCount, err := s.store.CountPolicies(ctx)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("count policies: %w", err)
	}

	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	tokens, err := s.store.SumTokensLast24Hours(ctx, since)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("sum tokens: %w", err)
	}

	return DashboardStats{
		ActiveRuns:       activeRuns,
		PendingApprovals: pendingApprovals,
		PendingFeedback:  pendingFeedback,
		PolicyCount:      policyCount,
		TokensLast24h:    tokens,
	}, nil
}
