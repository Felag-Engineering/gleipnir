// Package timeout provides a generic background scanner for resolving expired
// pending requests (approvals, feedback) as timed-out failures. Domain-specific
// packages (approval, feedback) supply the callbacks that differ between
// domains; this package owns the shared scan loop and resolve logic.
package timeout

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

// ExpiredItem is the minimal representation of an expired pending request as
// needed by the scan loop. Domain-specific ListExpired callbacks map their
// DB types (db.ApprovalRequest, db.FeedbackRequest) to this struct.
type ExpiredItem struct {
	ID       string
	RunID    string
	ToolName string
}

// Config captures every domain-specific difference between the approval and
// feedback scan loops. All fields are required unless noted otherwise.
type Config struct {
	// Name identifies the scanner in log messages (e.g. "approval", "feedback").
	Name string

	// ListExpired returns all pending items whose deadline has passed. The
	// cutoff is a UTC RFC3339Nano timestamp string for the current moment.
	ListExpired func(ctx context.Context, cutoff string) ([]ExpiredItem, error)

	// ClaimTimeout claims a single item via a conditional UPDATE
	// (WHERE status='pending'). Returns the number of rows affected: 1 means
	// this caller won the race and must proceed; 0 means another writer
	// already resolved the item and downstream side-effects must be skipped.
	ClaimTimeout func(ctx context.Context, id string, now string) (int64, error)

	// WaitingRunStatus is the run status expected while a request is pending
	// (RunStatusWaitingForApproval or RunStatusWaitingForFeedback). If the
	// run has already left this status (e.g. interrupted on restart), the
	// scanner skips the run-failure step.
	WaitingRunStatus model.RunStatus

	// ErrorCode is written into the error step's content JSON ("approval_timeout"
	// or the string form of model.ErrorCodeFeedbackTimeout).
	ErrorCode string

	// ErrorMessage builds the human-readable failure message for a given tool name.
	ErrorMessage func(toolName string) string

	// SSEEventName is the domain-specific event emitted after a successful run
	// transition ("approval.resolved" or "feedback.timed_out").
	SSEEventName string

	// SSEPayload builds the event payload published under SSEEventName. The
	// key names differ between domains ("approval_id" vs "feedback_id").
	SSEPayload func(id string, runID string) map[string]string
}

// Scanner periodically scans for expired pending requests and resolves them as
// timed-out failures. It is constructed by domain packages (approval, feedback)
// that supply a Config with domain-specific callbacks.
type Scanner struct {
	store     *db.Store
	interval  time.Duration
	publisher event.Publisher
	cfg       Config
}

// ScannerOption is a functional option for Scanner.
type ScannerOption func(*Scanner)

// WithPublisher sets an optional SSE publisher so the scanner can emit
// run.status_changed and domain-specific events to connected clients.
func WithPublisher(p event.Publisher) ScannerOption {
	return func(s *Scanner) {
		s.publisher = p
	}
}

// NewScanner creates a Scanner that checks for expired requests on the given
// interval using the domain-specific callbacks in cfg.
func NewScanner(store *db.Store, interval time.Duration, cfg Config, opts ...ScannerOption) *Scanner {
	s := &Scanner{
		store:    store,
		interval: interval,
		cfg:      cfg,
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
					slog.Error(s.cfg.Name+" scanner error", "err", err)
				}
			}
		}
	}()
}

// Scan finds all pending requests whose deadline has passed and resolves each
// one as a timeout failure. Errors on individual items are logged and skipped
// so the scan loop continues on subsequent ticks.
// Exported so tests and callers outside the package can drive a synchronous scan.
func (s *Scanner) Scan(ctx context.Context) error {
	return s.scan(ctx)
}

func (s *Scanner) scan(ctx context.Context) error {
	cutoff := time.Now().UTC().Format(time.RFC3339Nano)
	expired, err := s.cfg.ListExpired(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("list expired %s requests: %w", s.cfg.Name, err)
	}

	idAttr := s.cfg.Name + "_id"
	for _, item := range expired {
		if err := s.resolveTimeout(ctx, item); err != nil {
			slog.Warn("failed to resolve timed-out "+s.cfg.Name,
				idAttr, item.ID,
				"run_id", item.RunID,
				"tool_name", item.ToolName,
				"err", err,
			)
		}
	}
	return nil
}

// resolveTimeout marks a single expired item as timed-out, optionally marks
// the associated run as failed, and emits SSE events.
func (s *Scanner) resolveTimeout(ctx context.Context, item ExpiredItem) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Attempt to claim this timeout with a conditional update. The WHERE
	// status='pending' guard ensures exactly one writer wins when concurrent
	// scanners race on the same row.
	rows, err := s.cfg.ClaimTimeout(ctx, item.ID, now)
	if err != nil {
		return fmt.Errorf("claim %s timeout: %w", s.cfg.Name, err)
	}
	if rows == 0 {
		// Another writer (concurrent scanner or agent timer) already resolved
		// this item. Skip all downstream side-effects to prevent duplicate
		// error steps and spurious run-status events.
		slog.Debug(s.cfg.Name+" already resolved, skipping",
			s.cfg.Name+"_id", item.ID,
			"run_id", item.RunID,
		)
		return nil
	}

	// Check whether the run is still waiting. ScanOrphanedRuns may have already
	// marked it interrupted on process restart; in that case we skip the run
	// update but the item has already been marked timed-out above.
	run, err := s.store.Queries().GetRun(ctx, item.RunID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	slog.Warn(s.cfg.Name+" timed out",
		"run_id", item.RunID,
		s.cfg.Name+"_id", item.ID,
		"tool_name", item.ToolName,
	)

	if run.Status != string(s.cfg.WaitingRunStatus) {
		// Run was already moved out of the waiting status (e.g. interrupted
		// by ScanOrphanedRuns on restart). The item is marked timed-out;
		// nothing more to do.
		return nil
	}

	// Insert an error RunStep so the run's trace explains why it failed.
	stepCount, err := s.store.Queries().CountRunSteps(ctx, item.RunID)
	if err != nil {
		return fmt.Errorf("count run steps: %w", err)
	}

	errMsg := s.cfg.ErrorMessage(item.ToolName)
	content, _ := json.Marshal(map[string]string{
		"message": errMsg,
		"code":    s.cfg.ErrorCode,
	})

	if _, err := s.store.Queries().CreateRunStep(ctx, db.CreateRunStepParams{
		ID:         model.NewULID(),
		RunID:      item.RunID,
		StepNumber: stepCount,
		Type:       string(model.StepTypeError),
		Content:    string(content),
		TokenCost:  0,
		CreatedAt:  now,
	}); err != nil {
		return fmt.Errorf("create error step: %w", err)
	}

	if err := runstate.TransitionRunFailed(ctx, s.store.Queries(), s.publisher, item.RunID, errMsg); errors.Is(err, runstate.ErrIllegalTransition) {
		// The scanner already verified the run status above. An illegal-
		// transition error here means another component moved the run between
		// our GetRun and this call. Benign race — skip silently.
		slog.Debug("TransitionRunFailed race: run already moved",
			"run_id", item.RunID,
			"err", err,
		)
	} else if err != nil {
		return fmt.Errorf("transition run to failed: %w", err)
	} else if s.publisher != nil {
		// Publish the domain-specific event. The generic run.status_changed
		// event was already published by TransitionRunFailed.
		if data, err := json.Marshal(s.cfg.SSEPayload(item.ID, item.RunID)); err == nil {
			s.publisher.Publish(s.cfg.SSEEventName, data)
		}
	}

	return nil
}
