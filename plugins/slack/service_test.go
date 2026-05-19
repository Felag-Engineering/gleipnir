package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
)

// services groups the in-process clients for all three Slack services. The
// cleanup function is returned alongside this struct from setupAll — not stored
// on the struct — so callers always `defer cleanup()` against the named local.
type services struct {
	tool    toolv1.ToolServiceClient
	trigger triggerv1.TriggerServiceClient
	channel channelv1.ChannelServiceClient
}

// setupAll starts in-process gRPC servers for the fake host and all three
// Slack service implementations. slackBackend is an optional httptest.Server
// used as the fake Slack API endpoint; when non-nil, its client and URL are
// passed to NewToolService so requests go to the test server instead of
// the real Slack API. Pass nil to use http.DefaultClient with no override.
func setupAll(t *testing.T, slackBackend *httptest.Server, hostOpts ...plugintest.Option) (*services, func()) {
	t.Helper()

	host := plugintest.NewFakeHost(hostOpts...)

	// Start the host gRPC server.
	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for host: %v", err)
	}
	hostSrv := grpc.NewServer()
	host.Register(hostSrv)
	go func() { _ = hostSrv.Serve(hostLis) }()

	// Dial the host and build the typed client used by the service implementations.
	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	hostClient := hostv1.NewHostServiceClient(hostConn)

	// --- ToolService ---
	//
	// When a slackBackend is provided, use its HTTP client and URL so test
	// requests go to the httptest.Server instead of the real Slack API.
	// The trailing slash in slackBackend.URL + "/" is required because
	// slack-go builds URLs as apiURL + methodName (slack.go:168).
	var toolSvc *ToolService
	if slackBackend != nil {
		toolSvc = NewToolService(hostClient, slackBackend.Client(), slackBackend.URL+"/")
	} else {
		toolSvc = NewToolService(hostClient, nil, "")
	}

	toolLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for tool: %v", err)
	}
	toolSrv := grpc.NewServer()
	toolv1.RegisterToolServiceServer(toolSrv, toolSvc)
	go func() { _ = toolSrv.Serve(toolLis) }()
	toolConn, err := grpc.NewClient(toolLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial tool: %v", err)
	}

	// --- TriggerService ---
	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	triggerv1.RegisterTriggerServiceServer(triggerSrv, NewTriggerService(hostClient))
	go func() { _ = triggerSrv.Serve(triggerLis) }()
	triggerConn, err := grpc.NewClient(triggerLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial trigger: %v", err)
	}

	// --- ChannelService ---
	chanLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for channel: %v", err)
	}
	chanSrv := grpc.NewServer()
	channelv1.RegisterChannelServiceServer(chanSrv, NewChannelService(hostClient))
	go func() { _ = chanSrv.Serve(chanLis) }()
	chanConn, err := grpc.NewClient(chanLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial channel: %v", err)
	}

	svcs := &services{
		tool:    toolv1.NewToolServiceClient(toolConn),
		trigger: triggerv1.NewTriggerServiceClient(triggerConn),
		channel: channelv1.NewChannelServiceClient(chanConn),
	}

	cleanup := func() {
		toolConn.Close()
		toolSrv.Stop()
		triggerConn.Close()
		triggerSrv.Stop()
		chanConn.Close()
		chanSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
	}
	return svcs, cleanup
}

// ── fakeSocketModeRunner ─────────────────────────────────────────────────────

// fakeSocketModeRunner is a test double for socketModeRunner. It plays back
// a configured slice of events sequentially (preserving ordering), then blocks
// on ctx.Done() and returns ctx.Err() — or returns runErr immediately without
// playing any events when runErr is non-nil.
//
// Ack records every call so tests can assert that acknowledgement happened.
type fakeSocketModeRunner struct {
	events []socketmode.Event
	runErr error

	mu   sync.Mutex
	acks []socketmode.Request
}

// Run plays back configured events sequentially, then blocks until ctx is done.
// If runErr is set, it returns runErr immediately (no events played).
func (f *fakeSocketModeRunner) Run(ctx context.Context, onEvent func(socketmode.Event)) error {
	if f.runErr != nil {
		return f.runErr
	}
	for _, ev := range f.events {
		onEvent(ev)
	}
	<-ctx.Done()
	return ctx.Err()
}

// Ack records the acknowledged request for later assertion.
func (f *fakeSocketModeRunner) Ack(req socketmode.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, req)
}

// AckCount returns the number of times Ack was called.
func (f *fakeSocketModeRunner) AckCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acks)
}

// ── setupAllWithFactory ──────────────────────────────────────────────────────

// setupAllWithFactory starts a TriggerService gRPC server using an injected
// socketModeFactory (for testing without a real Slack connection) backed by a
// FakeHost configured to return cfgJSON from GetInstanceConfig.
// It returns the services client, the FakeHost for health/event assertions,
// and a cleanup function.
func setupAllWithFactory(t *testing.T, factory socketModeFactory, cfgJSON string) (*services, *plugintest.FakeHost, func()) {
	t.Helper()

	host := plugintest.NewFakeHost(
		plugintest.WithInstanceConfigJSON(cfgJSON),
	)

	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for host: %v", err)
	}
	hostSrv := grpc.NewServer()
	host.Register(hostSrv)
	go func() { _ = hostSrv.Serve(hostLis) }()

	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	hostClient := hostv1.NewHostServiceClient(hostConn)

	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	triggerSvc := newTriggerServiceWithFactory(hostClient, factory)
	triggerv1.RegisterTriggerServiceServer(triggerSrv, triggerSvc)
	go func() { _ = triggerSrv.Serve(triggerLis) }()

	triggerConn, err := grpc.NewClient(triggerLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial trigger: %v", err)
	}

	svcs := &services{
		trigger: triggerv1.NewTriggerServiceClient(triggerConn),
	}
	cleanup := func() {
		triggerConn.Close()
		triggerSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
	}
	return svcs, host, cleanup
}

// ── helpers ──────────────────────────────────────────────────────────────────

// eventsAPISocketEvent builds a socketmode.Event of type EventTypeEventsAPI
// wrapping a slackevents.EventsAPIEvent whose InnerEvent.Data is a pointer
// (mirroring what slackevents.ParseEvent produces via reflect.New).
func eventsAPISocketEvent(channelID, channelType, user, text, ts, teamID string) socketmode.Event {
	inner := slackevents.EventsAPIInnerEvent{
		Type: "message",
		Data: &slackevents.MessageEvent{
			Channel:        channelID,
			ChannelType:    channelType,
			User:           user,
			Text:           text,
			TimeStamp:      ts,
			EventTimeStamp: ts,
		},
	}
	outerEvt := slackevents.EventsAPIEvent{
		TeamID:     teamID,
		InnerEvent: inner,
	}
	return socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Data:    outerEvt,
		Request: &socketmode.Request{EnvelopeID: "env-" + ts},
	}
}

// startTriggerInBackground calls svcs.trigger.Start in a goroutine and
// returns a channel that receives the first Recv error (or nil on clean EOF).
// The cancel function should be called to shut down the trigger stream.
func startTriggerInBackground(t *testing.T, svcs *services, scopeJSON string) (cancel func(), done <-chan error) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() {
		stream, err := svcs.trigger.Start(ctx, &triggerv1.StartRequest{
			WatchScopeJson: scopeJSON,
		})
		if err != nil {
			ch <- err
			return
		}
		_, recvErr := stream.Recv()
		ch <- recvErr
	}()
	return cancelFn, ch
}

// ── New trigger tests ─────────────────────────────────────────────────────────

// TestTriggerStartFailedPreconditionWithoutToken asserts that Start returns
// codes.FailedPrecondition when the instance config carries no app_level_token,
// and that the FakeHost records UNHEALTHY/config_missing.
func TestTriggerStartFailedPreconditionWithoutToken(t *testing.T) {
	svcs, host, cleanup := setupAllWithFactory(t,
		func(_ string) (socketModeRunner, error) {
			t.Fatal("socketModeFactory should not be called when token is missing")
			return nil, nil
		},
		`{}`, // no app_level_token
	)
	defer cleanup()

	stream, err := svcs.trigger.Start(context.Background(), &triggerv1.StartRequest{})
	if err != nil {
		t.Fatalf("Start open: %v", err)
	}
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error from Recv, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("code: want FailedPrecondition, got %v", st.Code())
	}

	healthState, detail, healthOK := host.Health()
	if !healthOK {
		t.Fatal("expected health state to be set")
	}
	if healthState != plugintest.HealthStateUnhealthy {
		t.Errorf("health state: want Unhealthy, got %v", healthState)
	}
	if detail != "config_missing" {
		t.Errorf("health detail: want %q, got %q", "config_missing", detail)
	}
}

// TestTriggerStartEmitsEventOnFakeSocketModeMessage asserts that a fake
// socketmode.Event of type EventTypeEventsAPI causes Start to call
// host.EmitEvent with kind=channel_message, a non-empty deterministic event_id,
// the correct payload, and that Ack is called exactly once for that event.
func TestTriggerStartEmitsEventOnFakeSocketModeMessage(t *testing.T) {
	const (
		channelID = "C01TESTEMIT"
		ts        = "1700010000.000100"
		teamID    = "T01TEAM"
		userID    = "U01USER"
		msgText   = "hello from trigger test"
	)

	fake := &fakeSocketModeRunner{
		events: []socketmode.Event{
			eventsAPISocketEvent(channelID, "channel", userID, msgText, ts, teamID),
		},
	}

	// emitCh receives the event the moment EmitEvent completes on the host.
	// Buffer size 1 so the callback never blocks even if the consumer is slow.
	emitCh := make(chan plugintest.Event, 1)

	host := plugintest.NewFakeHost(
		plugintest.WithInstanceConfigJSON(`{"app_level_token":"xapp-test-token"}`),
		plugintest.OnEmitEvent(func(ev plugintest.Event) {
			select {
			case emitCh <- ev:
			default:
			}
		}),
	)

	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for host: %v", err)
	}
	hostSrv := grpc.NewServer()
	host.Register(hostSrv)
	go func() { _ = hostSrv.Serve(hostLis) }()
	t.Cleanup(hostSrv.Stop)

	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	t.Cleanup(func() { hostConn.Close() })
	hostClient := hostv1.NewHostServiceClient(hostConn)

	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	triggerv1.RegisterTriggerServiceServer(triggerSrv,
		newTriggerServiceWithFactory(hostClient, func(_ string) (socketModeRunner, error) {
			return fake, nil
		}))
	go func() { _ = triggerSrv.Serve(triggerLis) }()
	t.Cleanup(triggerSrv.Stop)

	triggerConn, err := grpc.NewClient(triggerLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial trigger: %v", err)
	}
	t.Cleanup(func() { triggerConn.Close() })

	svcs := &services{trigger: triggerv1.NewTriggerServiceClient(triggerConn)}
	cancel, done := startTriggerInBackground(t, svcs, `{}`)

	// Wait for EmitEvent to complete on the host side before cancelling the
	// context. hostCtx is derived from stream.Context(), so cancelling before
	// EmitEvent finishes would cause the RPC to fail with codes.Canceled.
	var ev plugintest.Event
	select {
	case ev = <-emitCh:
		// EmitEvent returned successfully.
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for EmitEvent")
	}
	cancel()
	<-done // wait for Start to return

	// Ack must have been called exactly once (fires before EmitEvent in onEvent).
	if n := fake.AckCount(); n != 1 {
		t.Errorf("Ack call count: want 1, got %d", n)
	}

	if ev.EventKind != "channel_message" {
		t.Errorf("event kind: want channel_message, got %q", ev.EventKind)
	}
	if ev.EventID == "" {
		t.Error("event_id: want non-empty ULID string, got empty")
	}

	// Verify the event_id is deterministic: same inputs produce the same ULID.
	wantID := deriveEventID(channelID, ts)
	if ev.EventID != wantID {
		t.Errorf("event_id: want %q (deterministic), got %q", wantID, ev.EventID)
	}

	// Verify key payload fields.
	var p SlackChannelMessagePayload
	if err := json.Unmarshal([]byte(ev.PayloadJSON), &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.ChannelID != channelID {
		t.Errorf("payload channel_id: want %q, got %q", channelID, p.ChannelID)
	}
	if p.User != userID {
		t.Errorf("payload user: want %q, got %q", userID, p.User)
	}
	if p.Text != msgText {
		t.Errorf("payload text: want %q, got %q", msgText, p.Text)
	}
	if p.TeamID != teamID {
		t.Errorf("payload team_id: want %q, got %q", teamID, p.TeamID)
	}
	if p.Mentioned {
		t.Error("payload mentioned: want false for plain message event")
	}
}

// TestTriggerStartHonorsSubscriptionScopeChannels asserts that when the
// subscription scope restricts channels to a list that excludes the incoming
// event's channel, no EmitEvent is recorded — but Ack is still called (because
// Ack happens before the scope filter, per the synchronous-Ack requirement).
func TestTriggerStartHonorsSubscriptionScopeChannels(t *testing.T) {
	const (
		channelID = "C99EXCLUDED"
		ts        = "1700020000.000200"
	)

	fake := &fakeSocketModeRunner{
		events: []socketmode.Event{
			eventsAPISocketEvent(channelID, "channel", "U01USER", "filtered out", ts, "T01TEAM"),
		},
	}

	cfgJSON := `{"app_level_token":"xapp-test-token"}`
	// Scope only allows "#incidents", which excludes C99EXCLUDED.
	scopeJSON := `{"channels":["#incidents"]}`

	svcs, host, cleanup := setupAllWithFactory(t,
		func(_ string) (socketModeRunner, error) { return fake, nil },
		cfgJSON,
	)
	defer cleanup()

	cancel, done := startTriggerInBackground(t, svcs, scopeJSON)

	// Poll until Ack is recorded (Ack fires before scope check).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && fake.AckCount() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	// Ack still fires even though the event is scope-filtered.
	if n := fake.AckCount(); n != 1 {
		t.Errorf("Ack call count: want 1 (ack precedes scope filter), got %d", n)
	}

	// No EmitEvent should have been recorded.
	events := host.Events()
	if len(events) != 0 {
		t.Errorf("EmitEvent call count: want 0 (scope filtered), got %d", len(events))
	}
}

// TestTriggerStartHealthUnhealthyOnInvalidAuth drives the RunContext-return
// auth-failure path: the fake runner's Run returns errors.New("invalid_auth")
// immediately (no events played). The test asserts codes.Unauthenticated and
// that the FakeHost records UNHEALTHY/auth_expired.
func TestTriggerStartHealthUnhealthyOnInvalidAuth(t *testing.T) {
	fake := &fakeSocketModeRunner{
		runErr: errors.New("invalid_auth"),
	}

	cfgJSON := `{"app_level_token":"xapp-test-token"}`
	svcs, host, cleanup := setupAllWithFactory(t,
		func(_ string) (socketModeRunner, error) { return fake, nil },
		cfgJSON,
	)
	defer cleanup()

	stream, err := svcs.trigger.Start(context.Background(), &triggerv1.StartRequest{})
	if err != nil {
		t.Fatalf("Start open: %v", err)
	}
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error from Recv, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("code: want Unauthenticated, got %v", st.Code())
	}

	healthState, detail, healthOK := host.Health()
	if !healthOK {
		t.Fatal("expected health state to be set")
	}
	if healthState != plugintest.HealthStateUnhealthy {
		t.Errorf("health state: want Unhealthy, got %v", healthState)
	}
	if detail != "auth_expired" {
		t.Errorf("health detail: want %q, got %q", "auth_expired", detail)
	}
}

// ── Existing tests ────────────────────────────────────────────────────────────

// TestStubsReturnNotImplemented asserts that Channel.Notify and Channel.Request
// return an in-band ErrorEnvelope with ERROR_CODE_INTERNAL and a non-empty
// message containing "not implemented". The gRPC call itself must succeed (nil
// transport error) — the error is communicated in-band.
//
// Tool.Call is no longer a stub (#233 implements it), so it is not included here.
func TestStubsReturnNotImplemented(t *testing.T) {
	svcs, cleanup := setupAll(t, nil)
	defer cleanup()

	cases := []struct {
		name string
		run  func(t *testing.T) *commonv1.ErrorEnvelope
	}{
		{
			name: "Channel.Notify",
			run: func(t *testing.T) *commonv1.ErrorEnvelope {
				t.Helper()
				resp, err := svcs.channel.Notify(context.Background(), &channelv1.NotifyRequest{})
				if err != nil {
					t.Fatalf("Notify RPC error: %v", err)
				}
				if resp.GetOk() {
					t.Fatal("expected ok=false for stub, got ok=true")
				}
				return resp.GetError()
			},
		},
		{
			name: "Channel.Request",
			run: func(t *testing.T) *commonv1.ErrorEnvelope {
				t.Helper()
				resp, err := svcs.channel.Request(context.Background(), &channelv1.RequestRequest{})
				if err != nil {
					t.Fatalf("Request RPC error: %v", err)
				}
				return resp.GetError()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.run(t)
			if env == nil {
				t.Fatal("expected non-nil ErrorEnvelope, got nil")
			}
			if env.GetCode() != commonv1.ErrorCode_ERROR_CODE_INTERNAL {
				t.Errorf("code: want ERROR_CODE_INTERNAL, got %v", env.GetCode())
			}
			if env.GetMessage() == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}

// TestToolListToolsAdvertisesAll asserts that ListTools returns exactly the five
// Slack tools by name and that each InputSchema parses as a JSON object.
// This replaces TestToolListToolsEmpty from the scaffold.
func TestToolListToolsAdvertisesAll(t *testing.T) {
	svcs, cleanup := setupAll(t, nil)
	defer cleanup()

	resp, err := svcs.tool.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	wantNames := []string{"post_message", "list_channels", "search_messages", "react", "set_topic"}
	tools := resp.GetTools()
	if len(tools) != len(wantNames) {
		t.Fatalf("want %d tools, got %d", len(wantNames), len(tools))
	}

	for i, want := range wantNames {
		got := tools[i]
		if got.GetName() != want {
			t.Errorf("tool[%d]: want name %q, got %q", i, want, got.GetName())
		}
		// Each InputSchema must parse as a JSON object with type=object at root.
		var schema map[string]any
		if err := json.Unmarshal([]byte(got.GetInputSchema()), &schema); err != nil {
			t.Errorf("tool[%d] %s: InputSchema is not valid JSON: %v", i, want, err)
			continue
		}
		if typ, _ := schema["type"].(string); typ != "object" {
			t.Errorf("tool[%d] %s: InputSchema root type: want \"object\", got %q", i, want, typ)
		}
	}
}

// TestToolCancelIsNoOp asserts that Cancel returns an empty response with no
// error. Cancellation for the Slack ToolService is context-driven; there is no
// in-process goroutine state to abort.
func TestToolCancelIsNoOp(t *testing.T) {
	svcs, cleanup := setupAll(t, nil)
	defer cleanup()

	resp, err := svcs.tool.Cancel(context.Background(), &toolv1.CancelRequest{})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil CancelResponse, got nil")
	}
}

// TestToolCallMissingCredentials asserts that a Call with no credentials
// configured returns PERMISSION and sets health to UNHEALTHY/auth_missing.
func TestToolCallMissingCredentials(t *testing.T) {
	// Empty string credentials — no token configured.
	svcs, cleanup := setupAll(t, nil,
		plugintest.WithCredentialsJSON(`{}`),
	)
	defer cleanup()

	resp, err := svcs.tool.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "post_message",
		InputJson: `{"channel":"C123","text":"hello"}`,
	})
	if err != nil {
		t.Fatalf("Call RPC error: %v", err)
	}

	env := resp.GetError()
	if env == nil {
		t.Fatal("expected non-nil ErrorEnvelope")
	}
	if env.GetCode() != commonv1.ErrorCode_ERROR_CODE_PERMISSION {
		t.Errorf("code: want PERMISSION, got %v", env.GetCode())
	}
	if !strings.Contains(env.GetMessage(), "credentials") && !strings.Contains(env.GetMessage(), "auth") {
		t.Errorf("message should mention credentials or auth, got: %q", env.GetMessage())
	}
}
