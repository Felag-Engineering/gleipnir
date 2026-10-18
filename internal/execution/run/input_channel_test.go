package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
	"github.com/felag-engineering/gleipnir/internal/plugin/inapptask"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// permissionInput / informationInput are the single ask being routed, as the
// agent hands it over.
func permissionInput(runID string) agent.InputChannelAsk {
	return agent.InputChannelAsk{
		AudienceID: "aud-1",
		RunID:      runID,
		PolicyName: "deploy-bot",
		Request:    mcp.InputRequest{ID: "q1", Message: "delete the prod database?", RequestedSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		Kind:       model.ElicitationKindPermission,
		Wait:       time.Minute,
	}
}

const ticketSchema = `{"type":"object","properties":{"ticket":{"type":"string"}},"required":["ticket"]}`

func informationInput(runID string) agent.InputChannelAsk {
	return agent.InputChannelAsk{
		AudienceID: "aud-1",
		RunID:      runID,
		PolicyName: "deploy-bot",
		Request:    mcp.InputRequest{ID: "q1", Message: "which ticket?", RequestedSchema: json.RawMessage(ticketSchema)},
		Kind:       model.ElicitationKindInformation,
		Wait:       time.Minute,
	}
}

func askInputAsync(adapters *run.TaskChannelAdapters, ask agent.InputChannelAsk) <-chan asyncResult[agent.InputChannelAnswer] {
	return runAsync(func() (agent.InputChannelAnswer, error) {
		return adapters.AskInput(context.Background(), ask)
	})
}

// A permission ask is put to the channel as Approve/Reject, and a linked
// approver's approval comes back as an accept naming that user.
func TestTaskChannelAdapters_AskInput_PermissionApprovedByLinkedApprover(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-ti-approve")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	resultCh := askInputAsync(adapters, permissionInput("r-ti-approve"))
	waitForPersistedTask(t, f, "r-ti-approve")
	completeTask(t, f, stub, "task-1", "approve", "U123", nil)
	answer, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("AskInput: %v", err)
	}

	sent, ok := stub.LastRequest()
	if !ok {
		t.Fatal("the channel was never asked")
	}
	if len(sent.Options) != 2 || sent.Options[0].ID != "approve" || sent.Options[1].ID != "reject" {
		t.Errorf("options = %+v, want approve/reject", sent.Options)
	}
	if len(sent.RequestedSchema) != 0 {
		t.Errorf("requested schema = %s, want none for a consent ask", sent.RequestedSchema)
	}
	if sent.Message != "delete the prod database?" {
		t.Errorf("message = %q, want the server's question", sent.Message)
	}

	if answer.Action != "accept" {
		t.Errorf("action = %q, want accept", answer.Action)
	}
	if answer.Actor.ExternalID != "U123" || answer.Actor.UserID != "u-approver" || answer.Actor.Username != "u-approver" || !answer.Actor.MayResolve {
		t.Errorf("actor = %+v, want U123 linked to u-approver, allowed to resolve", answer.Actor)
	}
	if answer.Channel.EntryID != "entry-inst-1" || answer.Channel.InstanceID != "inst-1" || answer.Channel.Assurance != string(mcp.ChannelAssuranceAuthenticated) {
		t.Errorf("channel = %+v, want entry-inst-1 on inst-1, authenticated", answer.Channel)
	}

	// The agent owns the request and its one decision record.
	records, err := f.decisions.ForRun(context.Background(), "r-ti-approve")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("adapter wrote %d decision records, want none", len(records))
	}
}

// A refusal can only deny, so an unlinked actor's reject is still handed back,
// as the channel's unverified claim. An unlinked actor's approval is returned
// too, but it is not allowed to resolve and is audited at high severity: the
// agent refuses it.
func TestTaskChannelAdapters_AskInput_UnlinkedActor(t *testing.T) {
	cases := []struct {
		name       string
		optionID   string
		wantAction string
		wantAudit  bool
	}{
		{name: "reject is honored", optionID: "reject", wantAction: "decline"},
		{name: "approval is flagged", optionID: "approve", wantAction: "accept", wantAudit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := mcp.NewFakeChannelServer()
			f := newChannelAdapterFixture(t, stub)
			serverID := f.newRun(t, "r-ti-unlinked")
			insertInstance(t, f.store, "inst-1")
			adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

			resultCh := askInputAsync(adapters, permissionInput("r-ti-unlinked"))
			waitForPersistedTask(t, f, "r-ti-unlinked")
			completeTask(t, f, stub, "task-1", tc.optionID, "U-stranger", nil)
			answer, err := awaitResult(t, resultCh)
			if err != nil {
				t.Fatalf("AskInput: %v", err)
			}
			if answer.Action != tc.wantAction {
				t.Errorf("action = %q, want %q", answer.Action, tc.wantAction)
			}
			if answer.Actor.ExternalID != "U-stranger" || answer.Actor.UserID != "" || answer.Actor.MayResolve {
				t.Errorf("actor = %+v, want the unverified claim U-stranger", answer.Actor)
			}

			var sawAudit bool
			for _, ev := range auditEventsFor(t, f, "r-ti-unlinked") {
				if ev.EventType == "unauthorized_approval_attempt" && ev.Severity == "high" {
					sawAudit = true
				}
			}
			if sawAudit != tc.wantAudit {
				t.Errorf("unauthorized_approval_attempt written = %v, want %v", sawAudit, tc.wantAudit)
			}
		})
	}
}

// A linked user without the role the ask's kind requires is not asserted as
// the responder: the actor comes back as the channel's claim only.
func TestTaskChannelAdapters_AskInput_LinkedUserWithoutTheRoleIsUnverified(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-ti-role")
	insertInstance(t, f.store, "inst-1")
	adapters, err := run.NewTaskChannelAdapters(f.router, f.waiter, fixedEntriesResolver{entries: []hitl.Entry{pluginEntry("inst-1", serverID)}}, f.store.Queries())
	if err != nil {
		t.Fatalf("NewTaskChannelAdapters: %v", err)
	}
	adapters.WithActorResolver(mapActorResolver{users: map[string]fakeLinkedUser{
		"U-aud": {id: "u-aud", roles: []model.Role{model.RoleAuditor}},
	}})

	resultCh := askInputAsync(adapters, permissionInput("r-ti-role"))
	waitForPersistedTask(t, f, "r-ti-role")
	completeTask(t, f, stub, "task-1", "approve", "U-aud", nil)
	answer, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("AskInput: %v", err)
	}
	if answer.Actor.UserID != "" || answer.Actor.MayResolve {
		t.Errorf("actor = %+v, want an unverified claim: an auditor may not grant permission", answer.Actor)
	}
}

// An information ask carries the server's form, may settle on a weak channel,
// and comes back as an accept carrying the submitted content.
func TestTaskChannelAdapters_AskInput_InformationOnWeakChannel(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	stub.Assurance = mcp.ChannelAssuranceWeak
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-ti-info")
	insertInstance(t, f.store, "inst-1")
	adapters, err := run.NewTaskChannelAdapters(f.router, f.waiter, fixedEntriesResolver{entries: []hitl.Entry{pluginEntry("inst-1", serverID)}}, f.store.Queries())
	if err != nil {
		t.Fatalf("NewTaskChannelAdapters: %v", err)
	}
	adapters.WithActorResolver(mapActorResolver{users: map[string]fakeLinkedUser{
		"U-op": {id: "u-op", roles: []model.Role{model.RoleOperator}},
	}})

	resultCh := askInputAsync(adapters, informationInput("r-ti-info"))
	waitForPersistedTask(t, f, "r-ti-info")
	completeTask(t, f, stub, "task-1", "", "U-op", []byte(`{"ticket":"OPS-7"}`))
	answer, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("AskInput: %v", err)
	}

	sent, _ := stub.LastRequest()
	if string(sent.RequestedSchema) != ticketSchema || len(sent.Options) != 0 {
		t.Errorf("request = schema %s options %+v, want the server's form and no options", sent.RequestedSchema, sent.Options)
	}
	if answer.Action != "accept" || string(answer.Content) != `{"ticket":"OPS-7"}` {
		t.Errorf("answer = %s %s, want accept with the submitted form", answer.Action, answer.Content)
	}
	if answer.Channel.Assurance != string(mcp.ChannelAssuranceWeak) {
		t.Errorf("assurance = %q, want weak", answer.Channel.Assurance)
	}
	if answer.Actor.UserID != "u-op" || !answer.Actor.MayResolve {
		t.Errorf("actor = %+v, want u-op allowed to answer", answer.Actor)
	}
}

// A permission ask skips a weak entry, settles on the next, and the skip comes
// back as Considered for the agent's decision record.
func TestTaskChannelAdapters_AskInput_PermissionSkipsWeakEntry(t *testing.T) {
	weakStub := mcp.NewFakeChannelServer()
	weakStub.Assurance = mcp.ChannelAssuranceWeak
	weakClient := httptestClient(t, weakStub)

	strongStub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, strongStub)
	strongServerID := f.newRun(t, "r-ti-skip")
	testutil.InsertMcpServer(t, f.store, "srv-weak", "weak-server", "http://example.invalid")
	insertInstance(t, f.store, "weak-inst")
	insertInstance(t, f.store, "strong-inst")

	manager := inapptask.NewManager(f.store.Queries())
	router, err := hitl.New(hitl.Config{
		Clients:   multiHitlResolver{clients: map[string]hitl.ChannelClient{"weak-inst": weakClient, "strong-inst": f.client}},
		InApp:     manager,
		Completer: manager,
		Tasks:     f.store.Queries(),
	})
	if err != nil {
		t.Fatalf("hitl.New: %v", err)
	}
	adapters, err := run.NewTaskChannelAdapters(router, f.waiter, fixedEntriesResolver{entries: []hitl.Entry{
		pluginEntry("weak-inst", "srv-weak"),
		pluginEntry("strong-inst", strongServerID),
	}}, f.store.Queries())
	if err != nil {
		t.Fatalf("NewTaskChannelAdapters: %v", err)
	}
	adapters.WithActorResolver(defaultTestActors())

	resultCh := askInputAsync(adapters, permissionInput("r-ti-skip"))
	waitForPersistedTask(t, f, "r-ti-skip")
	completeTask(t, f, strongStub, "task-1", "approve", "U9", nil)
	answer, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("AskInput: %v", err)
	}
	if _, asked := weakStub.LastRequest(); asked {
		t.Error("the weak entry must never be asked a permission question")
	}
	if answer.Channel.EntryID != "entry-strong-inst" {
		t.Errorf("settled on %q, want entry-strong-inst", answer.Channel.EntryID)
	}
	if len(answer.Channel.Considered) != 1 || answer.Channel.Considered[0].EntryID != "entry-weak-inst" {
		t.Errorf("Considered = %+v, want the skipped weak entry", answer.Channel.Considered)
	}
}

// With no plugin entry able to take the ask, the agent is told to ask in-app,
// with the reasons every entry was passed over.
func TestTaskChannelAdapters_AskInput_NothingEligibleRoutesToInApp(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	stub.Assurance = mcp.ChannelAssuranceWeak
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-ti-inapp")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, append([]hitl.Entry{pluginEntry("inst-1", serverID)}, inAppOnlyEntries()...))

	_, err := adapters.AskInput(context.Background(), permissionInput("r-ti-inapp"))
	var toInApp *agent.InputRouteToInAppError
	if !errors.As(err, &toInApp) {
		t.Fatalf("err = %v, want *agent.InputRouteToInAppError", err)
	}
	if len(toInApp.Considered) != 1 || toInApp.Considered[0].EntryID != "entry-inst-1" {
		t.Errorf("Considered = %+v, want the weak entry", toInApp.Considered)
	}
	if _, asked := stub.LastRequest(); asked {
		t.Error("a weak channel was asked a permission question")
	}
}

// A routed ask nobody answers in time is a timed-out wait error, and the
// channel's task is cancelled so it stops waiting too.
func TestTaskChannelAdapters_AskInput_TimeoutCancelsTheTask(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-ti-timeout")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	ask := permissionInput("r-ti-timeout")
	ask.Wait = 20 * time.Millisecond
	_, err := adapters.AskInput(context.Background(), ask)
	var waitErr *agent.InputChannelWaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("err = %v, want *agent.InputChannelWaitError", err)
	}
	if !waitErr.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if waitErr.Channel.EntryID != "entry-inst-1" {
		t.Errorf("channel = %+v, want the entry that was asked", waitErr.Channel)
	}
	if got := stub.TaskStatusOf("task-1"); got != mcp.TaskStatusCancelled {
		t.Errorf("task status = %q, want cancelled", got)
	}
}

// An answer that is neither approve nor reject is refused, never read as
// either, and audited.
func TestTaskChannelAdapters_AskInput_UnrecognizedOptionIsRefused(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-ti-bogus")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	resultCh := askInputAsync(adapters, permissionInput("r-ti-bogus"))
	waitForPersistedTask(t, f, "r-ti-bogus")
	completeTask(t, f, stub, "task-1", "Approve ", "U123", nil)
	_, err := awaitResult(t, resultCh)
	var waitErr *agent.InputChannelWaitError
	if !errors.As(err, &waitErr) || waitErr.TimedOut {
		t.Fatalf("err = %v, want a non-timeout *agent.InputChannelWaitError", err)
	}
	var sawAudit bool
	for _, ev := range auditEventsFor(t, f, "r-ti-bogus") {
		if ev.EventType == "channel_answer_undecodable" && ev.Severity == "high" {
			sawAudit = true
		}
	}
	if !sawAudit {
		t.Error("no high-severity channel_answer_undecodable event")
	}
}

// notifyResolver hands one notify client to every instance.
type notifyResolver struct{ client hitl.NotifyClient }

func (r notifyResolver) ChannelClientFor(string) (hitl.NotifyClient, error) { return r.client, nil }

type fixedPublicURL string

func (u fixedPublicURL) GetPublicURL(context.Context) (string, error) { return string(u), nil }

// Routing a tool-initiated ask fans the fixed "input requested" message out to
// the audience's notify entries — never the ask's own text, and never to the
// entry that already has the question.
func TestTaskChannelAdapters_AskInput_NotifiesWithoutTheQuestion(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-ti-notify")
	insertInstance(t, f.store, "inst-1")

	notifyStub := mcp.NewFakeChannelServer()
	notifyClient := httptestClient(t, notifyStub)

	asked := pluginEntry("inst-1", serverID)
	asked.Notify = true
	watcher := hitl.Entry{
		EntryID: "entry-watchers", InstanceID: "inst-watch", Notify: true,
		Target: mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryShared, Address: "#ops"},
	}
	adapters := newApprovalAdapters(t, f, []hitl.Entry{asked, watcher})
	adapters.WithNotifier(hitl.NewNotifier(hitl.NotifyConfig{Clients: notifyResolver{client: notifyClient}}), fixedPublicURL("https://gleipnir.example"))

	resultCh := askInputAsync(adapters, permissionInput("r-ti-notify"))
	waitForPersistedTask(t, f, "r-ti-notify")
	completeTask(t, f, stub, "task-1", "approve", "U123", nil)
	if _, err := awaitResult(t, resultCh); err != nil {
		t.Fatalf("AskInput: %v", err)
	}
	adapters.WaitNotifications()

	if n := notifyStub.NotificationCount(); n != 1 {
		t.Fatalf("%d notifications, want exactly one (the asked entry is skipped)", n)
	}
	got, _ := notifyStub.LastNotification()
	if got.Target.Address != "#ops" {
		t.Errorf("notified %q, want #ops", got.Target.Address)
	}
	if strings.Contains(got.Message, "prod database") {
		t.Errorf("notify message %q leaks the server's question", got.Message)
	}
	if !strings.Contains(got.Message, "https://gleipnir.example/runs/r-ti-notify") || !strings.Contains(got.Message, "deploy-bot") {
		t.Errorf("notify message %q, want the run link and policy name", got.Message)
	}
}

// With the audience's in-app fallback disabled, an ask no entry may take fails
// loudly instead of quietly falling back.
func TestTaskChannelAdapters_AskInput_NothingEligibleWithFallbackDisabled(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	stub.Assurance = mcp.ChannelAssuranceWeak
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-ti-nofallback")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})

	_, err := adapters.AskInput(context.Background(), permissionInput("r-ti-nofallback"))
	var noChannel *agent.InputNoChannelError
	if !errors.As(err, &noChannel) {
		t.Fatalf("err = %v, want *agent.InputNoChannelError", err)
	}
	if len(noChannel.Considered) != 1 || noChannel.Considered[0].EntryID != "entry-inst-1" {
		t.Errorf("Considered = %+v, want the weak entry", noChannel.Considered)
	}
}
