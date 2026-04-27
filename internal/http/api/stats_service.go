package api

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/felag-engineering/gleipnir/internal/db"
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

// wrapErr wraps a non-nil error with context. Returns nil if err is nil.
func wrapErr(err error, msg string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// Compute queries the DB for all five dashboard aggregates concurrently.
func (s *StatsService) Compute(ctx context.Context) (DashboardStats, error) {
	var (
		activeRuns       int64
		pendingApprovals int64
		pendingFeedback  int64
		policyCount      int64
		tokens           int64
	)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		var err error
		activeRuns, err = s.store.CountActiveRuns(ctx)
		return wrapErr(err, "count active runs")
	})
	g.Go(func() error {
		var err error
		pendingApprovals, err = s.store.CountPendingApprovalRequests(ctx)
		return wrapErr(err, "count pending approvals")
	})
	g.Go(func() error {
		var err error
		pendingFeedback, err = s.store.CountPendingFeedbackRequests(ctx)
		return wrapErr(err, "count pending feedback")
	})
	g.Go(func() error {
		var err error
		policyCount, err = s.store.CountPolicies(ctx)
		return wrapErr(err, "count policies")
	})
	g.Go(func() error {
		since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
		var err error
		tokens, err = s.store.SumTokensLast24Hours(ctx, since)
		return wrapErr(err, "sum tokens")
	})

	if err := g.Wait(); err != nil {
		return DashboardStats{}, err
	}

	return DashboardStats{
		ActiveRuns:       activeRuns,
		PendingApprovals: pendingApprovals,
		PendingFeedback:  pendingFeedback,
		PolicyCount:      policyCount,
		TokensLast24h:    tokens,
	}, nil
}
