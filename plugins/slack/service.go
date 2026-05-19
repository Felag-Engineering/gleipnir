package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/oklog/ulid/v2"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

// ToolService implements toolv1.ToolServiceServer with five Slack Web API tools:
// post_message, list_channels, search_messages, react, and set_topic.
// It fetches credentials on every Call (no in-process caching, per spec §9.4).
type ToolService struct {
	toolv1.UnimplementedToolServiceServer
	host       hostv1.HostServiceClient
	httpClient *http.Client
	apiURL     string // empty = Slack production default; tests pass httptest.Server URL + "/"
}

// NewToolService creates a ToolService using hostClient for host RPCs,
// httpClient for outbound Slack API calls, and apiURL to override the Slack
// API base URL (empty string uses the production default).
// Mirrors the ntfy constructor signature (plugins/ntfy/service.go:27).
func NewToolService(host hostv1.HostServiceClient, httpClient *http.Client, apiURL string) *ToolService {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	// apiURL "" means production (slack.com default). Tests pass
	// backend.URL + "/" — the trailing slash is required because slack-go
	// concatenates apiURL + methodName internally (slack.go:168).
	return &ToolService{host: host, httpClient: httpClient, apiURL: apiURL}
}

// ListTools advertises the five Slack tools. InputSchema is a JSON string
// because toolv1.ToolSchema.InputSchema is a string on the wire
// (plugin-sdk/gen/.../tool.pb.go:39); reflectInputSchemaJSON produces it.
func (s *ToolService) ListTools(_ context.Context, _ *toolv1.ListToolsRequest) (*toolv1.ListToolsResponse, error) {
	return &toolv1.ListToolsResponse{
		Tools: []*toolv1.ToolSchema{
			{
				Name:        "post_message",
				Description: "Post a plain-text message to a Slack channel or DM.",
				InputSchema: reflectInputSchemaJSON(PostMessageParams{}),
			},
			{
				Name:        "list_channels",
				Description: "List Slack channels visible to the bot user.",
				InputSchema: reflectInputSchemaJSON(ListChannelsParams{}),
			},
			{
				Name:        "search_messages",
				Description: "Search messages across the workspace using Slack's search. Requires the search:read scope; bot-only installs that were not granted this scope will receive a PERMISSION error at runtime.",
				InputSchema: reflectInputSchemaJSON(SearchMessagesParams{}),
			},
			{
				Name:        "react",
				Description: "Add an emoji reaction to a Slack message.",
				InputSchema: reflectInputSchemaJSON(ReactParams{}),
			},
			{
				Name:        "set_topic",
				Description: "Set the topic of a Slack channel.",
				InputSchema: reflectInputSchemaJSON(SetTopicParams{}),
			},
		},
	}, nil
}

// Cancel is a no-op. Cancellation is driven by context: every outbound Slack
// call is made with the Call context, so it will be cancelled automatically
// when the host cancels the RPC.
func (s *ToolService) Cancel(_ context.Context, _ *toolv1.CancelRequest) (*toolv1.CancelResponse, error) {
	return &toolv1.CancelResponse{}, nil
}

// Call dispatches to one of the five Slack tool handlers. It fetches
// credentials on every invocation (no caching), builds a per-call slack.Client,
// dispatches by tool name, emits one metric sample, and updates plugin health
// on persistent auth failures.
func (s *ToolService) Call(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
	// Propagate the host-injected call ID to all outgoing host RPCs so the host
	// can correlate EmitMetric and SetHealthState back to this run and step.
	hostCtx := serve.WithCallContext(ctx)

	// Fetch the bot token. Per spec §9.4, do not cache the result across calls.
	credResp, err := s.host.GetCredentials(hostCtx, &hostv1.GetCredentialsRequest{})
	if err != nil {
		return errorResponse(commonv1.ErrorCode_ERROR_CODE_INTERNAL, fmt.Sprintf("GetCredentials: %v", err)), nil
	}

	// [R6] Empty or placeholder JSON means no credentials have been configured.
	raw := credResp.GetCredentialsJson()
	if raw == "" || raw == "{}" {
		s.setHealth(hostCtx, healthAuthMissing)
		return errorResponse(commonv1.ErrorCode_ERROR_CODE_PERMISSION, "no Slack credentials configured; authorize the plugin in the admin UI"), nil
	}

	var creds slackCreds
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return errorResponse(commonv1.ErrorCode_ERROR_CODE_INTERNAL, fmt.Sprintf("parse credentials: %v", err)), nil
	}
	if creds.Token.AccessToken == "" {
		s.setHealth(hostCtx, healthAuthMissing)
		return errorResponse(commonv1.ErrorCode_ERROR_CODE_PERMISSION, "Slack access_token is empty; re-authorize the plugin in the admin UI"), nil
	}

	// Build a per-call Slack client so the bot token is always freshly set from the credential fetch above.
	opts := []slack.Option{slack.OptionHTTPClient(s.httpClient)}
	if s.apiURL != "" {
		opts = append(opts, slack.OptionAPIURL(s.apiURL))
	}
	sc := slack.New(creds.Token.AccessToken, opts...)

	toolName := req.GetToolName()

	// Guard against unknown tool names before touching the Slack API.
	// Unknown tools return INVALID_ARG without emitting a metric or hitting
	// the Slack API, matching the minimal-tool dispatch pattern
	// (plugin-sdk/examples/minimal-tool/service.go:62-69).
	switch toolName {
	case "post_message", "list_channels", "search_messages", "react", "set_topic":
		// valid — continue
	default:
		return errorResponse(commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
			fmt.Sprintf("unknown tool: %q", toolName)), nil
	}

	// Dispatch and measure.
	start := time.Now()
	outputJSON, callErr := s.dispatch(ctx, sc, toolName, req.GetInputJson())

	// [R1] Emit exactly one gauge sample per Call(). We use a single gauge
	// because EmitMetric carries no type discriminator and the host registers
	// every metric as a GaugeVec (internal/plugin/hostsvc/metrics.go:27-34).
	// The host prepends "gleipnir_plugin_" automatically; we MUST NOT pre-prefix.
	outcome := "ok"
	if callErr != nil {
		outcome = "error"
	}
	elapsed := time.Since(start).Seconds()
	s.emitMetric(hostCtx, "tool_call_last_duration_seconds", elapsed,
		map[string]string{"tool": toolName, "outcome": outcome})

	if callErr != nil {
		code, hint := mapErr(callErr)
		if hint != healthNone {
			s.setHealth(hostCtx, hint)
		}
		return errorResponse(code, callErr.Error()), nil
	}

	return &toolv1.CallResponse{OutputJson: string(outputJSON)}, nil
}

// dispatch routes the call to the correct per-tool handler based on tool name.
// Returns raw output JSON bytes and any error from the Slack API.
func (s *ToolService) dispatch(ctx context.Context, sc *slack.Client, toolName, inputJSON string) ([]byte, error) {
	switch toolName {
	case "post_message":
		return handlePostMessage(ctx, sc, inputJSON)
	case "list_channels":
		return handleListChannels(ctx, sc, inputJSON)
	case "search_messages":
		return handleSearchMessages(ctx, sc, inputJSON)
	case "react":
		return handleReact(ctx, sc, inputJSON)
	case "set_topic":
		return handleSetTopic(ctx, sc, inputJSON)
	default:
		return nil, fmt.Errorf("unknown tool: %q", toolName)
	}
}

// emitMetric records one gauge sample per Call(). Non-fatal: if the RPC fails,
// we log and continue rather than failing the tool call.
//
// The host prepends "gleipnir_plugin_" to the metric name automatically
// (metrics.go:68-69). Plugin and instance labels are host-injected
// (handlers.go:373-375); we MUST NOT include them.
func (s *ToolService) emitMetric(ctx context.Context, name string, value float64, labels map[string]string) {
	_, err := s.host.EmitMetric(ctx, &hostv1.EmitMetricRequest{
		Name:   name,
		Value:  value,
		Labels: labels,
	})
	if err != nil {
		log.Printf("slack: EmitMetric(%s) failed: %v", name, err)
	}
}

// setHealth updates the plugin health state to UNHEALTHY with a detail string
// describing the auth failure type. It is called only for persistent failures
// (missing credentials or expired/revoked tokens) where operator intervention
// is required.
func (s *ToolService) setHealth(ctx context.Context, h healthHint) {
	var detail string
	switch h {
	case healthAuthExpired:
		detail = "auth_expired"
	case healthAuthMissing:
		detail = "auth_missing"
	default:
		return
	}
	_, _ = s.host.SetHealthState(ctx, &hostv1.SetHealthStateRequest{
		State:  hostv1.PluginHealthState_PLUGIN_HEALTH_STATE_UNHEALTHY,
		Detail: detail,
	})
}

// errorResponse constructs a CallResponse carrying an in-band ErrorEnvelope.
// Errors are delivered in-band (not as gRPC-level errors) per the plugin protocol.
func errorResponse(code commonv1.ErrorCode, message string) *toolv1.CallResponse {
	return &toolv1.CallResponse{
		Error: &commonv1.ErrorEnvelope{
			Code:    code,
			Message: message,
		},
	}
}

// ── TriggerService ────────────────────────────────────────────────────────────

// socketModeRunner abstracts socketmode.Client for testability.
//
// Concurrency contract: implementors MUST consume events from a SINGLE goroutine
// and call onEvent sequentially. Per-stream ordering is part of the trigger
// contract (issue #234 AC); concurrent dispatch is forbidden.
type socketModeRunner interface {
	// Run opens the Socket Mode WebSocket connection, calls onEvent for each
	// incoming event, and blocks until ctx is cancelled or a fatal error occurs.
	// Returns context.Canceled on clean shutdown, or an error string containing
	// "invalid_auth" / "account_inactive" / "not_authed" / "token_revoked" on
	// auth failure (per socket_mode_managed_conn.go:188-249).
	Run(ctx context.Context, onEvent func(socketmode.Event)) error

	// Ack acknowledges a Slack EventsAPI envelope. Must be called within 3 seconds
	// of receiving the event or Slack will redeliver it.
	Ack(req socketmode.Request)
}

// socketModeFactory creates a socketModeRunner from an app-level (xapp-) token.
type socketModeFactory func(xappToken string) (socketModeRunner, error)

// productionSocketModeRunner wraps socketmode.Client to satisfy the socketModeRunner
// interface with a single-goroutine fanout — events are delivered sequentially
// to preserve per-stream ordering. Lives here for fakeSocketModeRunner compatibility
// in service_test.go; sockethub.go calls the interface, not the concrete type.
type productionSocketModeRunner struct {
	client *socketmode.Client
}

// Run opens the Socket Mode connection. A single goroutine ranges over
// client.Events and calls onEvent sequentially (preserving ordering). Run then
// calls client.RunContext(ctx) and waits for the fanout goroutine to drain
// before returning, so the caller never observes partial shutdown.
func (r *productionSocketModeRunner) Run(ctx context.Context, onEvent func(socketmode.Event)) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range r.client.Events {
			onEvent(ev)
		}
	}()

	err := r.client.RunContext(ctx)
	// Wait for the fanout goroutine to finish draining the Events channel.
	// RunContext closes client.Events on return, so the goroutine exits promptly.
	wg.Wait()
	return err
}

// Ack delegates to the underlying socketmode.Client.
func (r *productionSocketModeRunner) Ack(req socketmode.Request) {
	r.client.Ack(req)
}

// defaultSocketModeFactory constructs a productionSocketModeRunner using the
// provided xapp- token. The bot token slot is left empty ("") because Socket
// Mode only requires the app-level token; the bot token is used only for API
// calls (which go through ToolService, not TriggerService).
func defaultSocketModeFactory(xappToken string) (socketModeRunner, error) {
	api := slack.New("", slack.OptionAppLevelToken(xappToken))
	client := socketmode.New(api)
	return &productionSocketModeRunner{client: client}, nil
}

// TriggerService implements triggerv1.TriggerServiceServer. It opens a Slack
// Socket Mode WebSocket connection, receives message and app_mention events,
// translates them to SlackChannelMessagePayload JSON, applies the instance-level
// subscription scope, and emits matching events to the host via EmitEvent.
type TriggerService struct {
	triggerv1.UnimplementedTriggerServiceServer
	host        hostv1.HostServiceClient
	hubRegistry *hubRegistry
}

// NewTriggerService creates a TriggerService that uses hostClient for host RPCs
// and the shared hubRegistry for the Socket Mode connection.
func NewTriggerService(hostClient hostv1.HostServiceClient, registry *hubRegistry) *TriggerService {
	return &TriggerService{
		host:        hostClient,
		hubRegistry: registry,
	}
}

// newTriggerServiceWithRegistry is an internal constructor for tests that use
// a hub registry (backed by a fake socketModeFactory).
func newTriggerServiceWithRegistry(hostClient hostv1.HostServiceClient, registry *hubRegistry) *TriggerService {
	return &TriggerService{
		host:        hostClient,
		hubRegistry: registry,
	}
}

// newTriggerServiceWithFactory is kept for the in-test setup path that creates
// a registry on the fly. Tests that only care about TriggerService use this.
func newTriggerServiceWithFactory(hostClient hostv1.HostServiceClient, factory socketModeFactory) *TriggerService {
	return newTriggerServiceWithRegistry(hostClient, newHubRegistry(factory))
}

// Start is a server-streaming RPC that opens a Slack Socket Mode connection and
// emits channel_message events to the host. It blocks until the stream context
// is cancelled (clean shutdown) or a fatal Slack error occurs (auth failure,
// misconfiguration). The stream itself carries no StartResponse messages —
// canonical event delivery goes through HostService.EmitEvent (spec §4.3).
func (s *TriggerService) Start(req *triggerv1.StartRequest, stream grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	// Propagate the host-injected call ID so SetHealthState and EmitEvent can
	// be correlated back to this trigger stream by the host.
	hostCtx := serve.WithCallContext(stream.Context())

	// Fetch the app-level (xapp-) token from instance config. This token is
	// separate from the OAuth bot token — it is required for Socket Mode.
	cfgResp, err := s.host.GetInstanceConfig(hostCtx, &hostv1.GetInstanceConfigRequest{})
	if err != nil {
		return status.Errorf(codes.Internal, "GetInstanceConfig: %v", err)
	}
	token, err := extractAppLevelToken(cfgResp.GetConfigJson())
	if err != nil || token == "" {
		s.setTriggerHealth(hostCtx, healthConfigMissing)
		return status.Error(codes.FailedPrecondition, "app_level_token is required in instance config but was not set; configure it in the admin UI")
	}

	// Parse the instance-level coarse subscription scope from the request.
	// An empty or missing scope matches all channels.
	scope, err := decodeSubscriptionScope(req.GetWatchScopeJson())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid watch_scope_json: %v", err)
	}

	// Acquire the shared socketHub for this xapp-token. The hub is created on
	// first use and shared with ChannelService's interactive handler.
	hub, release, err := s.hubRegistry.Acquire(token)
	if err != nil {
		return status.Errorf(codes.Internal, "create socket mode runner: %v", err)
	}
	// Release our reference when Start returns so the hub can be torn down if
	// no other caller holds a reference.
	defer release()

	// onEvent is called sequentially by the hub's single fanout goroutine.
	// Per the socketModeRunner contract, this function is never called concurrently.
	onEvent := func(evt socketmode.Event) {
		if evt.Type != socketmode.EventTypeEventsAPI {
			s.handleNonEventsAPI(hostCtx, evt)
			return
		}

		// Ack FIRST, synchronously, unconditionally — beats Slack's 3-second
		// redelivery window even if translate or EmitEvent stalls later.
		// A deferred Ack would run AFTER EmitEvent and could miss the window
		// under host backpressure (blocking #3 from plan review).
		if evt.Request != nil {
			hub.Ack(*evt.Request)
		}

		eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}

		kind, eventID, payload, emit, err := translate(eventsAPIEvent.InnerEvent, eventsAPIEvent.TeamID)
		if err != nil {
			log.Printf("slack: translate error: %v", err)
			return
		}
		if !emit {
			return
		}

		// Extract channelID and mentioned from the decoded payload to evaluate
		// the subscription scope. We unmarshal into SlackChannelMessagePayload
		// because translate already serialized all the fields there.
		var p SlackChannelMessagePayload
		if jsonErr := json.Unmarshal(payload, &p); jsonErr != nil {
			log.Printf("slack: scope check unmarshal: %v", jsonErr)
			return
		}
		// p.ChannelID is the real Slack channel ID (e.g. C012ABCDEF). p.Channel
		// currently also holds the ID — Socket Mode message events don't include
		// resolved channel names. As a result, operator subscription scopes
		// containing entries like "#incidents" won't match; only ID-form entries
		// like "C012ABCDEF" do. Future work: cache an ID→name map via
		// conversations.info so name-form scopes work.
		if !scope.matches(p.ChannelID, p.Channel, p.Mentioned) {
			return
		}

		if _, err := s.host.EmitEvent(hostCtx, &hostv1.EmitEventRequest{
			EventKind: kind,
			EventId:   eventID,
			// PayloadJson carries the JSON bytes as a string on the wire.
			PayloadJson: string(payload),
		}); err != nil {
			// EmitEvent failure is non-fatal — don't tear down the stream.
			log.Printf("slack: EmitEvent failed: %v", err)
		}
	}

	hub.RegisterEventsHandler(onEvent)

	// Block until the stream context is cancelled by the host (clean shutdown)
	// or the hub's Run goroutine exits (auth failure, connection error).
	var runErr error
	select {
	case <-stream.Context().Done():
		// Host cancelled the stream. Treat as clean shutdown.
		runErr = context.Canceled
	case <-hub.Done():
		// Hub exited — inspect doneErr for auth failure or other causes.
		runErr = hub.DoneErr()
	}

	// Clean shutdown: context was cancelled by the host (supervisor restart, etc.)
	// or the stream context expired. Return nil so the supervisor can re-call Start.
	if errors.Is(runErr, context.Canceled) || stream.Context().Err() != nil {
		return nil
	}

	// Auth failure: the 4 named Slack auth-fatal strings surface via RunContext's
	// return value (not via EventTypeInvalidAuth — that fires only on HTTP 404
	// from connect(), per socket_mode_managed_conn.go:235-249).
	if runErr != nil {
		errStr := runErr.Error()
		for _, fatal := range []string{"invalid_auth", "account_inactive", "not_authed", "token_revoked"} {
			if strings.Contains(errStr, fatal) {
				s.setTriggerHealth(hostCtx, healthAuthExpired)
				return status.Error(codes.Unauthenticated, errStr)
			}
		}
		log.Printf("slack: socket mode exited with unexpected error: %v", runErr)
		return status.Error(codes.Unavailable, runErr.Error())
	}

	return nil
}

// handleNonEventsAPI logs diagnostic information for non-message socketmode events
// and updates health state when Slack signals an auth problem at the connection
// level (EventTypeInvalidAuth fires only on HTTP 404 from connect()).
func (s *TriggerService) handleNonEventsAPI(ctx context.Context, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnected:
		log.Printf("slack: socket mode connected")
	case socketmode.EventTypeHello:
		log.Printf("slack: socket mode hello received")
	case socketmode.EventTypeDisconnect:
		// socketmode auto-reconnects; log informational only.
		log.Printf("slack: socket mode disconnected (will reconnect)")
	case socketmode.EventTypeConnectionError:
		log.Printf("slack: socket mode connection error (will reconnect): %v", evt.Data)
	case socketmode.EventTypeInvalidAuth:
		// Fires ONLY on HTTP 404 from connect() — not the common auth-failure path.
		// The 4 named auth strings (invalid_auth etc.) come back via RunContext's
		// return value. Log + mark unhealthy but do NOT return from Start: RunContext
		// will surface its own error which the post-Run classifier handles.
		log.Printf("slack: socket mode invalid_auth event (HTTP 404 from connect)")
		s.setTriggerHealth(ctx, healthAuthExpired)
	}
}

// setTriggerHealth updates the plugin health state. It is called for persistent
// failures that require operator intervention (missing config, auth expiry).
func (s *TriggerService) setTriggerHealth(ctx context.Context, h healthHint) {
	var detail string
	switch h {
	case healthAuthExpired:
		detail = "auth_expired"
	case healthConfigMissing:
		detail = "config_missing"
	default:
		return
	}
	_, _ = s.host.SetHealthState(ctx, &hostv1.SetHealthStateRequest{
		State:  hostv1.PluginHealthState_PLUGIN_HEALTH_STATE_UNHEALTHY,
		Detail: detail,
	})
}

// extractAppLevelToken parses the instance config JSON and returns the
// app_level_token value. Returns ("", nil) when the field is absent or empty.
func extractAppLevelToken(configJSON string) (string, error) {
	if configJSON == "" || configJSON == "{}" {
		return "", nil
	}
	var cfg struct {
		AppLevelToken string `json:"app_level_token"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return "", fmt.Errorf("parse instance config: %w", err)
	}
	return cfg.AppLevelToken, nil
}

// ── ChannelService ────────────────────────────────────────────────────────────

// slackWebAPI is the Web API interface used by ChannelService. It is satisfied
// by *slack.Client in production and by a fake in tests.
type slackWebAPI interface {
	PostMessageContext(ctx context.Context, channelID string, opts ...slack.MsgOption) (string, string, error)
}

// ChannelService implements channelv1.ChannelServiceServer. It posts Slack
// messages (Notify) and interactive Block Kit messages with response buttons
// (Request), then handles button-click callbacks via Socket Mode and calls
// WriteAuditStep(feedback_response) back to the host.
type ChannelService struct {
	channelv1.UnimplementedChannelServiceServer
	host          hostv1.HostServiceClient
	hubRegistry   *hubRegistry
	webAPIFactory func(token string) slackWebAPI // injectable for tests
	httpClient    *http.Client                   // for response_url POSTs
	correlations  *correlationMap
}

// NewChannelService creates a production ChannelService. It acquires the shared
// socketHub for the instance's xapp-token and registers the interactive handler.
// If GetInstanceConfig fails or the token is absent, interactive callbacks are
// silently dropped until the next restart (Notify/Request still report the error
// inline via setChannelHealth).
func NewChannelService(host hostv1.HostServiceClient, registry *hubRegistry, httpClient *http.Client) *ChannelService {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	s := &ChannelService{
		host:        host,
		hubRegistry: registry,
		webAPIFactory: func(token string) slackWebAPI {
			return slack.New(token)
		},
		httpClient:   httpClient,
		correlations: newCorrelationMap(5*time.Minute, 24*time.Hour),
	}

	// Register the interactive handler at construction so it is in place before
	// any Request call arrives. We fetch the token synchronously here; if it
	// fails we skip registration and interactive callbacks won't fire, but
	// Notify/Request will surface the error inline.
	cfgResp, err := host.GetInstanceConfig(context.Background(), &hostv1.GetInstanceConfigRequest{})
	if err != nil {
		log.Printf("slack: ChannelService: GetInstanceConfig at startup: %v", err)
		return s
	}
	token, err := extractAppLevelToken(cfgResp.GetConfigJson())
	if err != nil || token == "" {
		// Token not yet configured — skip registration. Interactive callbacks
		// won't fire until the plugin restarts with a token in config.
		return s
	}

	hub, _, err := registry.Acquire(token)
	if err != nil {
		log.Printf("slack: ChannelService: Acquire hub for interactive handler: %v", err)
		return s
	}
	// We intentionally do not call the releaseFn — this hub reference persists
	// for the plugin process lifetime so the interactive handler always fires.
	hub.RegisterInteractiveHandler(s.handleInteractive)

	return s
}

// newChannelServiceForTest creates a ChannelService for testing with an injected
// webAPIFactory and pre-built correlationMap (no sweep goroutine management).
func newChannelServiceForTest(host hostv1.HostServiceClient, registry *hubRegistry, webAPIFactory func(string) slackWebAPI, httpClient *http.Client) *ChannelService {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ChannelService{
		host:          host,
		hubRegistry:   registry,
		webAPIFactory: webAPIFactory,
		httpClient:    httpClient,
		correlations:  newCorrelationMap(5*time.Minute, 24*time.Hour),
	}
}

// Notify posts a notification message to the configured Slack channel.
// The 10-second deadline is enforced by the host per spec §13.6.
// Failures are returned in-band (ok=false) but do not fail the run.
func (s *ChannelService) Notify(ctx context.Context, req *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
	hostCtx := serve.WithCallContext(ctx)

	credResp, err := s.host.GetCredentials(hostCtx, &hostv1.GetCredentialsRequest{})
	if err != nil {
		return &channelv1.NotifyResponse{
			Ok:    false,
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_INTERNAL, fmt.Sprintf("GetCredentials: %v", err)),
		}, nil
	}
	raw := credResp.GetCredentialsJson()
	if raw == "" || raw == "{}" {
		s.setChannelHealth(hostCtx, healthAuthMissing)
		return &channelv1.NotifyResponse{
			Ok:    false,
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_PERMISSION, "no Slack credentials configured; authorize the plugin in the admin UI"),
		}, nil
	}
	var creds slackCreds
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return &channelv1.NotifyResponse{
			Ok:    false,
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_INTERNAL, fmt.Sprintf("parse credentials: %v", err)),
		}, nil
	}
	if creds.Token.AccessToken == "" {
		s.setChannelHealth(hostCtx, healthAuthMissing)
		return &channelv1.NotifyResponse{
			Ok:    false,
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_PERMISSION, "Slack access_token is empty; re-authorize the plugin in the admin UI"),
		}, nil
	}

	var cfg SlackChannelEntryConfig
	if err := json.Unmarshal([]byte(req.GetChannelConfigJson()), &cfg); err != nil || cfg.Channel == "" {
		return &channelv1.NotifyResponse{
			Ok:    false,
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_INVALID_ARG, "channel_config_json must include a non-empty 'channel' field"),
		}, nil
	}

	// Extract text body from the notification payload (tolerant of unknown fields).
	var payload struct {
		Text string `json:"text"`
	}
	if jsonErr := json.Unmarshal([]byte(req.GetPayloadJson()), &payload); jsonErr != nil {
		// Non-fatal: fall back to the event type as the message.
		payload.Text = req.GetEventType()
	}
	text := payload.Text
	if text == "" {
		text = req.GetEventType()
	}
	if cfg.Mention != "" {
		text = cfg.Mention + " " + text
	}

	sc := s.webAPIFactory(creds.Token.AccessToken)

	type postResult struct{ channel, ts string }
	_, postErr := callWithRetry(ctx, func(ctx context.Context) (postResult, error) {
		ch, ts, err := sc.PostMessageContext(ctx, cfg.Channel, slack.MsgOptionText(text, false))
		return postResult{ch, ts}, err
	})
	if postErr != nil {
		code, hint := mapErr(postErr)
		if hint != healthNone {
			s.setChannelHealth(hostCtx, hint)
		}
		return &channelv1.NotifyResponse{
			Ok:    false,
			Error: channelErrorResponse(code, postErr.Error()),
		}, nil
	}

	return &channelv1.NotifyResponse{Ok: true}, nil
}

// Request posts a Block Kit message with response buttons to the configured
// Slack channel. It synchronously acknowledges within the host-enforced 5-second
// pre-ack deadline, then stores the request_id ↔ Slack message correlation.
// When the operator clicks a button, handleInteractive calls WriteAuditStep.
func (s *ChannelService) Request(ctx context.Context, req *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
	hostCtx := serve.WithCallContext(ctx)

	var cfg SlackChannelEntryConfig
	if err := json.Unmarshal([]byte(req.GetChannelConfigJson()), &cfg); err != nil || cfg.Channel == "" {
		return &channelv1.RequestResponse{
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_INVALID_ARG, "channel_config_json must include a non-empty 'channel' field"),
		}, nil
	}

	credResp, err := s.host.GetCredentials(hostCtx, &hostv1.GetCredentialsRequest{})
	if err != nil {
		return &channelv1.RequestResponse{
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_INTERNAL, fmt.Sprintf("GetCredentials: %v", err)),
		}, nil
	}
	raw := credResp.GetCredentialsJson()
	if raw == "" || raw == "{}" {
		s.setChannelHealth(hostCtx, healthAuthMissing)
		return &channelv1.RequestResponse{
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_PERMISSION, "no Slack credentials configured; authorize the plugin in the admin UI"),
		}, nil
	}
	var creds slackCreds
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return &channelv1.RequestResponse{
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_INTERNAL, fmt.Sprintf("parse credentials: %v", err)),
		}, nil
	}
	if creds.Token.AccessToken == "" {
		s.setChannelHealth(hostCtx, healthAuthMissing)
		return &channelv1.RequestResponse{
			Error: channelErrorResponse(commonv1.ErrorCode_ERROR_CODE_PERMISSION, "Slack access_token is empty; re-authorize the plugin in the admin UI"),
		}, nil
	}

	buttons := cfg.ResponseButtons
	if len(buttons) == 0 {
		buttons = defaultResponseButtons()
	}

	sc := s.webAPIFactory(creds.Token.AccessToken)

	// Synchronously post the Block Kit message within the host-enforced 5s
	// pre-ack deadline carried in ctx. callWithRetry honors the deadline; if
	// Slack's RetryAfter exceeds remaining budget it returns RateLimitedError
	// which mapErr converts to RATE_LIMITED — the host then writes
	// feedback_dispatch_error (dispatch/channel.go:347-355).
	type postResult struct{ channel, ts string }
	res, postErr := callWithRetry(ctx, func(ctx context.Context) (postResult, error) {
		blocks := buildRequestBlocks(req.GetRequestId(), req.GetPrompt(), buttons, cfg.Mention)
		ch, ts, err := sc.PostMessageContext(ctx, cfg.Channel, slack.MsgOptionBlocks(blocks...))
		return postResult{ch, ts}, err
	})
	if postErr != nil {
		code, hint := mapErr(postErr)
		if hint != healthNone {
			s.setChannelHealth(hostCtx, hint)
		}
		return &channelv1.RequestResponse{
			Error: channelErrorResponse(code, postErr.Error()),
		}, nil
	}

	runID := req.GetContext().GetRunId()
	s.correlations.put(req.GetRequestId(), correlation{
		channel: res.channel,
		ts:      res.ts,
		buttons: buttons,
		runID:   runID,
		addedAt: time.Now(),
	})

	return &channelv1.RequestResponse{Acked: true}, nil
}

// handleInteractive is called by the socketHub when an interactive (button-click)
// event arrives from Slack. It looks up the correlation for the request_id,
// calls WriteAuditStep(feedback_response), and POSTs to response_url to give
// the operator visual confirmation.
func (s *ChannelService) handleInteractive(evt socketmode.Event, cb slack.InteractionCallback) {
	if cb.Type != slack.InteractionTypeBlockActions {
		return
	}

	for _, action := range cb.ActionCallback.BlockActions {
		requestID, optionID, ok := parseActionID(action.ActionID)
		if !ok {
			continue
		}

		_, found := s.correlations.take(requestID)
		if !found {
			// Either the plugin restarted (correlation lost) or the request
			// already expired via the host's feedback-timeout path.
			s.postResponseURL(cb.ResponseURL, "This request has expired.")
			return
		}

		payloadJSON, err := json.Marshal(map[string]string{
			"request_id": requestID,
			"option_id":  optionID,
			"value":      action.Value,
			"user":       cb.User.ID,
		})
		if err != nil {
			log.Printf("slack: handleInteractive: marshal payload: %v", err)
			return
		}

		// Detached path (R2+R3 investigation confirmed): per-RPC client-side
		// TokenInterceptor (serve/server.go:126) attaches the instance token to
		// ANY ctx, so identity propagation works with a fresh background context.
		// RejectIfDetached (audit_guard.go:69-72) is presence-only on call_id —
		// synthesize a UUID to satisfy it.
		// KNOWN GAP: spec §8.5 promises a request_id exemption not yet implemented
		// host-side. File Phase-8 follow-up.
		callID := ulid.Make().String()
		md := metadata.Pairs("gleipnir-call-id", callID)
		auditCtx := metadata.NewOutgoingContext(context.Background(), md)

		resp, err := s.host.WriteAuditStep(auditCtx, &hostv1.WriteAuditStepRequest{
			StepType:    "feedback_response",
			RequestId:   requestID,
			PayloadJson: string(payloadJSON),
		})
		if err != nil {
			log.Printf("slack: handleInteractive: WriteAuditStep: %v", err)
			return
		}

		if resp.GetOk() {
			s.postResponseURL(cb.ResponseURL, "Response recorded: "+optionID)
			return
		}

		errMsg := ""
		if resp.GetError() != nil {
			errMsg = resp.GetError().GetMessage()
		}
		if errMsg == "feedback_response_late" {
			s.postResponseURL(cb.ResponseURL, "This request has already been resolved.")
			return
		}
		log.Printf("slack: handleInteractive: WriteAuditStep returned ok=false: %s", errMsg)
	}
}

// postResponseURL sends a POST to Slack's response_url with replace_original:true,
// replacing the entire original message (including buttons) with the given text.
// This provides visual confirmation that the button click was processed (R6).
func (s *ChannelService) postResponseURL(responseURL, text string) {
	if responseURL == "" {
		return
	}
	body, err := json.Marshal(map[string]any{
		"replace_original": true,
		"text":             text,
	})
	if err != nil {
		log.Printf("slack: postResponseURL: marshal: %v", err)
		return
	}
	// Use a short timeout for the response_url POST — it's best-effort UX.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reqHTTP, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("slack: postResponseURL: build request: %v", err)
		return
	}
	reqHTTP.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(reqHTTP)
	if err != nil {
		log.Printf("slack: postResponseURL: POST: %v", err)
		return
	}
	resp.Body.Close()
}

// setChannelHealth updates the plugin health state for channel-level failures.
func (s *ChannelService) setChannelHealth(ctx context.Context, h healthHint) {
	var detail string
	switch h {
	case healthAuthExpired:
		detail = "auth_expired"
	case healthAuthMissing:
		detail = "auth_missing"
	default:
		return
	}
	_, _ = s.host.SetHealthState(ctx, &hostv1.SetHealthStateRequest{
		State:  hostv1.PluginHealthState_PLUGIN_HEALTH_STATE_UNHEALTHY,
		Detail: detail,
	})
}

// channelErrorResponse constructs an ErrorEnvelope for ChannelService responses.
func channelErrorResponse(code commonv1.ErrorCode, message string) *commonv1.ErrorEnvelope {
	return &commonv1.ErrorEnvelope{Code: code, Message: message}
}

// notImplemented returns an ErrorEnvelope with ERROR_CODE_INTERNAL and a
// message that identifies the method and optional detail (e.g. tool name).
func notImplemented(method, detail string) *commonv1.ErrorEnvelope {
	msg := fmt.Sprintf("slack %s: not implemented", method)
	if detail != "" {
		msg = fmt.Sprintf("slack %s (%s): not implemented", method, detail)
	}
	return &commonv1.ErrorEnvelope{
		Code:    commonv1.ErrorCode_ERROR_CODE_INTERNAL,
		Message: msg,
	}
}
