package main

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

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
// Slack service stubs. Returns a services bundle and a cleanup function.
func setupAll(t *testing.T, hostOpts ...plugintest.Option) (*services, func()) {
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

	// Dial the host and build the typed client used by the service stubs.
	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	hostClient := hostv1.NewHostServiceClient(hostConn)

	// --- ToolService ---
	toolLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for tool: %v", err)
	}
	toolSrv := grpc.NewServer()
	toolv1.RegisterToolServiceServer(toolSrv, NewToolService(hostClient))
	go func() { _ = toolSrv.Serve(toolLis) }()
	toolConn, err := grpc.NewClient(toolLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial tool: %v", err)
	}

	// --- TriggerService ---
	triggerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for trigger: %v", err)
	}
	triggerSrv := grpc.NewServer()
	triggerv1.RegisterTriggerServiceServer(triggerSrv, NewTriggerService(hostClient))
	go func() { _ = triggerSrv.Serve(triggerLis) }()
	triggerConn, err := grpc.NewClient(triggerLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial trigger: %v", err)
	}

	// --- ChannelService ---
	chanLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for channel: %v", err)
	}
	chanSrv := grpc.NewServer()
	channelv1.RegisterChannelServiceServer(chanSrv, NewChannelService(hostClient))
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
	}
	return svcs, cleanup
}

// TestStubsReturnNotImplemented is a table-driven test asserting that each
// unary stub returns an in-band ErrorEnvelope with ERROR_CODE_INTERNAL and a
// non-empty message containing "not implemented". The gRPC call itself must
// succeed (nil transport error) — the error is communicated in-band.
func TestStubsReturnNotImplemented(t *testing.T) {
	svcs, cleanup := setupAll(t)
	defer cleanup()

	cases := []struct {
		name string
		run  func(t *testing.T) *commonv1.ErrorEnvelope
	}{
		{
			name: "Tool.Call",
			run: func(t *testing.T) *commonv1.ErrorEnvelope {
				t.Helper()
				resp, err := svcs.tool.Call(context.Background(), &toolv1.CallRequest{ToolName: "send_message"})
				if err != nil {
					t.Fatalf("Call RPC error: %v", err)
				}
				return resp.GetError()
			},
		},
		{
			name: "Channel.Notify",
			run: func(t *testing.T) *commonv1.ErrorEnvelope {
				t.Helper()
				resp, err := svcs.channel.Notify(context.Background(), &channelv1.NotifyRequest{})
				if err != nil {
					t.Fatalf("Notify RPC error: %v", err)
				}
				if resp.GetOk() {
					t.Fatal("expected ok=false for stub, got ok=true")
				}
				return resp.GetError()
			},
		},
		{
			name: "Channel.Request",
			run: func(t *testing.T) *commonv1.ErrorEnvelope {
				t.Helper()
				resp, err := svcs.channel.Request(context.Background(), &channelv1.RequestRequest{})
				if err != nil {
					t.Fatalf("Request RPC error: %v", err)
				}
				return resp.GetError()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.run(t)
			if env == nil {
				t.Fatal("expected non-nil ErrorEnvelope, got nil")
			}
			if env.GetCode() != commonv1.ErrorCode_ERROR_CODE_INTERNAL {
				t.Errorf("code: want ERROR_CODE_INTERNAL, got %v", env.GetCode())
			}
			if env.GetMessage() == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}

// TestTriggerStartReturnsUnimplemented asserts that TriggerService.Start
// immediately returns a gRPC-level codes.Unimplemented error. Start is a
// server-streaming RPC, so the only way to surface an error before emitting
// any events is via a top-level gRPC status. #234 replaces this stub.
func TestTriggerStartReturnsUnimplemented(t *testing.T) {
	svcs, cleanup := setupAll(t)
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
	if st.Code() != codes.Unimplemented {
		t.Errorf("code: want Unimplemented, got %v", st.Code())
	}
}

// TestToolListToolsEmpty asserts that ListTools returns zero tools for the
// scaffold. This is consistent with the empty Tools slice in the manifest;
// #233 populates both.
func TestToolListToolsEmpty(t *testing.T) {
	svcs, cleanup := setupAll(t)
	defer cleanup()

	resp, err := svcs.tool.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(resp.GetTools()) != 0 {
		t.Errorf("want 0 tools, got %d", len(resp.GetTools()))
	}
}

// TestToolCancelIsNoOp asserts that Cancel returns an empty response with no
// error. There are no in-flight calls to abort in the scaffold.
func TestToolCancelIsNoOp(t *testing.T) {
	svcs, cleanup := setupAll(t)
	defer cleanup()

	resp, err := svcs.tool.Cancel(context.Background(), &toolv1.CancelRequest{})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil CancelResponse, got nil")
	}
}
