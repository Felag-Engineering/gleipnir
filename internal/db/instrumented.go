package db

import (
	"context"
	"time"
)

// InstrumentedQueries is a thin wrapper around the sqlc-generated *Queries that
// records gleipnir_db_query_duration_seconds for the six hot-path queries
// identified in the metrics spec (ADR-037). Only wrap additional queries on
// demonstrated diagnostic need — the label cardinality must remain bounded.
//
// It wraps *Queries and shadows the six hot-path methods to observe query
// duration. All other sqlc methods are still callable via the embedded *Queries.
type InstrumentedQueries struct {
	*Queries
}

// NewInstrumentedQueries wraps q with timing instrumentation.
func NewInstrumentedQueries(q *Queries) *InstrumentedQueries {
	return &InstrumentedQueries{Queries: q}
}

func (iq *InstrumentedQueries) CreateRun(ctx context.Context, arg CreateRunParams) (Run, error) {
	start := time.Now()
	defer func() {
		dbQueryDurationSeconds.WithLabelValues("CreateRun").Observe(time.Since(start).Seconds())
	}()
	return iq.Queries.CreateRun(ctx, arg)
}

func (iq *InstrumentedQueries) GetRun(ctx context.Context, id string) (Run, error) {
	start := time.Now()
	defer func() {
		dbQueryDurationSeconds.WithLabelValues("GetRun").Observe(time.Since(start).Seconds())
	}()
	return iq.Queries.GetRun(ctx, id)
}

func (iq *InstrumentedQueries) CreateRunStep(ctx context.Context, arg CreateRunStepParams) (RunStep, error) {
	start := time.Now()
	defer func() {
		dbQueryDurationSeconds.WithLabelValues("CreateRunStep").Observe(time.Since(start).Seconds())
	}()
	return iq.Queries.CreateRunStep(ctx, arg)
}

func (iq *InstrumentedQueries) ListPolicies(ctx context.Context) ([]Policy, error) {
	start := time.Now()
	defer func() {
		dbQueryDurationSeconds.WithLabelValues("ListPolicies").Observe(time.Since(start).Seconds())
	}()
	return iq.Queries.ListPolicies(ctx)
}

func (iq *InstrumentedQueries) GetPolicy(ctx context.Context, id string) (Policy, error) {
	start := time.Now()
	defer func() {
		dbQueryDurationSeconds.WithLabelValues("GetPolicy").Observe(time.Since(start).Seconds())
	}()
	return iq.Queries.GetPolicy(ctx, id)
}

func (iq *InstrumentedQueries) GetApprovalRequest(ctx context.Context, id string) (ApprovalRequest, error) {
	start := time.Now()
	defer func() {
		dbQueryDurationSeconds.WithLabelValues("GetApprovalRequest").Observe(time.Since(start).Seconds())
	}()
	return iq.Queries.GetApprovalRequest(ctx, id)
}
