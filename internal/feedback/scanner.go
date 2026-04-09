// Package feedback provides the background timeout scanner for pending feedback
// requests. When a run is waiting_for_feedback and the feedback window expires,
// the scanner marks the feedback request as timed_out and the run as failed.
package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/event"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/runstate"
)

// Scanner periodically scans for expired pending feedback requests and resolves
// them as timed_out. It handles both normal timeout and crash-recovery cases
// (when the process restarted while a run was waiting_for_feedback).
//
// Only feedback requests with a non-NULL expires_at are considered for timeout.
// Requests without expires_at were created without a timeout configured and
// block indefinitely until the operator responds or the run is cancelled.
type Scanner struct {
	store     *db.Store
	interval  time.Duration
	publisher event.Publisher
}

// ScannerOption is a functional option for Scanner.
type ScannerOption func(*Scanner)

// WithPublisher sets an optional SSE publisher so the scanner can emit
// run.status_changed and feedback.timed_out events to connected clients.
func WithPublisher(p event.Publisher) ScannerOption {
	return func(s *Scanner) {
		s.publisher = p
	}
}

// NewScanner creates a Scanner that will check for expired feedback requests on
// the given interval. Pass functional options to configure optional behaviour.
func NewScanner(store *db.Store, interval time.Duration, opts ...ScannerOption) *Scanner {
	s := &Scanner{
		store:    store,
		interval: interval,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start launches the background scan goroutine. It returns immediately; the
// goroutine exits when ctx is cancelled.
func (s *Scanner) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.scan(ctx); err != nil {
					slog.Error("feedback scanner error", "err", err)
				}
			}
		}
	}()
}

// Scan finds all pending feedback requests whose expires_at is in the past and
// resolves each one as timed_out. Errors on individual requests are logged and
// skipped so the ticker continues on subsequent intervals.
// Exported so tests and callers outside the package can drive a synchronous scan.
func (s *Scanner) Scan(ctx context.Context) error {
	return s.scan(ctx)
}

func (s *Scanner) scan(ctx context.Context) error {
	cutoff := time.Now().UTC().Format(time.RFC3339Nano)
	expired, err := s.store.Queries().ListExpiredFeedbackRequests(ctx, &cutoff)
	if err != nil {
		return fmt.Errorf("list expired feedback requests: %w", err)
	}

	for _, req := range expired {
		if err := s.resolveTimeout(ctx, req); err != nil {
			slog.Warn("failed to resolve timed-out feedback",
				"feedback_id", req.ID,
				"run_id", req.RunID,
				"tool_name", req.ToolName,
				"err", err,
			)
		}
	}
	return nil
}

// resolveTimeout marks a single expired feedback request as timed_out, optionally
// marks the associated run as failed, and emits SSE events.
func (s *Scanner) resolveTimeout(ctx context.Context, req db.FeedbackRequest) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Attempt to claim this timeout with a conditional update. The WHERE status='pending'
	// guard ensures exactly one writer wins when concurrent scanners race on the same row.
	rows, err := s.store.Queries().UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
		Status:     "timed_out",
		Response:   nil,
		ResolvedAt: &now,
		ID:         req.ID,
	})
	if err != nil {
		return fmt.Errorf("update feedback status to timed_out: %w", err)
	}
	if rows == 0 {
		// Another writer (concurrent scanner or agent timer) already resolved this
		// request. Skip all downstream side-effects to prevent duplicate error steps
		// and spurious run-status events.
		slog.Debug("feedback already resolved, skipping", "feedback_id", req.ID, "run_id", req.RunID)
		return nil
	}

	// Check whether the run is still waiting for feedback. ScanOrphanedRuns may
	// have already marked it interrupted on process restart; in that case we skip
	// the run update but the feedback request has already been marked timed_out above.
	run, err := s.store.Queries().GetRun(ctx, req.RunID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	slog.Warn("feedback timed out",
		"run_id", req.RunID,
		"feedback_id", req.ID,
		"tool_name", req.ToolName,
	)

	if run.Status != string(model.RunStatusWaitingForFeedback) {
		// Run was already moved out of waiting_for_feedback (e.g. interrupted
		// by ScanOrphanedRuns on restart, or the in-agent timer fired first).
		// Feedback is marked timed_out; nothing more to do.
		return nil
	}

	// Insert an error RunStep so the run's trace explains why it failed.
	stepCount, err := s.store.Queries().CountRunSteps(ctx, req.RunID)
	if err != nil {
		return fmt.Errorf("count run steps: %w", err)
	}

	errMsg := fmt.Sprintf("feedback timeout: operator did not respond within the configured timeout for %s", req.ToolName)
	content, _ := json.Marshal(map[string]string{
		"message": errMsg,
		"code":    string(model.ErrorCodeFeedbackTimeout),
	})

	if _, err := s.store.Queries().CreateRunStep(ctx, db.CreateRunStepParams{
		ID:         model.NewULID(),
		RunID:      req.RunID,
		StepNumber: stepCount,
		Type:       string(model.StepTypeError),
		Content:    string(content),
		TokenCost:  0,
		CreatedAt:  now,
	}); err != nil {
		return fmt.Errorf("create error step: %w", err)
	}

	if err := runstate.TransitionRunFailed(ctx, s.store.Queries(), s.publisher, req.RunID, errMsg); errors.Is(err, runstate.ErrIllegalTransition) {
		// The scanner already verified status == waiting_for_feedback above.
		// An illegal-transition error here means another component moved the
		// run between our GetRun and this call. Benign race — skip silently.
		slog.Debug("TransitionRunFailed race: run already moved",
			"run_id", req.RunID,
			"err", err,
		)
	} else if err != nil {
		return fmt.Errorf("transition run to failed: %w", err)
	} else if s.publisher != nil {
		// Publish the domain-specific feedback.timed_out event. The generic
		// run.status_changed event was already published by transitionFailed.
		if data, err := json.Marshal(map[string]string{
			"feedback_id": req.ID,
			"run_id":      req.RunID,
			"status":      "timed_out",
		}); err == nil {
			s.publisher.Publish("feedback.timed_out", data)
		}
	}

	return nil
}
