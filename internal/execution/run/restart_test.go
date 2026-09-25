package run_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
	"github.com/felag-engineering/gleipnir/internal/plugin/inapptask"
)

// TestTaskWaiter_RestartResumesPendingTask is the DoD's restart test: a
// pending mcp_tasks row left behind by a process that crashed mid-wait is
// re-polled and resolved by a FRESH PollScheduler + TaskWaiter pair — the
// same construction main.go performs once at boot, carrying nothing over
// from whatever process created the row.
//
// The row is created directly through hitl.Router.Route rather than through
// TaskChannelAdapters.DispatchApproval, deliberately: a real crash runs no
// cleanup at all (no context cancellation, no tasks/cancel, no decision
// record) — routing the ask and then simply never waiting on it is the
// faithful way to leave behind exactly what a crash leaves behind, and
// nothing more.
func TestTaskWaiter_RestartResumesPendingTask(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-restart")

	routed, err := f.router.Route(context.Background(), hitl.Ask{
		RunID:   "r-restart",
		Message: "Approve?",
		Options: []inapptask.Option{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
		Kind:    model.ElicitationKindPermission,
	}, []hitl.Entry{pluginEntry("inst-1", serverID)})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	// The restart: a brand new PollScheduler and TaskWaiter over the SAME
	// store, wired exactly as main.go wires them fresh at boot.
	var freshWaiter *run.TaskWaiter
	freshScheduler := mcp.NewPollScheduler(f.store.Queries(), staticClientResolver{client: f.client},
		mcp.WithOnResolved(func(ctx context.Context, task db.McpTask, err error) {
			freshWaiter.OnResolved(ctx, task, err)
		}))
	freshWaiter = run.NewTaskWaiter(freshScheduler, f.store.Queries())

	resultCh := runAsync(func() (db.McpTask, error) {
		return freshWaiter.Wait(context.Background(), routed.RowID, 5*time.Second)
	})

	stub.CompleteTask(routed.TaskID, "approve", "U1", nil)
	if err := freshScheduler.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	task, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("Wait after restart: %v", err)
	}
	if task.Result == nil {
		t.Fatal("resolved task carries no result")
	}
	resolution, err := mcp.DecodeChannelResolution(json.RawMessage(*task.Result))
	if err != nil {
		t.Fatalf("DecodeChannelResolution: %v", err)
	}
	if resolution.OptionID != "approve" {
		t.Errorf("OptionID = %q, want approve", resolution.OptionID)
	}
}
