// Package run — this file bridges mcp.PollScheduler's WithOnResolved hook
// (internal/mcp/tasks_scheduler.go) into a per-task-row wait: the
// plugin-routed counterpart to inapptask.Manager's in-memory waiter map.
//
// TaskWaiter knows nothing about approvals, feedback, or audiences — it only
// ever sees an mcp_tasks row ID and a terminal outcome. That is deliberate:
// #995 (tool-initiated HITL routed through audiences) reuses this file
// unchanged, because waiting for a task's terminal state is exactly the same
// problem whether the ask came from a policy gate or a tool's own
// elicitation.
package run

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
)

// timeNow is this package's injectable clock (CLAUDE.md "Testing
// time-dependent code"). Shared with channel_adapter.go's waitDuration, so a
// test can freeze the whole dispatch-and-wait path on one fake clock. Tests
// swap it via t.Cleanup and must not call t.Parallel().
var timeNow = func() time.Time { return time.Now() }

// ErrTaskWaitTimeout reports that no terminal task resolution arrived before
// the caller's own deadline.
//
// This is deliberately distinct from mcp.ErrTaskExpired: that one is the
// SERVER's declared TTL elapsing, while this is the caller's clock — the
// policy timeout, which spec §6.3 makes authoritative over the task TTL. A
// task can still be sitting "working" on the server long after the policy has
// given up on it, and it is this timeout, not the server's, that ends the
// wait and triggers the Cancel that follows it.
var ErrTaskWaitTimeout = errors.New("mcp task wait: no terminal resolution before the caller's deadline")

// TaskCanceler is the mcp.PollScheduler surface TaskWaiter needs to cancel a
// task it is waiting on. *mcp.PollScheduler satisfies it.
type TaskCanceler interface {
	Cancel(ctx context.Context, taskID string) error
}

// TaskGetter is the mcp_tasks read TaskWaiter needs to close the
// registration race documented on Wait. *db.Queries satisfies it.
type TaskGetter interface {
	GetMCPTask(ctx context.Context, id string) (db.McpTask, error)
}

// taskResolution is what one waiter receives once its task reaches a
// terminal DB status.
type taskResolution struct {
	task db.McpTask
	err  error
}

// TaskWaiter delivers mcp_tasks resolutions into a waiter map keyed by the
// row's own ID (mcp_tasks.id — the same identifier hitl.Routed.RowID names,
// and the same one mcp.PollScheduler's own GetTask/Cancel/PollNow accept as
// "taskID").
//
// Register it with a PollScheduler via:
//
//	waiter := NewTaskWaiter(scheduler, store.Queries())
//	scheduler := mcp.NewPollScheduler(store.Queries(), resolver, mcp.WithOnResolved(waiter.OnResolved))
//
// (the scheduler is threaded back into the waiter's TaskCanceler after
// construction — see #962's assembly wiring, which owns both lifetimes.)
type TaskWaiter struct {
	canceler TaskCanceler
	getter   TaskGetter

	mu      sync.Mutex
	waiters map[string]chan taskResolution
}

// NewTaskWaiter constructs a TaskWaiter over canceler (typically the
// *mcp.PollScheduler this waiter is registered with) and getter (typically
// store.Queries()).
func NewTaskWaiter(canceler TaskCanceler, getter TaskGetter) *TaskWaiter {
	return &TaskWaiter{
		canceler: canceler,
		getter:   getter,
		waiters:  make(map[string]chan taskResolution),
	}
}

// OnResolved is the mcp.WithOnResolved hook. err is nil for a completed task,
// and one of mcp.ErrTaskCanceled, *mcp.TaskFailedError, or mcp.ErrTaskExpired
// otherwise — passed straight through from the scheduler's own vocabulary
// rather than translated, since this package has nothing to add to it.
//
// task is trusted as-is: PollScheduler.finalize (tasks_scheduler.go) sets
// Status, Result, and UpdatedAt on its own copy before calling notify, so
// this already reflects the write that just committed — a completed task's
// Result is not re-fetched here, deliberately, since a second read could
// only ever confirm what the scheduler already just told us.
func (w *TaskWaiter) OnResolved(_ context.Context, task db.McpTask, err error) {
	w.mu.Lock()
	ch, ok := w.waiters[task.ID]
	if ok {
		delete(w.waiters, task.ID)
	}
	w.mu.Unlock()
	if !ok {
		// Nobody is waiting: the row resolved after the wait already gave up
		// (timeout/cancellation released the registration), or before anyone
		// ever registered one. Either way there is nothing to deliver.
		return
	}
	ch <- taskResolution{task: task, err: err}
}

// Wait blocks for rowID's terminal resolution, the context's cancellation, or
// deadline, whichever comes first.
//
// Registration happens before the post-registration check below, which
// closes the one race this package cannot register its way out of: rowID's
// task can resolve between hitl.Router.Route persisting the row and this call
// registering a waiter for it — a fast plugin, or an operator's click landing
// inside that same instant via the AuthorizeActor poll-now hint. The check
// reads the row directly rather than trusting a channel miss to mean "still
// pending" — the same fallback inapptask.Manager.Await uses when this
// process holds no waiter for a task it did not itself open.
func (w *TaskWaiter) Wait(ctx context.Context, rowID string, deadline time.Duration) (db.McpTask, error) {
	ch := w.register(rowID)

	if task, terminal, outcome := w.checkAlreadyResolved(ctx, rowID); terminal {
		w.unregister(rowID)
		return task, outcome
	}

	timer := time.NewTimer(deadline)
	defer timer.Stop()

	select {
	case res := <-ch:
		return res.task, res.err
	case <-timer.C:
		w.unregister(rowID)
		return db.McpTask{}, ErrTaskWaitTimeout
	case <-ctx.Done():
		w.unregister(rowID)
		return db.McpTask{}, ctx.Err()
	}
}

// checkAlreadyResolved reads rowID directly. terminal is false both when the
// row is genuinely still pending (the normal case) and when the read itself
// failed — a transient read error must never be mistaken for a resolution,
// so the caller falls back to waiting on the channel either way.
func (w *TaskWaiter) checkAlreadyResolved(ctx context.Context, rowID string) (db.McpTask, bool, error) {
	task, err := w.getter.GetMCPTask(ctx, rowID)
	if err != nil {
		return db.McpTask{}, false, nil
	}
	outcome, terminal := taskOutcome(task)
	if !terminal {
		return db.McpTask{}, false, nil
	}
	return task, true, outcome
}

// Cancel sends tasks/cancel for rowID — the §6.3 replacement for the v1
// dispatcher's RequestTerminated notification: an expired policy deadline
// tells the plugin to stop waiting, rather than leaving its own UI showing a
// live prompt for a request the host has already given up on.
func (w *TaskWaiter) Cancel(ctx context.Context, rowID string) error {
	if err := w.canceler.Cancel(ctx, rowID); err != nil {
		return fmt.Errorf("cancel task %s: %w", rowID, err)
	}
	return nil
}

// taskOutcome maps a stored mcp_tasks row's status onto the same outcome
// vocabulary mcp.PollScheduler's WithOnResolved hook reports live — used by
// the registration-race fallback in Wait, where only the row itself (not a
// fresh live status message) is available. terminal is false for "working"
// and "input_required": the row has not settled yet.
func taskOutcome(task db.McpTask) (error, bool) {
	switch task.Status {
	case "complete":
		return nil, true
	case "cancelled":
		return mcp.ErrTaskCanceled, true
	case "expired":
		return mcp.ErrTaskExpired, true
	case "failed":
		// The row itself carries no separate failure-message column (only the
		// terminal `result` blob, which a failed task may leave empty) — this
		// fallback path necessarily loses the message a live OnResolved
		// delivery would have carried in status.StatusMessage.
		return &mcp.TaskFailedError{}, true
	default:
		return nil, false
	}
}

// register allocates rowID's waiter channel. Exactly one Wait call may be
// outstanding for a given rowID at a time — each mcp_tasks row is created
// fresh per ask (hitl.Router.Route mints a new ULID every time), so two
// concurrent Wait calls for the SAME rowID should never happen in practice.
// If it ever did, this would silently replace the first caller's channel
// with a second one nobody delivers to when the first was actually waiting,
// so the first Wait would ride out its own deadline rather than being
// woken; it is documented here rather than guarded (e.g. panicking on a
// pre-existing entry) because the fresh-row-per-ask invariant already makes
// it structurally unreachable from this package's own callers.
func (w *TaskWaiter) register(rowID string) chan taskResolution {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := make(chan taskResolution, 1)
	w.waiters[rowID] = ch
	return ch
}

func (w *TaskWaiter) unregister(rowID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.waiters, rowID)
}
