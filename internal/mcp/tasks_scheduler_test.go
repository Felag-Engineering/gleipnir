package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// fakeClientResolver hands back a single pre-built *Client for every
// serverID, ignoring the argument — every scheduler test in this file talks
// to exactly one fake MCP server.
type fakeClientResolver struct {
	client *Client
}

func (f fakeClientResolver) ClientForServerID(ctx context.Context, serverID string) (*Client, error) {
	return f.client, nil
}

// newSchedulerTestTask creates the policy/run/server rows an mcp_tasks row's
// foreign keys require, then inserts the task itself, returning its
// (internal ULID) ID. serverTTL is an RFC3339Nano timestamp or "" for none.
func newSchedulerTestTask(t *testing.T, s *db.Store, kind, serverTaskID, serverTTL string) string {
	t.Helper()
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, s, "r1", "p1", "running")
	testutil.InsertMcpServer(t, s, "srv1", "test-server", "http://example.invalid")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	params := db.CreateMCPTaskParams{
		ID:        "task1",
		RunID:     "r1",
		ServerID:  strPtrTest("srv1"),
		TaskID:    serverTaskID,
		Kind:      kind,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if serverTTL != "" {
		params.ServerTtl = &serverTTL
	}
	if _, err := s.Queries().CreateMCPTask(context.Background(), params); err != nil {
		t.Fatalf("CreateMCPTask: %v", err)
	}
	return params.ID
}

func getMCPTaskStatus(t *testing.T, s *db.Store, id string) string {
	t.Helper()
	task, err := s.Queries().GetMCPTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}
	return task.Status
}

// TestNextPollInterval is the pure-function table test for "poll cadence
// honoring the server interval": a fresh server hint always wins over the
// task's stored creation-time interval, which in turn wins over the
// scheduler's own default.
func TestNextPollInterval(t *testing.T) {
	stored := int64(15000)
	tests := []struct {
		name   string
		task   db.McpTask
		status TaskStatus
		want   time.Duration
	}{
		{
			name:   "server hint wins over everything",
			task:   db.McpTask{PollIntervalMs: &stored},
			status: TaskStatus{PollInterval: 7 * time.Second},
			want:   7 * time.Second,
		},
		{
			name:   "falls back to the task's stored interval when the server sends no hint",
			task:   db.McpTask{PollIntervalMs: &stored},
			status: TaskStatus{},
			want:   15 * time.Second,
		},
		{
			name:   "falls back to the scheduler default when neither is set",
			task:   db.McpTask{},
			status: TaskStatus{},
			want:   defaultTaskPollInterval,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextPollInterval(tc.task, tc.status); got != tc.want {
				t.Errorf("nextPollInterval() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPollScheduler_Scan_HonorsServerPollInterval proves the server's
// pollIntervalMs hint governs re-poll timing end to end: after one Scan
// establishes a 10-minute cadence, a second Scan called immediately
// afterward must NOT poll again (it is nowhere near due). No clock faking is
// needed — two synchronous calls are microseconds apart, nowhere close to
// the 10-minute interval, so this is deterministic without touching timeNow.
func TestPollScheduler_Scan_HonorsServerPollInterval(t *testing.T) {
	s := testutil.NewTestStore(t)
	newSchedulerTestTask(t, s, "tool_call", "remote-task-1", "")

	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			return map[string]any{"taskId": taskID, "status": "working", "pollIntervalMs": 600000}, 0, ""
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	sched := NewPollScheduler(s.Queries(), fakeClientResolver{client: newModernClient(srv)})

	if err := sched.Scan(context.Background()); err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	if got := len(fake.requests); got != 1 {
		t.Fatalf("requests after first Scan = %d, want 1", got)
	}

	if err := sched.Scan(context.Background()); err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	if got := len(fake.requests); got != 1 {
		t.Errorf("requests after second Scan = %d, want 1 (task not due yet under the 10m server-declared interval)", got)
	}
}

// TestPollScheduler_Scan_ResumesPersistedHandlesOnBoot proves boot resume:
// a task row that was never previously seen by this (freshly-constructed)
// scheduler is polled on the very first Scan — modeling a task that was
// persisted before a host restart.
func TestPollScheduler_Scan_ResumesPersistedHandlesOnBoot(t *testing.T) {
	s := testutil.NewTestStore(t)
	newSchedulerTestTask(t, s, "channel_request", "remote-task-1", "")

	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			return map[string]any{"taskId": taskID, "status": "working", "pollIntervalMs": 30000}, 0, ""
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	sched := NewPollScheduler(s.Queries(), fakeClientResolver{client: newModernClient(srv)})
	if err := sched.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if got := len(fake.RequestsFor(methodTasksGet)); got != 1 {
		t.Fatalf("tasks/get requests = %d, want 1 (boot resume must poll immediately)", got)
	}
	if got := getMCPTaskStatus(t, s, "task1"); got != "working" {
		t.Errorf("status = %q, want working (still non-terminal)", got)
	}
}

// TestPollScheduler_PollNow_BypassesTheScheduledInterval proves the
// poll-on-signal seam: once a task is scheduled far in the future by a Scan
// establishing a long server-declared interval, a plain Scan does not poll
// it again, but PollNow does — regardless of due-ness.
func TestPollScheduler_PollNow_BypassesTheScheduledInterval(t *testing.T) {
	s := testutil.NewTestStore(t)
	newSchedulerTestTask(t, s, "tool_call", "remote-task-1", "")

	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			return map[string]any{"taskId": taskID, "status": "working", "pollIntervalMs": 600000}, 0, ""
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	sched := NewPollScheduler(s.Queries(), fakeClientResolver{client: newModernClient(srv)})

	if err := sched.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if err := sched.Scan(context.Background()); err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	if got := len(fake.requests); got != 1 {
		t.Fatalf("requests after two Scans = %d, want 1 (not due)", got)
	}

	if err := sched.PollNow(context.Background(), "task1"); err != nil {
		t.Fatalf("PollNow: %v", err)
	}
	if got := len(fake.requests); got != 2 {
		t.Errorf("requests after PollNow = %d, want 2 (PollNow must bypass the schedule)", got)
	}
}

// TestPollScheduler_Cancel_PropagatesToServerAndResolvesRow proves cancel
// propagation: Cancel calls tasks/cancel on the task's server, and the DB row
// transitions to cancelled with the ErrTaskCanceled outcome delivered to
// OnResolved.
func TestPollScheduler_Cancel_PropagatesToServerAndResolvesRow(t *testing.T) {
	s := testutil.NewTestStore(t)
	newSchedulerTestTask(t, s, "tool_call", "remote-task-1", "")

	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			if method != methodTasksCancel {
				t.Fatalf("unexpected method %q, want tasks/cancel", method)
			}
			return map[string]any{"taskId": taskID, "status": "cancelled"}, 0, ""
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var resolvedTask db.McpTask
	var resolvedErr error
	sched := NewPollScheduler(s.Queries(), fakeClientResolver{client: newModernClient(srv)},
		WithOnResolved(func(ctx context.Context, task db.McpTask, err error) {
			resolvedTask = task
			resolvedErr = err
		}),
	)

	if err := sched.Cancel(context.Background(), "task1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if got := len(fake.requests); got != 1 {
		t.Fatalf("requests = %d, want 1 (tasks/cancel must reach the server)", got)
	}
	if got := getMCPTaskStatus(t, s, "task1"); got != "cancelled" {
		t.Errorf("status = %q, want cancelled", got)
	}
	if resolvedTask.ID != "task1" {
		t.Errorf("OnResolved task.ID = %q, want task1", resolvedTask.ID)
	}
	if !errors.Is(resolvedErr, ErrTaskCanceled) {
		t.Errorf("OnResolved err = %v, want ErrTaskCanceled", resolvedErr)
	}

	// A second Cancel on an already-terminal task is a silent no-op — no
	// second tasks/cancel call.
	if err := sched.Cancel(context.Background(), "task1"); err != nil {
		t.Fatalf("second Cancel: %v", err)
	}
	if got := len(fake.requests); got != 1 {
		t.Errorf("requests after second Cancel = %d, want 1 (already-terminal task must not be re-cancelled)", got)
	}
}

// TestPollScheduler_TTLExpiry_IsADistinctTypedFailure proves spec §6.3/§6.5:
// a task whose server-declared TTL has elapsed is resolved as "expired" — a
// status distinct from "failed" — and OnResolved receives ErrTaskExpired,
// which does not satisfy errors.Is against a *TaskFailedError or
// ErrTaskCanceled. The TTL check happens before any network call, so the
// fake server here must never receive a tasks/get request at all.
func TestPollScheduler_TTLExpiry_IsADistinctTypedFailure(t *testing.T) {
	s := testutil.NewTestStore(t)
	pastTTL := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	newSchedulerTestTask(t, s, "channel_request", "remote-task-1", pastTTL)

	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			t.Fatalf("unexpected %s call: TTL expiry must be detected before contacting the server", method)
			return nil, 0, ""
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var resolvedErr error
	sched := NewPollScheduler(s.Queries(), fakeClientResolver{client: newModernClient(srv)},
		WithOnResolved(func(ctx context.Context, task db.McpTask, err error) {
			resolvedErr = err
		}),
	)

	if err := sched.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if got := getMCPTaskStatus(t, s, "task1"); got != "expired" {
		t.Errorf("status = %q, want expired (distinct from failed)", got)
	}
	if !errors.Is(resolvedErr, ErrTaskExpired) {
		t.Errorf("OnResolved err = %v, want ErrTaskExpired", resolvedErr)
	}
	var failedErr *TaskFailedError
	if errors.As(resolvedErr, &failedErr) {
		t.Errorf("ErrTaskExpired must not also satisfy *TaskFailedError, got %v", failedErr)
	}
	if errors.Is(resolvedErr, ErrTaskCanceled) {
		t.Error("ErrTaskExpired must not equal ErrTaskCanceled")
	}
}

// TestPollScheduler_StartWait_ResolvesAndDrains exercises the Start/Wait
// lifecycle end to end: a persisted task resolves via the background scan
// loop, synchronized on the published event (signal-don't-poll) rather than
// a fixed sleep, and Wait() returns cleanly after cancellation.
func TestPollScheduler_StartWait_ResolvesAndDrains(t *testing.T) {
	s := testutil.NewTestStore(t)
	newSchedulerTestTask(t, s, "tool_call", "remote-task-1", "")

	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			return map[string]any{"taskId": taskID, "status": "completed", "result": map[string]any{"ok": true}}, 0, ""
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	pub := &testutil.RecordingPublisher{}
	sched := NewPollScheduler(s.Queries(), fakeClientResolver{client: newModernClient(srv)}, WithTaskPublisher(pub))

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx, 10*time.Millisecond)

	waitForTaskEvent(t, pub, 5*time.Second)
	cancel()
	sched.Wait()

	if got := getMCPTaskStatus(t, s, "task1"); got != "complete" {
		t.Errorf("status = %q, want complete", got)
	}
}

// waitForTaskEvent polls until at least one "mcp_task.resolved" event has
// been published, or fails the test after deadline — the deterministic
// synchronization point for the scheduler's background goroutine, per
// CLAUDE.md "Signal-don't-poll". The deadline is a generous CI-tolerance
// bound, not a real assertion.
func waitForTaskEvent(t *testing.T, pub *testutil.RecordingPublisher, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(pub.EventsByType("mcp_task.resolved")) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for mcp_task.resolved", timeout)
}

// strPtrTest is the pointer helper the nullable server_id column needs
// (migration 0048, #801).
func strPtrTest(s string) *string { return &s }
