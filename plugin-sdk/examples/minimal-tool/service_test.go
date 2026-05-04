package main

import (
	"context"
	"log/slog"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
)

// setup starts an in-process gRPC server hosting both the fake host and the
// tool service. It returns the tool client, the fake host, and a cleanup function.
func setup(t *testing.T, hostOpts ...plugintest.Option) (toolv1.ToolServiceClient, *plugintest.FakeHost, func()) {
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

	// Dial the host and build the hostv1 client used by ToolService.
	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	hostClient := hostv1.NewHostServiceClient(hostConn)

	// Start the tool service gRPC server.
	toolLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for tool: %v", err)
	}
	toolSrv := grpc.NewServer()
	toolv1.RegisterToolServiceServer(toolSrv, NewToolService(hostClient))
	go func() { _ = toolSrv.Serve(toolLis) }()

	// Dial the tool service.
	toolConn, err := grpc.NewClient(toolLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial tool: %v", err)
	}
	toolClient := toolv1.NewToolServiceClient(toolConn)

	cleanup := func() {
		toolConn.Close()
		toolSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
	}
	return toolClient, host, cleanup
}

func TestEchoTool_Happy(t *testing.T) {
	toolClient, host, cleanup := setup(t,
		plugintest.WithInstanceConfigJSON(`{"greeting":"hi"}`),
		plugintest.WithRunContext(plugintest.RunContext{
			RunID:    "r-1",
			PolicyID: "p-1",
		}),
	)
	defer cleanup()

	resp, err := toolClient.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "echo",
		InputJson: `{"message":"hello world"}`,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("unexpected error envelope: %v", resp.GetError().GetMessage())
	}
	if resp.GetOutputJson() == "" {
		t.Error("expected non-empty output_json")
	}

	host.AssertMetricEmitted(t, "echo_calls_total", map[string]string{"tool": "echo"})
	host.AssertLogContains(t, slog.LevelInfo, "echo handled")
}

func TestEchoTool_MalformedInput(t *testing.T) {
	toolClient, host, cleanup := setup(t)
	defer cleanup()

	resp, err := toolClient.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "echo",
		InputJson: `not-valid-json`,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.GetError() == nil {
		t.Fatal("expected error envelope for malformed JSON, got nil")
	}

	host.AssertNoMetricEmitted(t, "echo_calls_total")
}

func TestEchoTool_ListTools(t *testing.T) {
	toolClient, _, cleanup := setup(t)
	defer cleanup()

	resp, err := toolClient.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(resp.GetTools()) != 1 {
		t.Fatalf("want 1 tool, got %d", len(resp.GetTools()))
	}
	if resp.GetTools()[0].GetName() != "echo" {
		t.Errorf("want tool name 'echo', got %q", resp.GetTools()[0].GetName())
	}
}
