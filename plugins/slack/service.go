package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/slack-go/slack"

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

// TriggerService implements triggerv1.TriggerServiceServer with a stub Start.
// #234 will implement Start to subscribe to the Slack Events API and emit
// channel_message events matching the policy's binding config.
type TriggerService struct {
	triggerv1.UnimplementedTriggerServiceServer
	host hostv1.HostServiceClient
}

// NewTriggerService creates a TriggerService that uses hostClient for host RPCs.
func NewTriggerService(hostClient hostv1.HostServiceClient) *TriggerService {
	return &TriggerService{host: hostClient}
}

// Start is a server-streaming RPC. There is no response slot before the first
// event, so a top-level gRPC error is the only way to signal failure here.
// #234 replaces this with the Slack Events API subscription loop.
func (s *TriggerService) Start(_ *triggerv1.StartRequest, _ grpc.ServerStreamingServer[triggerv1.StartResponse]) error {
	return status.Error(codes.Unimplemented, "slack TriggerService.Start: not implemented")
}

// ── ChannelService ────────────────────────────────────────────────────────────

// ChannelService implements channelv1.ChannelServiceServer with stub methods.
// #235 will implement Notify (post a message to a Slack channel/DM) and
// Request (post a message and wait for an operator reaction/reply).
type ChannelService struct {
	channelv1.UnimplementedChannelServiceServer
	host hostv1.HostServiceClient
}

// NewChannelService creates a ChannelService that uses hostClient for host RPCs.
func NewChannelService(hostClient hostv1.HostServiceClient) *ChannelService {
	return &ChannelService{host: hostClient}
}

// Notify returns a not-implemented error envelope. #235 implements the real
// Slack chat.postMessage call using the channel config's target and mention.
func (s *ChannelService) Notify(_ context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
	return &channelv1.NotifyResponse{Ok: false, Error: notImplemented("Notify", "")}, nil
}

// Request returns a not-implemented error envelope. #235 implements posting
// a message to Slack and waiting for an operator reply or reaction.
func (s *ChannelService) Request(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
	return &channelv1.RequestResponse{Error: notImplemented("Request", "")}, nil
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
