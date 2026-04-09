// Package approval provides the background timeout scanner for pending approval
// requests. When a run is waiting_for_approval and the approval window expires,
// the scanner marks the approval as timeout and the run as failed.
package approval

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

// Scanner periodically scans for expired pending approval requests and resolves
// them as timeout. It handles both normal timeout and crash-recovery cases
// (when the process restarted while a run was waiting_for_approval).
//
// All timeouts result in run failure. The on_timeout field only accepts "reject"
// (the "approve" value was removed in issue #313 because this scanner could not
// honor it during crash recovery).
type Scanner struct {
	store     *db.Store
	interval  time.Duration
	publisher event.Publisher
}

// ScannerOption is a functional option for Scanner.
type ScannerOption func(*Scanner)

// WithPublisher sets an optional SSE publisher so the scanner can emit
// run.status_changed and approval.resolved events to connected clients.
func WithPublisher(p event.Publisher) ScannerOption {
	return func(s *Scanner) {
		s.publisher = p
	}
}

// NewScanner creates a Scanner that will check for expired approvals on the
// given interval. Pass functional options to configure optional behaviour.
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
					slog.Error("approval scanner error", "err", err)
				}
			}
		}
	}()
}

// Scan finds all pending approval requests whose expires_at is in the past and
// resolves each one as a timeout. Errors on individual requests are logged and
// skipped so the ticker continues on subsequent intervals.
// Exported so tests and callers outside the package can drive a synchronous scan.
func (s *Scanner) Scan(ctx context.Context) error {
	return s.scan(ctx)
}

func (s *Scanner) scan(ctx context.Context) error {
	cutoff := time.Now().UTC().Format(time.RFC3339Nano)
	expired, err := s.store.Queries().ListExpiredApprovalRequests(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("list expired approval requests: %w", err)
	}

	for _, req := range expired {
		if err := s.resolveTimeout(ctx, req); err != nil {
			slog.Warn("failed to resolve timed-out approval",
				"approval_id", req.ID,
				"run_id", req.RunID,
				"tool_name", req.ToolName,
				"err", err,
			)
		}
	}
	return nil
}

// resolveTimeout marks a single expired approval request as timeout, optionally
// marks the associated run as failed, and emits SSE events.
func (s *Scanner) resolveTimeout(ctx context.Context, req db.ApprovalRequest) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	timeoutStatus := string(model.ApprovalStatusTimeout)

	// Attempt to claim this timeout with a conditional update. The WHERE status='pending'
	// guard ensures exactly one writer wins when concurrent scanners race on the same row.
	rows, err := s.store.Queries().UpdateApprovalRequestStatus(ctx, db.UpdateApprovalRequestStatusParams{
		Status:    timeoutStatus,
		DecidedAt: &now,
		Note:      nil,
		ID:        req.ID,
	})
	if err != nil {
		return fmt.Errorf("update approval status: %w", err)
	}
	if rows == 0 {
		// Another writer (concurrent scanner or agent timer) already resolved this
		// request. Skip all downstream side-effects to prevent duplicate error steps
		// and spurious run-status events.
		slog.Debug("approval already resolved, skipping", "approval_id", req.ID, "run_id", req.RunID)
		return nil
	}

	// Check whether the run is still waiting for approval. ScanOrphanedRuns may
	// have already marked it interrupted on process restart; in that case we
	// skip the run update but the approval has already been marked timeout above.
	run, err := s.store.Queries().GetRun(ctx, req.RunID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	slog.Warn("approval timed out",
		"run_id", req.RunID,
		"approval_id", req.ID,
		"tool_name", req.ToolName,
	)

	if run.Status != string(model.RunStatusWaitingForApproval) {
		// Run was already moved out of waiting_for_approval (e.g. interrupted
		// by ScanOrphanedRuns on restart). Approval is marked timeout; nothing
		// more to do.
		return nil
	}

	// Insert an error RunStep so the run's trace explains why it failed.
	stepCount, err := s.store.Queries().CountRunSteps(ctx, req.RunID)
	if err != nil {
		return fmt.Errorf("count run steps: %w", err)
	}

	errMsg := fmt.Sprintf("approval timeout: %s not approved within timeout window", req.ToolName)
	content, _ := json.Marshal(map[string]string{
		"message": errMsg,
		"code":    "approval_timeout",
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
		// The scanner already verified status == waiting_for_approval above.
		// An illegal-transition error here means another component moved the
		// run between our GetRun and this call. Benign race — skip silently.
		slog.Debug("TransitionRunFailed race: run already moved",
			"run_id", req.RunID,
			"err", err,
		)
	} else if err != nil {
		return fmt.Errorf("transition run to failed: %w", err)
	} else if s.publisher != nil {
		// Publish the domain-specific approval.resolved event. The generic
		// run.status_changed event was already published by transitionFailed.
		if data, err := json.Marshal(map[string]string{
			"approval_id": req.ID,
			"run_id":      req.RunID,
			"status":      string(model.ApprovalStatusTimeout),
		}); err == nil {
			s.publisher.Publish("approval.resolved", data)
		}
	}

	return nil
}
