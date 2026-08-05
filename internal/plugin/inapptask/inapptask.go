// Package inapptask runs the in-app channel's Request on the SAME task
// lifecycle a plugin-routed Request uses (ADR-055, mcp-realignment-spec.md
// §6.4), minus the MCP hop.
//
// The design claim being honored is one sentence from the spec: *the in-app
// channel is modeled on the same internal task lifecycle so every Request has
// one shape regardless of route.* One shape is not a tidiness preference — it
// is what makes a single resolution path, a single audit record shape, and a
// single restart story possible. Two shapes would mean an operator's decision
// meant something subtly different depending on where they made it, which is
// exactly the kind of difference an audit trail must not have.
//
// So an in-app Request is a row in `mcp_tasks`, kind `channel_request`, with a
// NULL server_id meaning "resolved internally". It is created, polled,
// completed, and cancelled through the same table and the same terminal
// vocabulary as a task living on a real MCP server.
//
// What is NOT shared, deliberately, is the poll loop. A plugin-routed task is
// polled because the host has no other way to learn the answer; an in-app task
// is completed by an operator inside this process, so the answer is delivered
// directly and immediately (spec §6.4: "no interval wait — resolution is
// immediate, preserving today's UX"). Polling something whose answer arrives
// by function call would add latency for nothing.
//
// The v1.1 gRPC channel path stays live and untouched — this builds the new
// route alongside it. The old one is deleted at cutover (#22).
package inapptask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// KindChannelRequest is the mcp_tasks.kind value for a channel Request,
// matching the table's CHECK constraint. In-app and plugin-routed requests
// share it — that sameness is the point.
const KindChannelRequest = "channel_request"

// Terminal task statuses, matching the mcp_tasks.status CHECK constraint.
const (
	StatusWorking   = "working"
	StatusComplete  = "complete"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusExpired   = "expired"
)

// ErrUnknownTask is returned when no task matches an ID. Callers treat it as a
// benign late-callback signal, the same way the feedback path treats an unknown
// request ID: the wait has already ended one way or another.
var ErrUnknownTask = errors.New("unknown in-app task")

// ErrAlreadyResolved is returned when a task has already reached a terminal
// state. It is distinct from ErrUnknownTask because the two mean different
// things to an operator: "someone else answered this" versus "this never
// existed".
var ErrAlreadyResolved = errors.New("in-app task is already resolved")

// Store is the narrow task-table surface this package needs. *db.Queries
// satisfies it.
type Store interface {
	CreateMCPTask(ctx context.Context, arg db.CreateMCPTaskParams) (db.McpTask, error)
	GetMCPTask(ctx context.Context, id string) (db.McpTask, error)
	ResolveMCPTask(ctx context.Context, arg db.ResolveMCPTaskParams) (int64, error)
	ListResumableMCPTasks(ctx context.Context) ([]db.McpTask, error)
}

// OpenRequest is one in-app ask.
type OpenRequest struct {
	RunID string

	// Message is what the operator is being asked.
	Message string

	// Options are the choices offered, mirroring the channel extension's
	// elicitation-shaped payload. Empty when RequestedSchema carries the ask.
	Options []Option

	// RequestedSchema is the JSON Schema of a form, when the ask needs typed
	// values rather than a choice.
	RequestedSchema json.RawMessage

	// Kind is the elicitation kind, which decides which role may answer
	// (model.ElicitationKind.RequiredRole).
	Kind model.ElicitationKind
}

// Option is one choice an operator may pick.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Resolution is what an operator decided — the same shape a plugin-routed
// channel task returns, so a consumer cannot tell the routes apart.
type Resolution struct {
	// OptionID is the chosen option, for a pick-one ask.
	OptionID string `json:"optionId,omitempty"`

	// Content is the submitted form payload, for a schema ask.
	Content json.RawMessage `json:"content,omitempty"`

	// ActorExternalID identifies who acted. For the in-app channel this is the
	// Gleipnir user ID — the strongest actor identity in the system, since the
	// person authenticated to this host rather than to a third party. That is
	// why the in-app channel's assurance is `authenticated`.
	ActorExternalID string `json:"actorExternalId"`
}

// Payload is what an in-app task carries while it waits, persisted in the
// task's result column only once resolved. The question itself lives here so a
// restart can re-render it without the caller having to keep it in memory.
type Payload struct {
	Message         string          `json:"message"`
	Options         []Option        `json:"options,omitempty"`
	RequestedSchema json.RawMessage `json:"requestedSchema,omitempty"`
	Kind            string          `json:"kind,omitempty"`
}

// timeNow is the package's injectable clock (CLAUDE.md "Testing time-dependent
// code"). Tests swap it via t.Cleanup and must not call t.Parallel().
var timeNow = func() time.Time { return time.Now() }

// Manager owns in-app task creation and resolution.
//
// It holds an in-memory waiter per open task so a completion delivers
// immediately. The waiter is an optimization over the durable row, never a
// substitute for it: a host that dies loses the waiter and keeps the row, and
// the answer an operator submits afterwards still lands.
type Manager struct {
	store Store

	mu      sync.Mutex
	waiters map[string]chan Resolution
}

func NewManager(store Store) *Manager {
	return &Manager{store: store, waiters: make(map[string]chan Resolution)}
}

// TaskHandle identifies an open in-app task.
type TaskHandle struct {
	// ID is the mcp_tasks row ID.
	ID string

	// TaskID is the task identifier. For an internal task the host mints it,
	// where a plugin-routed task would carry the server's. Same field, same
	// meaning to every consumer.
	TaskID string
}

// Open creates the durable task record and registers its waiter.
//
// Registration happens BEFORE the row is visible to anything that could answer
// it, so an answer arriving immediately after creation is never lost — the same
// invariant the feedback and tool-input paths maintain.
func (m *Manager) Open(ctx context.Context, req OpenRequest) (TaskHandle, error) {
	if req.RunID == "" {
		return TaskHandle{}, fmt.Errorf("in-app task: run ID is required")
	}
	if req.Message == "" {
		return TaskHandle{}, fmt.Errorf("in-app task: message is empty; there is nothing to ask")
	}
	if len(req.Options) == 0 && len(req.RequestedSchema) == 0 {
		// Same rule the channel extension's client enforces: neither a choice
		// nor a form is not a question, and would leave a task open forever
		// waiting for an answer the operator has no way to give.
		return TaskHandle{}, fmt.Errorf("in-app task: request carries neither options nor a requestedSchema")
	}

	id := model.NewULID()
	taskID := model.NewULID()
	now := timeNow().UTC().Format(time.RFC3339Nano)

	m.register(id)

	if _, err := m.store.CreateMCPTask(ctx, db.CreateMCPTaskParams{
		ID:    id,
		RunID: req.RunID,
		// NULL: resolved internally, no MCP server behind it.
		ServerID: nil,
		TaskID:   taskID,
		Kind:     KindChannelRequest,
		// No poll interval and no server TTL: nothing polls an in-app task,
		// and no server owns a clock over it. The §6.3 policy deadline still
		// governs the human leg, exactly as it does for every other route.
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		m.unregister(id)
		return TaskHandle{}, fmt.Errorf("create in-app task: %w", err)
	}

	return TaskHandle{ID: id, TaskID: taskID}, nil
}

// Complete records an operator's decision and delivers it to the waiter.
//
// The durable write happens first. A delivery without a committed row would
// let a run resume on an answer that a crash could erase, which is the one
// ordering that can lose a decision a human actually made.
func (m *Manager) Complete(ctx context.Context, taskID string, r Resolution) error {
	encoded, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal in-app resolution: %w", err)
	}
	result := string(encoded)
	now := timeNow().UTC().Format(time.RFC3339Nano)

	rows, err := m.store.ResolveMCPTask(ctx, db.ResolveMCPTaskParams{
		Status:    StatusComplete,
		Result:    &result,
		UpdatedAt: now,
		ID:        taskID,
	})
	if err != nil {
		return fmt.Errorf("resolve in-app task %s: %w", taskID, err)
	}
	if rows == 0 {
		// The CAS lost: the task was already terminal. Distinguish "already
		// answered" from "never existed" so the API can say which.
		if _, getErr := m.store.GetMCPTask(ctx, taskID); getErr != nil {
			return ErrUnknownTask
		}
		return ErrAlreadyResolved
	}

	m.deliver(taskID, r)
	return nil
}

// Cancel terminates an open task without an answer.
func (m *Manager) Cancel(ctx context.Context, taskID string) error {
	now := timeNow().UTC().Format(time.RFC3339Nano)
	rows, err := m.store.ResolveMCPTask(ctx, db.ResolveMCPTaskParams{
		Status:    StatusCancelled,
		UpdatedAt: now,
		ID:        taskID,
	})
	if err != nil {
		return fmt.Errorf("cancel in-app task %s: %w", taskID, err)
	}
	if rows == 0 {
		if _, getErr := m.store.GetMCPTask(ctx, taskID); getErr != nil {
			return ErrUnknownTask
		}
		return ErrAlreadyResolved
	}
	m.close(taskID)
	return nil
}

// Await blocks until the task is resolved, the context is cancelled, or the
// deadline passes.
//
// It returns as soon as Complete delivers — this is the PollNow semantics the
// spec asks for. There is no interval, because there is nothing to poll: the
// answer arrives by function call inside this process.
func (m *Manager) Await(ctx context.Context, handle TaskHandle, deadline time.Duration) (Resolution, error) {
	m.mu.Lock()
	ch, ok := m.waiters[handle.ID]
	m.mu.Unlock()
	if !ok {
		// No waiter: this process did not open the task, which after a restart
		// is the normal case. Fall back to the durable row — the answer, if
		// one landed, is there.
		return m.readResolved(ctx, handle.ID)
	}

	timer := time.NewTimer(deadline)
	defer timer.Stop()

	select {
	case resolution := <-ch:
		return resolution, nil
	case <-timer.C:
		return Resolution{}, fmt.Errorf("in-app task %s: no operator answered within %s", handle.ID, deadline)
	case <-ctx.Done():
		return Resolution{}, fmt.Errorf("in-app task %s: %w", handle.ID, ctx.Err())
	}
}

// readResolved reads a terminal answer straight from the row. This is the
// restart path: the waiter died with the process, the row did not.
func (m *Manager) readResolved(ctx context.Context, id string) (Resolution, error) {
	task, err := m.store.GetMCPTask(ctx, id)
	if err != nil {
		return Resolution{}, ErrUnknownTask
	}
	return DecodeResolution(task)
}

// DecodeResolution reads a completed task's answer.
//
// It works on ANY channel-request task row, internal or plugin-routed, which is
// the shared-shape claim made executable: a consumer decoding a decision never
// has to know which route produced it.
func DecodeResolution(task db.McpTask) (Resolution, error) {
	switch task.Status {
	case StatusComplete:
	case StatusWorking:
		return Resolution{}, fmt.Errorf("task %s has not been answered yet", task.ID)
	default:
		// Cancelled, failed, expired: terminal without a decision. Reading one
		// as an empty resolution would read a non-answer as an answer.
		return Resolution{}, fmt.Errorf("task %s ended as %q without a decision", task.ID, task.Status)
	}
	if task.Result == nil {
		return Resolution{}, fmt.Errorf("task %s completed with no result payload", task.ID)
	}

	var r Resolution
	if err := json.Unmarshal([]byte(*task.Result), &r); err != nil {
		return Resolution{}, fmt.Errorf("task %s result does not parse: %w", task.ID, err)
	}
	if r.OptionID == "" && len(r.Content) == 0 {
		return Resolution{}, fmt.Errorf("task %s result carries neither optionId nor content", task.ID)
	}
	return r, nil
}

// IsInternal reports whether a task is resolved in-process rather than on an
// MCP server. It is the one place a consumer legitimately cares which route a
// task took — to decide whether to poll it.
func IsInternal(task db.McpTask) bool { return task.ServerID == nil }

// Resumable returns the open in-app tasks, for re-arming waits after a restart.
//
// The rows survive a restart on their own; what does not is this process's
// waiter map. Re-registering means an operator answering after a restart still
// delivers immediately rather than waiting for something to notice.
func (m *Manager) Resumable(ctx context.Context) ([]db.McpTask, error) {
	all, err := m.store.ListResumableMCPTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list resumable tasks: %w", err)
	}
	var out []db.McpTask
	for _, task := range all {
		if IsInternal(task) && task.Kind == KindChannelRequest {
			out = append(out, task)
		}
	}
	return out, nil
}

// Rearm registers a waiter for a task this process did not open. Idempotent.
func (m *Manager) Rearm(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.waiters[id]; !ok {
		m.waiters[id] = make(chan Resolution, 1)
	}
}

func (m *Manager) register(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waiters[id] = make(chan Resolution, 1)
}

func (m *Manager) unregister(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.waiters, id)
}

// deliver hands the resolution to the waiter and drops the registration so a
// second answer cannot arrive. The send is outside the lock: the channel is
// buffered with a single reader, so it never blocks.
func (m *Manager) deliver(id string, r Resolution) {
	m.mu.Lock()
	ch, ok := m.waiters[id]
	delete(m.waiters, id)
	m.mu.Unlock()
	if ok {
		ch <- r
	}
}

// close drops a waiter without delivering — the cancel path. The waiting side
// sees its deadline or its context, which is correct: a cancelled task has no
// answer to hand over.
func (m *Manager) close(id string) { m.unregister(id) }
