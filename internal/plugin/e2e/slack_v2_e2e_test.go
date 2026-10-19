package e2e_test

// slack_v2_e2e_test.go is the Slack v2 proving-ground flow (#997): a Slack
// message becomes a run, the run's tool asks for permission, the question is
// put to a human in Slack as buttons, and the click that settles it ends up in
// a decision record that says who, through which channel and how strongly that
// channel knows them.
//
// Real: the slack-v2 binary (built here and run as a subprocess, speaking real
// HTTP to the host), the host endpoint behind its production middleware chain,
// the events supervisor -> listen sink -> dispatcher -> RunLauncher pipeline,
// the audience router, the Tasks poller, the actor directory and the decisions
// handler. Faked: Slack (internal/testutil/fakeslack), the LLM (a script), the
// MRTR tool server (internal/testutil/mrtrfake, which stands in for Relay) and
// the container runtime. There is no runtime: the plugin is a local process
// and its managed endpoint row points at 127.0.0.1, which is the only part of
// the reconciler's job this test does by hand.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/actordir"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/inapptask"
	"github.com/felag-engineering/gleipnir/internal/plugin/pluginmetrics"
	plugintrigger "github.com/felag-engineering/gleipnir/internal/plugin/trigger"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/testutil/fakeslack"
	"github.com/felag-engineering/gleipnir/internal/testutil/mrtrfake"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

const (
	slackInstanceName = "slack-e2e"
	slackChannelID    = "C0E2E"
	slackApproverID   = "UAPPROVER"
	slackStrangerID   = "USTRANGER"
	slackHumanID      = "UHUMAN"
	slackBotToken     = "xoxb-e2e-bot-token"

	// slackBudget bounds every wait on the subprocess, which has to start, dial
	// back into the host, open a websocket and make several round trips.
	slackBudget = 2 * time.Minute
)

// slackSignals turns the publisher's run.status_changed events into wake-ups
// for waitRun, so the test re-reads the run when it may have changed rather
// than on a timer.
type slackSignals struct {
	testutil.RecordingPublisher
	runs chan struct{}
}

func (p *slackSignals) Publish(eventType string, data json.RawMessage) {
	p.RecordingPublisher.Publish(eventType, data)
	if eventType != "run.status_changed" {
		return
	}
	select {
	case p.runs <- struct{}{}:
	default: // a wake-up is already pending; one is enough
	}
}

// routedSignal wraps the task read the TaskWaiter makes the moment an ask has
// been routed: the plugin has answered channel/request (so it has posted the
// buttons and recorded the message), the mcp_tasks row exists, and the waiter
// is registered. That is the earliest instant a click can be processed, and it
// is a call the production code makes anyway, not one the test added.
type routedSignal struct {
	*db.Queries
	routed chan<- string
}

func (s routedSignal) GetMCPTask(ctx context.Context, id string) (db.McpTask, error) {
	select {
	case s.routed <- id:
	default: // only the first ask is awaited
	}
	return s.Queries.GetMCPTask(ctx, id)
}

// slackChannelClients adapts the shared MCP registry to hitl's resolver. The
// lookup is the same one the events supervisor and the poller use, so the
// assurance gate and the poll talk to one client per instance.
type slackChannelClients struct {
	q        *db.Queries
	registry *mcp.Registry
}

func (c slackChannelClients) ChannelClientFor(instanceID string) (hitl.ChannelClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv, err := c.q.GetMCPServerByPluginInstance(ctx, &instanceID)
	if err != nil {
		return nil, fmt.Errorf("resolve managed endpoint for instance %s: %w", instanceID, err)
	}
	client, err := c.registry.ClientForServerID(ctx, srv.ID)
	if err != nil {
		return nil, fmt.Errorf("resolve mcp client for instance %s: %w", instanceID, err)
	}
	return client, nil
}

// repoRoot is the root module directory, found from this file's location.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

// buildSlackV2 compiles the plugin. It lives in its own module, so the build
// names the repository's go.work explicitly: the root module's tests run with
// GOWORK=off, under which plugin-sdk would not resolve.
func buildSlackV2(t *testing.T, root string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "slack-v2")
	cmd := exec.Command("go", "build", "-o", out, "./plugins/slack-v2")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK="+filepath.Join(root, "go.work"))
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building plugins/slack-v2: %v\n%s", err, combined)
	}
	return out
}

// lockedBuffer collects the subprocess's output, which is written from the
// os/exec copier goroutines while the test may read it to report a failure.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startSlackV2 runs the plugin binary the way the container would: reaching
// the host through the endpoint URL and token in its environment, Slack
// through SLACK_API_URL, and listening for the host on addr. It returns once
// the port accepts connections.
func startSlackV2(t *testing.T, bin, addr string, env []string) {
	t.Helper()
	out := &lockedBuffer{}
	cmd := exec.Command(bin)
	// A deliberately empty environment: nothing from the developer's shell
	// (proxies in particular) may reach the plugin.
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "NO_PROXY=127.0.0.1,localhost"}, env...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting slack-v2: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			t.Logf("slack-v2 output:\n%s", out.String())
		}
	})

	// The only wall-clock wait in the test: an external process offers no
	// event for "the port is bound".
	deadline := time.Now().Add(slackBudget)
	for {
		select {
		case <-exited:
			t.Fatalf("slack-v2 exited during startup:\n%s", out.String())
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("slack-v2 never listened on %s:\n%s", addr, out.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return addr
}

// buttonActionIDs returns the action_id of every button in a posted message's
// blocks, keyed by the option the id encodes.
func buttonActionIDs(t *testing.T, blocks string) (taskID string, byOption map[string]string) {
	t.Helper()
	var parsed []struct {
		Elements []struct {
			Type     string `json:"type"`
			ActionID string `json:"action_id"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(blocks), &parsed); err != nil {
		t.Fatalf("decoding posted blocks %q: %v", blocks, err)
	}
	byOption = map[string]string{}
	for _, block := range parsed {
		for _, el := range block.Elements {
			if el.Type != "button" {
				continue
			}
			task, option, ok := fakeslack.ParseActionID(el.ActionID)
			if !ok {
				t.Fatalf("button action_id %q is not in the plugin's format", el.ActionID)
			}
			taskID = task
			byOption[option] = el.ActionID
		}
	}
	return taskID, byOption
}

func TestSlackV2_EventToRunToApprovalInSlackToDecisionRecord(t *testing.T) {
	ctx := context.Background()
	root := repoRoot(t)
	bin := buildSlackV2(t, root)

	store := testutil.NewTestStore(t)
	q := store.Queries()
	// Registered right after the store so the manager drains before the store
	// closes (cleanup is LIFO).
	mgr := runpkg.NewRunManager()
	t.Cleanup(mgr.Wait)

	pub := &slackSignals{runs: make(chan struct{}, 1)}

	// --- the world outside Gleipnir -------------------------------------
	slack := fakeslack.New(t)
	slack.SetBotUserID("UBOT")

	relay := mrtrfake.New()
	relaySrv := httptest.NewServer(relay)
	t.Cleanup(relaySrv.Close)

	// --- the plugin's rows, as install + approve + create-instance leave them
	manifest, err := os.ReadFile(filepath.Join(root, "plugins", "slack-v2", "manifest.yaml"))
	if err != nil {
		t.Fatalf("reading the slack-v2 manifest: %v", err)
	}
	encKey := bytesOfLen(32)
	creds, err := crypto.Encrypt(encKey, fmt.Sprintf(
		`{"strategy":"oauth2_authcode","token":{"access_token":%q,"token_type":"Bearer"}}`, slackBotToken))
	if err != nil {
		t.Fatalf("encrypting the bot credentials: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	pluginID := model.NewULID()
	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID: pluginID, Name: "slack-v2", PluginVersion: "0.1.0", ManifestSnapshot: string(manifest),
		Status: string(model.PluginStatusActive), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	instanceID := model.NewULID()
	config, err := json.Marshal(map[string]string{"app_token": "xapp-e2e-app-token"})
	if err != nil {
		t.Fatalf("encoding the instance config: %v", err)
	}
	scope, err := json.Marshal(map[string]any{"channels": []string{slackChannelID}})
	if err != nil {
		t.Fatalf("encoding the subscription scope: %v", err)
	}
	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: instanceID, PluginID: pluginID, InstanceName: slackInstanceName,
		ConfigJson: string(config), SubscriptionScopeJson: string(scope), CredentialsEncrypted: &creds,
		HandshakeVersions: "{}", HealthState: string(model.PluginHealthStateHealthy),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}

	// An approver an admin has linked to a Slack user on THIS instance
	// (#964), and a stranger nobody has linked.
	approverUserID := createApprover(t, q, "slack-approver", instanceID, slackApproverID)

	// --- the host endpoint, behind its production middleware chain -----------
	registry := mcp.NewRegistry(q)
	health := caphealth.NewRegistry()

	var poller *mcp.PollScheduler
	var waiter *runpkg.TaskWaiter
	routed := make(chan string, 1)
	poller = mcp.NewPollScheduler(q, registry,
		mcp.WithTaskPublisher(pub),
		mcp.WithOnResolved(func(ctx context.Context, task db.McpTask, err error) { waiter.OnResolved(ctx, task, err) }))
	waiter = runpkg.NewTaskWaiter(poller, routedSignal{Queries: q, routed: routed})

	hostServer := hostendpoint.NewServer()
	hostServer.Register(hostendpoint.Tier1Tools(hostendpoint.Tier1Deps{
		Querier: q, EncryptionKey: encKey, Metrics: pluginmetrics.New(), Health: health,
	})...)
	hostServer.Register(hostendpoint.Tier2Tools(hostendpoint.Tier2Deps{Querier: q})...)
	hostServer.Register(hostendpoint.AuthorizeActorTools(hostendpoint.AuthorizeActorDeps{
		Querier: q, Directory: actordir.DB{Querier: q}, PollHint: poller,
	})...)
	hostServer.Register(hostendpoint.UserLinkTools(hostendpoint.UserLinkDeps{Querier: q})...)

	tokens := identity.New()
	token, err := tokens.Issue(instanceID)
	if err != nil {
		t.Fatalf("issuing the instance token: %v", err)
	}
	generations := generation.New()
	generations.RegisterInstance(instanceID)
	hostSrv := httptest.NewServer(hostendpoint.Chain(hostServer, hostendpoint.RegistryResolver{Registry: tokens}, generations))
	t.Cleanup(hostSrv.Close)

	// --- the plugin process -------------------------------------------------
	pluginAddr := freeLoopbackAddr(t)
	startSlackV2(t, bin, pluginAddr, []string{
		hostclient.HostEndpointURLEnvVar + "=" + hostSrv.URL,
		hostclient.InstanceTokenEnvVar + "=" + token,
		"SLACK_API_URL=" + slack.URL(),
		"DATA_DIR=" + t.TempDir(),
		"LISTEN_ADDR=" + pluginAddr,
	})

	// The reconciler's attach step, by hand: a managed endpoint row for the
	// instance, then tool discovery through it.
	registrar, err := mcp.NewManagedRegistrar(q, model.NewULID, nil)
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	managed, err := registrar.Register(ctx, mcp.ManagedEndpoint{
		InstanceID: instanceID, InstanceName: slackInstanceName, URL: "http://" + pluginAddr + "/",
	})
	if err != nil {
		t.Fatalf("registering the managed endpoint: %v", err)
	}
	if _, err := registry.RefreshTools(ctx, managed.ID); err != nil {
		t.Fatalf("discovering the plugin's tools: %v", err)
	}
	tools, err := q.ListMCPToolsByServer(ctx, managed.ID)
	if err != nil {
		t.Fatalf("ListMCPToolsByServer: %v", err)
	}
	if len(tools) != 9 {
		t.Errorf("discovered %d slack-v2 tools, want 9", len(tools))
	}
	// What the prober would have established from a healthy container and an
	// answered server/discover.
	health.SetLiveness(instanceID, caphealth.Liveness{ContainerHealthy: true, DiscoverOK: true})

	// --- relay (the tool server) and the policy ----------------------------
	if _, err := mcp.RegisterServerForTest(ctx, q, registry, "relay", relaySrv.URL); err != nil {
		t.Fatalf("registering the relay tool server: %v", err)
	}
	createRequestAudience(t, q, "slack-approvers", instanceID, slackApproverID)
	const policyYAML = `name: slack-e2e
audience: slack-approvers
trigger:
  type: subscribed
  source: ` + slackInstanceName + `
  event_kind: channel_message
  binding:
    channel: ` + slackChannelID + `
capabilities:
  tools:
    - tool: relay.run_operation
agent:
  task: restart the gateway
  model:
    provider: anthropic
    name: claude-haiku-4-5
`
	if _, err := policy.Parse(policyYAML, "anthropic", "claude-haiku-4-5"); err != nil {
		t.Fatalf("policy does not parse: %v", err)
	}
	testutil.InsertPolicy(t, store, "pol-slack", "slack-e2e", string(model.TriggerTypeSubscribed), policyYAML)

	// --- the run stack, with the tool-input router wired as the v2 assembly does
	router, err := hitl.New(hitl.Config{
		Clients: slackChannelClients{q: q, registry: registry}, InApp: inapptask.NewManager(q),
		Completer: inapptask.NewManager(q), Tasks: q,
	})
	if err != nil {
		t.Fatalf("hitl.New: %v", err)
	}
	adapters, err := runpkg.NewTaskChannelAdapters(router, waiter, runpkg.NewDBAudienceEntriesResolver(q), q)
	if err != nil {
		t.Fatalf("NewTaskChannelAdapters: %v", err)
	}
	adapters.WithActorResolver(actordir.DB{Querier: q})

	llmClient := testutil.NewMockLLMClient(
		testutil.MakeToolCallResponse("relay.run_operation", "call-1", map[string]any{
			"selector": "node:role=gateway", "operation": "service.restart", "args": map[string]any{"unit": "api-gateway"},
		}),
		testutil.MakeTextResponse("done"),
	)
	launcher := runpkg.NewRunLauncher(runpkg.RunLauncherConfig{
		Store:    store,
		Resolver: runpkg.NewDefaultToolResolver(registry, nil, nil),
		Manager:  mgr,
		AgentFactory: func(cfg agent.Config) (*agent.BoundAgent, error) {
			cfg.LLMClient = llmClient
			return agent.New(cfg)
		},
		Publisher:          pub,
		ApprovalDispatcher: adapters,
		FeedbackDispatcher: adapters,
		InputChannelRouter: adapters,
	})

	// The poller is cancelled and joined after the run drain below: a run
	// blocked on its task needs the poller to be torn down, not the reverse.
	pollCtx, stopPoller := context.WithCancel(ctx)
	poller.Start(pollCtx, 200*time.Millisecond)
	t.Cleanup(func() {
		stopPoller()
		poller.Wait()
	})
	t.Cleanup(func() {
		mgr.CancelAll()
		mgr.Wait()
	})

	// --- the events supervisor, joined before the run drain (#787) --------------
	dispatcher := plugintrigger.NewDispatcher(plugintrigger.DispatcherConfig{
		Launcher: launcher, Querier: q, Dedup: dedup.NewDBStore(q), Publisher: pub,
	})
	streamOpen := make(chan struct{}, 1)
	supervisor := events.NewSupervisor(events.Config{
		Querier: q, Servers: q, Clients: registry, Capability: health,
		Cursor: events.NewDBStore(q), Sink: plugintrigger.NewListenSinkAdapter(dispatcher),
		Publisher: pub, RootCtx: ctx,
		// Called once the events/listen stream is open, which is also the
		// moment the plugin has recorded the subscription scope: an event
		// injected before that would be dropped as unsubscribed.
		HealthSetter: func(_ context.Context, id string, state model.PluginHealthState, _ string) {
			if id == instanceID && state == model.PluginHealthStateHealthy {
				select {
				case streamOpen <- struct{}{}:
				default:
				}
			}
		},
	})
	t.Cleanup(supervisor.StopAll)
	supervisor.Start(ctx, instanceID)

	select {
	case <-streamOpen:
	case <-time.After(slackBudget):
		t.Fatal("the events/listen stream never opened")
	}

	// === event -> run -> permission ask -> buttons in Slack ===================
	slack.InjectEvent(map[string]any{
		"type": "message", "channel": slackChannelID, "channel_type": "channel",
		"user": slackHumanID, "text": "please restart the gateway",
		"ts": "1700000000.000100", "event_ts": "1700000000.000100",
	})

	var ask fakeslack.PostedMessage
	select {
	case ask = <-slack.PostedMessages():
	case <-time.After(slackBudget):
		t.Fatal("the permission ask never reached Slack")
	}
	if want := "D" + slackApproverID; ask.Channel != want {
		t.Errorf("ask posted to %q, want the approver's direct message %q", ask.Channel, want)
	}
	taskID, buttons := buttonActionIDs(t, ask.Blocks)
	if taskID == "" || buttons["approve"] == "" || buttons["reject"] == "" {
		t.Fatalf("ask carries task %q and buttons %v, want Approve and Reject on one task", taskID, buttons)
	}
	select {
	case <-routed:
	case <-time.After(slackBudget):
		t.Fatal("the host never finished routing the ask")
	}
	runID := onlyRunID(t, q)

	// === an unmapped click: ephemeral, audit row, task stays open ================
	slack.InjectBlockAction(taskID, "approve", slackStrangerID)
	select {
	case eph := <-slack.Ephemerals():
		if eph.User != slackStrangerID || !strings.Contains(eph.Text, "not authorized") {
			t.Errorf("ephemeral = user %q text %q, want a not-authorized reply to %s", eph.User, eph.Text, slackStrangerID)
		}
	case <-time.After(slackBudget):
		t.Fatal("the unmapped click got no ephemeral reply")
	}
	attempts, err := q.ListPluginAuditEventsByType(ctx, db.ListPluginAuditEventsByTypeParams{
		EventType: hostendpoint.EventTypeUnauthorizedApproval, Limit: 10,
	})
	if err != nil {
		t.Fatalf("listing unauthorized_approval_attempt events: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("%d unauthorized_approval_attempt events, want 1", len(attempts))
	}
	if a := attempts[0]; a.Severity != "high" || a.PluginInstanceID == nil || *a.PluginInstanceID != instanceID ||
		!strings.Contains(a.PayloadJson, slackStrangerID) || !strings.Contains(a.PayloadJson, taskID) {
		t.Errorf("audit event = severity %q instance %v payload %s, want high, %s, naming %s and task %s",
			a.Severity, a.PluginInstanceID, a.PayloadJson, instanceID, slackStrangerID, taskID)
	}
	task, err := q.GetMCPTaskByServerAndTaskID(ctx, db.GetMCPTaskByServerAndTaskIDParams{ServerID: &managed.ID, TaskID: taskID})
	if err != nil {
		t.Fatalf("reading the task row: %v", err)
	}
	if task.Status != string(mcp.TaskStatusWorking) && task.Status != string(mcp.TaskStatusInputRequired) {
		t.Errorf("task status after the unauthorized click = %q, want it still open", task.Status)
	}
	if run := getRun(t, q, runID); model.IsTerminalStatus(model.RunStatus(run.Status)) {
		t.Fatalf("run is %s after an unauthorized click, want it still waiting", run.Status)
	}
	if got := listDecisions(t, store, mgr, pub, runID); len(got) != 0 {
		t.Fatalf("%d decision records after an unauthorized click, want none: %+v", len(got), got)
	}

	// === the mapped click: AuthorizeActor -> PollNow -> run completes =============
	slack.InjectBlockAction(taskID, "approve", slackApproverID)
	run := waitRunTerminal(t, q, pub, runID)
	if run.Status != string(model.RunStatusComplete) {
		t.Fatalf("run ended %s, want complete (error: %v)", run.Status, run.Error)
	}

	// The approval went back to the tool server as an accept on the retry.
	var sawRetry bool
	for _, call := range relay.Calls() {
		if !call.IsRetry {
			continue
		}
		sawRetry = true
		if len(call.InputResponses) != 1 {
			t.Errorf("retry carried %d input responses, want 1", len(call.InputResponses))
		}
		for id, resp := range call.InputResponses {
			if resp.Action != "accept" {
				t.Errorf("input response %s action = %q, want accept", id, resp.Action)
			}
		}
	}
	if !sawRetry {
		t.Error("the tool server never saw the MRTR retry")
	}

	// === the decision record, through the decisions handler =======================
	records := listDecisions(t, store, mgr, pub, runID)
	if len(records) != 1 {
		t.Fatalf("%d decision records, want exactly one: %+v", len(records), records)
	}
	rec := records[0]
	checks := []struct{ name, got, want string }{
		{"type", rec.Type, "tool_permission_request"},
		{"outcome", rec.Outcome, "answered"},
		{"channel entry", rec.ChannelEntryID, "entry-slack-approvers"},
		{"channel instance", rec.ChannelInstanceID, instanceID},
		{"channel assurance", rec.ChannelAssurance, "authenticated"},
		{"actor external id", rec.ActorExternalID, slackApproverID},
		{"actor user id", rec.ActorUserID, approverUserID},
		{"link method", rec.LinkMethod, "directory_mapping"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("decision record %s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if !rec.LinkVerified {
		t.Error("decision record link_verified = false, want true: the host resolved the actor through its directory")
	}
}

func bytesOfLen(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// createApprover creates a user holding the approver role and links actorID to
// it on instanceID: the per-instance, admin-set identity authorize_actor and
// the task channel adapter resolve.
func createApprover(t *testing.T, q *db.Queries, username, instanceID, actorID string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	user, err := q.CreateUser(ctx, db.CreateUserParams{ID: "user-" + username, Username: username, PasswordHash: "unused", CreatedAt: now})
	if err != nil {
		t.Fatalf("creating user %s: %v", username, err)
	}
	if err := q.AssignRole(ctx, db.AssignRoleParams{UserID: user.ID, Role: string(model.RoleApprover), CreatedAt: now}); err != nil {
		t.Fatalf("assigning approver to %s: %v", username, err)
	}
	if err := q.SetAdminIdentity(ctx, db.SetAdminIdentityParams{
		InstanceID: instanceID, UserID: user.ID, ExternalUserID: actorID, CreatedAt: now,
	}); err != nil {
		t.Fatalf("linking %q to %s on instance %s: %v", actorID, username, instanceID, err)
	}
	return user.ID
}

// createRequestAudience creates an audience whose only entry is the instance,
// enabled for requests and delivering directly to address, with the in-app
// fallback off so the plugin entry is the only one there is.
func createRequestAudience(t *testing.T, q *db.Queries, name, instanceID, address string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	aud, err := q.CreatePluginAudience(ctx, db.CreatePluginAudienceParams{
		ID: "aud-" + name, Name: name, CreatedAt: now, UpdatedAt: now, DisableInAppFallback: 1,
	})
	if err != nil {
		t.Fatalf("creating audience %s: %v", name, err)
	}
	if _, err := q.CreateAudienceEntry(ctx, db.CreateAudienceEntryParams{
		ID: "entry-" + name, AudienceID: aud.ID, PluginInstanceID: instanceID, Position: 0, Request: 1,
		ConfigJson: fmt.Sprintf(`{"delivery":"direct","address":%q}`, address),
	}); err != nil {
		t.Fatalf("adding instance %s to audience %s: %v", instanceID, name, err)
	}
}

func onlyRunID(t *testing.T, q *db.Queries) string {
	t.Helper()
	runs, err := q.ListRunsByPolicy(context.Background(), db.ListRunsByPolicyParams{PolicyID: "pol-slack", Limit: 10})
	if err != nil {
		t.Fatalf("listing runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("%d runs of the policy, want exactly 1 (one event, one run)", len(runs))
	}
	return runs[0].ID
}

func getRun(t *testing.T, q *db.Queries, runID string) db.Run {
	t.Helper()
	run, err := q.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return run
}

// waitRunTerminal re-reads the run after every run.status_changed event until
// it reaches a terminal status.
func waitRunTerminal(t *testing.T, q *db.Queries, pub *slackSignals, runID string) db.Run {
	t.Helper()
	deadline := time.After(slackBudget)
	for {
		run := getRun(t, q, runID)
		if model.IsTerminalStatus(model.RunStatus(run.Status)) {
			return run
		}
		select {
		case <-pub.runs:
		case <-deadline:
			t.Fatalf("run %s still %s after %s", runID, run.Status, slackBudget)
		}
	}
}

// listDecisions reads the run's decision records the way the UI does, through
// the decisions handler.
func listDecisions(t *testing.T, store *db.Store, mgr *runpkg.RunManager, pub *slackSignals, runID string) []runpkg.DecisionSummary {
	t.Helper()
	handler := runpkg.NewRunsHandler(store, mgr, pub)
	r := chi.NewRouter()
	r.Get("/api/v1/runs/{runID}/decisions", handler.ListDecisions)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID+"/decisions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET decisions = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []runpkg.DecisionSummary `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding decisions %q: %v", rec.Body.String(), err)
	}
	return body.Data
}
