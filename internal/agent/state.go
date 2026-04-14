package agent

import (
	"context"
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

// RunStateMachine tracks and persists the status of a single agent run.
// It enforces the legal transition graph and writes every transition to the DB.
// Safe for concurrent use.
type RunStateMachine struct {
	runID     string
	current   model.RunStatus
	mu        sync.Mutex
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

// NewRunStateMachine returns a RunStateMachine initialised to the given status.
// The initial status is not written to the DB — the caller is responsible for
// ensuring the DB row already reflects that state.
func NewRunStateMachine(runID string, initial model.RunStatus, queries *db.Queries, opts ...StateMachineOption) *RunStateMachine {
	sm := &RunStateMachine{
		runID:   runID,
		current: initial,
		queries: queries,
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

// Transition validates and persists a run status transition.
// For failed and interrupted transitions, errMsg is written to the runs.error column.
// It is safe for concurrent calls from multiple goroutines.
func (sm *RunStateMachine) Transition(ctx context.Context, next model.RunStatus, errMsg string, opts ...TransitionOption) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	from := sm.current

	if !runstate.IsLegalTransition(from, next) {
		return fmt.Errorf("illegal run status transition: %s → %s", from, next)
	}

	var completedAt *string
	if model.IsTerminalStatus(next) {
		t := time.Now().UTC().Format(time.RFC3339Nano)
		completedAt = &t
	}

	if next == model.RunStatusFailed || next == model.RunStatusInterrupted {
		if err := sm.queries.UpdateRunError(ctx, db.UpdateRunErrorParams{
			Status:      string(next),
			Error:       &errMsg,
			CompletedAt: completedAt,
			ID:          sm.runID,
		}); err != nil {
			return fmt.Errorf("persisting run status %s: %w", next, err)
		}
	} else {
		if err := sm.queries.UpdateRunStatus(ctx, db.UpdateRunStatusParams{
			Status:      string(next),
			CompletedAt: completedAt,
			ID:          sm.runID,
		}); err != nil {
			return fmt.Errorf("persisting run status %s: %w", next, err)
		}
	}

	// Apply options.
	var topts transitionOpts
	for _, o := range opts {
		o(&topts)
	}

	// Create the approval_requests DB record when entering waiting_for_approval.
	if next == model.RunStatusWaitingForApproval && topts.approval != nil {
		p := topts.approval
		if _, err := sm.queries.CreateApprovalRequest(ctx, db.CreateApprovalRequestParams{
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
	if next == model.RunStatusWaitingForFeedback && topts.feedback != nil {
		p := topts.feedback
		// Only store expires_at when a timeout is set. NULL in the DB means no timeout,
		// which the feedback scanner uses to exclude old rows from the expired query.
		var expiresAt *string
		if p.ExpiresAt != "" {
			expiresAt = &p.ExpiresAt
		}
		if _, err := sm.queries.CreateFeedbackRequest(ctx, db.CreateFeedbackRequestParams{
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

	sm.current = next
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

// Queries returns the db.Queries handle used by the state machine.
// Needed by BoundAgent's timeout branches to perform guarded status updates
// without duplicating DB dependencies in the Config struct.
func (sm *RunStateMachine) Queries() *db.Queries {
	return sm.queries
}
