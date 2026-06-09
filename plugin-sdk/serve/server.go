package serve

import (
	"context"
	"fmt"
	"sync"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bootstrapv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/bootstrap/v1"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
)

// sdkVersion is the version string reported in Handshake.Negotiate.
// It identifies the SDK release, not the plugin version.
const sdkVersion = "0.1.0"

// pluginGRPCPlugin is the go-plugin GRPCPlugin implementation that all SDK
// users share. One instance is created per Serve call.
//
// The adapter pattern is used for service registration: go-plugin calls
// GRPCServer before the host connection is established, so we cannot call the
// user's factory yet. We register adapter wrappers immediately and install the
// real implementations after Bootstrap.Bind fires.
type pluginGRPCPlugin struct {
	goplugin.Plugin

	cfg *config

	// broker is stored in GRPCServer so Bind can dial the host.
	broker *goplugin.GRPCBroker

	// hostConn is the connection to HostService, established inside Bind.
	// Closed by shutdown().
	hostConn *grpc.ClientConn

	// adapters registered on the gRPC server in GRPCServer; each holds a
	// pointer that is populated atomically inside Bind after the host
	// connection is ready.
	channel *channelAdapter
	tool    *toolAdapter
	trigger *triggerAdapter
}

func newPluginGRPCPlugin(cfg *config) *pluginGRPCPlugin {
	return &pluginGRPCPlugin{
		cfg:     cfg,
		channel: &channelAdapter{},
		tool:    &toolAdapter{},
		trigger: &triggerAdapter{},
	}
}

// GRPCServer is called by go-plugin before the server starts accepting
// connections. We register the adapters and the bootstrap/handshake services
// here. The user's real implementations are injected by Bind after the host
// connection is available.
func (p *pluginGRPCPlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	p.broker = broker

	handshakev1.RegisterHandshakeServiceServer(s, p)
	bootstrapv1.RegisterBootstrapServiceServer(s, p)

	// Always register adapters for all three services so the gRPC server
	// exposes a stable surface. Adapters return codes.Unavailable until Bind
	// installs the real impl. Services whose factory is nil will always return
	// Unavailable — that is the documented zero-capability behaviour.
	channelv1.RegisterChannelServiceServer(s, p.channel)
	toolv1.RegisterToolServiceServer(s, p.tool)
	triggerv1.RegisterTriggerServiceServer(s, p.trigger)

	return nil
}

// GRPCClient is not used on the plugin side.
func (p *pluginGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, _ *grpc.ClientConn) (interface{}, error) {
	return nil, status.Error(codes.Unimplemented, "serve: GRPCClient not used on plugin side")
}

// Negotiate implements handshakev1.HandshakeServiceServer. It advertises the
// actual capabilities derived from the registered factories.
func (p *pluginGRPCPlugin) Negotiate(_ context.Context, _ *handshakev1.NegotiateRequest) (*handshakev1.NegotiateResponse, error) {
	pluginVersion := "0.0.0"
	if p.cfg.manifest != nil {
		pluginVersion = p.cfg.manifest.Version
	}

	var caps []handshakev1.ServiceCapability
	// Capability values mirror the proto enum: TOOL=1, CHANNEL=2, TRIGGER=3.
	if p.cfg.channelFactory != nil {
		caps = append(caps, handshakev1.ServiceCapability_SERVICE_CAPABILITY_CHANNEL)
	}
	if p.cfg.toolFactory != nil {
		caps = append(caps, handshakev1.ServiceCapability_SERVICE_CAPABILITY_TOOL)
	}
	if p.cfg.triggerFactory != nil {
		caps = append(caps, handshakev1.ServiceCapability_SERVICE_CAPABILITY_TRIGGER)
	}

	return &handshakev1.NegotiateResponse{
		SdkVersion:         sdkVersion,
		PluginVersion:      pluginVersion,
		Ok:                 true,
		ActualCapabilities: caps,
	}, nil
}

// Bind implements bootstrapv1.BootstrapServiceServer. The host calls this once
// after Negotiate succeeds, passing the broker stream ID we must Dial to reach
// HostService.
//
// We dial the broker here — eagerly, right after Bind — rather than lazily on
// the first RPC call. This keeps the adapter-forwarding path simple (no
// once-in-every-method lazy dial) and surfaces connection errors early.
func (p *pluginGRPCPlugin) Bind(_ context.Context, req *bootstrapv1.BindRequest) (*bootstrapv1.BindResponse, error) {
	brokerID := req.GetHostBrokerId()

	// DialWithOptions attaches the token interceptor; bare Dial(id) would drop it.
	conn, err := p.broker.DialWithOptions(brokerID, grpc.WithUnaryInterceptor(TokenInterceptorFromEnv()))
	if err != nil {
		return &bootstrapv1.BindResponse{
			Ok:          false,
			ErrorDetail: fmt.Sprintf("dial host broker %d: %v", brokerID, err),
		}, nil
	}

	p.hostConn = conn
	hostClient := hostv1.NewHostServiceClient(conn)

	// Install user implementations into adapters now that the host client is ready.
	if p.cfg.channelFactory != nil {
		p.channel.install(p.cfg.channelFactory(hostClient))
	}
	if p.cfg.toolFactory != nil {
		p.tool.install(p.cfg.toolFactory(hostClient))
	}
	if p.cfg.triggerFactory != nil {
		p.trigger.install(p.cfg.triggerFactory(hostClient))
	}

	return &bootstrapv1.BindResponse{Ok: true}, nil
}

// shutdown closes the host connection. Called on the SIGTERM/SIGINT path;
// best-effort because SIGKILL or a crash will skip it entirely.
func (p *pluginGRPCPlugin) shutdown() {
	if p.hostConn != nil {
		_ = p.hostConn.Close()
	}
}

// ── adapters ─────────────────────────────────────────────────────────────────

// channelAdapter forwards every ChannelService RPC to the real implementation
// installed by Bind. Calls that arrive before Bind (which should not happen in
// production but can in tests) receive codes.Unavailable.
type channelAdapter struct {
	channelv1.UnimplementedChannelServiceServer

	mu   sync.RWMutex
	real channelv1.ChannelServiceServer
}

func (a *channelAdapter) install(impl channelv1.ChannelServiceServer) {
	a.mu.Lock()
	a.real = impl
	a.mu.Unlock()
}

func (a *channelAdapter) Notify(ctx context.Context, req *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
	a.mu.RLock()
	impl := a.real
	a.mu.RUnlock()
	if impl == nil {
		return nil, status.Error(codes.Unavailable, "plugin not yet bound or ChannelService not registered")
	}
	return impl.Notify(ctx, req)
}

func (a *channelAdapter) Request(ctx context.Context, req *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
	a.mu.RLock()
	impl := a.real
	a.mu.RUnlock()
	if impl == nil {
		return nil, status.Error(codes.Unavailable, "plugin not yet bound or ChannelService not registered")
	}
	return impl.Request(ctx, req)
}

// toolAdapter forwards every ToolService RPC to the real implementation.
type toolAdapter struct {
	toolv1.UnimplementedToolServiceServer

	mu   sync.RWMutex
	real toolv1.ToolServiceServer
}

func (a *toolAdapter) install(impl toolv1.ToolServiceServer) {
	a.mu.Lock()
	a.real = impl
	a.mu.Unlock()
}

func (a *toolAdapter) ListTools(ctx context.Context, req *toolv1.ListToolsRequest) (*toolv1.ListToolsResponse, error) {
	a.mu.RLock()
	impl := a.real
	a.mu.RUnlock()
	if impl == nil {
		return nil, status.Error(codes.Unavailable, "plugin not yet bound or ToolService not registered")
	}
	return impl.ListTools(ctx, req)
}

func (a *toolAdapter) Call(ctx context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
	a.mu.RLock()
	impl := a.real
	a.mu.RUnlock()
	if impl == nil {
		return nil, status.Error(codes.Unavailable, "plugin not yet bound or ToolService not registered")
	}
	return impl.Call(ctx, req)
}

func (a *toolAdapter) Cancel(ctx context.Context, req *toolv1.CancelRequest) (*toolv1.CancelResponse, error) {
	a.mu.RLock()
	impl := a.real
	a.mu.RUnlock()
	if impl == nil {
		return nil, status.Error(codes.Unavailable, "plugin not yet bound or ToolService not registered")
	}
	return impl.Cancel(ctx, req)
}

// triggerAdapter forwards every TriggerService RPC to the real implementation.
type triggerAdapter struct {
	triggerv1.UnimplementedTriggerServiceServer

	mu   sync.RWMutex
	real triggerv1.TriggerServiceServer
}

func (a *triggerAdapter) install(impl triggerv1.TriggerServiceServer) {
	a.mu.Lock()
	a.real = impl
	a.mu.Unlock()
}

func (a *triggerAdapter) Start(req *triggerv1.StartRequest, stream triggerv1.TriggerService_StartServer) error {
	a.mu.RLock()
	impl := a.real
	a.mu.RUnlock()
	if impl == nil {
		return status.Error(codes.Unavailable, "plugin not yet bound or TriggerService not registered")
	}
	return impl.Start(req, stream)
}
