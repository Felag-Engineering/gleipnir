// Package logctx provides context-based structured log correlation for run-scoped
// operations. It stores run_id and policy_id in a context.Context and provides a
// Logger function that returns an *slog.Logger enriched with those attributes.
//
// This is a leaf package with no internal imports — it depends only on the
// standard library.
package logctx

import (
	"context"
	"log/slog"
)

type contextKey struct{}

type runCorrelation struct {
	RunID    string
	PolicyID string
}

// WithRunCorrelation returns a new context carrying run_id and policy_id for
// structured log correlation. Downstream code retrieves these via Logger(ctx).
func WithRunCorrelation(ctx context.Context, runID, policyID string) context.Context {
	return context.WithValue(ctx, contextKey{}, runCorrelation{
		RunID:    runID,
		PolicyID: policyID,
	})
}

// Logger returns an *slog.Logger enriched with run_id and policy_id from the
// context. If the context does not carry correlation IDs (e.g. context.Background()),
// it returns slog.Default() with no extra attributes.
func Logger(ctx context.Context) *slog.Logger {
	v, ok := ctx.Value(contextKey{}).(runCorrelation)
	if !ok {
		return slog.Default()
	}
	return slog.Default().With("run_id", v.RunID, "policy_id", v.PolicyID)
}
