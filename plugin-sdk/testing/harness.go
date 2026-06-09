package testing

import (
	"context"
	"fmt"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channel"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
	"github.com/felag-engineering/gleipnir/plugin-sdk/tool"
	"github.com/felag-engineering/gleipnir/plugin-sdk/trigger"
)

// ToolHarness wires an author's tool.Service against a live FakeHost, all
// in-process over bufconn. Use NewToolHarness to create one.
type ToolHarness struct {
	// Host is the FakeHost the service calls back to. Use it after each
	// test call to inspect metrics, logs, and events.
	Host *FakeHost
	// Client is the live gRPC ToolServiceClient connected to the author's service.
	// Use it when you need to inspect raw proto responses (e.g. resp.GetError()).
	Client toolv1.ToolServiceClient
}

// NewToolHarness starts an in-process ToolService backed by a FakeHost. The
// factory receives the live host client so it can call GetInstanceConfig,
// EmitMetric, and other host RPCs exactly as it does in production.
//
// A single t.Cleanup closes both servers (service first, then host) so tests
// do not need their own teardown.
func NewToolHarness(t *testing.T, factory func(hostv1.HostServiceClient) tool.Service, opts ...Option) *ToolHarness {
	t.Helper()

	host := NewFakeHost(opts...)
	hostClient, hostConn, hostSrv, hostLis := newHostConn(t, host)

	svc := factory(hostClient)
	svcConn, svcSrv, svcLis := serveInProcess(t, func(s *grpc.Server) {
		toolv1.RegisterToolServiceServer(s, serve.NewToolServer(svc))
	})

	// Single closure: service torn down before host so in-flight callbacks do
	// not race with the host shutdown.
	t.Cleanup(func() {
		svcConn.Close()
		svcSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
		svcLis.Close()
		hostLis.Close()
	})

	return &ToolHarness{
		Host:   host,
		Client: toolv1.NewToolServiceClient(svcConn),
	}
}

// Call is a convenience wrapper around Client.Call that unwraps the response
// envelope. On success it returns the raw output JSON bytes. On an application-
// level error (resp.Error != nil) it returns a Go error with the envelope
// message so callers do not need to inspect proto fields for the common case.
// Use h.Client.Call directly when you need to inspect the ErrorEnvelope itself.
func (h *ToolHarness) Call(ctx context.Context, name string, input []byte) ([]byte, error) {
	resp, err := h.Client.Call(ctx, &toolv1.CallRequest{
		ToolName:  name,
		InputJson: string(input),
	})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("tool error (%v): %s", e.GetCode(), e.GetMessage())
	}
	return []byte(resp.GetOutputJson()), nil
}

// ListTools is a convenience wrapper around Client.ListTools that projects the
// proto response to []tool.ToolSpec so callers do not need to import toolv1.
func (h *ToolHarness) ListTools(ctx context.Context) ([]tool.ToolSpec, error) {
	resp, err := h.Client.ListTools(ctx, &toolv1.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	specs := make([]tool.ToolSpec, len(resp.GetTools()))
	for i, s := range resp.GetTools() {
		specs[i] = tool.ToolSpec{
			Name:        s.GetName(),
			Description: s.GetDescription(),
			InputSchema: s.GetInputSchema(),
		}
	}
	return specs, nil
}

// ChannelHarness wires an author's channel.Service against a live FakeHost,
// all in-process over bufconn. Use NewChannelHarness to create one.
type ChannelHarness struct {
	// Host is the FakeHost the service calls back to.
	Host *FakeHost
	// Client is the live gRPC ChannelServiceClient. Use it when you need to
	// inspect raw proto responses (e.g. resp.GetOk(), resp.GetError()).
	Client channelv1.ChannelServiceClient
}

// NewChannelHarness starts an in-process ChannelService backed by a FakeHost.
// The factory closes over any test-scoped dependencies (e.g. an httptest.Client)
// and receives the live host client so the service can call GetInstanceConfig,
// GetCredentials, and other host RPCs.
//
// A single t.Cleanup closes both servers (service first, then host).
func NewChannelHarness(t *testing.T, factory func(hostv1.HostServiceClient) channel.Service, opts ...Option) *ChannelHarness {
	t.Helper()

	host := NewFakeHost(opts...)
	hostClient, hostConn, hostSrv, hostLis := newHostConn(t, host)

	svc := factory(hostClient)
	svcConn, svcSrv, svcLis := serveInProcess(t, func(s *grpc.Server) {
		channelv1.RegisterChannelServiceServer(s, serve.NewChannelServer(svc))
	})

	t.Cleanup(func() {
		svcConn.Close()
		svcSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
		svcLis.Close()
		hostLis.Close()
	})

	return &ChannelHarness{
		Host:   host,
		Client: channelv1.NewChannelServiceClient(svcConn),
	}
}

// Notify is a convenience wrapper around Client.Notify that returns nil on
// success and a Go error carrying the envelope message on failure.
// Use h.Client.Notify directly when you need to inspect the ErrorEnvelope.
func (h *ChannelHarness) Notify(ctx context.Context, n channel.Notification) error {
	resp, err := h.Client.Notify(ctx, &channelv1.NotifyRequest{
		EventType:         n.EventType,
		PayloadJson:       string(n.Payload),
		ChannelConfigJson: string(n.ChannelConfig),
	})
	if err != nil {
		return err
	}
	if !resp.GetOk() {
		if e := resp.GetError(); e != nil {
			return fmt.Errorf("notify error (%v): %s", e.GetCode(), e.GetMessage())
		}
		return fmt.Errorf("notify returned ok=false with no error detail")
	}
	return nil
}

// Request is a convenience wrapper around Client.Request that returns nil on
// ack and a Go error carrying the envelope message on failure.
// Use h.Client.Request directly when you need to inspect the ErrorEnvelope.
func (h *ChannelHarness) Request(ctx context.Context, r channel.FeedbackRequest) error {
	resp, err := h.Client.Request(ctx, &channelv1.RequestRequest{
		RequestId:         r.RequestID,
		Prompt:            r.Prompt,
		ChannelConfigJson: string(r.ChannelConfig),
	})
	if err != nil {
		return err
	}
	if !resp.GetAcked() {
		if e := resp.GetError(); e != nil {
			return fmt.Errorf("request error (%v): %s", e.GetCode(), e.GetMessage())
		}
		return fmt.Errorf("request returned acked=false with no error detail")
	}
	return nil
}

// TriggerHarness wires an author's trigger.Service against a live FakeHost,
// all in-process over bufconn. Use NewTriggerHarness to create one.
type TriggerHarness struct {
	// Host is the FakeHost the service calls back to.
	Host *FakeHost
	// Client is the live gRPC TriggerServiceClient. Start is a server-streaming
	// call; use it directly to control the stream lifecycle.
	Client triggerv1.TriggerServiceClient
}

// NewTriggerHarness starts an in-process TriggerService backed by a FakeHost.
// A single t.Cleanup closes both servers (service first, then host).
func NewTriggerHarness(t *testing.T, factory func(hostv1.HostServiceClient) trigger.Service, opts ...Option) *TriggerHarness {
	t.Helper()

	host := NewFakeHost(opts...)
	hostClient, hostConn, hostSrv, hostLis := newHostConn(t, host)

	svc := factory(hostClient)
	svcConn, svcSrv, svcLis := serveInProcess(t, func(s *grpc.Server) {
		triggerv1.RegisterTriggerServiceServer(s, serve.NewTriggerServer(svc))
	})

	t.Cleanup(func() {
		svcConn.Close()
		svcSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
		svcLis.Close()
		hostLis.Close()
	})

	return &TriggerHarness{
		Host:   host,
		Client: triggerv1.NewTriggerServiceClient(svcConn),
	}
}

// ── internal wiring ───────────────────────────────────────────────────────────

// newHostConn starts a bufconn gRPC server hosting the given FakeHost and
// returns a live HostServiceClient, the underlying client conn, the server,
// and the listener. The caller is responsible for closing them in t.Cleanup.
func newHostConn(t *testing.T, host *FakeHost) (hostv1.HostServiceClient, *grpc.ClientConn, *grpc.Server, *bufconn.Listener) {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	host.Register(srv)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("newHostConn: dial: %v", err)
	}
	return hostv1.NewHostServiceClient(conn), conn, srv, lis
}

// serveInProcess starts a bufconn gRPC server with the given registration
// closure and returns a live *grpc.ClientConn (not yet wrapped in a typed
// client), the server, and the listener. The caller wraps the conn in the
// appropriate typed client and tears everything down in t.Cleanup.
func serveInProcess(t *testing.T, register func(*grpc.Server)) (*grpc.ClientConn, *grpc.Server, *bufconn.Listener) {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	register(srv)
	go func() { _ = srv.Serve(lis) }()

	// "passthrough:///" is mandatory when using WithContextDialer + grpc.NewClient
	// so the client skips DNS resolution and passes the target directly to the
	// dialer (see grpc v1.79.3 test/gracefulstop_test.go for the canonical pattern).
	conn, err := grpc.NewClient("passthrough:///",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("serveInProcess: dial: %v", err)
	}
	return conn, srv, lis
}
