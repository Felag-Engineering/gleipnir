package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/slack-go/slack"
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

	// Shared hub registry — no real Slack connection (no token in default host config).
	registry := newHubRegistry(defaultSocketModeFactory)

	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	triggerv1.RegisterTriggerServiceServer(triggerSrv, NewTriggerService(hostClient, registry, nil, ""))
	go func() { _ = triggerSrv.Serve(triggerLis) }()
	triggerConn, err := grpc.NewClient(triggerLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial trigger: %v", err)
	}

	chanLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for channel: %v", err)
	}
	chanSrv := grpc.NewServer()
	chanSvc := newChannelServiceForTest(hostClient, registry,
		func(_ string) slackWebAPI { return nil },
		nil,
	)
	channelv1.RegisterChannelServiceServer(chanSrv, chanSvc)
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
		chanSvc.correlations.Stop()
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

func (f *fakeSocketModeRunner) Ack(req socketmode.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, req)
}

func (f *fakeSocketModeRunner) AckCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acks)
}

// channelFakeRunner supports injecting events after Run has started via a channel.
type channelFakeRunner struct {
	events chan socketmode.Event
	mu     sync.Mutex
	acks   []socketmode.Request
}

func newChannelFakeRunner() *channelFakeRunner {
	return &channelFakeRunner{events: make(chan socketmode.Event, 10)}
}

func (f *channelFakeRunner) Run(ctx context.Context, onEvent func(socketmode.Event)) error {
	for {
		select {
		case evt := <-f.events:
			onEvent(evt)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (f *channelFakeRunner) Ack(req socketmode.Request) {
	f.mu.Lock()
	f.acks = append(f.acks, req)
	f.mu.Unlock()
}

func (f *channelFakeRunner) AckCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acks)
}

func (f *channelFakeRunner) Send(evt socketmode.Event) {
	f.events <- evt
}

// ── setupAllWithFactory ──────────────────────────────────────────────────────

// setupAllWithFactory starts a TriggerService gRPC server using an injected
// socketModeFactory backed by a FakeHost configured to return cfgJSON from
// GetInstanceConfig. Returns the services client, FakeHost, and cleanup.
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

	registry := newHubRegistry(factory)

	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	triggerSvc := newTriggerServiceWithRegistry(hostClient, registry, nil, "")
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

// setupChannelServiceWithRunner creates a ChannelService in-process test harness
// using an injected socketModeRunner. Returns the gRPC client, the concrete
// service (for direct method calls in same-package tests), the FakeHost, and cleanup.
func setupChannelServiceWithRunner(
	t *testing.T,
	cred, cfg string,
	runner socketModeRunner,
	slackHandler http.Handler,
	hostOpts ...plugintest.Option,
) (channelv1.ChannelServiceClient, *ChannelService, *plugintest.FakeHost, func()) {
	t.Helper()

	allOpts := []plugintest.Option{
		plugintest.WithCredentialsJSON(cred),
		plugintest.WithInstanceConfigJSON(cfg),
	}
	allOpts = append(allOpts, hostOpts...)
	host := plugintest.NewFakeHost(allOpts...)

	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen host: %v", err)
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

	slackSrv := httptest.NewServer(slackHandler)
	slackAPIURL := slackSrv.URL + "/"

	registry := newHubRegistry(func(_ string) (socketModeRunner, error) {
		return runner, nil
	})
	hub, _, err := registry.Acquire("xapp-test-token")
	if err != nil {
		t.Fatalf("acquire hub: %v", err)
	}

	chanSvc := newChannelServiceForTest(
		hostClient,
		registry,
		func(tok string) slackWebAPI {
			return slack.New(tok,
				slack.OptionHTTPClient(slackSrv.Client()),
				slack.OptionAPIURL(slackAPIURL),
			)
		},
		slackSrv.Client(),
	)
	hub.RegisterInteractiveHandler(chanSvc.handleInteractive)

	chanLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen channel: %v", err)
	}
	chanSrv := grpc.NewServer()
	channelv1.RegisterChannelServiceServer(chanSrv, chanSvc)
	go func() { _ = chanSrv.Serve(chanLis) }()

	chanConn, err := grpc.NewClient(chanLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial channel: %v", err)
	}

	client := channelv1.NewChannelServiceClient(chanConn)

	cleanup := func() {
		chanConn.Close()
		chanSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
		slackSrv.Close()
		chanSvc.correlations.Stop()
	}
	return client, chanSvc, host, cleanup
}

// ── Shared helpers ────────────────────────────────────────────────────────────

// authTestHandler returns an http.HandlerFunc that responds to Slack's auth.test
// endpoint. When ok is true it returns a successful auth.test response; when ok
// is false it returns an invalid_auth error.
func authTestHandler(ok bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ok {
			_, _ = w.Write([]byte(`{"ok":true,"url":"https://example.slack.com/","team":"T","user":"U","team_id":"T","user_id":"U"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}
}

// newSlackMux returns an *http.ServeMux with /auth.test pre-registered.
// Callers register per-path handlers on the returned mux.
func newSlackMux(authTestOK bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.test", authTestHandler(authTestOK))
	return mux
}

// eventsAPISocketEvent builds a socketmode.Event of type EventTypeEventsAPI.
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

// interactiveSocketEvent builds a socketmode.Event of type EventTypeInteractive
// wrapping a slack.InteractionCallback with a single BlockAction.
func interactiveSocketEvent(requestID, optionID, value, responseURL string) socketmode.Event {
	cb := slack.InteractionCallback{
		Type:        slack.InteractionTypeBlockActions,
		ResponseURL: responseURL,
		User:        slack.User{ID: "U01TESTUSER"},
		ActionCallback: slack.ActionCallbacks{
			BlockActions: []*slack.BlockAction{
				{ActionID: actionIDFor(requestID, optionID), Value: value},
			},
		},
	}
	return socketmode.Event{
		Type:    socketmode.EventTypeInteractive,
		Data:    cb,
		Request: &socketmode.Request{EnvelopeID: "env-interactive-" + requestID},
	}
}

// startTriggerInBackground calls svcs.trigger.Start in a goroutine and
// returns a channel that receives the first Recv error (or nil on clean EOF).
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

// slackPostMessageOK returns an http.Handler that responds with a successful
// chat.postMessage JSON response.
func slackPostMessageOK(channel, ts string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"channel": channel,
			"ts":      ts,
		})
	})
}

// credJSON builds a credentials JSON string for a given bot token.
func credJSON(token string) string {
	return fmt.Sprintf(`{"token":{"access_token":%q}}`, token)
}

// cfgWithToken builds a GetInstanceConfig JSON string with an xapp-token.
func cfgWithToken(token string) string {
	return fmt.Sprintf(`{"app_level_token":%q}`, token)
}

// channelCfgJSON builds a channel_config_json string for tests.
func channelCfgJSON(channel, mention string) string {
	if mention == "" {
		return fmt.Sprintf(`{"channel":%q}`, channel)
	}
	return fmt.Sprintf(`{"channel":%q,"mention":%q}`, channel, mention)
}

// pollUntil polls fn every 5ms until it returns true or deadline passes.
func pollUntil(t *testing.T, deadline time.Duration, fn func() bool) bool {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if fn() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// ── Trigger tests ─────────────────────────────────────────────────────────────

// TestTriggerStartFailedPreconditionWithoutToken asserts that Start returns
// codes.FailedPrecondition when the instance config carries no app_level_token.
func TestTriggerStartFailedPreconditionWithoutToken(t *testing.T) {
	svcs, host, cleanup := setupAllWithFactory(t,
		func(_ string) (socketModeRunner, error) {
			t.Fatal("socketModeFactory should not be called when token is missing")
			return nil, nil
		},
		`{}`,
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
// host.EmitEvent with kind=channel_message, deterministic event_id, and correct
// payload. Ack is called exactly once.
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

	registry := newHubRegistry(func(_ string) (socketModeRunner, error) {
		return fake, nil
	})

	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	triggerv1.RegisterTriggerServiceServer(triggerSrv,
		newTriggerServiceWithRegistry(hostClient, registry, nil, ""))
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

	var ev plugintest.Event
	select {
	case ev = <-emitCh:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for EmitEvent")
	}
	cancel()
	<-done

	if n := fake.AckCount(); n != 1 {
		t.Errorf("Ack call count: want 1, got %d", n)
	}
	if ev.EventKind != "channel_message" {
		t.Errorf("event kind: want channel_message, got %q", ev.EventKind)
	}
	if ev.EventID == "" {
		t.Error("event_id: want non-empty ULID string, got empty")
	}

	wantID := deriveEventID(channelID, ts)
	if ev.EventID != wantID {
		t.Errorf("event_id: want %q (deterministic), got %q", wantID, ev.EventID)
	}

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

// TestTriggerStartHonorsSubscriptionScopeChannels asserts that when the scope
// excludes the event's channel, no EmitEvent is recorded — but Ack is still called.
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

	svcs, host, cleanup := setupAllWithFactory(t,
		func(_ string) (socketModeRunner, error) { return fake, nil },
		`{"app_level_token":"xapp-test-token"}`,
	)
	defer cleanup()

	cancel, done := startTriggerInBackground(t, svcs, `{"channels":["#incidents"]}`)

	pollUntil(t, 3*time.Second, func() bool { return fake.AckCount() >= 1 })
	cancel()
	<-done

	if n := fake.AckCount(); n != 1 {
		t.Errorf("Ack call count: want 1 (ack precedes scope filter), got %d", n)
	}
	if events := host.Events(); len(events) != 0 {
		t.Errorf("EmitEvent call count: want 0 (scope filtered), got %d", len(events))
	}
}

// TestTriggerStartHealthUnhealthyOnInvalidAuth drives the auth-failure path.
func TestTriggerStartHealthUnhealthyOnInvalidAuth(t *testing.T) {
	fake := &fakeSocketModeRunner{runErr: errors.New("invalid_auth")}

	svcs, host, cleanup := setupAllWithFactory(t,
		func(_ string) (socketModeRunner, error) { return fake, nil },
		`{"app_level_token":"xapp-test-token"}`,
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

// TestTriggerStartEmitsDMEventOnImMessage asserts that a socketmode MessageEvent
// with channel_type=="im" causes Start to call host.EmitEvent with kind=direct_message
// when the subscription scope has direct_messages=true.
func TestTriggerStartEmitsDMEventOnImMessage(t *testing.T) {
	const (
		channelID = "D05DMCHAN"
		ts        = "1700030000.000100"
		teamID    = "T01TEAM"
		userID    = "U01USER"
		msgText   = "what's on my calendar?"
	)

	fake := &fakeSocketModeRunner{
		events: []socketmode.Event{
			eventsAPISocketEvent(channelID, "im", userID, msgText, ts, teamID),
		},
	}

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

	registry := newHubRegistry(func(_ string) (socketModeRunner, error) {
		return fake, nil
	})

	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	triggerv1.RegisterTriggerServiceServer(triggerSrv,
		newTriggerServiceWithRegistry(hostClient, registry, nil, ""))
	go func() { _ = triggerSrv.Serve(triggerLis) }()
	t.Cleanup(triggerSrv.Stop)

	triggerConn, err := grpc.NewClient(triggerLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial trigger: %v", err)
	}
	t.Cleanup(func() { triggerConn.Close() })

	svcs := &services{trigger: triggerv1.NewTriggerServiceClient(triggerConn)}
	cancel, done := startTriggerInBackground(t, svcs, `{"direct_messages":true}`)

	var ev plugintest.Event
	select {
	case ev = <-emitCh:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for EmitEvent")
	}
	cancel()
	<-done

	if ev.EventKind != "direct_message" {
		t.Errorf("event kind: want direct_message, got %q", ev.EventKind)
	}
	var p SlackChannelMessagePayload
	if err := json.Unmarshal([]byte(ev.PayloadJSON), &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.ChannelID != channelID {
		t.Errorf("payload channel_id: want %q, got %q", channelID, p.ChannelID)
	}
	if p.ChannelType != "im" {
		t.Errorf("payload channel_type: want im, got %q", p.ChannelType)
	}
}

// TestTriggerStartDMSuppressedWithoutDirectMessages asserts that a DM event
// is NOT emitted when the subscription scope does not set direct_messages=true.
func TestTriggerStartDMSuppressedWithoutDirectMessages(t *testing.T) {
	const (
		channelID = "D05DMCHAN"
		ts        = "1700031000.000100"
	)

	fake := &fakeSocketModeRunner{
		events: []socketmode.Event{
			eventsAPISocketEvent(channelID, "im", "U01USER", "hello bot", ts, "T01TEAM"),
		},
	}

	svcs, host, cleanup := setupAllWithFactory(t,
		func(_ string) (socketModeRunner, error) { return fake, nil },
		`{"app_level_token":"xapp-test-token"}`,
	)
	defer cleanup()

	// Scope has no direct_messages=true — DM events must be suppressed.
	cancel, done := startTriggerInBackground(t, svcs, `{}`)

	pollUntil(t, 3*time.Second, func() bool { return fake.AckCount() >= 1 })
	cancel()
	<-done

	if events := host.Events(); len(events) != 0 {
		t.Errorf("EmitEvent call count: want 0 (DM filtered by scope), got %d", len(events))
	}
}

// TestTriggerStartFetchesBotUserIDOnStart verifies that Start fetches the bot
// user ID via auth.test and uses it for mention detection. Concretely: a
// MessageEvent whose text contains <@U07BOT> must arrive with mentioned=true in
// the emitted payload when auth.test returns user_id="U07BOT".
func TestTriggerStartFetchesBotUserIDOnStart(t *testing.T) {
	const botID = "U07BOT"
	const channelID = "C01CHAN"
	const ts = "1700032000.000100"

	// The auth.test endpoint returns the known bot user ID. We serve it on
	// the injected Slack API backend (same httptest.Server as ToolService tests).
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"url":"https://example.slack.com/","team":"T","user":"slackbot","team_id":"T01TEAM","user_id":%q}`, botID)
	})
	slackSrv := httptest.NewServer(mux)
	t.Cleanup(slackSrv.Close)

	// A message event whose text contains the bot mention tag.
	fake := &fakeSocketModeRunner{
		events: []socketmode.Event{
			eventsAPISocketEvent(channelID, "channel", "U01USER",
				"<@"+botID+"> deploy staging", ts, "T01TEAM"),
		},
	}

	emitCh := make(chan plugintest.Event, 1)

	// FakeHost provides credentials (bot token) and the xapp- instance config.
	host := plugintest.NewFakeHost(
		plugintest.WithInstanceConfigJSON(`{"app_level_token":"xapp-test-token"}`),
		plugintest.WithCredentialsJSON(credJSON("xoxb-test-token")),
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

	registry := newHubRegistry(func(_ string) (socketModeRunner, error) {
		return fake, nil
	})

	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	// Inject the httptest.Server client + URL so auth.test calls go to our stub.
	triggerv1.RegisterTriggerServiceServer(triggerSrv,
		newTriggerServiceWithRegistry(hostClient, registry, slackSrv.Client(), slackSrv.URL+"/"))
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

	var ev plugintest.Event
	select {
	case ev = <-emitCh:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for EmitEvent")
	}
	cancel()
	<-done

	if ev.EventKind != "channel_message" {
		t.Errorf("event kind: want channel_message, got %q", ev.EventKind)
	}
	var p SlackChannelMessagePayload
	if err := json.Unmarshal([]byte(ev.PayloadJSON), &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	// The bot ID was fetched via auth.test and threaded into translate, so the
	// mention tag in the text must have been detected.
	if !p.Mentioned {
		t.Error("payload mentioned: want true (bot tag in text, bot ID fetched via auth.test)")
	}
}

// TestTriggerStartSelfTriggerGuard asserts that a message posted by the bot
// itself is not emitted as a trigger event (self-trigger guard).
func TestTriggerStartSelfTriggerGuard(t *testing.T) {
	const botID = "U07BOT"
	const channelID = "C01CHAN"
	const ts = "1700033000.000100"

	mux := http.NewServeMux()
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"url":"https://example.slack.com/","team":"T","user":"slackbot","team_id":"T01TEAM","user_id":%q}`, botID)
	})
	slackSrv := httptest.NewServer(mux)
	t.Cleanup(slackSrv.Close)

	// The event's User is the bot itself — must be dropped.
	fake := &fakeSocketModeRunner{
		events: []socketmode.Event{
			eventsAPISocketEvent(channelID, "channel", botID, "I said something", ts, "T01TEAM"),
		},
	}

	host := plugintest.NewFakeHost(
		plugintest.WithInstanceConfigJSON(`{"app_level_token":"xapp-test-token"}`),
		plugintest.WithCredentialsJSON(credJSON("xoxb-test-token")),
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

	registry := newHubRegistry(func(_ string) (socketModeRunner, error) {
		return fake, nil
	})

	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	triggerv1.RegisterTriggerServiceServer(triggerSrv,
		newTriggerServiceWithRegistry(hostClient, registry, slackSrv.Client(), slackSrv.URL+"/"))
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

	// Wait for the fake runner to deliver and ack the event, then cancel.
	pollUntil(t, 3*time.Second, func() bool { return fake.AckCount() >= 1 })
	cancel()
	<-done

	if events := host.Events(); len(events) != 0 {
		t.Errorf("EmitEvent call count: want 0 (self-trigger guard), got %d", len(events))
	}
}

// ── Tool tests ────────────────────────────────────────────────────────────────

// TestToolListToolsAdvertisesAll asserts that ListTools returns exactly the
// expected Slack tools by name and that each InputSchema parses as a JSON object.
func TestToolListToolsAdvertisesAll(t *testing.T) {
	svcs, cleanup := setupAll(t, nil)
	defer cleanup()

	resp, err := svcs.tool.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	wantNames := []string{
		"post_message", "list_channels", "react", "set_topic",
		"read_thread", "read_history", "update_message", "delete_message", "lookup_user",
	}
	tools := resp.GetTools()
	if len(tools) != len(wantNames) {
		t.Fatalf("want %d tools, got %d", len(wantNames), len(tools))
	}

	for i, want := range wantNames {
		got := tools[i]
		if got.GetName() != want {
			t.Errorf("tool[%d]: want name %q, got %q", i, want, got.GetName())
		}
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

// TestToolCancelIsNoOp asserts that Cancel returns an empty response.
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
// returns PERMISSION and sets health to UNHEALTHY/auth_missing.
func TestToolCallMissingCredentials(t *testing.T) {
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

// ── Channel tests ─────────────────────────────────────────────────────────────

// TestChannelNotifyHappyPath asserts that Notify posts a message and returns ok=true.
func TestChannelNotifyHappyPath(t *testing.T) {
	const (
		channel = "C01CHANNEL"
		ts      = "1700030000.000300"
		token   = "xoxb-test-notify"
	)

	runner := newChannelFakeRunner()
	client, _, _, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		slackPostMessageOK(channel, ts),
	)
	defer cleanup()

	resp, err := client.Notify(context.Background(), &channelv1.NotifyRequest{
		ChannelConfigJson: channelCfgJSON(channel, ""),
		PayloadJson:       `{"text":"incident resolved"}`,
		EventType:         "run_complete",
	})
	if err != nil {
		t.Fatalf("Notify RPC: %v", err)
	}
	if !resp.GetOk() {
		t.Errorf("Notify: want ok=true, got ok=false (error: %v)", resp.GetError().GetMessage())
	}
}

// TestChannelNotifyWithMention asserts that when a mention is configured,
// Notify prepends it to the message text posted to Slack.
func TestChannelNotifyWithMention(t *testing.T) {
	const (
		channel = "C01CHANNEL"
		ts      = "1700030000.000301"
		token   = "xoxb-test-notify-mention"
		mention = "<@U07ONCALL>"
	)

	var observedText string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// slack-go encodes chat.postMessage as application/x-www-form-urlencoded.
		_ = r.ParseForm()
		observedText = r.FormValue("text")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"channel":%q,"ts":%q}`, channel, ts)
	})

	runner := newChannelFakeRunner()
	client, _, _, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		handler,
	)
	defer cleanup()

	resp, err := client.Notify(context.Background(), &channelv1.NotifyRequest{
		ChannelConfigJson: channelCfgJSON(channel, mention),
		PayloadJson:       `{"text":"incident resolved"}`,
		EventType:         "run_complete",
	})
	if err != nil {
		t.Fatalf("Notify RPC: %v", err)
	}
	if !resp.GetOk() {
		t.Errorf("Notify: want ok=true, got ok=false (error: %v)", resp.GetError().GetMessage())
	}
	if !strings.HasPrefix(observedText, mention+" ") {
		t.Errorf("expected mention prefix %q in posted text, got %q", mention, observedText)
	}
	if !strings.Contains(observedText, "incident resolved") {
		t.Errorf("expected message body in posted text, got %q", observedText)
	}
}

// TestChannelNotifyMissingCredentials asserts that Notify with no credentials
// returns PERMISSION and sets health to UNHEALTHY/auth_missing.
func TestChannelNotifyMissingCredentials(t *testing.T) {
	runner := newChannelFakeRunner()
	client, _, host, cleanup := setupChannelServiceWithRunner(t,
		`{}`,
		cfgWithToken("xapp-test-token"),
		runner,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("Slack API should not be called when no credentials")
		}),
	)
	defer cleanup()

	resp, err := client.Notify(context.Background(), &channelv1.NotifyRequest{
		ChannelConfigJson: channelCfgJSON("C01CHANNEL", ""),
		EventType:         "run_complete",
	})
	if err != nil {
		t.Fatalf("Notify RPC: %v", err)
	}
	if resp.GetOk() {
		t.Error("want ok=false for missing credentials, got ok=true")
	}
	if resp.GetError().GetCode() != commonv1.ErrorCode_ERROR_CODE_PERMISSION {
		t.Errorf("code: want PERMISSION, got %v", resp.GetError().GetCode())
	}

	state, detail, ok := host.Health()
	if !ok {
		t.Fatal("expected health state to be set")
	}
	if state != plugintest.HealthStateUnhealthy {
		t.Errorf("health state: want Unhealthy, got %v", state)
	}
	if detail != "auth_missing" {
		t.Errorf("health detail: want auth_missing, got %q", detail)
	}
}

// TestChannelNotifyMissingChannel asserts that Notify without a channel returns INVALID_ARG.
func TestChannelNotifyMissingChannel(t *testing.T) {
	runner := newChannelFakeRunner()
	client, _, _, cleanup := setupChannelServiceWithRunner(t,
		credJSON("xoxb-test"),
		cfgWithToken("xapp-test-token"),
		runner,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("Slack API should not be called for missing channel")
		}),
	)
	defer cleanup()

	resp, err := client.Notify(context.Background(), &channelv1.NotifyRequest{
		ChannelConfigJson: `{}`,
		EventType:         "run_complete",
	})
	if err != nil {
		t.Fatalf("Notify RPC: %v", err)
	}
	if resp.GetOk() {
		t.Error("want ok=false for missing channel, got ok=true")
	}
	if resp.GetError().GetCode() != commonv1.ErrorCode_ERROR_CODE_INVALID_ARG {
		t.Errorf("code: want INVALID_ARG, got %v", resp.GetError().GetCode())
	}
}

// TestChannelNotifyAuthExpired asserts that a Slack invalid_auth error sets health
// to UNHEALTHY/auth_expired.
func TestChannelNotifyAuthExpired(t *testing.T) {
	runner := newChannelFakeRunner()
	client, _, host, cleanup := setupChannelServiceWithRunner(t,
		credJSON("xoxb-revoked"),
		cfgWithToken("xapp-test-token"),
		runner,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
		}),
	)
	defer cleanup()

	resp, err := client.Notify(context.Background(), &channelv1.NotifyRequest{
		ChannelConfigJson: channelCfgJSON("C01CHANNEL", ""),
		EventType:         "run_complete",
	})
	if err != nil {
		t.Fatalf("Notify RPC: %v", err)
	}
	if resp.GetOk() {
		t.Error("want ok=false for expired auth, got ok=true")
	}

	state, detail, ok := host.Health()
	if !ok {
		t.Fatal("expected health state to be set")
	}
	if state != plugintest.HealthStateUnhealthy {
		t.Errorf("health state: want Unhealthy, got %v", state)
	}
	if detail != "auth_expired" {
		t.Errorf("health detail: want auth_expired, got %q", detail)
	}
}

// TestChannelRequestHappyPath asserts that Request posts the Block Kit message
// and returns acked=true.
func TestChannelRequestHappyPath(t *testing.T) {
	const (
		channel   = "C01CHANNEL"
		ts        = "1700040000.000400"
		requestID = "req-happy-path"
		token     = "xoxb-test-request"
	)

	runner := newChannelFakeRunner()
	client, _, _, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		slackPostMessageOK(channel, ts),
	)
	defer cleanup()

	resp, err := client.Request(context.Background(), &channelv1.RequestRequest{
		RequestId:         requestID,
		Prompt:            "Approve the deployment?",
		ChannelConfigJson: channelCfgJSON(channel, ""),
	})
	if err != nil {
		t.Fatalf("Request RPC: %v", err)
	}
	if !resp.GetAcked() {
		t.Errorf("Request: want acked=true, got acked=false (error: %v)", resp.GetError().GetMessage())
	}
}

// TestChannelRequestPreAckFailure asserts that when the Slack API returns a 500,
// Request returns acked=false with an error envelope.
func TestChannelRequestPreAckFailure(t *testing.T) {
	runner := newChannelFakeRunner()
	client, _, _, cleanup := setupChannelServiceWithRunner(t,
		credJSON("xoxb-test"),
		cfgWithToken("xapp-test-token"),
		runner,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}),
	)
	defer cleanup()

	resp, err := client.Request(context.Background(), &channelv1.RequestRequest{
		RequestId:         "req-fail",
		Prompt:            "Approve?",
		ChannelConfigJson: channelCfgJSON("C01CHANNEL", ""),
	})
	if err != nil {
		t.Fatalf("Request RPC: %v", err)
	}
	if resp.GetAcked() {
		t.Error("want acked=false on Slack 500, got acked=true")
	}
	if resp.GetError() == nil {
		t.Error("expected non-nil ErrorEnvelope, got nil")
	}
}

// TestChannelRequestRateLimitExceedsBudget asserts that when Slack returns
// 429 + Retry-After=10s and the ctx has a 5s deadline, callWithRetry
// short-circuits to RATE_LIMITED without waiting 10s.
func TestChannelRequestRateLimitExceedsBudget(t *testing.T) {
	var callCount atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Retry-After", "10")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "ratelimited"})
	})

	runner := newChannelFakeRunner()
	client, _, _, cleanup := setupChannelServiceWithRunner(t,
		credJSON("xoxb-test"),
		cfgWithToken("xapp-test-token"),
		runner,
		handler,
	)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := client.Request(ctx, &channelv1.RequestRequest{
		RequestId:         "req-ratelimit",
		Prompt:            "Approve?",
		ChannelConfigJson: channelCfgJSON("C01CHANNEL", ""),
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Request RPC: %v", err)
	}

	if elapsed > 8*time.Second {
		t.Errorf("callWithRetry took too long (%v); expected short-circuit on rate limit", elapsed)
	}
	if resp.GetAcked() {
		t.Error("want acked=false on rate limit, got acked=true")
	}
	if resp.GetError().GetCode() != commonv1.ErrorCode_ERROR_CODE_RATE_LIMITED {
		t.Errorf("code: want RATE_LIMITED, got %v", resp.GetError().GetCode())
	}
	if n := callCount.Load(); n != 1 {
		t.Errorf("Slack API call count: want 1 (no retry), got %d", n)
	}
}

// TestChannelRequestInteractiveCallback tests the full interactive round-trip:
// Request → operator clicks button → WriteAuditStep(feedback_response) →
// response_url POST with "Response recorded: approve".
func TestChannelRequestInteractiveCallback(t *testing.T) {
	const (
		channel   = "C01CHANNEL"
		ts        = "1700060000.000600"
		requestID = "req-interactive"
		token     = "xoxb-interactive"
	)

	var respURLBody atomic.Value
	responseURLSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		respURLBody.Store(string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer responseURLSrv.Close()

	runner := newChannelFakeRunner()
	client, chanSvc, host, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		slackPostMessageOK(channel, ts),
	)
	defer cleanup()

	// Point httpClient at the responseURLSrv for postResponseURL.
	chanSvc.httpClient = responseURLSrv.Client()

	// Step 1: Request.
	resp, err := client.Request(context.Background(), &channelv1.RequestRequest{
		RequestId:         requestID,
		Prompt:            "Approve the deployment?",
		ChannelConfigJson: channelCfgJSON(channel, ""),
	})
	if err != nil {
		t.Fatalf("Request RPC: %v", err)
	}
	if !resp.GetAcked() {
		t.Fatalf("Request: want acked=true, got false: %v", resp.GetError().GetMessage())
	}

	// Step 2: Inject interactive event.
	responseURL := responseURLSrv.URL + "/response"
	runner.Send(interactiveSocketEvent(requestID, "approve", "approve", responseURL))

	// Step 3: Wait for WriteAuditStep.
	if !pollUntil(t, 3*time.Second, func() bool { return len(host.AuditSteps()) > 0 }) {
		t.Fatal("timed out waiting for WriteAuditStep")
	}

	steps := host.AuditSteps()
	if len(steps) != 1 {
		t.Fatalf("want 1 audit step, got %d", len(steps))
	}
	step := steps[0]
	if step.StepType != "feedback_response" {
		t.Errorf("step_type: want feedback_response, got %q", step.StepType)
	}
	if step.RequestID != requestID {
		t.Errorf("request_id: want %q, got %q", requestID, step.RequestID)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(step.PayloadJSON), &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload["option_id"] != "approve" {
		t.Errorf("option_id: want approve, got %q", payload["option_id"])
	}

	// Step 4: Wait for response_url POST.
	if !pollUntil(t, 3*time.Second, func() bool {
		v := respURLBody.Load()
		return v != nil && v.(string) != ""
	}) {
		t.Fatal("timed out waiting for response_url POST")
	}

	body := respURLBody.Load().(string)
	if !strings.Contains(body, "Response recorded") {
		t.Errorf("response_url body: want 'Response recorded', got %q", body)
	}
}

// TestChannelRequestInteractiveCallbackLateRequest asserts that when the
// interactive callback arrives for an unknown request_id, the response_url
// receives "expired" and no audit step is written.
func TestChannelRequestInteractiveCallbackLateRequest(t *testing.T) {
	const token = "xoxb-late"

	var respURLBody atomic.Value
	responseURLSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		respURLBody.Store(string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer responseURLSrv.Close()

	runner := newChannelFakeRunner()
	_, chanSvc, host, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// No Slack API calls expected.
		}),
	)
	defer cleanup()

	chanSvc.httpClient = responseURLSrv.Client()

	// No prior Request call — correlation won't be found.
	responseURL := responseURLSrv.URL + "/response"
	runner.Send(interactiveSocketEvent("unknown-req-id", "approve", "approve", responseURL))

	if !pollUntil(t, 3*time.Second, func() bool {
		v := respURLBody.Load()
		return v != nil && v.(string) != ""
	}) {
		t.Fatal("timed out waiting for response_url POST")
	}

	body := respURLBody.Load().(string)
	if !strings.Contains(body, "expired") {
		t.Errorf("response_url body: want 'expired', got %q", body)
	}
	if n := len(host.AuditSteps()); n != 0 {
		t.Errorf("want 0 audit steps, got %d", n)
	}
}

// TestChannelRequestInteractiveCallbackHostReportsLate tests that when
// WriteAuditStep returns Ok=false + "feedback_response_late", the response_url
// receives "already been resolved".
func TestChannelRequestInteractiveCallbackHostReportsLate(t *testing.T) {
	const (
		channel   = "C01CHANNEL"
		ts        = "1700070000.000700"
		requestID = "req-host-late"
		token     = "xoxb-late-host"
	)

	var respURLBody atomic.Value
	responseURLSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		respURLBody.Store(string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer responseURLSrv.Close()

	runner := newChannelFakeRunner()

	// Inline custom host that returns feedback_response_late for our request_id.
	lateHost := &lateAuditHost{
		requestID: requestID,
		credJSON:  credJSON(token),
	}

	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen host: %v", err)
	}
	hostSrv := grpc.NewServer()
	hostv1.RegisterHostServiceServer(hostSrv, lateHost)
	go func() { _ = hostSrv.Serve(hostLis) }()
	defer hostSrv.Stop()

	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	defer hostConn.Close()
	hostClient := hostv1.NewHostServiceClient(hostConn)

	slackSrv := httptest.NewServer(slackPostMessageOK(channel, ts))
	defer slackSrv.Close()

	registry := newHubRegistry(func(_ string) (socketModeRunner, error) { return runner, nil })
	hub, _, err := registry.Acquire("xapp-test-token")
	if err != nil {
		t.Fatalf("acquire hub: %v", err)
	}

	chanSvc := newChannelServiceForTest(
		hostClient,
		registry,
		func(tok string) slackWebAPI {
			return slack.New(tok,
				slack.OptionHTTPClient(slackSrv.Client()),
				slack.OptionAPIURL(slackSrv.URL+"/"),
			)
		},
		responseURLSrv.Client(),
	)
	hub.RegisterInteractiveHandler(chanSvc.handleInteractive)
	defer chanSvc.correlations.Stop()

	// Step 1: Request — stores correlation.
	reqResp, err := chanSvc.Request(context.Background(), &channelv1.RequestRequest{
		RequestId:         requestID,
		Prompt:            "Approve?",
		ChannelConfigJson: channelCfgJSON(channel, ""),
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !reqResp.GetAcked() {
		t.Fatalf("Request: want acked=true, got false: %v", reqResp.GetError())
	}

	// Step 2: Inject interactive event; host returns late.
	responseURL := responseURLSrv.URL + "/response"
	runner.Send(interactiveSocketEvent(requestID, "approve", "approve", responseURL))

	if !pollUntil(t, 3*time.Second, func() bool {
		v := respURLBody.Load()
		return v != nil && v.(string) != ""
	}) {
		t.Fatal("timed out waiting for response_url POST")
	}

	body := respURLBody.Load().(string)
	if !strings.Contains(body, "already been resolved") {
		t.Errorf("response_url body: want 'already been resolved', got %q", body)
	}
}

// TestChannelInteractiveAckedSynchronously asserts that when an interactive
// event arrives, the hub calls Ack BEFORE dispatching the handler. It does
// this by overriding the hub's interactive handler with a probe that captures
// runner.AckCount() at the instant the handler starts. If Ack ran first, the
// captured count must be 1; if the hub called the handler first it would be 0.
func TestChannelInteractiveAckedSynchronously(t *testing.T) {
	const requestID = "req-ack-sync"

	runner := newChannelFakeRunner()
	_, chanSvc, _, cleanup := setupChannelServiceWithRunner(t,
		credJSON("xoxb-ack-sync"),
		cfgWithToken("xapp-test-token"),
		runner,
		slackPostMessageOK("C01CHANNEL", "1700080000.000800"),
	)
	defer cleanup()

	// Acquire the same hub that setupChannelServiceWithRunner wired up so we
	// can install our probe handler. RegisterInteractiveHandler uses
	// atomic.Pointer.Store, so a second call overwrites the first — the probe
	// replaces chanSvc.handleInteractive for the duration of this test.
	hub, _, err := chanSvc.hubRegistry.Acquire("xapp-test-token")
	if err != nil {
		t.Fatalf("Acquire hub: %v", err)
	}

	var ackCountAtHandlerEntry int32
	handlerDone := make(chan struct{})
	hub.RegisterInteractiveHandler(func(_ socketmode.Event, _ slack.InteractionCallback) {
		// Capture AckCount at the instant we enter the handler. If Ack ran
		// before this handler was called (as sockethub.go guarantees), the
		// count will be 1. If the hub dispatched the handler first, it would be 0.
		atomic.StoreInt32(&ackCountAtHandlerEntry, int32(runner.AckCount()))
		close(handlerDone)
	})

	runner.Send(interactiveSocketEvent(requestID, "approve", "approve", ""))

	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not run within 3s")
	}
	if got := atomic.LoadInt32(&ackCountAtHandlerEntry); got != 1 {
		t.Fatalf("expected AckCount==1 at handler entry (Ack before handler), got %d", got)
	}
}

// threadedMessageSocketEvent builds a socketmode.Event of type EventTypeEventsAPI
// with a MessageEvent whose ThreadTimeStamp is set (a threaded reply).
func threadedMessageSocketEvent(channelID, user, text, ts, threadTS, teamID string) socketmode.Event {
	inner := slackevents.EventsAPIInnerEvent{
		Type: "message",
		Data: &slackevents.MessageEvent{
			Channel:         channelID,
			ChannelType:     "channel",
			User:            user,
			Text:            text,
			TimeStamp:       ts,
			ThreadTimeStamp: threadTS,
			EventTimeStamp:  ts,
		},
	}
	outerEvt := slackevents.EventsAPIEvent{
		TeamID:     teamID,
		InnerEvent: inner,
	}
	return socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Data:    outerEvt,
		Request: &socketmode.Request{EnvelopeID: "env-thread-" + ts},
	}
}

// feedbackCfgJSON builds a channel_config_json string for feedback mode.
func feedbackCfgJSON(channel string) string {
	return fmt.Sprintf(`{"channel":%q,"mode":"feedback"}`, channel)
}

// ── Feedback mode channel tests ───────────────────────────────────────────────

// TestChannelRequestFeedbackMode asserts that Request in feedback mode posts a
// plain text message (no buttons), stores a correlation with mode="feedback", and
// returns acked=true.
func TestChannelRequestFeedbackMode(t *testing.T) {
	const (
		channel   = "C01FEEDBACK"
		ts        = "1700090000.000900"
		requestID = "req-feedback-mode"
		token     = "xoxb-feedback-mode"
	)

	var postBody atomic.Value
	slackMux := newSlackMux(true)
	slackMux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		postBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"channel": channel,
			"ts":      ts,
		})
	})

	runner := newChannelFakeRunner()
	client, chanSvc, _, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		slackMux,
	)
	defer cleanup()

	resp, err := client.Request(context.Background(), &channelv1.RequestRequest{
		RequestId:         requestID,
		Prompt:            "What should I do next?",
		ChannelConfigJson: feedbackCfgJSON(channel),
		Context: &commonv1.RequestContext{
			RunId:    "run-feedback-1",
			PolicyId: "pol-1",
		},
	})
	if err != nil {
		t.Fatalf("Request RPC: %v", err)
	}
	if !resp.GetAcked() {
		t.Fatalf("Request: want acked=true, got false: %v", resp.GetError().GetMessage())
	}

	// Verify that the POST body does not contain Block Kit blocks (no buttons).
	rawBody := postBody.Load()
	if rawBody == nil {
		t.Fatal("Slack API was not called")
	}
	bodyStr := rawBody.(string)
	if strings.Contains(bodyStr, "blocks") {
		t.Error("feedback mode must post plain text, not Block Kit blocks")
	}

	// Verify correlation is stored with mode="feedback".
	corr, ok := chanSvc.correlations.take(requestID)
	if !ok {
		t.Fatal("correlation not stored for requestID")
	}
	if corr.mode != "feedback" {
		t.Errorf("correlation.mode: want %q, got %q", "feedback", corr.mode)
	}
	if corr.ts != ts {
		t.Errorf("correlation.ts: want %q, got %q", ts, corr.ts)
	}
}

// TestChannelRequestFeedbackThreadReply tests the full feedback round-trip:
// Request (feedback mode) → operator sends threaded reply → handleThreadReply
// calls WriteAuditStep(feedback_response).
func TestChannelRequestFeedbackThreadReply(t *testing.T) {
	const (
		channel   = "C01FBTHREAD"
		ts        = "1700091000.000100" // parent message ts = thread_ts we watch
		replyTS   = "1700091000.000200"
		requestID = "req-fb-thread"
		token     = "xoxb-fb-thread"
		replyText = "Please proceed with caution."
	)

	runner := newChannelFakeRunner()
	_, chanSvc, host, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		slackPostMessageOK(channel, ts),
	)
	defer cleanup()

	// Register the thread-reply handler on the hub.
	hub, _, err := chanSvc.hubRegistry.Acquire("xapp-test-token")
	if err != nil {
		t.Fatalf("Acquire hub: %v", err)
	}
	hub.RegisterMessageHandler(chanSvc.handleThreadReply)

	// Step 1: Request in feedback mode — posts plain text, stores correlation.
	reqResp, err := chanSvc.Request(context.Background(), &channelv1.RequestRequest{
		RequestId:         requestID,
		Prompt:            "Which deployment strategy should I use?",
		ChannelConfigJson: feedbackCfgJSON(channel),
		Context:           &commonv1.RequestContext{RunId: "run-thread-1", PolicyId: "pol-1"},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !reqResp.GetAcked() {
		t.Fatalf("Request: want acked=true, got false: %v", reqResp.GetError())
	}

	// Step 2: Inject a threaded reply from an operator.
	// The thread_ts of the reply must match the parent message ts stored in the correlation.
	runner.Send(threadedMessageSocketEvent(channel, "U01OPERATOR", replyText, replyTS, ts, "T01TEAM"))

	// Step 3: Wait for WriteAuditStep.
	if !pollUntil(t, 3*time.Second, func() bool { return len(host.AuditSteps()) > 0 }) {
		t.Fatal("timed out waiting for WriteAuditStep from thread reply")
	}

	steps := host.AuditSteps()
	if len(steps) != 1 {
		t.Fatalf("want 1 audit step, got %d", len(steps))
	}
	step := steps[0]
	if step.StepType != "feedback_response" {
		t.Errorf("step_type: want feedback_response, got %q", step.StepType)
	}
	if step.RequestID != requestID {
		t.Errorf("request_id: want %q, got %q", requestID, step.RequestID)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(step.PayloadJSON), &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload["text"] != replyText {
		t.Errorf("text: want %q, got %q", replyText, payload["text"])
	}
}

// TestChannelRequestFeedbackThreadReply_IgnoresNonThread asserts that a plain
// (non-threaded) message event does not trigger WriteAuditStep.
func TestChannelRequestFeedbackThreadReply_IgnoresNonThread(t *testing.T) {
	const (
		channel   = "C01FBNOTHREAD"
		ts        = "1700092000.000100"
		requestID = "req-fb-nothread"
		token     = "xoxb-fb-nothread"
	)

	runner := newChannelFakeRunner()
	_, chanSvc, host, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		slackPostMessageOK(channel, ts),
	)
	defer cleanup()

	hub, _, err := chanSvc.hubRegistry.Acquire("xapp-test-token")
	if err != nil {
		t.Fatalf("Acquire hub: %v", err)
	}
	hub.RegisterMessageHandler(chanSvc.handleThreadReply)

	// Request in feedback mode.
	reqResp, err := chanSvc.Request(context.Background(), &channelv1.RequestRequest{
		RequestId:         requestID,
		Prompt:            "Please advise.",
		ChannelConfigJson: feedbackCfgJSON(channel),
		Context:           &commonv1.RequestContext{RunId: "run-nothread-1"},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !reqResp.GetAcked() {
		t.Fatalf("Request: want acked=true, got false")
	}

	// Send a plain (non-threaded) message — no ThreadTimeStamp.
	runner.Send(eventsAPISocketEvent(channel, "channel", "U01USER", "just a comment", "1700092000.000200", "T01TEAM"))

	// Give a short window; no audit step should appear.
	time.Sleep(100 * time.Millisecond)
	if n := len(host.AuditSteps()); n != 0 {
		t.Errorf("want 0 audit steps for non-threaded message, got %d", n)
	}
}

// TestChannelRequestFeedbackThreadReply_IgnoresApprovalThread asserts that a
// threaded reply on an approval (button mode) message does not trigger
// WriteAuditStep and does not consume the correlation.
func TestChannelRequestFeedbackThreadReply_IgnoresApprovalThread(t *testing.T) {
	const (
		channel   = "C01APPROVAL"
		ts        = "1700093000.000100"
		replyTS   = "1700093000.000200"
		requestID = "req-approval-thread"
		token     = "xoxb-approval-thread"
	)

	runner := newChannelFakeRunner()
	_, chanSvc, host, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		slackPostMessageOK(channel, ts),
	)
	defer cleanup()

	hub, _, err := chanSvc.hubRegistry.Acquire("xapp-test-token")
	if err != nil {
		t.Fatalf("Acquire hub: %v", err)
	}
	hub.RegisterMessageHandler(chanSvc.handleThreadReply)

	// Request in approval (button) mode — no "mode":"feedback" in config.
	reqResp, err := chanSvc.Request(context.Background(), &channelv1.RequestRequest{
		RequestId:         requestID,
		Prompt:            "Approve?",
		ChannelConfigJson: channelCfgJSON(channel, ""),
		Context:           &commonv1.RequestContext{RunId: "run-approval-1"},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !reqResp.GetAcked() {
		t.Fatalf("Request: want acked=true, got false")
	}

	// Send a threaded reply to the approval message.
	runner.Send(threadedMessageSocketEvent(channel, "U01OPERATOR", "FYI, I'm approving via buttons", replyTS, ts, "T01TEAM"))

	// Short window — no audit step should appear (approval replies come via buttons).
	time.Sleep(100 * time.Millisecond)
	if n := len(host.AuditSteps()); n != 0 {
		t.Errorf("want 0 audit steps for thread reply on approval message, got %d", n)
	}

	// The correlation must still be present (not consumed by handleThreadReply).
	if _, ok := chanSvc.correlations.take(requestID); !ok {
		t.Error("approval correlation was incorrectly consumed by handleThreadReply")
	}
}

// ── lateAuditHost ─────────────────────────────────────────────────────────────

// lateAuditHost is an inline hostv1.HostServiceServer that returns
// Ok=false + Error.Message="feedback_response_late" for WriteAuditStep with a
// specific requestID. All other RPCs use minimal stubs.
type lateAuditHost struct {
	hostv1.UnimplementedHostServiceServer
	requestID string
	credJSON  string
}

func (h *lateAuditHost) GetCredentials(_ context.Context, _ *hostv1.GetCredentialsRequest) (*hostv1.GetCredentialsResponse, error) {
	return &hostv1.GetCredentialsResponse{CredentialsJson: h.credJSON}, nil
}

func (h *lateAuditHost) GetInstanceConfig(_ context.Context, _ *hostv1.GetInstanceConfigRequest) (*hostv1.GetInstanceConfigResponse, error) {
	return &hostv1.GetInstanceConfigResponse{ConfigJson: `{"app_level_token":"xapp-test"}`}, nil
}

func (h *lateAuditHost) SetHealthState(_ context.Context, _ *hostv1.SetHealthStateRequest) (*hostv1.SetHealthStateResponse, error) {
	return &hostv1.SetHealthStateResponse{Ok: true}, nil
}

func (h *lateAuditHost) WriteAuditStep(_ context.Context, req *hostv1.WriteAuditStepRequest) (*hostv1.WriteAuditStepResponse, error) {
	if req.GetRequestId() == h.requestID {
		return &hostv1.WriteAuditStepResponse{
			Ok: false,
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INTERNAL,
				Message: "feedback_response_late",
			},
		}, nil
	}
	return &hostv1.WriteAuditStepResponse{Ok: true}, nil
}

// ── credentialRotatingHost ────────────────────────────────────────────────────

// credentialRotatingHost is an inline hostv1.HostServiceServer that returns
// tokenA on the first GetCredentials call and tokenB on all subsequent calls.
// It mirrors the lateAuditHost pattern so tests can exercise the verifiedToken
// short-circuit by swapping the token between two Call() invocations without
// needing a FakeHost credential mutation API (which doesn't exist).
type credentialRotatingHost struct {
	hostv1.UnimplementedHostServiceServer
	callCount      atomic.Int32
	tokenA, tokenB string
	setHealthCalls atomic.Int32
}

func (h *credentialRotatingHost) GetCredentials(_ context.Context, _ *hostv1.GetCredentialsRequest) (*hostv1.GetCredentialsResponse, error) {
	n := h.callCount.Add(1)
	tok := h.tokenA
	if n > 1 {
		tok = h.tokenB
	}
	return &hostv1.GetCredentialsResponse{
		CredentialsJson: fmt.Sprintf(`{"strategy":"oauth2_authcode","token":{"access_token":%q}}`, tok),
	}, nil
}

func (h *credentialRotatingHost) GetInstanceConfig(_ context.Context, _ *hostv1.GetInstanceConfigRequest) (*hostv1.GetInstanceConfigResponse, error) {
	return &hostv1.GetInstanceConfigResponse{ConfigJson: "{}"}, nil
}

func (h *credentialRotatingHost) SetHealthState(_ context.Context, _ *hostv1.SetHealthStateRequest) (*hostv1.SetHealthStateResponse, error) {
	h.setHealthCalls.Add(1)
	return &hostv1.SetHealthStateResponse{}, nil
}

func (h *credentialRotatingHost) EmitMetric(_ context.Context, _ *hostv1.EmitMetricRequest) (*hostv1.EmitMetricResponse, error) {
	return &hostv1.EmitMetricResponse{}, nil
}

// ── Auth.test verification tests ─────────────────────────────────────────────

// TestToolService_Call_AuthTestFails_SetsUnhealthy asserts that when auth.test
// returns invalid_auth, Call returns a PERMISSION error envelope, the plugin
// transitions to UNHEALTHY/auth_expired, and the tool handler is never called.
func TestToolService_Call_AuthTestFails_SetsUnhealthy(t *testing.T) {
	var toolCallCount atomic.Int32
	mux := newSlackMux(false) // auth.test returns invalid_auth
	mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		toolCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1.0"}`))
	})

	backend := httptest.NewServer(mux)
	defer backend.Close()

	host := plugintest.NewFakeHost(
		plugintest.WithCredentialsJSON(`{"strategy":"oauth2_authcode","token":{"access_token":"xoxb-test"}}`),
	)
	svcs, cleanup := setupAllWithHost(t, host, backend)
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
		t.Fatal("expected error envelope, got success")
	}
	if env.GetCode() != commonv1.ErrorCode_ERROR_CODE_PERMISSION {
		t.Errorf("error code: want PERMISSION, got %v", env.GetCode())
	}
	if !strings.Contains(env.GetMessage(), "auth.test failed") {
		t.Errorf("message should mention auth.test failed, got: %q", env.GetMessage())
	}

	state, detail, ok := host.Health()
	if !ok {
		t.Fatal("expected SetHealthState to be called, but no health state recorded")
	}
	if state != plugintest.HealthStateUnhealthy {
		t.Errorf("health state: want Unhealthy, got %v", state)
	}
	if detail != "auth_expired" {
		t.Errorf("health detail: want auth_expired, got %q", detail)
	}

	if n := toolCallCount.Load(); n != 0 {
		t.Errorf("/chat.postMessage hit count: want 0 (tool not called on auth failure), got %d", n)
	}
}

// TestToolService_Call_AuthTestOnce_SkippedOnSubsequentCalls asserts that
// auth.test is called exactly once when the same token is used for two
// consecutive Call() invocations. The verifiedToken short-circuit prevents the
// second auth.test round-trip.
func TestToolService_Call_AuthTestOnce_SkippedOnSubsequentCalls(t *testing.T) {
	var authTestCount atomic.Int32
	// Build the mux manually (not via newSlackMux) so we can wrap /auth.test
	// with a counter without registering the pattern twice (Go 1.22+ panics
	// on duplicate registrations in the same mux).
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, r *http.Request) {
		authTestCount.Add(1)
		authTestHandler(true)(w, r)
	})
	mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1.0"}`))
	})

	backend := httptest.NewServer(mux)
	defer backend.Close()

	host := plugintest.NewFakeHost(
		plugintest.WithCredentialsJSON(`{"strategy":"oauth2_authcode","token":{"access_token":"xoxb-same-token"}}`),
	)
	svcs, cleanup := setupAllWithHost(t, host, backend)
	defer cleanup()

	callReq := &toolv1.CallRequest{
		ToolName:  "post_message",
		InputJson: `{"channel":"C123","text":"hello"}`,
	}

	resp, err := svcs.tool.Call(context.Background(), callReq)
	if err != nil {
		t.Fatalf("first Call RPC error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("first Call: expected success, got error: %v", resp.GetError().GetMessage())
	}

	resp, err = svcs.tool.Call(context.Background(), callReq)
	if err != nil {
		t.Fatalf("second Call RPC error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("second Call: expected success, got error: %v", resp.GetError().GetMessage())
	}

	if n := authTestCount.Load(); n != 1 {
		t.Errorf("auth.test hit count: want 1 (short-circuit on second call), got %d", n)
	}
}

// TestToolService_Call_AuthTestReruns_OnTokenRotation asserts that auth.test
// runs again when the access token changes between Call() invocations. A custom
// credentialRotatingHost is used because FakeHost's WithCredentialsJSON is
// constructor-only (no setter to mutate between calls).
func TestToolService_Call_AuthTestReruns_OnTokenRotation(t *testing.T) {
	var authTestCount atomic.Int32
	// Build the mux manually (not via newSlackMux) so we can wrap /auth.test
	// with a counter without registering the pattern twice (Go 1.22+ panics
	// on duplicate registrations in the same mux).
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, r *http.Request) {
		authTestCount.Add(1)
		authTestHandler(true)(w, r)
	})
	mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1.0"}`))
	})

	backend := httptest.NewServer(mux)
	defer backend.Close()

	rotatingHost := &credentialRotatingHost{
		tokenA: "xoxb-token-a",
		tokenB: "xoxb-token-b",
	}

	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen host: %v", err)
	}
	hostSrv := grpc.NewServer()
	hostv1.RegisterHostServiceServer(hostSrv, rotatingHost)
	go func() { _ = hostSrv.Serve(hostLis) }()
	defer hostSrv.Stop()

	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	defer hostConn.Close()
	hostClient := hostv1.NewHostServiceClient(hostConn)

	toolSvc := NewToolService(hostClient, backend.Client(), backend.URL+"/")

	toolLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tool: %v", err)
	}
	toolSrv := grpc.NewServer()
	toolv1.RegisterToolServiceServer(toolSrv, toolSvc)
	go func() { _ = toolSrv.Serve(toolLis) }()
	defer toolSrv.Stop()

	toolConn, err := grpc.NewClient(toolLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial tool: %v", err)
	}
	defer toolConn.Close()
	toolClient := toolv1.NewToolServiceClient(toolConn)

	callReq := &toolv1.CallRequest{
		ToolName:  "post_message",
		InputJson: `{"channel":"C123","text":"hello"}`,
	}

	// First call: uses tokenA → auth.test fires (count becomes 1).
	resp, err := toolClient.Call(context.Background(), callReq)
	if err != nil {
		t.Fatalf("first Call RPC error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("first Call: expected success, got error: %v", resp.GetError().GetMessage())
	}

	// Second call: credentialRotatingHost returns tokenB → verifiedToken mismatch
	// → auth.test fires again (count becomes 2).
	resp, err = toolClient.Call(context.Background(), callReq)
	if err != nil {
		t.Fatalf("second Call RPC error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("second Call: expected success, got error: %v", resp.GetError().GetMessage())
	}

	if n := authTestCount.Load(); n != 2 {
		t.Errorf("auth.test hit count: want 2 (one per distinct token), got %d", n)
	}
}

// ── ChannelService contract tests ─────────────────────────────────────────────

// TestChannelService_Notify_DoesNotPerformAuthTest locks the contract that
// ChannelService.Notify never calls auth.test. The contract is direct:
// auth.test is a ToolService-internal concern (service.go:162); ChannelService
// fetches credentials and posts the message without a token verification step.
// If a future regression added auth.test to ChannelService, the counter would
// rise from 0 to non-zero and this test would fail.
func TestChannelService_Notify_DoesNotPerformAuthTest(t *testing.T) {
	const (
		channel = "C01CONTRACT"
		ts      = "1700060000.000600"
		token   = "xoxb-channel-contract"
	)

	var authTestCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, r *http.Request) {
		authTestCount.Add(1)
		authTestHandler(true)(w, r)
	})
	mux.Handle("/chat.postMessage", slackPostMessageOK(channel, ts))

	backend := httptest.NewServer(mux)
	defer backend.Close()

	runner := newChannelFakeRunner()
	client, _, _, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		mux,
	)
	defer cleanup()

	for range 2 {
		resp, err := client.Notify(context.Background(), &channelv1.NotifyRequest{
			ChannelConfigJson: channelCfgJSON(channel, ""),
			PayloadJson:       `{"text":"contract check"}`,
			EventType:         "run_complete",
		})
		if err != nil {
			t.Fatalf("Notify RPC: %v", err)
		}
		if !resp.GetOk() {
			t.Fatalf("Notify: want ok=true, got ok=false (error: %v)", resp.GetError().GetMessage())
		}
	}

	if n := authTestCount.Load(); n != 0 {
		t.Errorf("auth.test hit count: want 0 (ChannelService never calls auth.test), got %d", n)
	}
}

// TestChannelService_Request_HandleInteractiveTakesCorrelation verifies that
// handleInteractive consumes the correlation entry created by Request. After
// Request returns Acked=true, the entry must exist; once handleInteractive runs
// (evidenced by a WriteAuditStep call), the entry must be gone — proving that
// handleInteractive, not the test, consumed it via c.correlations.take.
func TestChannelService_Request_HandleInteractiveTakesCorrelation(t *testing.T) {
	const (
		channel   = "C01CORRELATION"
		ts        = "1700062000.000602"
		requestID = "req-correlation-test"
		token     = "xoxb-correlation-test"
	)

	// Serve both the Slack API and the response_url endpoint on one mux so
	// handleInteractive can post its acknowledgment after consuming the entry.
	responseURLSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer responseURLSrv.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"channel":%q,"ts":%q}`, channel, ts)
	})

	runner := newChannelFakeRunner()
	client, svc, host, cleanup := setupChannelServiceWithRunner(t,
		credJSON(token),
		cfgWithToken("xapp-test-token"),
		runner,
		mux,
	)
	defer cleanup()

	// Allow handleInteractive to post the response_url acknowledgment.
	svc.httpClient = responseURLSrv.Client()

	resp, err := client.Request(context.Background(), &channelv1.RequestRequest{
		RequestId:         requestID,
		ChannelConfigJson: channelCfgJSON(channel, ""),
		Prompt:            "Should we proceed?",
	})
	if err != nil {
		t.Fatalf("Request RPC: %v", err)
	}
	if !resp.GetAcked() {
		t.Fatalf("Request: want acked=true, got acked=false (error: %v)", resp.GetError().GetMessage())
	}

	// Inject the interactive event — handleInteractive will call take() and
	// WriteAuditStep. Do NOT call take() here; we want handleInteractive to
	// be the one that consumes the entry.
	responseURL := responseURLSrv.URL + "/response"
	runner.Send(interactiveSocketEvent(requestID, "approve", "approve", responseURL))

	// Wait for handleInteractive to call WriteAuditStep (proof it ran and
	// therefore called take() already).
	if !pollUntil(t, 3*time.Second, func() bool { return len(host.AuditSteps()) > 0 }) {
		t.Fatal("timed out waiting for WriteAuditStep — handleInteractive may not have run")
	}

	// Now take() must return found=false: handleInteractive already consumed
	// the entry. If this returns true, handleInteractive didn't call take().
	_, stillPresent := svc.correlations.take(requestID)
	if stillPresent {
		t.Error("correlation entry still present after handleInteractive ran; handleInteractive must call take()")
	}
}
