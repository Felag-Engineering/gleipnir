package main

import (
	"context"
	"encoding/json"
	"fmt"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/pluginerr"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
	"github.com/felag-engineering/gleipnir/plugin-sdk/tool"
)

// ToolService implements tool.Service with a single "echo" tool.
// It communicates with the host via hostClient, which is provided at
// construction time (in production by serve.Serve; in tests by loopback TCP).
type ToolService struct {
	host hostv1.HostServiceClient
}

// NewToolService creates a ToolService that uses hostClient for host RPCs.
func NewToolService(hostClient hostv1.HostServiceClient) *ToolService {
	return &ToolService{host: hostClient}
}

// ListTools returns the single "echo" tool declaration.
func (s *ToolService) ListTools(_ context.Context) ([]tool.ToolSpec, error) {
	return []tool.ToolSpec{
		{
			Name:        "echo",
			Description: "Returns the message it received.",
			InputSchema: `{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`,
		},
	}, nil
}

// Call handles the "echo" tool. It:
//  1. Calls GetInstanceConfig so tests can verify host connectivity.
//  2. Emits a metric counting each invocation.
//  3. Logs a confirmation line.
//  4. Returns the echoed message as output JSON.
//
// Plugins MUST honor ctx.Done(): when the host cancels the call (run cancelled
// or operator cancellation), every blocking I/O performed inside Call must use
// this ctx so the goroutine returns promptly. The host enforces a 5s grace
// before force-disconnecting (spec §13.8).
func (s *ToolService) Call(ctx context.Context, toolName string, input []byte) ([]byte, error) {
	if toolName != "echo" {
		return nil, pluginerr.InvalidArg(fmt.Sprintf("unknown tool: %q", toolName))
	}

	var in struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, pluginerr.InvalidArg(fmt.Sprintf("invalid input JSON: %v", err))
	}

	// Propagate the host-injected call ID to all outgoing host RPCs so the host
	// can correlate Log, EmitMetric, and WriteAuditStep calls back to this run
	// and step. See serve.WithCallContext and plugin-system-spec.md §8.5.
	hostCtx := serve.WithCallContext(ctx)

	// 1. GetInstanceConfig — exercises host connectivity.
	if _, err := s.host.GetInstanceConfig(hostCtx, &hostv1.GetInstanceConfigRequest{}); err != nil {
		return nil, fmt.Errorf("GetInstanceConfig: %w", err)
	}

	// 2. EmitMetric.
	if _, err := s.host.EmitMetric(hostCtx, &hostv1.EmitMetricRequest{
		Name:   "echo_calls_total",
		Value:  1,
		Labels: map[string]string{"tool": "echo"},
	}); err != nil {
		return nil, fmt.Errorf("EmitMetric: %w", err)
	}

	// 3. Log confirmation.
	if _, err := s.host.Log(hostCtx, &hostv1.LogRequest{
		Level: hostv1.LogLevel_LOG_LEVEL_INFO,
		Msg:   "echo handled",
	}); err != nil {
		return nil, fmt.Errorf("Log: %w", err)
	}

	// 4. Return the echoed output.
	out, _ := json.Marshal(map[string]string{"echoed": in.Message})
	return out, nil
}
