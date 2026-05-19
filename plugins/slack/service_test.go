package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
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
// Slack service implementations. slackBackend is an optional httptest.Server
// used as the fake Slack API endpoint; when non-nil, its client and URL are
// passed to NewToolService so requests go to the test server instead of
// the real Slack API. Pass nil to use http.DefaultClient with no override.
func setupAll(t *testing.T, slackBackend *httptest.Server, hostOpts ...plugintest.Option) (*services, func()) {
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

	// Dial the host and build the typed client used by the service implementations.
	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	hostClient := hostv1.NewHostServiceClient(hostConn)

	// --- ToolService ---
	//
	// When a slackBackend is provided, use its HTTP client and URL so test
	// requests go to the httptest.Server instead of the real Slack API.
	// The trailing slash in slackBackend.URL + "/" is required because
	// slack-go builds URLs as apiURL + methodName (slack.go:168).
	var toolSvc *ToolService
	if slackBackend != nil {
		toolSvc = NewToolService(hostClient, slackBackend.Client(), slackBackend.URL+"/")
	} else {
		toolSvc = NewToolService(hostClient, nil, "")
	}

	toolLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for tool: %v", err)
	}
	toolSrv := grpc.NewServer()
	toolv1.RegisterToolServiceServer(toolSrv, toolSvc)
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

// TestStubsReturnNotImplemented asserts that Channel.Notify and Channel.Request
// return an in-band ErrorEnvelope with ERROR_CODE_INTERNAL and a non-empty
// message containing "not implemented". The gRPC call itself must succeed (nil
// transport error) — the error is communicated in-band.
//
// Tool.Call is no longer a stub (#233 implements it), so it is not included here.
func TestStubsReturnNotImplemented(t *testing.T) {
	svcs, cleanup := setupAll(t, nil)
	defer cleanup()

	cases := []struct {
		name string
		run  func(t *testing.T) *commonv1.ErrorEnvelope
	}{
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
	svcs, cleanup := setupAll(t, nil)
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

// TestToolListToolsAdvertisesAll asserts that ListTools returns exactly the five
// Slack tools by name and that each InputSchema parses as a JSON object.
// This replaces TestToolListToolsEmpty from the scaffold.
func TestToolListToolsAdvertisesAll(t *testing.T) {
	svcs, cleanup := setupAll(t, nil)
	defer cleanup()

	resp, err := svcs.tool.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	wantNames := []string{"post_message", "list_channels", "search_messages", "react", "set_topic"}
	tools := resp.GetTools()
	if len(tools) != len(wantNames) {
		t.Fatalf("want %d tools, got %d", len(wantNames), len(tools))
	}

	for i, want := range wantNames {
		got := tools[i]
		if got.GetName() != want {
			t.Errorf("tool[%d]: want name %q, got %q", i, want, got.GetName())
		}
		// Each InputSchema must parse as a JSON object with type=object at root.
		var schema map[string]any
		if err := json.Unmarshal([]byte(got.GetInputSchema()), &schema); err != nil {
			t.Errorf("tool[%d] %s: InputSchema is not valid JSON: %v", i, want, err)
			continue
		}
		if typ, _ := schema["type"].(string); typ != "object" {
			t.Errorf("tool[%d] %s: InputSchema root type: want \"object\", got %q", i, want, typ)
		}
	}
}

// TestToolCancelIsNoOp asserts that Cancel returns an empty response with no
// error. Cancellation for the Slack ToolService is context-driven; there is no
// in-process goroutine state to abort.
func TestToolCancelIsNoOp(t *testing.T) {
	svcs, cleanup := setupAll(t, nil)
	defer cleanup()

	resp, err := svcs.tool.Cancel(context.Background(), &toolv1.CancelRequest{})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil CancelResponse, got nil")
	}
}

// TestToolCallMissingCredentials asserts that a Call with no credentials
// configured returns PERMISSION and sets health to UNHEALTHY/auth_missing.
func TestToolCallMissingCredentials(t *testing.T) {
	// Empty string credentials — no token configured.
	svcs, cleanup := setupAll(t, nil,
		plugintest.WithCredentialsJSON(`{}`),
	)
	defer cleanup()

	resp, err := svcs.tool.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "post_message",
		InputJson: `{"channel":"C123","text":"hello"}`,
	})
	if err != nil {
		t.Fatalf("Call RPC error: %v", err)
	}

	env := resp.GetError()
	if env == nil {
		t.Fatal("expected non-nil ErrorEnvelope")
	}
	if env.GetCode() != commonv1.ErrorCode_ERROR_CODE_PERMISSION {
		t.Errorf("code: want PERMISSION, got %v", env.GetCode())
	}
	if !strings.Contains(env.GetMessage(), "credentials") && !strings.Contains(env.GetMessage(), "auth") {
		t.Errorf("message should mention credentials or auth, got: %q", env.GetMessage())
	}
}
