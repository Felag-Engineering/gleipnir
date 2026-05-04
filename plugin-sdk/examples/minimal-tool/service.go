package main

import (
	"context"
	"encoding/json"
	"fmt"

	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
)

// ToolService implements toolv1.ToolServiceServer with a single "echo" tool.
// It communicates with the host via hostClient, which is provided at
// construction time (in production by serve.Serve; in tests by loopback TCP (127.0.0.1:0)).
type ToolService struct {
	toolv1.UnimplementedToolServiceServer
	host hostv1.HostServiceClient
}

// NewToolService creates a ToolService that uses hostClient for host RPCs.
func NewToolService(hostClient hostv1.HostServiceClient) *ToolService {
	return &ToolService{host: hostClient}
}

// ListTools returns the single "echo" tool declaration.
func (s *ToolService) ListTools(_ context.Context, _ *toolv1.ListToolsRequest) (*toolv1.ListToolsResponse, error) {
	return &toolv1.ListToolsResponse{
		Tools: []*toolv1.ToolSchema{
			{
				Name:        "echo",
				Description: "Returns the message it received.",
				InputSchema: `{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`,
			},
		},
	}, nil
}

// Call handles the "echo" tool. It:
//  1. Calls GetInstanceConfig so tests can verify host connectivity.
//  2. Emits a metric counting each invocation.
//  3. Logs a confirmation line.
//  4. Returns the echoed message as output JSON.
//
// Any JSON parse error results in a populated ErrorEnvelope in the response.
func (s *ToolService) Call(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
	if req.GetToolName() != "echo" {
		return &toolv1.CallResponse{
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
				Message: fmt.Sprintf("unknown tool: %q", req.GetToolName()),
			},
		}, nil
	}

	var input struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(req.GetInputJson()), &input); err != nil {
		return &toolv1.CallResponse{
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
				Message: fmt.Sprintf("invalid input JSON: %v", err),
			},
		}, nil
	}

	// 1. GetInstanceConfig — exercises host connectivity.
	if _, err := s.host.GetInstanceConfig(ctx, &hostv1.GetInstanceConfigRequest{}); err != nil {
		return &toolv1.CallResponse{
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INTERNAL,
				Message: fmt.Sprintf("GetInstanceConfig: %v", err),
			},
		}, nil
	}

	// 2. EmitMetric.
	if _, err := s.host.EmitMetric(ctx, &hostv1.EmitMetricRequest{
		Name:   "echo_calls_total",
		Value:  1,
		Labels: map[string]string{"tool": "echo"},
	}); err != nil {
		return &toolv1.CallResponse{
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INTERNAL,
				Message: fmt.Sprintf("EmitMetric: %v", err),
			},
		}, nil
	}

	// 3. Log confirmation.
	if _, err := s.host.Log(ctx, &hostv1.LogRequest{
		Level: hostv1.LogLevel_LOG_LEVEL_INFO,
		Msg:   "echo handled",
	}); err != nil {
		return &toolv1.CallResponse{
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INTERNAL,
				Message: fmt.Sprintf("Log: %v", err),
			},
		}, nil
	}

	// 4. Return the echoed output.
	out, _ := json.Marshal(map[string]string{"echoed": input.Message})
	return &toolv1.CallResponse{OutputJson: string(out)}, nil
}
