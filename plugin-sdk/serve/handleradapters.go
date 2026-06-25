package serve

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channel"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
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

// RequestTerminated is an optional ergonomic adapter: if the wrapped service
// implements channel.TerminationAware, the proto reason is translated and
// forwarded. Otherwise this is a silent no-op — the host treats the caller's
// codes.Unimplemented stub as a best-effort no-op already, so returning
// {Ok:true} here for a non-implementing service is correct and avoids
// surfacing a spurious Unimplemented error to the host.
func (a *channelHandlerAdapter) RequestTerminated(ctx context.Context, req *channelv1.RequestTerminatedRequest) (*channelv1.RequestTerminatedResponse, error) {
	aware, ok := a.svc.(channel.TerminationAware)
	if !ok {
		// Service does not implement TerminationAware — silent no-op.
		return &channelv1.RequestTerminatedResponse{Ok: true}, nil
	}
	t := channel.Termination{
		RequestID: req.GetRequestId(),
		Reason:    channel.TerminalReason(req.GetReason()),
		Resolver:  req.GetResolver(),
	}
	if err := aware.RequestTerminated(ctx, t); err != nil {
		return &channelv1.RequestTerminatedResponse{Ok: false, Error: errorToEnvelope(err)}, nil
	}
	return &channelv1.RequestTerminatedResponse{Ok: true}, nil
}

// ── Trigger adapter ───────────────────────────────────────────────────────────

// triggerHandlerAdapter bridges trigger.Service onto triggerv1.TriggerServiceServer.
// The struct stays unexported; use NewTriggerServer to construct one.
type triggerHandlerAdapter struct {
	triggerv1.UnimplementedTriggerServiceServer
	svc  trigger.Service
	host hostv1.HostServiceClient
}

// NewTriggerServer wraps svc in a triggerv1.TriggerServiceServer adapter.
// The returned value can be registered directly with
// triggerv1.RegisterTriggerServiceServer.
//
// Event delivery (issue #495, spec §4.3): the ergonomic emit callback routes
// each Event through the canonical HostService.EmitEvent Host RPC — NOT through
// stream.Send. This is the single blessed delivery mechanism (#214): EmitEvent
// carries identity (instance-token interceptor), per-instance rate limiting, the
// payload size cap, SSE observability, and generation-drain semantics that the
// long-lived Start stream itself does not. The Start stream is kept open purely
// as a liveness/cancellation channel and carries no StartResponse messages —
// exactly what the plugins/slack raw-seam reference already does. host must be
// the typed client bound after Bootstrap.Bind (see WithTriggerHandler).
//
// Author errors from Start propagate as the gRPC stream return status.
func NewTriggerServer(svc trigger.Service, host hostv1.HostServiceClient) triggerv1.TriggerServiceServer {
	return &triggerHandlerAdapter{svc: svc, host: host}
}

func (a *triggerHandlerAdapter) Start(req *triggerv1.StartRequest, stream triggerv1.TriggerService_StartServer) error {
	scope := trigger.StartScope{
		WatchScope: []byte(req.GetWatchScopeJson()),
	}

	// WithCallContext propagates the host-injected call-id (if any) onto the
	// outgoing metadata of every EmitEvent made from this scope, so the host can
	// correlate emitted events back to the trigger stream. The SDK's instance-
	// token interceptor attaches the identity token independently.
	hostCtx := WithCallContext(stream.Context())

	emit := func(e trigger.Event) error {
		_, err := a.host.EmitEvent(hostCtx, &hostv1.EmitEventRequest{
			EventId:     e.EventID,
			EventKind:   e.EventKind,
			PayloadJson: string(e.Payload),
		})
		return err
	}

	return a.svc.Start(stream.Context(), scope, emit)
}
