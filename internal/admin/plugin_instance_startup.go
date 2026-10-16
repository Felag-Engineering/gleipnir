package admin

import (
	"context"
	"time"
)

// InstanceStartupFault is why an instance's plugin container keeps failing to
// start, as returned under `startup` by GET
// /api/v1/admin/plugins/{id}/instances/{iid}.
//
// It sits next to the instance's health state rather than inside it. Health
// has one writer, the capability prober, and this is not a health verdict:
// during a failing upgrade the previous generation keeps serving, and that
// instance must not be reported down. ServingGeneration says which case the
// operator is looking at — nil means nothing is serving.
type InstanceStartupFault struct {
	// Failures is the number of consecutive attempts that failed to start at
	// the instance's current image and config.
	Failures int `json:"failures"`
	// LastError is the newest failure's cause. It comes from the container
	// runtime or the health gate and may quote text the container produced, so
	// it is untrusted.
	LastError      string    `json:"last_error"`
	LastGeneration int64     `json:"last_generation"`
	LastFailedAt   time.Time `json:"last_failed_at"`
	// NextAttemptAt may be in the past: the backoff has run out and the next
	// reconcile pass will try again.
	NextAttemptAt     time.Time `json:"next_attempt_at"`
	AttemptInProgress bool      `json:"attempt_in_progress"`
	ServingGeneration *int64    `json:"serving_generation"`
}

// InstanceStartupReader reports an instance's startup fault, or nil when it
// has none. nil in v1, which has no container generations to read; the v2
// assembly derives it from generation history, the same rows the reconciler's
// mint backoff is computed from.
type InstanceStartupReader interface {
	InstanceStartup(ctx context.Context, instanceID string) (*InstanceStartupFault, error)
}
