package main

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
)

// ToolService implements toolv1.ToolServiceServer with stub methods.
// #233 will populate ListTools and Call with real Slack API behavior
// (send_message, list_channels, etc.).
type ToolService struct {
	toolv1.UnimplementedToolServiceServer
	host hostv1.HostServiceClient
}

// NewToolService creates a ToolService that uses hostClient for host RPCs.
func NewToolService(hostClient hostv1.HostServiceClient) *ToolService {
	return &ToolService{host: hostClient}
}

// ListTools returns an empty tool list. #233 adds send_message, list_channels,
// and any other declared tools once the Slack API client lands.
func (s *ToolService) ListTools(_ context.Context, _ *toolv1.ListToolsRequest) (*toolv1.ListToolsResponse, error) {
	return &toolv1.ListToolsResponse{Tools: nil}, nil
}

// Call returns a not-implemented error envelope for any tool name. #233
// implements the real dispatch table against the Slack Web API.
func (s *ToolService) Call(_ context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
	return &toolv1.CallResponse{Error: notImplemented("Call", req.GetToolName())}, nil
}

// Cancel is a no-op for the scaffold. Real in-flight cancellation lands in
// #233 alongside the Slack HTTP client that would need to be aborted.
func (s *ToolService) Cancel(_ context.Context, _ *toolv1.CancelRequest) (*toolv1.CancelResponse, error) {
	return &toolv1.CancelResponse{}, nil
}

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
