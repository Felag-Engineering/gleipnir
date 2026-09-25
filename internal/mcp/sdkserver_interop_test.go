// Cross-module contract test: internal/mcp.Client, the root module's real
// MCP HTTP client, driven against plugin-sdk/mcpserver.Server — two
// independently written implementations of the 2026-07-28 transport's
// server/discover, tools/list, and tools/call. Mirrors
// internal/mcp/sdkevents_integration_test.go (the same pin for
// plugin-sdk/events) and
// internal/plugin/hostendpoint/sdkclient_integration_test.go (the same pin
// for the host endpoint's own contract).
package mcp_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

func TestCrossModule_DiscoverAndCallTool(t *testing.T) {
	srv := mcpserver.NewServer("cross-module-test", "1.0.0")
	if err := srv.RegisterTool(mcpserver.Tool{
		Name:        "echo",
		Description: "echoes back the name argument",
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, err
			}
			return "hello " + in.Name, nil
		},
	}); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}

	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)

	client := mcp.NewClient(httpSrv.URL, mcp.WithProtocolVersion(mcp.ProtocolVersion20260728))
	ctx := context.Background()

	probe, err := client.ProbeProtocolVersion(ctx)
	if err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}
	if probe.Version != mcp.ProtocolVersion20260728 {
		t.Fatalf("negotiated version = %q, want %q", probe.Version, mcp.ProtocolVersion20260728)
	}
	if probe.ServerInfo.Name != "cross-module-test" || probe.ServerInfo.Version != "1.0.0" {
		t.Fatalf("ServerInfo = %+v, want name=cross-module-test version=1.0.0", probe.ServerInfo)
	}

	tools, err := client.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("DiscoverTools = %+v, want exactly one tool named echo", tools)
	}
	if tools[0].Description != "echoes back the name argument" {
		t.Fatalf("Description = %q, did not round-trip", tools[0].Description)
	}

	result, err := client.CallTool(ctx, "echo", map[string]any{"name": "world"}, mcp.CallOptions{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool returned isError, output: %s", result.Output)
	}
	if !strings.Contains(string(result.Output), "hello world") {
		t.Fatalf("CallTool output = %s, want it to contain %q", result.Output, "hello world")
	}
}

func TestCrossModule_CallToolUnknownName(t *testing.T) {
	srv := mcpserver.NewServer("cross-module-test", "1.0.0")
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)

	client := mcp.NewClient(httpSrv.URL, mcp.WithProtocolVersion(mcp.ProtocolVersion20260728))

	_, err := client.CallTool(context.Background(), "does_not_exist", nil, mcp.CallOptions{})
	if err == nil {
		t.Fatal("CallTool against an unregistered tool succeeded, want an error")
	}
}
