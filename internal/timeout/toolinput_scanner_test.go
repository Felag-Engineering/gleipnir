// Package timeout_test — toolinput_scanner_test.go covers the tool-initiated
// input scanner (ADR-055, spec §6.3). The generic scan-loop behaviors are
// already pinned by scanner_test.go against the approval backend; what is
// specific here is that this table has an effective deadline rather than a
// policy timeout, and that the scanner is the ONLY thing that can resolve a
// request whose in-process waiter died with its host.
package timeout_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/timeout"
)

// insertToolInputRequest creates a pending row whose effective deadline is
// expiresAt.
func insertToolInputRequest(t *testing.T, store *db.Store, id, runID, expiresAt, deadlineSource string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	source := deadlineSource
	if _, err := store.Queries().CreateToolInputRequest(context.Background(), db.CreateToolInputRequestParams{
		ID:              id,
		RunID:           runID,
		ServerID:        "srv-timeout",
		ToolName:        "myserver.deploy",
		CallArgs:        `{"env":"prod"}`,
		RequestState:    `{"cursor":"abc"}`,
		RequestPayload:  `[{"message":"deploy to prod?"}]`,
		ElicitationKind: "permission",
		ExpiresAt:       expiresAt,
		DeadlineSource:  &source,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("insertToolInputRequest %s: %v", id, err)
	}
}

// toolInputFixture stands up a run paused on a tool-input request.
func toolInputFixture(t *testing.T, runID string) *db.Store {
	t.Helper()
	store := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, store, "p-"+runID, "policy-"+runID, "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, runID, "p-"+runID, model.RunStatusWaitingForFeedback)
	testutil.InsertMcpServer(t, store, "srv-timeout", "myserver", "http://example.invalid")
	return store
}

// The hole this scanner closes: a host that died mid-wait left a pending row
// whose in-process timer died with it. Nothing but the scanner can resolve it.
func TestToolInputScanner_ResolvesRequestStrandedByRestart(t *testing.T) {
	ctx := context.Background()
	store := toolInputFixture(t, "r-stranded")

	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	insertToolInputRequest(t, store, "tir-stranded", "r-stranded", past, "policy")

	scanner := timeout.NewToolInputScanner(store, time.Hour)
	if err := scanner.Scan(ctx); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	row, err := store.Queries().GetToolInputRequest(ctx, "tir-stranded")
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "timed_out" {
		t.Errorf("status = %q, want timed_out", row.Status)
	}
	if row.ResolvedAt == nil {
		t.Error("resolved_at is nil; a claimed timeout must record when it was claimed")
	}

	run, err := store.Queries().GetRun(ctx, "r-stranded")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != string(model.RunStatusFailed) {
		t.Errorf("run status = %q, want failed — a run nobody can answer must not stay paused", run.Status)
	}
}

// A request still inside its deadline is left alone.
func TestToolInputScanner_LeavesUnexpiredRequestsAlone(t *testing.T) {
	ctx := context.Background()
	store := toolInputFixture(t, "r-live")

	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	insertToolInputRequest(t, store, "tir-live", "r-live", future, "policy")

	scanner := timeout.NewToolInputScanner(store, time.Hour)
	if err := scanner.Scan(ctx); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	row, err := store.Queries().GetToolInputRequest(ctx, "tir-live")
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "pending" {
		t.Errorf("status = %q, want pending", row.Status)
	}
	run, err := store.Queries().GetRun(ctx, "r-live")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != string(model.RunStatusWaitingForFeedback) {
		t.Errorf("run status = %q, want waiting_for_feedback", run.Status)
	}
}

// The scanner reads the EFFECTIVE deadline, so a server TTL that lands before
// the policy timeout is honored without the scanner knowing anything about
// server clocks — the precedence was resolved once, at write time.
func TestToolInputScanner_HonorsAServerShortenedDeadline(t *testing.T) {
	ctx := context.Background()
	store := toolInputFixture(t, "r-server-ttl")

	// The policy would have allowed hours; the server's TTL already passed.
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertToolInputRequest(t, store, "tir-server-ttl", "r-server-ttl", past, "server_ttl")

	scanner := timeout.NewToolInputScanner(store, time.Hour)
	if err := scanner.Scan(ctx); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	row, err := store.Queries().GetToolInputRequest(ctx, "tir-server-ttl")
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "timed_out" {
		t.Errorf("status = %q, want timed_out", row.Status)
	}
	// The source survives the timeout, which is what lets a later answer-replay
	// pass tell a server-TTL expiry from a human no-show.
	if row.DeadlineSource == nil || *row.DeadlineSource != "server_ttl" {
		t.Errorf("deadline_source = %v, want server_ttl to survive the timeout", row.DeadlineSource)
	}
}

// tasks/cancel must fire exactly once per expiry, including when the scanner
// runs again over the same rows. The CAS claim is what guarantees it: the
// second scan finds nothing pending to claim.
func TestToolInputScanner_TerminationHookFiresExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store := toolInputFixture(t, "r-cancel")

	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	insertToolInputRequest(t, store, "tir-cancel", "r-cancel", past, "policy")

	// Stands in for the tasks/cancel call. The wait is not task-backed until a
	// tools/call produces a task handle, so what is pinned here is the
	// exactly-once property of the hook that will carry the cancel.
	var cancels atomic.Int64
	var gotID atomic.Value
	scanner := timeout.NewToolInputScanner(store, time.Hour,
		timeout.WithOnTerminated(func(_ context.Context, item timeout.ExpiredItem) {
			cancels.Add(1)
			gotID.Store(item.ID)
		}),
	)

	for i := 0; i < 3; i++ {
		if err := scanner.Scan(ctx); err != nil {
			t.Fatalf("Scan %d: %v", i, err)
		}
	}

	if got := cancels.Load(); got != 1 {
		t.Errorf("termination hook fired %d times across 3 scans, want exactly 1", got)
	}
	if id, _ := gotID.Load().(string); id != "tir-cancel" {
		t.Errorf("hook received item %q, want tir-cancel", id)
	}
}

// A concurrent in-process waiter and the scanner race for the same row; exactly
// one wins, so the run is never failed twice.
func TestToolInputScanner_LosesCleanlyToAnInProcessResolution(t *testing.T) {
	ctx := context.Background()
	store := toolInputFixture(t, "r-raced")

	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	insertToolInputRequest(t, store, "tir-raced", "r-raced", past, "policy")

	// The agent's waiter resolves it first.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	response := `[{"action":"accept","content":{"ok":true}}]`
	rows, err := store.Queries().ResolveToolInputRequest(ctx, db.ResolveToolInputRequestParams{
		Response:   &response,
		ResolvedAt: &now,
		ID:         "tir-raced",
	})
	if err != nil {
		t.Fatalf("ResolveToolInputRequest: %v", err)
	}
	if rows != 1 {
		t.Fatalf("in-process resolve affected %d rows, want 1", rows)
	}

	var cancels atomic.Int64
	scanner := timeout.NewToolInputScanner(store, time.Hour,
		timeout.WithOnTerminated(func(context.Context, timeout.ExpiredItem) { cancels.Add(1) }),
	)
	if err := scanner.Scan(ctx); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	row, err := store.Queries().GetToolInputRequest(ctx, "tir-raced")
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "resolved" {
		t.Errorf("status = %q, want resolved — the scanner must not overwrite a delivered answer", row.Status)
	}
	if got := cancels.Load(); got != 0 {
		t.Errorf("termination hook fired %d times for an already-answered request, want 0", got)
	}

	run, err := store.Queries().GetRun(ctx, "r-raced")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status == string(model.RunStatusFailed) {
		t.Error("run was failed by the scanner despite the answer landing first")
	}
}
