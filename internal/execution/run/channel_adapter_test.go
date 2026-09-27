package run_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
	"github.com/felag-engineering/gleipnir/internal/plugin/inapptask"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// --- async test helpers --------------------------------------------------

// asyncResult carries a dispatch call's return values across a goroutine.
type asyncResult[T any] struct {
	val T
	err error
}

// runAsync starts fn in a goroutine and returns immediately — the caller
// drives whatever fn is blocked waiting on (completing a fake channel task,
// here) before collecting the result with awaitResult. Running fn
// synchronously would deadlock: DispatchApproval/DispatchFeedback block on
// TaskWaiter until the task settles, and nothing settles it until the test
// drives the stub.
func runAsync[T any](fn func() (T, error)) <-chan asyncResult[T] {
	ch := make(chan asyncResult[T], 1)
	go func() {
		val, err := fn()
		ch <- asyncResult[T]{val: val, err: err}
	}()
	return ch
}

// awaitResult collects runAsync's result, bounded generously relative to the
// in-process work under test (no real network or human latency involved).
func awaitResult[T any](t *testing.T, ch <-chan asyncResult[T]) (T, error) {
	t.Helper()
	select {
	case r := <-ch:
		return r.val, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not return within the deadline")
		var zero T
		return zero, nil
	}
}

// settle simulates ApprovalHandler/FeedbackHandler.Wait's own post-dispatch
// step: calling Settle(ctx, won) once the caller's own approval_requests /
// feedback_requests CAS has resolved. TaskChannelAdapters defers its decision
// record to this hook (issue #961 review item 1d) — DispatchApproval and
// DispatchFeedback's tests must call it themselves to observe the record a
// production ApprovalHandler.Wait call would have triggered.
func settle(t *testing.T, s interface {
	settleHook() func(context.Context, bool)
}, won bool) {
	t.Helper()
	hook := s.settleHook()
	if hook == nil {
		t.Fatal("Settle is nil, want a non-nil settlement hook")
	}
	hook(context.Background(), won)
}

// approvalSettlement / feedbackSettlement adapt agent.ApprovalSettlement /
// agent.FeedbackSettlement to the settleHook interface above, since the two
// types share no common shape of their own.
type approvalSettlement agent.ApprovalSettlement

func (s approvalSettlement) settleHook() func(context.Context, bool) { return s.Settle }

type feedbackSettlement agent.FeedbackSettlement

func (s feedbackSettlement) settleHook() func(context.Context, bool) { return s.Settle }

// --- fixtures --------------------------------------------------------------

// staticClientResolver hands back a single pre-built *mcp.Client for every
// serverID. It satisfies mcp.ClientResolver (for the PollScheduler).
type staticClientResolver struct{ client *mcp.Client }

func (r staticClientResolver) ClientForServerID(_ context.Context, _ string) (*mcp.Client, error) {
	return r.client, nil
}

// staticHitlResolver satisfies hitl.ClientResolver the same way.
type staticHitlResolver struct{ client hitl.ChannelClient }

func (r staticHitlResolver) ChannelClientFor(_ string) (hitl.ChannelClient, error) {
	return r.client, nil
}

// multiHitlResolver resolves distinct instance IDs to distinct clients, for
// the skip-then-answer scenario where two different channels are in play.
type multiHitlResolver struct{ clients map[string]hitl.ChannelClient }

func (m multiHitlResolver) ChannelClientFor(instanceID string) (hitl.ChannelClient, error) {
	client, ok := m.clients[instanceID]
	if !ok {
		return nil, errors.New("no channel client for instance " + instanceID)
	}
	return client, nil
}

// fixedEntriesResolver returns a fixed entry list regardless of audience ID —
// the routing decision under test is what hitl.Router and TaskChannelAdapters
// do with a given entry list, not how one gets built from the DB (covered
// separately by hitl_entries_test.go).
type fixedEntriesResolver struct{ entries []hitl.Entry }

func (f fixedEntriesResolver) ResolveEntries(_ context.Context, _ string) ([]hitl.Entry, error) {
	return f.entries, nil
}

// channelAdapterFixture wires a real hitl.Router, TaskWaiter, and
// mcp.PollScheduler against a real FakeChannelServer, over a real
// (in-process) database — the same stack #962 assembles in production, minus
// the container substrate.
type channelAdapterFixture struct {
	store     *db.Store
	stub      *mcp.FakeChannelServer
	client    *mcp.Client
	scheduler *mcp.PollScheduler
	waiter    *run.TaskWaiter
	router    *hitl.Router
	decisions *decision.Recorder
}

func newChannelAdapterFixture(t *testing.T, stub *mcp.FakeChannelServer) *channelAdapterFixture {
	t.Helper()
	store := testutil.NewTestStore(t)

	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	client := mcp.NewClient(srv.URL, mcp.WithProtocolVersion(mcp.ProtocolVersion20260728), mcp.WithTrustTier(mcp.TrustTierManaged))
	if _, err := client.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}

	manager := inapptask.NewManager(store.Queries())

	// The scheduler needs the waiter's OnResolved hook at construction, and
	// the waiter needs the scheduler as its TaskCanceler — #962's assembly
	// wiring resolves the same forward reference this closure does.
	var waiter *run.TaskWaiter
	scheduler := mcp.NewPollScheduler(store.Queries(), staticClientResolver{client: client},
		mcp.WithOnResolved(func(ctx context.Context, task db.McpTask, err error) {
			waiter.OnResolved(ctx, task, err)
		}))
	waiter = run.NewTaskWaiter(scheduler, store.Queries())

	router, err := hitl.New(hitl.Config{
		Clients:   staticHitlResolver{client: client},
		InApp:     manager,
		Completer: manager,
		Tasks:     store.Queries(),
	})
	if err != nil {
		t.Fatalf("hitl.New: %v", err)
	}

	return &channelAdapterFixture{
		store:     store,
		stub:      stub,
		client:    client,
		scheduler: scheduler,
		waiter:    waiter,
		router:    router,
		decisions: decision.NewRecorder(store.Queries()),
	}
}

// newRun inserts the policy/run/mcp_servers rows a plugin-routed task's
// foreign keys require, and returns the server ID.
func (f *channelAdapterFixture) newRun(t *testing.T, runID string) (serverID string) {
	t.Helper()
	serverID = "srv-" + runID
	testutil.InsertPolicy(t, f.store, "p-"+runID, "policy-"+runID, "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, f.store, runID, "p-"+runID, model.RunStatusWaitingForApproval)
	testutil.InsertMcpServer(t, f.store, serverID, "srv-"+runID, "http://example.invalid")
	return serverID
}

// waitForPersistedTask blocks until runID's mcp_tasks row is committed —
// the signal that DispatchApproval/DispatchFeedback's background goroutine
// has gotten all the way through hitl.Router.Route (issued the channel/request
// AND persisted the row), so driving the fake channel to completion and
// calling Scan is not raced against a row that does not exist yet. This is
// the exact query Scan itself uses, so "found a row" here means Scan will
// find the same one. No event is exposed to synchronize on directly, so this
// polls a short, bounded interval; the deadline is generous relative to the
// in-process round trip it is waiting on (no network or human latency is
// actually involved).
func waitForPersistedTask(t *testing.T, f *channelAdapterFixture, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := f.store.Queries().ListResumableMCPTasks(context.Background())
		if err != nil {
			t.Fatalf("ListResumableMCPTasks: %v", err)
		}
		for _, row := range rows {
			if row.RunID == runID {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("channel/request task was never persisted")
}

// completeTask drives stub's first (and, in every single-channel test here,
// only) issued task to completion, then makes the scheduler observe it. The
// DB row transition happens before the OnResolved delivery, exactly as
// production's finalize does, so TaskWaiter.Wait is race-free regardless of
// whether it has registered by the time this runs (see
// TestTaskWaiter_Wait_AlreadyResolved / TestTaskWaiter_Wait_DeliversViaOnResolved).
// Callers must call waitForPersistedTask (or otherwise know the request was
// already issued) first, since CompleteTask is a silent no-op for an unknown
// task ID.
func completeTask(t *testing.T, f *channelAdapterFixture, stub *mcp.FakeChannelServer, taskID, optionID, actorExternalID string, content []byte) {
	t.Helper()
	stub.CompleteTask(taskID, optionID, actorExternalID, content)
	if err := f.scheduler.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
}

// insertInstance inserts the plugins/plugin_instances rows a decision
// record's ChannelInstance foreign key requires.
func insertInstance(t *testing.T, store *db.Store, instanceID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	pluginID := "plugin-" + instanceID
	if _, err := store.Queries().CreatePlugin(context.Background(), db.CreatePluginParams{
		ID:               pluginID,
		Name:             instanceID,
		PluginVersion:    "1.0.0",
		ManifestSnapshot: "{}",
		TrustedPubkey:    "pk",
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	emptyDetail := ""
	if _, err := store.Queries().CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID:                    instanceID,
		PluginID:              pluginID,
		InstanceName:          instanceID,
		ConfigJson:            "{}",
		SubscriptionScopeJson: "{}",
		HandshakeVersions:     "{}",
		HealthState:           "healthy",
		HealthDetail:          &emptyDetail,
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
}

func pluginEntry(instanceID, serverID string) hitl.Entry {
	return hitl.Entry{
		EntryID:    "entry-" + instanceID,
		InstanceID: instanceID,
		ServerID:   serverID,
		Request:    true,
		Target:     mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryDirect, Address: "person-7"},
	}
}

func inAppOnlyEntries() []hitl.Entry {
	return []hitl.Entry{{EntryID: "gleipnir.in-app", InApp: true, Request: true}}
}

func httptestClient(t *testing.T, stub *mcp.FakeChannelServer) *mcp.Client {
	t.Helper()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	client := mcp.NewClient(srv.URL, mcp.WithProtocolVersion(mcp.ProtocolVersion20260728), mcp.WithTrustTier(mcp.TrustTierManaged))
	if _, err := client.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}
	return client
}

func newApprovalAdapters(t *testing.T, f *channelAdapterFixture, entries []hitl.Entry) *run.TaskChannelAdapters {
	t.Helper()
	adapters, err := run.NewTaskChannelAdapters(f.router, f.waiter, fixedEntriesResolver{entries: entries}, f.store.Queries())
	if err != nil {
		t.Fatalf("NewTaskChannelAdapters: %v", err)
	}
	return adapters
}

func auditEventsFor(t *testing.T, f *channelAdapterFixture, runID string) []db.PluginAuditEvent {
	t.Helper()
	rows, err := f.store.Queries().ListPluginAuditEventsByRun(context.Background(), &runID)
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByRun: %v", err)
	}
	return rows
}

// --- DispatchApproval ------------------------------------------------------

// CH-HOST-APPROVE-AUTHENTICATED: an authenticated channel's "approve" answer
// settles the approval.
func TestTaskChannelAdapters_DispatchApproval_AuthenticatedApproves(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-approve")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
		return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
			AudienceID: "aud-1", RunID: "r-approve", ToolName: "some.tool",
			Prompt: "Approve?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-approve")
	completeTask(t, f, stub, "task-1", "approve", "U123", nil)
	settlement, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("DispatchApproval: %v", err)
	}
	if !settlement.Approved {
		t.Fatal("expected approved=true")
	}
	settle(t, approvalSettlement(settlement), true)

	records, err := f.decisions.ForRun(context.Background(), "r-approve")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.Outcome != decision.OutcomeAnswered {
		t.Errorf("Outcome = %q, want answered", rec.Outcome)
	}
	if rec.Kind != model.ElicitationKindPermission {
		t.Errorf("Kind = %q, want permission", rec.Kind)
	}
	if rec.ChannelAssurance != string(mcp.ChannelAssuranceAuthenticated) {
		t.Errorf("ChannelAssurance = %q, want authenticated", rec.ChannelAssurance)
	}
	if rec.ActorExternalID != "U123" {
		t.Errorf("ActorExternalID = %q, want U123", rec.ActorExternalID)
	}
	if rec.LinkMethod != decision.LinkUnverified {
		t.Errorf("LinkMethod = %q, want unverified (the channel's claim, not a resolved Gleipnir user)", rec.LinkMethod)
	}
}

// CH-HOST-APPROVE-REJECT: an authenticated channel's "reject" answer settles
// the approval as denied.
func TestTaskChannelAdapters_DispatchApproval_AuthenticatedRejects(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-reject")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
		return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
			AudienceID: "aud-1", RunID: "r-reject", ToolName: "some.tool",
			Prompt: "Approve?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-reject")
	completeTask(t, f, stub, "task-1", "reject", "U123", nil)
	settlement, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("DispatchApproval: %v", err)
	}
	if settlement.Approved {
		t.Fatal("expected approved=false")
	}
	settle(t, approvalSettlement(settlement), true)

	records, err := f.decisions.ForRun(context.Background(), "r-reject")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 || records[0].Outcome != decision.OutcomeRejected {
		t.Fatalf("records = %+v, want one rejected record", records)
	}
}

// issue #961 review item 1d: a decision lost to the timeout scanner (won=
// false) must never be recorded as one that was answered.
func TestTaskChannelAdapters_DispatchApproval_LostSettlementIsNotRecorded(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-lost")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
		return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
			AudienceID: "aud-1", RunID: "r-lost", ToolName: "some.tool",
			Prompt: "Approve?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-lost")
	completeTask(t, f, stub, "task-1", "approve", "U123", nil)
	settlement, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("DispatchApproval: %v", err)
	}
	if !settlement.Approved {
		t.Fatal("expected approved=true from the channel's own answer")
	}

	// The caller's own CAS lost the race with the scanner.
	settle(t, approvalSettlement(settlement), false)

	records, err := f.decisions.ForRun(context.Background(), "r-lost")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %+v, want none — a lost settlement must not be recorded as answered", records)
	}
}

// CH-HOST-WEAK-SKIP-APPROVAL: a weak-assurance channel may not settle a
// permission ask (ADR-055 §4.1). With no other candidate, the audience is
// exhausted and the request falls to the existing in-app path via the
// sentinel error — no ask ever reaches the channel.
func TestTaskChannelAdapters_DispatchApproval_WeakOnlyFallsToInApp(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	stub.Assurance = mcp.ChannelAssuranceWeak
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-weak")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	_, err := adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
		AudienceID: "aud-1", RunID: "r-weak", ToolName: "some.tool", Prompt: "Approve?",
	})
	if !errors.Is(err, agent.ErrApprovalRouteToInApp) {
		t.Fatalf("err = %v, want ErrApprovalRouteToInApp", err)
	}
	if _, asked := stub.LastRequest(); asked {
		t.Error("a weak channel must never be asked a permission question")
	}

	records, err := f.decisions.ForRun(context.Background(), "r-weak")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("no decision record should be written for the in-app fallback, got %+v", records)
	}
}

// CH-HOST-WEAK-SKIP-RECORDED: a weak entry that IS skipped, followed by an
// authenticated entry that answers, produces a decision record whose
// Considered list names the skip — the §6.6 "why THIS channel" evidence.
func TestTaskChannelAdapters_DispatchApproval_SkipIsRecordedInConsidered(t *testing.T) {
	weakStub := mcp.NewFakeChannelServer()
	weakStub.Assurance = mcp.ChannelAssuranceWeak
	weakClient := httptestClient(t, weakStub)

	strongStub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, strongStub)
	strongServerID := f.newRun(t, "r-skip")
	weakServerID := "srv-weak"
	testutil.InsertMcpServer(t, f.store, weakServerID, "weak-server", "http://example.invalid")
	insertInstance(t, f.store, "weak-inst")
	insertInstance(t, f.store, "strong-inst")

	resolver := multiHitlResolver{clients: map[string]hitl.ChannelClient{
		"weak-inst":   weakClient,
		"strong-inst": f.client,
	}}
	manager := inapptask.NewManager(f.store.Queries())
	router, err := hitl.New(hitl.Config{
		Clients:   resolver,
		InApp:     manager,
		Completer: manager,
		Tasks:     f.store.Queries(),
	})
	if err != nil {
		t.Fatalf("hitl.New: %v", err)
	}

	adapters, err := run.NewTaskChannelAdapters(router, f.waiter, fixedEntriesResolver{entries: []hitl.Entry{
		pluginEntry("weak-inst", weakServerID),
		pluginEntry("strong-inst", strongServerID),
	}}, f.store.Queries())
	if err != nil {
		t.Fatalf("NewTaskChannelAdapters: %v", err)
	}

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
		return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
			AudienceID: "aud-1", RunID: "r-skip", ToolName: "some.tool",
			Prompt: "Approve?", ExpiresAt: &expiresAt,
		})
	})
	// The weak entry never reaches a channel — only strongStub ever gets a
	// task, so completing "task-1" on it is exactly the one ask made.
	waitForPersistedTask(t, f, "r-skip")
	completeTask(t, f, strongStub, "task-1", "approve", "U9", nil)
	settlement, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("DispatchApproval: %v", err)
	}
	if !settlement.Approved {
		t.Fatal("expected approved=true")
	}
	settle(t, approvalSettlement(settlement), true)
	if _, asked := weakStub.LastRequest(); asked {
		t.Error("the skipped weak entry must never have been asked")
	}

	records, err := f.decisions.ForRun(context.Background(), "r-skip")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	if len(records[0].Considered) != 1 || records[0].Considered[0].EntryID != "entry-weak-inst" {
		t.Fatalf("Considered = %+v, want the skipped weak entry", records[0].Considered)
	}
}

// issue #961 review item 2 (trust decision #1028): a permission settlement
// with no actor identity at all is refused outright — a plugin's
// AuthorizeActor pre-check is trusted, but a completed task naming NO actor
// is refused unconditionally.
func TestTaskChannelAdapters_DispatchApproval_EmptyActorExternalIDRejected(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-noactor")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
		return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
			AudienceID: "aud-1", RunID: "r-noactor", ToolName: "some.tool",
			Prompt: "Approve?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-noactor")
	completeTask(t, f, stub, "task-1", "approve", "", nil) // no actor
	settlement, err := awaitResult(t, resultCh)
	if err == nil {
		t.Fatal("expected an error for an actor-less permission settlement")
	}
	if settlement.Approved {
		t.Fatal("an actor-less settlement must never report approved=true")
	}

	records, err := f.decisions.ForRun(context.Background(), "r-noactor")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 || records[0].Outcome != decision.OutcomeRejected {
		t.Fatalf("records = %+v, want one rejected record", records)
	}

	audits := auditEventsFor(t, f, "r-noactor")
	var found bool
	for _, a := range audits {
		if a.EventType == "unauthorized_approval_attempt" && a.Severity == "high" {
			found = true
		}
	}
	if !found {
		t.Errorf("audits = %+v, want a high-severity unauthorized_approval_attempt", audits)
	}
}

// issue #961 review item 3: an undecodable answer (here, a resolution
// carrying neither an option nor content) writes a decision record plus a
// high-severity plugin_audit_events row, and never fabricates an answer.
func TestTaskChannelAdapters_DispatchApproval_UndecodableAnswerIsRecorded(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-undecodable")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
		return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
			AudienceID: "aud-1", RunID: "r-undecodable", ToolName: "some.tool",
			Prompt: "Approve?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-undecodable")
	completeTask(t, f, stub, "task-1", "", "", nil) // neither optionId nor content
	settlement, err := awaitResult(t, resultCh)
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if settlement.Approved {
		t.Fatal("an undecodable answer must never report approved=true")
	}

	records, err := f.decisions.ForRun(context.Background(), "r-undecodable")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 || records[0].Outcome != decision.OutcomeCancelled {
		t.Fatalf("records = %+v, want one cancelled record", records)
	}

	audits := auditEventsFor(t, f, "r-undecodable")
	var found bool
	for _, a := range audits {
		if a.EventType == "channel_answer_undecodable" && a.Severity == "high" {
			found = true
		}
	}
	if !found {
		t.Errorf("audits = %+v, want a high-severity channel_answer_undecodable", audits)
	}
}

// issue #961 review item 9: an unrecognized option ID never maps to approve,
// for every near-miss shape a plugin might send.
func TestDecodeApprovalOptionID(t *testing.T) {
	tests := []struct {
		name         string
		optionID     string
		wantApproved bool
		wantErr      bool
	}{
		{name: "approve", optionID: "approve", wantApproved: true},
		{name: "reject", optionID: "reject", wantApproved: false},
		{name: "uppercase APPROVE is never approve", optionID: "APPROVE", wantErr: true},
		{name: "trailing space is never approve", optionID: "approve ", wantErr: true},
		{name: "synonym approved is never approve", optionID: "approved", wantErr: true},
		{name: "empty is never approve", optionID: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approved, err := run.DecodeApprovalOptionIDForTest(tc.optionID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for option %q", tc.optionID)
				}
				if approved {
					t.Fatalf("approved=true for a rejected/unrecognized option %q", tc.optionID)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if approved != tc.wantApproved {
				t.Errorf("approved = %v, want %v", approved, tc.wantApproved)
			}
		})
	}
}

// CH-HOST-WEAK-ALLOWED-FEEDBACK: a weak-assurance channel MAY settle an
// information ask (ADR-055 §4.1) — the same channel that could not answer a
// permission question answers feedback directly.
func TestTaskChannelAdapters_DispatchFeedback_WeakChannelAllowed(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	stub.Assurance = mcp.ChannelAssuranceWeak
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-feedback-weak")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.FeedbackSettlement, error) {
		return adapters.DispatchFeedback(context.Background(), agent.FeedbackDispatchRequest{
			AudienceID: "aud-1", RunID: "r-feedback-weak", ToolName: "gleipnir.ask_operator",
			Prompt: "What region?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-feedback-weak")
	completeTask(t, f, stub, "task-1", "", "U7", []byte(`{"text":"us-east-1"}`))
	settlement, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("DispatchFeedback: %v", err)
	}
	if settlement.Response != `{"text":"us-east-1"}` {
		t.Fatalf("response = %q, want the raw form content", settlement.Response)
	}
	settle(t, feedbackSettlement(settlement), true)

	records, err := f.decisions.ForRun(context.Background(), "r-feedback-weak")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 || records[0].ChannelAssurance != string(mcp.ChannelAssuranceWeak) {
		t.Fatalf("records = %+v, want one answered record from the weak channel", records)
	}
}

// issue #961 review item 9: a feedback settlement that answers with only an
// option ID (no form content) is a protocol violation, refused rather than
// treated as an empty reply.
func TestTaskChannelAdapters_DispatchFeedback_OptionOnlyAnswerIsError(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-feedback-optiononly")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.FeedbackSettlement, error) {
		return adapters.DispatchFeedback(context.Background(), agent.FeedbackDispatchRequest{
			AudienceID: "aud-1", RunID: "r-feedback-optiononly", ToolName: "gleipnir.ask_operator",
			Prompt: "What region?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-feedback-optiononly")
	completeTask(t, f, stub, "task-1", "approve", "U7", nil) // option id, no content
	settlement, err := awaitResult(t, resultCh)
	if err == nil {
		t.Fatal("expected an error for an option-only feedback answer")
	}
	if settlement.Response != "" {
		t.Fatalf("Response = %q, want empty on refusal", settlement.Response)
	}

	records, err := f.decisions.ForRun(context.Background(), "r-feedback-optiononly")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 || records[0].Outcome != decision.OutcomeCancelled {
		t.Fatalf("records = %+v, want one cancelled record", records)
	}
}

// CH-HOST-INAPP-SENTINEL: an audience with only the in-app entry never
// reaches a channel — the sentinel error is returned immediately, for both
// approval and feedback.
func TestTaskChannelAdapters_InAppOnlyEntriesReturnsSentinel(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	f.newRun(t, "r-inapp")
	adapters := newApprovalAdapters(t, f, inAppOnlyEntries())

	_, err := adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
		AudienceID: "aud-1", RunID: "r-inapp", ToolName: "some.tool", Prompt: "Approve?",
	})
	if !errors.Is(err, agent.ErrApprovalRouteToInApp) {
		t.Fatalf("err = %v, want ErrApprovalRouteToInApp", err)
	}

	_, err = adapters.DispatchFeedback(context.Background(), agent.FeedbackDispatchRequest{
		AudienceID: "aud-1", RunID: "r-inapp", ToolName: "gleipnir.ask_operator", Prompt: "Question?",
	})
	if !errors.Is(err, agent.ErrFeedbackRouteToInApp) {
		t.Fatalf("err = %v, want ErrFeedbackRouteToInApp", err)
	}
}

// CH-HOST-EXPIRY: the policy deadline passes before the channel answers. The
// task is cancelled (replacing the v1 RequestTerminated notification), the
// outcome is a timeout, and no answer is fabricated.
func TestTaskChannelAdapters_DispatchApproval_ExpiryCancelsAndRecordsTimeout(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-expire")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	// Never completed: the channel simply never answers before the deadline.
	expiresAt := time.Now().Add(30 * time.Millisecond)
	settlement, err := adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
		AudienceID: "aud-1", RunID: "r-expire", ToolName: "some.tool",
		Prompt: "Approve?", ExpiresAt: &expiresAt,
	})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if settlement.Approved {
		t.Fatal("a timed-out request must never report approved=true")
	}

	if status := stub.TaskStatusOf("task-1"); status != mcp.TaskStatusCancelled {
		t.Errorf("channel task status = %q, want cancelled (tasks/cancel must fire on expiry)", status)
	}

	records, err := f.decisions.ForRun(context.Background(), "r-expire")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	if records[0].Outcome != decision.OutcomeTimeout {
		t.Errorf("Outcome = %q, want timeout", records[0].Outcome)
	}
	if records[0].ActorExternalID != "" || records[0].ActorUserID != "" {
		t.Error("a timeout must not name an actor -- nobody acted")
	}

	// issue #961 review item 9: a late completion arriving after the
	// adapter's own timeout already fired must never retroactively change
	// what DispatchApproval already returned to the caller.
	completeTask(t, f, stub, "task-1", "approve", "U1", nil)
	if settlement.Approved {
		t.Fatal("the already-returned settlement must not have become approved")
	}
	records, err = f.decisions.ForRun(context.Background(), "r-expire")
	if err != nil {
		t.Fatalf("ForRun after late completion: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records after late completion = %+v, want still exactly 1 (the timeout)", records)
	}
}

// issue #961 review item 9: the channel side cancelling a task on its own
// (e.g. an operator dismissing the prompt in the plugin's own UI) settles as
// OutcomeCancelled, distinct from a host-initiated timeout cancel.
func TestTaskChannelAdapters_DispatchApproval_ServerCancelledProducesRecord(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-servercancel")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
		return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
			AudienceID: "aud-1", RunID: "r-servercancel", ToolName: "some.tool",
			Prompt: "Approve?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-servercancel")

	// The channel cancels the task on its own — not a host-initiated
	// tasks/cancel from this adapter's own timeout path.
	if _, err := f.client.CancelTask(context.Background(), "task-1"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if err := f.scheduler.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	settlement, err := awaitResult(t, resultCh)
	if err == nil {
		t.Fatal("expected an error for a server-cancelled task")
	}
	if settlement.Approved {
		t.Fatal("a cancelled task must never report approved=true")
	}

	records, err := f.decisions.ForRun(context.Background(), "r-servercancel")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 || records[0].Outcome != decision.OutcomeCancelled {
		t.Fatalf("records = %+v, want one cancelled record", records)
	}
}

// issue #961 review item 9: a task the server itself reports as failed
// settles as OutcomeCancelled (no dedicated "failed" outcome exists — see
// decision.Outcome's own vocabulary) and never fabricates an answer.
func TestTaskChannelAdapters_DispatchApproval_TaskFailedProducesRecord(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-taskfailed")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
		return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
			AudienceID: "aud-1", RunID: "r-taskfailed", ToolName: "some.tool",
			Prompt: "Approve?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-taskfailed")
	stub.ExpireTask("task-1") // drives the fake server's task to "failed"
	if err := f.scheduler.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	settlement, err := awaitResult(t, resultCh)
	if err == nil {
		t.Fatal("expected an error for a failed task")
	}
	if settlement.Approved {
		t.Fatal("a failed task must never report approved=true")
	}

	records, err := f.decisions.ForRun(context.Background(), "r-taskfailed")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 || records[0].Outcome != decision.OutcomeCancelled {
		t.Fatalf("records = %+v, want one cancelled record", records)
	}
}
