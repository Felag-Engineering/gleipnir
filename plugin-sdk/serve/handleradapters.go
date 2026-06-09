package serve

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channel"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/tool"
	"github.com/felag-engineering/gleipnir/plugin-sdk/trigger"
)

// ── Tool adapter ──────────────────────────────────────────────────────────────

// toolHandlerAdapter bridges tool.Service onto toolv1.ToolServiceServer.
// The struct stays unexported; use NewToolServer to construct one.
//
// Embedding UnimplementedToolServiceServer BY VALUE (not pointer) satisfies the
// generated testEmbeddedByValue check that fires at RegisterToolServiceServer time.
type toolHandlerAdapter struct {
	toolv1.UnimplementedToolServiceServer
	svc tool.Service
}

// NewToolServer wraps svc in a toolv1.ToolServiceServer adapter. The returned
// value can be registered directly with toolv1.RegisterToolServiceServer.
//
// Error mapping: svc.Call errors become application-level ErrorEnvelope values
// inside CallResponse (not gRPC status errors). svc.ListTools errors become a
// gRPC codes.Internal status because ListToolsResponse has no Error field.
func NewToolServer(svc tool.Service) toolv1.ToolServiceServer {
	return &toolHandlerAdapter{svc: svc}
}

// ListTools calls svc.ListTools and maps the result. On error it returns a
// gRPC-level codes.Internal status (not an envelope) because ListToolsResponse
// has no Error field.
func (a *toolHandlerAdapter) ListTools(ctx context.Context, _ *toolv1.ListToolsRequest) (*toolv1.ListToolsResponse, error) {
	specs, err := a.svc.ListTools(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s", err.Error())
	}

	schemas := make([]*toolv1.ToolSchema, len(specs))
	for i, s := range specs {
		schemas[i] = &toolv1.ToolSchema{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		}
	}
	return &toolv1.ListToolsResponse{Tools: schemas}, nil
}

// Call invokes svc.Call and maps the result to an application-level envelope.
// Errors from svc.Call are embedded in CallResponse.Error; the gRPC call
// itself always succeeds (nil error returned).
func (a *toolHandlerAdapter) Call(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
	out, err := a.svc.Call(ctx, req.GetToolName(), []byte(req.GetInputJson()))
	if err != nil {
		return &toolv1.CallResponse{Error: errorToEnvelope(err)}, nil
	}
	return &toolv1.CallResponse{OutputJson: string(out)}, nil
}

// Cancel is a no-op because cancellation is via ctx.Done() inside Call.
// The host sends Cancel with a 5s deadline; we acknowledge immediately and
// trust that Call is already selecting on ctx.Done().
func (a *toolHandlerAdapter) Cancel(_ context.Context, _ *toolv1.CancelRequest) (*toolv1.CancelResponse, error) {
	return &toolv1.CancelResponse{}, nil
}

// ── Channel adapter ───────────────────────────────────────────────────────────

// channelHandlerAdapter bridges channel.Service onto channelv1.ChannelServiceServer.
// The struct stays unexported; use NewChannelServer to construct one.
type channelHandlerAdapter struct {
	channelv1.UnimplementedChannelServiceServer
	svc channel.Service
}

// NewChannelServer wraps svc in a channelv1.ChannelServiceServer adapter.
// The returned value can be registered directly with
// channelv1.RegisterChannelServiceServer.
//
// Error mapping: errors become application-level ErrorEnvelope values inside
// NotifyResponse / RequestResponse (not gRPC status errors), consistent with
// the channel protocol where failures do not fail the run.
func NewChannelServer(svc channel.Service) channelv1.ChannelServiceServer {
	return &channelHandlerAdapter{svc: svc}
}

func (a *channelHandlerAdapter) Notify(ctx context.Context, req *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
	n := channel.Notification{
		EventType:     req.GetEventType(),
		Payload:       []byte(req.GetPayloadJson()),
		ChannelConfig: []byte(req.GetChannelConfigJson()),
	}
	if err := a.svc.Notify(ctx, n); err != nil {
		return &channelv1.NotifyResponse{Ok: false, Error: errorToEnvelope(err)}, nil
	}
	return &channelv1.NotifyResponse{Ok: true}, nil
}

func (a *channelHandlerAdapter) Request(ctx context.Context, req *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
	r := channel.FeedbackRequest{
		RequestID:     req.GetRequestId(),
		Prompt:        req.GetPrompt(),
		ChannelConfig: []byte(req.GetChannelConfigJson()),
	}
	if err := a.svc.Request(ctx, r); err != nil {
		return &channelv1.RequestResponse{Acked: false, Error: errorToEnvelope(err)}, nil
	}
	return &channelv1.RequestResponse{Acked: true}, nil
}

// ── Trigger adapter ───────────────────────────────────────────────────────────

// triggerHandlerAdapter bridges trigger.Service onto triggerv1.TriggerServiceServer.
// The struct stays unexported; use NewTriggerServer to construct one.
type triggerHandlerAdapter struct {
	triggerv1.UnimplementedTriggerServiceServer
	svc trigger.Service
}

// NewTriggerServer wraps svc in a triggerv1.TriggerServiceServer adapter.
// The returned value can be registered directly with
// triggerv1.RegisterTriggerServiceServer.
//
// The Start adapter passes stream.Context() as the ctx and wraps stream.Send
// as the emit callback. Author errors propagate as the gRPC stream return
// status (no ErrorEnvelope — StartResponse has no Error field).
func NewTriggerServer(svc trigger.Service) triggerv1.TriggerServiceServer {
	return &triggerHandlerAdapter{svc: svc}
}

func (a *triggerHandlerAdapter) Start(req *triggerv1.StartRequest, stream triggerv1.TriggerService_StartServer) error {
	scope := trigger.StartScope{
		WatchScope: []byte(req.GetWatchScopeJson()),
	}

	emit := func(e trigger.Event) error {
		return stream.Send(&triggerv1.StartResponse{
			EventId:     e.EventID,
			EventKind:   e.EventKind,
			PayloadJson: string(e.Payload),
		})
	}

	return a.svc.Start(stream.Context(), scope, emit)
}
