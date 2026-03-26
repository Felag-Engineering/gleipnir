// Package approval provides the background timeout scanner for pending approval
// requests. When a run is waiting_for_approval and the approval window expires,
// the scanner marks the approval as timeout and the run as failed.
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/event"
	"github.com/rapp992/gleipnir/internal/model"
)

// Scanner periodically scans for expired pending approval requests and resolves
// them as timeout. It handles both normal timeout and crash-recovery cases
// (when the process restarted while a run was waiting_for_approval).
//
// v1 simplification: all timeouts result in run failure regardless of the
// policy's on_timeout field. A future version can inspect the policy to support
// auto-approve-on-timeout.
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

// scan finds all pending approval requests whose expires_at is in the past and
// resolves each one as a timeout. Errors on individual requests are logged and
// skipped so the ticker continues on subsequent intervals.
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

	// Mark the approval as timeout.
	if err := s.store.Queries().UpdateApprovalRequestStatus(ctx, db.UpdateApprovalRequestStatusParams{
		Status:    timeoutStatus,
		DecidedAt: &now,
		Note:      nil,
		ID:        req.ID,
	}); err != nil {
		return fmt.Errorf("update approval status: %w", err)
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

	// Mark the run as failed.
	if err := s.store.Queries().UpdateRunError(ctx, db.UpdateRunErrorParams{
		Status:      string(model.RunStatusFailed),
		Error:       &errMsg,
		CompletedAt: &now,
		ID:          req.RunID,
	}); err != nil {
		return fmt.Errorf("update run status to failed: %w", err)
	}

	if s.publisher != nil {
		s.publishEvents(req.RunID, req.ID)
	}

	return nil
}

// publishEvents emits SSE events for a resolved timeout. Marshalling errors are
// not fatal — the DB state is already consistent at this point.
func (s *Scanner) publishEvents(runID, approvalID string) {
	if data, err := json.Marshal(map[string]string{"run_id": runID, "status": string(model.RunStatusFailed)}); err == nil {
		s.publisher.Publish("run.status_changed", data)
	}
	if data, err := json.Marshal(map[string]string{"approval_id": approvalID, "run_id": runID, "status": string(model.ApprovalStatusTimeout)}); err == nil {
		s.publisher.Publish("approval.resolved", data)
	}
}
