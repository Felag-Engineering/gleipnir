package inapptask

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func newStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "inapp.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// fixture stands up a store with a run to hang tasks off.
func fixture(t *testing.T, runID string) (*db.Store, *Manager) {
	t.Helper()
	store := newStore(t)
	testutil.InsertPolicy(t, store, "p-"+runID, "policy-"+runID, "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, runID, "p-"+runID, model.RunStatusWaitingForFeedback)
	return store, NewManager(store.Queries())
}

func askOptions(runID string) OpenRequest {
	return OpenRequest{
		RunID:   runID,
		Message: "Approve the production deploy?",
		Options: []Option{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
		Kind:    model.ElicitationKindPermission,
	}
}

// The headline acceptance criterion: an in-app request and a plugin-routed
// request resolve through the SAME code path and produce identical decision
// records, modulo channel identity.
//
// The shared path is DecodeResolution: it is handed a task row and does not ask
// which route produced it. If the two routes ever diverged in shape, this test
// is where it would show up — the decoder would need a branch.
func TestOneRequestShape_InAppAndPluginRoutedDecodeIdentically(t *testing.T) {
	ctx := context.Background()
	store, manager := fixture(t, "r-shape")

	// --- Route 1: in-app. No MCP hop; the operator answers in the UI. ---
	handle, err := manager.Open(ctx, askOptions("r-shape"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := manager.Complete(ctx, handle.ID, Resolution{
		OptionID:        "approve",
		ActorExternalID: "user-7",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	inAppRow, err := store.Queries().GetMCPTask(ctx, handle.ID)
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}

	// --- Route 2: plugin, over the real io.gleipnir/channel client. ---
	stub := mcp.NewFakeChannelServer()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	client := mcp.NewClient(srv.URL, mcp.WithProtocolVersion(mcp.ProtocolVersion20260728))

	serverID := "srv-1"
	testutil.InsertMcpServer(t, store, serverID, "channel-plugin", srv.URL)

	task, err := client.ChannelRequest(ctx, mcp.ChannelRequestParams{
		Target:  mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryDirect, Address: "person-7"},
		Message: "Approve the production deploy?",
		Options: []mcp.ChannelOption{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
		Kind:    mcp.ElicitationKindPermission,
	})
	if err != nil {
		t.Fatalf("ChannelRequest: %v", err)
	}
	stub.CompleteTask(task.TaskID, "approve", "external-user-9", nil)

	final, err := client.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	// The host records the plugin-routed answer in the same table, same kind.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	pluginRowID := model.NewULID()
	if _, err := store.Queries().CreateMCPTask(ctx, db.CreateMCPTaskParams{
		ID: pluginRowID, RunID: "r-shape", ServerID: &serverID,
		TaskID: task.TaskID, Kind: KindChannelRequest,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateMCPTask: %v", err)
	}
	pluginResult := string(final.Result)
	if _, err := store.Queries().ResolveMCPTask(ctx, db.ResolveMCPTaskParams{
		Status: StatusComplete, Result: &pluginResult, UpdatedAt: now, ID: pluginRowID,
	}); err != nil {
		t.Fatalf("ResolveMCPTask: %v", err)
	}
	pluginRow, err := store.Queries().GetMCPTask(ctx, pluginRowID)
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}

	// --- The assertion: one decoder, two routes, identical decisions. ---
	inApp, err := DecodeResolution(inAppRow)
	if err != nil {
		t.Fatalf("DecodeResolution(in-app): %v", err)
	}
	plugin, err := DecodeResolution(pluginRow)
	if err != nil {
		t.Fatalf("DecodeResolution(plugin): %v", err)
	}

	if inApp.OptionID != plugin.OptionID {
		t.Errorf("decisions differ by route: in-app %q, plugin %q", inApp.OptionID, plugin.OptionID)
	}
	if inApp.ActorExternalID == "" || plugin.ActorExternalID == "" {
		t.Error("a decision record is missing its actor; an approval without one is not evidence")
	}
	// Channel identity is the ONLY thing that differs, and it lives in the row
	// rather than the decision.
	if inAppRow.Kind != pluginRow.Kind {
		t.Errorf("kinds differ: %q vs %q", inAppRow.Kind, pluginRow.Kind)
	}
	if inAppRow.Status != pluginRow.Status {
		t.Errorf("statuses differ: %q vs %q", inAppRow.Status, pluginRow.Status)
	}
	if !IsInternal(inAppRow) {
		t.Error("the in-app task is not marked internal")
	}
	if IsInternal(pluginRow) {
		t.Error("the plugin-routed task is marked internal")
	}
}

// Resolution is immediate: no interval wait, because there is nothing to poll.
func TestAwait_CompletesImmediately(t *testing.T) {
	ctx := context.Background()
	_, manager := fixture(t, "r-await")

	handle, err := manager.Open(ctx, askOptions("r-await"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	done := make(chan Resolution, 1)
	errc := make(chan error, 1)
	go func() {
		r, err := manager.Await(ctx, handle, 10*time.Second)
		if err != nil {
			errc <- err
			return
		}
		done <- r
	}()

	if err := manager.Complete(ctx, handle.ID, Resolution{OptionID: "approve", ActorExternalID: "u1"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	select {
	case r := <-done:
		if r.OptionID != "approve" {
			t.Errorf("resolution = %+v, want approve", r)
		}
	case err := <-errc:
		t.Fatalf("Await: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Await did not return promptly; in-app resolution must not wait for an interval")
	}
}

// The restart criterion: the waiter dies with the process, the row does not,
// and an answer submitted afterwards still lands.
func TestRestartWhilePending(t *testing.T) {
	ctx := context.Background()
	store, manager := fixture(t, "r-restart")

	handle, err := manager.Open(ctx, askOptions("r-restart"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// The host restarts: a fresh Manager over the same store, with no waiters.
	restarted := NewManager(store.Queries())

	resumable, err := restarted.Resumable(ctx)
	if err != nil {
		t.Fatalf("Resumable: %v", err)
	}
	if len(resumable) != 1 || resumable[0].ID != handle.ID {
		t.Fatalf("resumable = %+v, want the pending in-app task", resumable)
	}

	// An operator answers after the restart.
	if err := restarted.Complete(ctx, handle.ID, Resolution{OptionID: "reject", ActorExternalID: "u2"}); err != nil {
		t.Fatalf("Complete after restart: %v", err)
	}

	// And Await on the restarted manager reads it straight from the row —
	// there is no waiter to deliver through, and there does not need to be.
	got, err := restarted.Await(ctx, handle, time.Second)
	if err != nil {
		t.Fatalf("Await after restart: %v", err)
	}
	if got.OptionID != "reject" {
		t.Errorf("resolution = %+v, want the answer submitted after the restart", got)
	}
}

// Re-arming makes a post-restart answer deliver immediately again, rather than
// waiting for something to notice the row changed.
func TestRearm_RestoresImmediateDelivery(t *testing.T) {
	ctx := context.Background()
	store, manager := fixture(t, "r-rearm")

	handle, err := manager.Open(ctx, askOptions("r-rearm"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	restarted := NewManager(store.Queries())
	restarted.Rearm(handle.ID)

	done := make(chan Resolution, 1)
	go func() {
		r, err := restarted.Await(ctx, handle, 10*time.Second)
		if err == nil {
			done <- r
		}
	}()

	if err := restarted.Complete(ctx, handle.ID, Resolution{OptionID: "approve", ActorExternalID: "u3"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	select {
	case r := <-done:
		if r.OptionID != "approve" {
			t.Errorf("resolution = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a re-armed waiter did not deliver")
	}
}

// Cancel rides the same terminal vocabulary, and produces no decision.
func TestCancel(t *testing.T) {
	ctx := context.Background()
	store, manager := fixture(t, "r-cancel")

	handle, err := manager.Open(ctx, askOptions("r-cancel"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := manager.Cancel(ctx, handle.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	row, err := store.Queries().GetMCPTask(ctx, handle.ID)
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}
	if row.Status != StatusCancelled {
		t.Errorf("status = %q, want cancelled", row.Status)
	}
	if _, err := DecodeResolution(row); err == nil {
		t.Error("a cancelled task decoded into a decision; a non-answer must never read as one")
	}
}

// Double resolution is a CAS loss, and the two failure modes are told apart
// because they mean different things to an operator.
func TestComplete_Errors(t *testing.T) {
	ctx := context.Background()
	_, manager := fixture(t, "r-double")

	handle, err := manager.Open(ctx, askOptions("r-double"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := manager.Complete(ctx, handle.ID, Resolution{OptionID: "approve", ActorExternalID: "u1"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	err = manager.Complete(ctx, handle.ID, Resolution{OptionID: "reject", ActorExternalID: "u2"})
	if !errors.Is(err, ErrAlreadyResolved) {
		t.Errorf("second Complete = %v, want ErrAlreadyResolved", err)
	}

	err = manager.Complete(ctx, "no-such-task", Resolution{OptionID: "approve", ActorExternalID: "u1"})
	if !errors.Is(err, ErrUnknownTask) {
		t.Errorf("Complete on a missing task = %v, want ErrUnknownTask", err)
	}
}

// The same guardrail the channel extension's client enforces: an ask nobody can
// answer would leave a task open forever.
func TestOpen_Rejects(t *testing.T) {
	ctx := context.Background()
	_, manager := fixture(t, "r-reject")

	tests := []struct {
		name string
		req  OpenRequest
	}{
		{name: "no run", req: OpenRequest{Message: "x", Options: []Option{{ID: "y"}}}},
		{name: "no message", req: OpenRequest{RunID: "r-reject", Options: []Option{{ID: "y"}}}},
		{name: "no way to answer", req: OpenRequest{RunID: "r-reject", Message: "FYI"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := manager.Open(ctx, tc.req); err == nil {
				t.Error("Open accepted an unusable request")
			}
		})
	}
}

// A form ask carries a schema instead of options, on both routes.
func TestOpen_FormAsk(t *testing.T) {
	ctx := context.Background()
	store, manager := fixture(t, "r-form")

	handle, err := manager.Open(ctx, OpenRequest{
		RunID:           "r-form",
		Message:         "Which ticket authorizes this?",
		RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ticket":{"type":"string"}}}`),
		Kind:            model.ElicitationKindInformation,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := manager.Complete(ctx, handle.ID, Resolution{
		Content:         json.RawMessage(`{"ticket":"OPS-1"}`),
		ActorExternalID: "u1",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	row, err := store.Queries().GetMCPTask(ctx, handle.ID)
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}
	got, err := DecodeResolution(row)
	if err != nil {
		t.Fatalf("DecodeResolution: %v", err)
	}
	if string(got.Content) != `{"ticket":"OPS-1"}` {
		t.Errorf("content = %s, want the submitted form verbatim", got.Content)
	}
}

// An unanswered task must not decode into an answer.
func TestDecodeResolution_Errors(t *testing.T) {
	tests := []struct {
		name string
		task db.McpTask
	}{
		{name: "still working", task: db.McpTask{ID: "t1", Status: StatusWorking}},
		{name: "cancelled", task: db.McpTask{ID: "t1", Status: StatusCancelled}},
		{name: "expired", task: db.McpTask{ID: "t1", Status: StatusExpired}},
		{name: "failed", task: db.McpTask{ID: "t1", Status: StatusFailed}},
		{name: "complete with no result", task: db.McpTask{ID: "t1", Status: StatusComplete}},
		{name: "complete with bad json", task: db.McpTask{ID: "t1", Status: StatusComplete, Result: strPtr(`{nope`)}},
		// Neither a choice nor content records no decision.
		{name: "no decision recorded", task: db.McpTask{ID: "t1", Status: StatusComplete, Result: strPtr(`{"actorExternalId":"u1"}`)}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeResolution(tc.task); err == nil {
				t.Error("DecodeResolution accepted an unusable task")
			}
		})
	}
}

// An in-app task must be distinguishable from a plugin-routed one, because
// that is what decides whether the poll scheduler touches it.
func TestIsInternal(t *testing.T) {
	if !IsInternal(db.McpTask{}) {
		t.Error("a task with no server is not reported internal")
	}
	if IsInternal(db.McpTask{ServerID: strPtr("srv-1")}) {
		t.Error("a server-backed task is reported internal")
	}
}

// Resumable returns only the in-app channel requests — a plugin-routed task is
// the poll scheduler's business, and a tool-call task is not a channel request
// at all.
func TestResumable_FiltersToInternalChannelRequests(t *testing.T) {
	ctx := context.Background()
	store, manager := fixture(t, "r-filter")
	testutil.InsertMcpServer(t, store, "srv-1", "plugin", "http://example.invalid")

	if _, err := manager.Open(ctx, askOptions("r-filter")); err != nil {
		t.Fatalf("Open: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	serverID := "srv-1"
	for _, task := range []db.CreateMCPTaskParams{
		{ID: model.NewULID(), RunID: "r-filter", ServerID: &serverID, TaskID: "t-plugin", Kind: KindChannelRequest, CreatedAt: now, UpdatedAt: now},
		{ID: model.NewULID(), RunID: "r-filter", ServerID: nil, TaskID: "t-toolcall", Kind: "tool_call", CreatedAt: now, UpdatedAt: now},
	} {
		if _, err := store.Queries().CreateMCPTask(ctx, task); err != nil {
			t.Fatalf("CreateMCPTask: %v", err)
		}
	}

	resumable, err := manager.Resumable(ctx)
	if err != nil {
		t.Fatalf("Resumable: %v", err)
	}
	if len(resumable) != 1 {
		t.Fatalf("%d resumable tasks, want only the in-app channel request", len(resumable))
	}
	if !IsInternal(resumable[0]) || resumable[0].Kind != KindChannelRequest {
		t.Errorf("resumable task = %+v, want an internal channel request", resumable[0])
	}
}

// Two internal tasks share a NULL server_id; the UNIQUE(server_id, task_id)
// constraint must not treat that as a collision.
func TestOpen_TwoInternalTasksCoexist(t *testing.T) {
	ctx := context.Background()
	_, manager := fixture(t, "r-two")

	first, err := manager.Open(ctx, askOptions("r-two"))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	second, err := manager.Open(ctx, askOptions("r-two"))
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if first.ID == second.ID || first.TaskID == second.TaskID {
		t.Error("two in-app tasks share an identifier")
	}
}

func strPtr(s string) *string { return &s }
