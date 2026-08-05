// Restart-surviving poll scheduler over the mcp_tasks table (spec §6.4,
// §13). Both a long-running MRTR tool call and a channel Request resolve as
// durable Tasks-extension tasks (Amendment 1), so PollScheduler knows nothing
// about tool calls, channels, or runs — it only ever sees mcp_tasks rows and
// the Tasks-extension client (tasks.go). Wiring a resolved task back into run
// state is later-milestone work; doing that here would require importing
// internal/execution, which this package must never do.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
)

// ErrTaskExpired reports that a Tasks-extension task's server-declared TTL
// elapsed before the task reached a terminal state on its own (spec §6.3:
// "server-side TTLs are weather" — recoverable, not a hard refusal). This is
// DELIBERATELY its own sentinel, distinct from TaskFailedError: a later
// milestone (#799, TTL answer replay) keys off exactly this distinction to
// decide whether a stored answer should be replayed against a fresh task
// rather than treated as genuinely refused.
var ErrTaskExpired = errors.New("mcp task expired: server ttl elapsed before a terminal status was reached")

// ErrTaskCanceled reports that a task reached the cancelled terminal state —
// either PollScheduler.Cancel asked the server to cancel it, or the server
// cancelled it independently (observed on a routine poll).
var ErrTaskCanceled = errors.New("mcp task cancelled")

// TaskFailedError reports that a task reached the failed terminal state.
// Message is the server's own bounded, untrusted statusMessage (see
// maxTaskStatusMessageLen, tasks.go) — same bounded-untrusted-string
// discipline as InputRequiredError.Reason (inputrequired.go).
type TaskFailedError struct {
	Message string
}

func (e *TaskFailedError) Error() string {
	return fmt.Sprintf("mcp task failed: %s", e.Message)
}

// defaultTaskPollInterval is the poll cadence PollScheduler falls back to
// when neither the server's most recent response nor the task's own stored
// poll_interval_ms supplies a hint (spec §6.4 "honor server-declared poll
// interval" — this is the fallback used only in that hint's absence, never an
// override of one).
const defaultTaskPollInterval = 30 * time.Second

// TaskStore is the subset of *db.Queries PollScheduler needs. Declared as an
// interface — rather than depending on *db.Queries or *db.Store directly —
// so the scheduler's coupling to internal/db's generated surface is limited
// to exactly the four operations it uses, and so tests can substitute a
// narrower fake; *db.Queries satisfies this structurally, so production
// callers pass store.Queries() unchanged.
type TaskStore interface {
	GetMCPTask(ctx context.Context, id string) (db.McpTask, error)
	ListResumableMCPTasks(ctx context.Context) ([]db.McpTask, error)
	ResolveMCPTask(ctx context.Context, arg db.ResolveMCPTaskParams) (int64, error)
	ExpireMCPTask(ctx context.Context, arg db.ExpireMCPTaskParams) (int64, error)
}

// ClientResolver resolves an mcp_servers row ID to a ready *Client. Satisfied
// by *Registry (ClientForServerID, registry.go); declared here as an
// interface so PollScheduler's tests can fake server resolution without
// standing up a full Registry backed by a real DB.
type ClientResolver interface {
	ClientForServerID(ctx context.Context, serverID string) (*Client, error)
}

// SchedulerOption configures a PollScheduler.
type SchedulerOption func(*PollScheduler)

// WithTaskPublisher sets an optional SSE-style publisher; PollScheduler emits
// "mcp_task.resolved" whenever a task reaches a terminal DB status
// (complete/failed/cancelled/expired).
func WithTaskPublisher(p event.Publisher) SchedulerOption {
	return func(s *PollScheduler) { s.publisher = p }
}

// WithOnResolved sets an optional hook invoked once per task resolution,
// after the DB row has been transitioned to a terminal status. err is nil for
// a completed task, and one of ErrTaskCanceled, *TaskFailedError, or
// ErrTaskExpired otherwise. This is the seam a later milestone uses to route
// a resolved task back into run state (waiting_for_feedback →
// running/failed) without PollScheduler itself knowing anything about runs.
func WithOnResolved(fn func(ctx context.Context, task db.McpTask, err error)) SchedulerOption {
	return func(s *PollScheduler) { s.onResolved = fn }
}

// PollScheduler polls every non-terminal MCP Tasks-extension task on its own
// server-declared cadence, resuming every handle still open in the DB on
// boot — the durability claim spec §13 makes explicit ("persisted
// requestState/task handles + channel-Request task re-polling survive
// restarts").
//
// PollScheduler keeps no persisted bookkeeping of its own beyond mcp_tasks
// itself: due-ness is tracked in an in-memory map keyed by mcp_tasks.id. A
// task not yet in that map is always treated as due — this is what makes
// "resume on boot" fall out of the normal Scan loop rather than needing a
// special first-run code path: the process's first Scan after a restart
// finds every persisted non-terminal row absent from a freshly-constructed
// (empty) map, so every one of them is polled immediately.
type PollScheduler struct {
	store      TaskStore
	resolver   ClientResolver
	publisher  event.Publisher
	onResolved func(ctx context.Context, task db.McpTask, err error)

	mu       sync.Mutex
	nextPoll map[string]time.Time // mcp_tasks.id -> next time this task is due
	wg       sync.WaitGroup
}

// NewPollScheduler returns a PollScheduler backed by store for task
// bookkeeping and resolver for reaching the MCP server that owns each task.
func NewPollScheduler(store TaskStore, resolver ClientResolver, opts ...SchedulerOption) *PollScheduler {
	s := &PollScheduler{
		store:    store,
		resolver: resolver,
		nextPoll: make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start launches the background scan goroutine on tickInterval — the
// housekeeping cadence at which Scan checks whether any resumable task is
// due, NOT the per-task poll cadence itself (that lives in s.nextPoll,
// seeded from the server's declared pollIntervalMs or
// defaultTaskPollInterval). tickInterval should be shorter than the
// shortest realistic per-task interval so a task is polled close to its
// actual due time. Mirrors internal/timeout.Scanner's Start/Wait shape.
func (s *PollScheduler) Start(ctx context.Context, tickInterval time.Duration) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.Scan(ctx); err != nil {
					slog.ErrorContext(ctx, "mcp task poll scheduler error", "err", err)
				}
			}
		}
	}()
}

// Wait blocks until the background scan goroutine has exited. Call after
// cancelling the context passed to Start to drain an in-flight scan cleanly
// during shutdown. Safe to call when Start was never called, and safe to
// call more than once.
func (s *PollScheduler) Wait() {
	s.wg.Wait()
}

// Scan resumes every currently-non-terminal task (ListResumableMCPTasks) and
// polls each one that is due. Exported so Start's ticker and tests share one
// code path — tests drive this synchronously instead of waiting on the
// background ticker.
func (s *PollScheduler) Scan(ctx context.Context) error {
	tasks, err := s.store.ListResumableMCPTasks(ctx)
	if err != nil {
		return fmt.Errorf("list resumable mcp tasks: %w", err)
	}

	now := timeNow()
	for _, task := range tasks {
		s.mu.Lock()
		due, tracked := s.nextPoll[task.ID]
		s.mu.Unlock()
		if tracked && now.Before(due) {
			continue
		}
		s.pollOne(ctx, task)
	}
	return nil
}

// PollNow triggers an immediate, out-of-band poll of taskID, bypassing its
// normal interval wait entirely. This is the spec §6.4 Amendment 1
// poll-on-signal hook: the click-time AuthorizeActor host callback (a later
// milestone) calls this so a channel Request resolves at the speed of the
// click rather than waiting for the next scheduled tick. It is a
// synchronous, one-shot poll of a single task and does not require Start's
// background loop to be running.
func (s *PollScheduler) PollNow(ctx context.Context, taskID string) error {
	task, err := s.store.GetMCPTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get mcp task %q: %w", taskID, err)
	}
	if task.Status != "working" && task.Status != "input_required" {
		// Already terminal or expired -- nothing left to poll.
		return nil
	}
	s.pollOne(ctx, task)
	return nil
}

// Cancel asks task's server to terminate it (tasks/cancel) and resolves the
// DB row accordingly. A task that is already terminal is a silent no-op,
// matching the CAS discipline every other resolution path in this file
// follows (internal/timeout.Scanner's resolveTimeout is the precedent).
func (s *PollScheduler) Cancel(ctx context.Context, taskID string) error {
	task, err := s.store.GetMCPTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get mcp task %q: %w", taskID, err)
	}
	if task.Status != "working" && task.Status != "input_required" {
		return nil
	}

	if task.ServerID == nil {
		// An internal task (spec §6.4: the in-app channel runs the same
		// lifecycle with no MCP hop). There is no server to send tasks/cancel
		// to, so cancellation is purely the local row transition.
		s.finalize(ctx, task, "cancelled", nil, ErrTaskCanceled)
		return nil
	}

	client, err := s.resolver.ClientForServerID(ctx, *task.ServerID)
	if err != nil {
		return fmt.Errorf("resolve client for task %q: %w", taskID, err)
	}

	status, err := client.CancelTask(ctx, task.TaskID)
	if err != nil {
		return fmt.Errorf("tasks/cancel for task %q: %w", taskID, err)
	}

	if status.Status.Terminal() {
		// The server's own status governs which terminal state we record —
		// it may answer "failed" instead of "cancelled" if cancellation raced
		// completion.
		s.resolveTerminal(ctx, task, status)
		return nil
	}
	// The server answered a non-terminal status to a cancel request. The
	// host asked for termination and must not leave the row open forever
	// waiting for a status the server may never send, so it is recorded as
	// cancelled here regardless.
	s.finalize(ctx, task, "cancelled", nil, ErrTaskCanceled)
	return nil
}

// pollOne is the shared poll implementation behind Scan and PollNow: check
// TTL first, then call tasks/get, then act on the result.
func (s *PollScheduler) pollOne(ctx context.Context, task db.McpTask) {
	if s.expireIfPastTTL(ctx, task) {
		return
	}

	if task.ServerID == nil {
		// Internal tasks are resolved by an operator acting in the UI, not by
		// polling a server that does not exist. Rescheduling one would spin
		// forever; dropping it from the schedule is correct because the
		// in-app completion path drives it directly.
		return
	}

	client, err := s.resolver.ClientForServerID(ctx, *task.ServerID)
	if err != nil {
		slog.WarnContext(ctx, "mcp task poll: resolve server client failed",
			"task_id", task.ID, "server_id", *task.ServerID, "err", err)
		s.scheduleNext(task.ID, defaultTaskPollInterval)
		return
	}

	status, err := client.GetTask(ctx, task.TaskID)
	if err != nil {
		slog.WarnContext(ctx, "mcp task poll: tasks/get failed", "task_id", task.ID, "err", err)
		s.scheduleNext(task.ID, defaultTaskPollInterval)
		return
	}

	s.applyStatus(ctx, task, status)
}

// applyStatus reschedules a non-terminal task at its (server-hinted or
// default) interval, or resolves a terminal one.
func (s *PollScheduler) applyStatus(ctx context.Context, task db.McpTask, status TaskStatus) {
	if !status.Status.Terminal() {
		s.scheduleNext(task.ID, nextPollInterval(task, status))
		return
	}
	s.resolveTerminal(ctx, task, status)
}

// nextPollInterval picks the cadence for task's next poll: the server's
// just-returned pollIntervalMs hint takes precedence (spec §6.4 "honor
// server-declared poll interval"), falling back to the interval recorded at
// task-creation time (mcp_tasks.poll_interval_ms), and finally to
// defaultTaskPollInterval when neither is set.
func nextPollInterval(task db.McpTask, status TaskStatus) time.Duration {
	if status.PollInterval > 0 {
		return status.PollInterval
	}
	if task.PollIntervalMs != nil && *task.PollIntervalMs > 0 {
		return time.Duration(*task.PollIntervalMs) * time.Millisecond
	}
	return defaultTaskPollInterval
}

// resolveTerminal maps a terminal TaskStatus onto an mcp_tasks status plus a
// typed outcome error, and finalizes it.
func (s *PollScheduler) resolveTerminal(ctx context.Context, task db.McpTask, status TaskStatus) {
	var (
		dbStatus string
		outcome  error
	)
	switch status.Status {
	case TaskStatusCompleted:
		dbStatus = "complete"
	case TaskStatusFailed:
		dbStatus = "failed"
		outcome = &TaskFailedError{Message: status.StatusMessage}
	case TaskStatusCancelled:
		dbStatus = "cancelled"
		outcome = ErrTaskCanceled
	default:
		// status.Status.Terminal() only returns true for the three cases
		// above, so this is unreachable in practice -- guarded rather than
		// assumed, matching this package's "never trust a branch implicitly"
		// posture elsewhere (decodeResultType, decodeTaskStatusValue).
		slog.WarnContext(ctx, "mcp task poll: terminal status with no mapping", "task_id", task.ID, "status", status.Status)
		s.scheduleNext(task.ID, defaultTaskPollInterval)
		return
	}
	s.finalize(ctx, task, dbStatus, status.Result, outcome)
}

// expireIfPastTTL resolves task to the host-only "expired" status when its
// server-declared TTL has elapsed without the task reaching a terminal state
// on its own (spec §6.3 "server-side TTLs are weather"). Returns true when
// task's TTL was checked and found expired (whether or not this call is the
// one that actually claimed the expiry) -- either way, task must not be
// polled again this tick.
func (s *PollScheduler) expireIfPastTTL(ctx context.Context, task db.McpTask) bool {
	if task.ServerTtl == nil {
		return false
	}
	ttl, err := time.Parse(time.RFC3339Nano, *task.ServerTtl)
	if err != nil {
		// A malformed stored TTL is not this scheduler's problem to repair;
		// treat it as "no TTL" rather than failing the poll outright.
		return false
	}
	if !timeNow().After(ttl) {
		return false
	}

	rows, err := s.store.ExpireMCPTask(ctx, db.ExpireMCPTaskParams{
		UpdatedAt: timeNow().UTC().Format(time.RFC3339Nano),
		ID:        task.ID,
	})
	if err != nil {
		slog.WarnContext(ctx, "mcp task poll: expire failed", "task_id", task.ID, "err", err)
		return true
	}
	s.forget(task.ID)
	if rows == 1 {
		s.notify(ctx, task, ErrTaskExpired)
	}
	return true
}

// finalize resolves task to dbStatus/result via the CAS-guarded
// ResolveMCPTask, stops tracking it, and — only on the write that actually
// won the CAS (rows == 1) — notifies. rows == 0 means another writer (e.g. a
// concurrent Cancel racing a Scan tick) already resolved this task; skipping
// the notify there prevents a duplicate resolution signal, mirroring
// internal/timeout.Scanner.resolveTimeout's identical guard.
func (s *PollScheduler) finalize(ctx context.Context, task db.McpTask, dbStatus string, result json.RawMessage, outcome error) {
	var resultPtr *string
	if len(result) > 0 {
		r := string(result)
		resultPtr = &r
	}

	rows, err := s.store.ResolveMCPTask(ctx, db.ResolveMCPTaskParams{
		Status:    dbStatus,
		Result:    resultPtr,
		UpdatedAt: timeNow().UTC().Format(time.RFC3339Nano),
		ID:        task.ID,
	})
	if err != nil {
		slog.WarnContext(ctx, "mcp task poll: resolve failed", "task_id", task.ID, "err", err)
		return
	}
	s.forget(task.ID)
	if rows == 0 {
		return
	}
	s.notify(ctx, task, outcome)
}

// notify publishes the "mcp_task.resolved" SSE-style event (if a publisher
// is configured) and invokes the OnResolved hook (if one is configured).
func (s *PollScheduler) notify(ctx context.Context, task db.McpTask, outcome error) {
	if s.publisher != nil {
		payload, err := json.Marshal(map[string]string{
			"task_id": task.ID,
			"run_id":  task.RunID,
			"kind":    task.Kind,
		})
		if err == nil {
			s.publisher.Publish("mcp_task.resolved", payload)
		}
	}
	if s.onResolved != nil {
		s.onResolved(ctx, task, outcome)
	}
}

func (s *PollScheduler) scheduleNext(taskID string, in time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextPoll[taskID] = timeNow().Add(in)
}

func (s *PollScheduler) forget(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nextPoll, taskID)
}
