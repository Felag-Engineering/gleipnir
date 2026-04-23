package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/event"
	"github.com/rapp992/gleipnir/internal/logctx"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/runstate"
)

// ErrTransitionConflict re-exports runstate.ErrTransitionConflict so callers in
// the agent package do not need to import internal/runstate to inspect it.
var ErrTransitionConflict = runstate.ErrTransitionConflict

// trackedForActive reports whether a run status contributes to the
// gleipnir_runs_active gauge. pending and terminal statuses are excluded —
// pending is effectively instantaneous, and terminal statuses (complete,
// failed, interrupted) are covered by runs_total, not an "active" gauge.
func trackedForActive(s model.RunStatus) bool {
	switch s {
	case model.RunStatusRunning,
		model.RunStatusWaitingForApproval,
		model.RunStatusWaitingForFeedback:
		return true
	}
	return false
}

// RunStateMachine tracks and persists the status of a single agent run.
// It enforces the legal transition graph and writes every transition to the DB
// inside a single SQL transaction with a CAS version guard. Two instances on
// the same row cannot both commit — the second writer returns ErrTransitionConflict.
//
// Safe for concurrent use within a single instance; cross-instance conflicts
// are detected via the version column, not the in-process mutex.
//
// IMPORTANT: callers must not open a transaction around Transition calls.
// Store opens the DB with MaxOpenConns(1), so a nested BeginTx would deadlock.
type RunStateMachine struct {
	runID     string
	current   model.RunStatus
	version   int64 // in-memory mirror of runs.version; updated only after tx.Commit
	mu        sync.Mutex
	database  *sql.DB
	queries   *db.Queries
	publisher event.Publisher
}

// StateMachineOption configures a RunStateMachine at construction time.
type StateMachineOption func(*RunStateMachine)

// WithStateMachinePublisher injects a Publisher that receives run.status_changed
// events after each successful transition.
func WithStateMachinePublisher(p event.Publisher) StateMachineOption {
	return func(sm *RunStateMachine) {
		sm.publisher = p
	}
}

// WithInitialVersion sets the starting optimistic-lock version. Pass the value
// read from the DB row at run creation time so the first transition's CAS guard
// matches. Defaults to 0 (correct for freshly-inserted rows).
func WithInitialVersion(v int64) StateMachineOption {
	return func(sm *RunStateMachine) { sm.version = v }
}

// NewRunStateMachine returns a RunStateMachine initialised to the given status.
// The initial status is not written to the DB — the caller is responsible for
// ensuring the DB row already reflects that state.
//
// database is the *sql.DB used to open transactions inside Transition.
// queries is the sqlc-generated accessor; it must operate on the same DB.
func NewRunStateMachine(runID string, initial model.RunStatus, database *sql.DB, queries *db.Queries, opts ...StateMachineOption) *RunStateMachine {
	sm := &RunStateMachine{
		runID:    runID,
		current:  initial,
		database: database,
		queries:  queries,
	}
	for _, opt := range opts {
		opt(sm)
	}
	return sm
}

// ApprovalPayload carries the data needed to create an approval_requests DB
// record when entering waiting_for_approval.
type ApprovalPayload struct {
	ApprovalID    string
	ToolName      string
	ProposedInput string // JSON-encoded
	ExpiresAt     string // RFC3339Nano
}

// FeedbackPayload carries the data needed to create a feedback_requests DB
// record when entering waiting_for_feedback.
type FeedbackPayload struct {
	FeedbackID    string
	ToolName      string
	ProposedInput string // JSON-encoded input the agent sent to the feedback tool
	Message       string // MCP tool output — the notification text sent to the operator
	ExpiresAt     string // RFC3339Nano; empty string means no timeout
}

type transitionOpts struct {
	approval *ApprovalPayload
	feedback *FeedbackPayload
}

// TransitionOption configures optional behavior for a Transition call.
type TransitionOption func(*transitionOpts)

// WithApprovalPayload attaches approval context to a waiting_for_approval transition.
func WithApprovalPayload(p ApprovalPayload) TransitionOption {
	return func(o *transitionOpts) { o.approval = &p }
}

// WithFeedbackPayload attaches feedback context to a waiting_for_feedback transition.
func WithFeedbackPayload(p FeedbackPayload) TransitionOption {
	return func(o *transitionOpts) { o.feedback = &p }
}

// Transition validates and atomically persists a run status transition.
// All DB writes (status UPDATE + optional approval/feedback INSERT) happen inside
// a single SQL transaction so a failed secondary INSERT rolls back the status change.
//
// The UPDATE uses a CAS guard (WHERE version = expectedVersion) to detect
// concurrent writers. When rows_affected == 0, ErrTransitionConflict is returned
// and in-memory state is not advanced — the caller must not assume its write landed.
//
// In-memory state (sm.current, sm.version) is updated ONLY after tx.Commit
// succeeds, so a commit failure leaves the state machine consistent with the DB.
//
// For failed and interrupted transitions, errMsg is written to the runs.error column.
// It is safe for concurrent calls from multiple goroutines on the same instance.
func (sm *RunStateMachine) Transition(ctx context.Context, next model.RunStatus, errMsg string, opts ...TransitionOption) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	from := sm.current
	expectedVersion := sm.version

	// IsLegalTransition operates purely on in-memory state. It detects within-
	// instance races (multiple goroutines on the same RunStateMachine). The CAS
	// guard on version detects cross-instance races (two separate RunStateMachine
	// instances on the same DB row).
	if !runstate.IsLegalTransition(from, next) {
		return fmt.Errorf("illegal run status transition: %s → %s", from, next)
	}

	var completedAt *string
	if model.IsTerminalStatus(next) {
		t := time.Now().UTC().Format(time.RFC3339Nano)
		completedAt = &t
	}

	// Apply options before opening the transaction so we can read topts inside.
	var topts transitionOpts
	for _, o := range opts {
		o(&topts)
	}

	tx, err := sm.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transition tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck — Rollback is a no-op after Commit

	qtx := sm.queries.WithTx(tx)

	// CAS UPDATE: bumps version and rejects the write when another writer already
	// advanced the row (rows == 0).
	var rows int64
	if next == model.RunStatusFailed || next == model.RunStatusInterrupted {
		rows, err = qtx.UpdateRunError(ctx, db.UpdateRunErrorParams{
			Status:          string(next),
			Error:           &errMsg,
			CompletedAt:     completedAt,
			ID:              sm.runID,
			ExpectedVersion: expectedVersion,
		})
	} else {
		rows, err = qtx.UpdateRunStatus(ctx, db.UpdateRunStatusParams{
			Status:          string(next),
			CompletedAt:     completedAt,
			ID:              sm.runID,
			ExpectedVersion: expectedVersion,
		})
	}
	if err != nil {
		return fmt.Errorf("persisting run status %s: %w", next, err)
	}
	if rows == 0 {
		// CAS miss: another writer already committed a transition on this row.
		return fmt.Errorf("%w: run %s expected version %d",
			ErrTransitionConflict, sm.runID, expectedVersion)
	}

	// Create the approval_requests DB record when entering waiting_for_approval.
	// This INSERT is inside the same transaction as the status UPDATE, so if it
	// fails the status change is automatically rolled back.
	if next == model.RunStatusWaitingForApproval && topts.approval != nil {
		p := topts.approval
		if _, err := qtx.CreateApprovalRequest(ctx, db.CreateApprovalRequestParams{
			ID:               p.ApprovalID,
			RunID:            sm.runID,
			ToolName:         p.ToolName,
			ProposedInput:    p.ProposedInput,
			ReasoningSummary: "{}",
			ExpiresAt:        p.ExpiresAt,
			CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return fmt.Errorf("creating approval request record: %w", err)
		}
	}

	// Create the feedback_requests DB record when entering waiting_for_feedback.
	// Same transactional guarantee as above.
	if next == model.RunStatusWaitingForFeedback && topts.feedback != nil {
		p := topts.feedback
		// Only store expires_at when a timeout is set. NULL in the DB means no timeout,
		// which the feedback scanner uses to exclude old rows from the expired query.
		var expiresAt *string
		if p.ExpiresAt != "" {
			expiresAt = &p.ExpiresAt
		}
		if _, err := qtx.CreateFeedbackRequest(ctx, db.CreateFeedbackRequestParams{
			ID:            p.FeedbackID,
			RunID:         sm.runID,
			ToolName:      p.ToolName,
			ProposedInput: p.ProposedInput,
			Message:       p.Message,
			ExpiresAt:     expiresAt,
			CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return fmt.Errorf("creating feedback request record: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transition tx: %w", err)
	}

	// Only advance in-memory state after the commit succeeds. Updating before
	// commit would leave the state machine believing a transition landed even if
	// the commit fails, causing the next Transition to use a stale version.
	sm.current = next
	sm.version = expectedVersion + 1

	// Update runs_active gauge based on whether we're entering or leaving a
	// tracked in-flight state. All Inc/Dec of runs_active live exclusively here
	// to prevent double-dec bugs — BoundAgent.Run does not touch the gauge.
	if trackedForActive(from) && !trackedForActive(next) {
		// Leaving a tracked state for a terminal (e.g. running → complete,
		// running → failed, waiting_for_approval → failed). Dec only.
		runsActive.WithLabelValues(string(from)).Dec()
	} else if !trackedForActive(from) && trackedForActive(next) {
		// Entering a tracked state from pending (pending → running). Inc only.
		runsActive.WithLabelValues(string(next)).Inc()
	} else if trackedForActive(from) && trackedForActive(next) {
		// Swap between tracked states (running → waiting_for_*, waiting_for_* → running).
		// Balanced Dec + Inc.
		runsActive.WithLabelValues(string(from)).Dec()
		runsActive.WithLabelValues(string(next)).Inc()
	}
	// pending → failed (pre-flight) is neither tracked state: no-op, as expected.

	runstate.RecordTransition(from, next)

	logctx.Logger(ctx).InfoContext(ctx, "run status transition", "from", string(from), "to", string(next))

	if sm.publisher != nil {
		data, err := json.Marshal(map[string]string{"run_id": sm.runID, "status": string(next)})
		if err != nil {
			return fmt.Errorf("marshal publish payload: %w", err)
		}
		sm.publisher.Publish("run.status_changed", data)
	}

	if sm.publisher != nil && topts.approval != nil {
		eventData, err := json.Marshal(map[string]string{"approval_id": topts.approval.ApprovalID, "run_id": sm.runID})
		if err != nil {
			return fmt.Errorf("marshal approval.created payload: %w", err)
		}
		sm.publisher.Publish("approval.created", eventData)
	}

	if sm.publisher != nil && topts.feedback != nil {
		eventData, err := json.Marshal(map[string]string{"feedback_id": topts.feedback.FeedbackID, "run_id": sm.runID})
		if err != nil {
			return fmt.Errorf("marshal feedback.created payload: %w", err)
		}
		sm.publisher.Publish("feedback.created", eventData)
	}

	return nil
}

// PersistSystemPrompt writes the rendered system prompt to the DB. Non-fatal:
// callers should log a warning on error rather than aborting the run.
func (sm *RunStateMachine) PersistSystemPrompt(ctx context.Context, prompt string) error {
	return sm.queries.UpdateRunSystemPrompt(ctx, db.UpdateRunSystemPromptParams{
		ID:           sm.runID,
		SystemPrompt: &prompt,
	})
}

// Current returns the current run status. Safe for concurrent use.
func (sm *RunStateMachine) Current() model.RunStatus {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.current
}

// Version returns the current optimistic-lock version as known to this instance.
// The value is updated after each successful Transition commit.
func (sm *RunStateMachine) Version() int64 {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.version
}

// Queries returns the db.Queries handle used by the state machine.
// Needed by BoundAgent's timeout branches to perform guarded status updates
// without duplicating DB dependencies in the Config struct.
func (sm *RunStateMachine) Queries() *db.Queries {
	return sm.queries
}
