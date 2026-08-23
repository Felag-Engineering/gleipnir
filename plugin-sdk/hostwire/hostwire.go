// Package hostwire is the single source of truth for go-plugin client wiring
// shared by the CLI `run` command and the future Phase-3 production loader.
//
// Any code that launches a plugin subprocess — whether in development (the
// `gleipnir-plugin run` CLI) or in production (the host's plugin loader) —
// must use the exported symbols from this package so both code paths exercise
// the same handshake and broker wiring. That consistency is the whole point.
//
// Architecture note: The host exposes HostService to plugins via the
// go-plugin GRPCBroker mechanism. go-plugin v1.6.3 has no AdditionalServices
// field; the broker stream ID must be communicated out-of-band. We use an
// additive BootstrapService.Bind RPC (proto/gleipnir/plugin/bootstrap/v1/)
// for this. The handshake.proto is IMMORTAL and must not be modified.
//
// Broker ID flow:
//  1. GRPCClient is called by go-plugin during Dispense with the live broker.
//  2. We call broker.NextId() to allocate a stream ID.
//  3. We start AcceptAndServe(id, …) in a goroutine to register HostService.
//  4. We stash the allocated ID in gleipnirPlugin.allocatedBrokerID.
//  5. Launch reads allocatedBrokerID after Dispense and calls Bootstrap.Bind.
package hostwire

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	plugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	bootstrapv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/bootstrap/v1"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
)

// HandshakeConfig is the go-plugin magic cookie configuration. Both the host
// and the plugin binary must use the same values or go-plugin refuses to
// connect, preventing misconfigured binaries from being loaded by accident.
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "GLEIPNIR_PLUGIN",
	MagicCookieValue: "gleipnir-plugin-v1",
}

// HostServer is implemented by anything that can register itself as a gRPC
// service on a server. The hostwire package uses this to register HostService
// on the broker-allocated gRPC server that it exposes back to the plugin.
type HostServer interface {
	Register(srv *grpc.Server)
}

// Client holds the typed gRPC clients for every plugin-side service. These are
// host→plugin clients: the host calls into the plugin via these stubs.
//
// The host's own HostService is not a field here — it is exposed as a gRPC
// server via the broker (see Launch), and the plugin Dials it using the broker
// stream ID conveyed by Bootstrap.Bind.
type Client struct {
	Handshake handshakev1.HandshakeServiceClient
	Tool      toolv1.ToolServiceClient
	Channel   channelv1.ChannelServiceClient
	Trigger   triggerv1.TriggerServiceClient
	Bootstrap bootstrapv1.BootstrapServiceClient

	// conn is the underlying gRPC connection backing the typed service clients.
	// Retained here so Conn() can expose it to the host dispatcher layer.
	conn *grpc.ClientConn

	// Pid is the OS process ID of the plugin subprocess. Set by Launch after
	// the handshake completes. Zero when not available (e.g. in tests that
	// inject a stub LaunchFunc returning a manually constructed Client).
	Pid int
}

// Conn returns the underlying gRPC connection that backs the typed service
// clients. The production host dispatcher uses this to satisfy
// dispatch.ConnFactory: it constructs channelv1.NewChannelServiceClient(conn)
// for each plugin instance rather than going through the typed Client fields
// (which are equivalent but require the full Client to be available).
//
// This is an additive SDK change per ADR-042 SDK stability rules.
func (c *Client) Conn() *grpc.ClientConn { return c.conn }

// Options configures a Launch call.
type Options struct {
	// Stderr receives the plugin subprocess's stderr output. If nil, stderr is
	// discarded.
	Stderr io.Writer

	// StartupTimeout is the maximum time to wait for the plugin subprocess to
	// report readiness. Defaults to 10 seconds.
	StartupTimeout time.Duration

	// Logger is the slog logger for host-side log output. If nil, the default
	// slog logger is used.
	Logger *slog.Logger

	// OnProcessExited is called in a goroutine when the plugin subprocess has
	// exited and all of go-plugin's internal goroutines have finished (including
	// the stderr copy goroutine). Callers use this to detect subprocess crashes
	// without polling. If nil, it is not called.
	//
	// At the point this function is called, go-plugin has stopped writing to
	// Stderr; any teardown of the Stderr writer (e.g. closing an io.PipeWriter)
	// should happen inside this callback.
	OnProcessExited func()

	// Env is a list of extra environment variables to set in the subprocess, in
	// "KEY=VALUE" form. These are appended to the subprocess's allowlisted base
	// environment (see safeEnvKeys). Use this to pass per-instance variables
	// such as GLEIPNIR_INSTANCE_ID and GLEIPNIR_INSTANCE_TOKEN.
	// If nil, no extra variables are added.
	Env []string

	// ServerInterceptors are chained (in slice order) onto the host-side gRPC
	// server before service registration. Order matters: token-auth must come
	// before any interceptor that reads instanceID from context.
	//
	// This is the seam through which the three host-RPC interceptors —
	// UnaryInstanceTokenInterceptor, UnaryGenerationRefcountInterceptor, and
	// UnaryCallIDInterceptor — are wired in production (#295). Closing this
	// single gap in hostwire activates all three with one additive SDK change.
	//
	// Zero-value (nil slice) preserves the current behaviour: no interceptors
	// are added to the broker gRPC server. This is an additive public-API
	// change per ADR-042 SDK stability rules.
	ServerInterceptors []grpc.UnaryServerInterceptor
}

// gleipnirPlugin is the go-plugin GRPCPlugin implementation for the Gleipnir
// plugin protocol. One instance is created per Launch call.
//
// The host field is set before Dispense. allocatedBrokerID is populated
// atomically inside GRPCClient and read by Launch after Dispense returns.
type gleipnirPlugin struct {
	plugin.Plugin

	// host is the HostService implementation to register on the broker server.
	// Set by newGleipnirPlugin before Dispense is called.
	host HostServer

	// serverInterceptors are chained onto the broker gRPC server in slice order.
	// When non-empty, GRPCClient prepends grpc.ChainUnaryInterceptor(...) to the
	// server options before calling grpc.NewServer.
	serverInterceptors []grpc.UnaryServerInterceptor

	// allocatedBrokerID is set inside GRPCClient after calling broker.NextId().
	// Launch reads this after Dispense to pass to Bootstrap.Bind.
	allocatedBrokerID uint32
}

// newGleipnirPlugin creates a gleipnirPlugin wired to the given host and
// optional server interceptors. It is used instead of a bare struct literal so
// the host and interceptor fields are always set before GRPCClient runs.
func newGleipnirPlugin(host HostServer, interceptors []grpc.UnaryServerInterceptor) *gleipnirPlugin {
	return &gleipnirPlugin{
		host:               host,
		serverInterceptors: interceptors,
	}
}

// GRPCServer is not used on the host side — plugins implement the server, not
// the host. Panics if called.
func (p *gleipnirPlugin) GRPCServer(_ *plugin.GRPCBroker, _ *grpc.Server) error {
	panic("hostwire: GRPCServer called on host side — this is a bug")
}

// GRPCClient builds typed plugin clients from the given connection and starts
// the host's HostService listener on the broker. The broker stream ID is
// allocated here (the only place where the broker is accessible), stashed in
// p.allocatedBrokerID, and then passed to Bootstrap.Bind by Launch after
// Dispense returns.
//
// Note on multiplexed gRPC broker: when GRPCBrokerMultiplex=true, go-plugin
// requires that AcceptAndServe calls are sequential (one Dial must complete
// before the next AcceptAndServe). We start exactly one AcceptAndServe per
// Launch call, satisfying that constraint.
func (p *gleipnirPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, conn *grpc.ClientConn) (interface{}, error) {
	id := broker.NextId()
	atomic.StoreUint32(&p.allocatedBrokerID, id)

	// Start the host-side gRPC server that the plugin will Dial. This must run
	// in a goroutine because AcceptAndServe blocks until the listener is closed.
	go broker.AcceptAndServe(id, func(opts []grpc.ServerOption) *grpc.Server {
		// Bound incoming message size to 1 MiB (1 << 20). A single Host RPC
		// payload (EmitEvent payload, Log message) should never legitimately
		// exceed this; the per-RPC handler caps below are the
		// first line of defence, but a network-level limit prevents a misbehaving
		// plugin from exhausting host memory before any handler logic runs.
		// Send size is capped at the same value to keep the contract symmetric.
		sizeLimits := []grpc.ServerOption{
			grpc.MaxRecvMsgSize(1 << 20),
			grpc.MaxSendMsgSize(1 << 20),
		}
		opts = append(sizeLimits, opts...)
		if len(p.serverInterceptors) > 0 {
			// Prepend the chain interceptor before the broker-provided server
			// options. Slice order determines invocation order; the caller is
			// responsible for placing token-auth first.
			opts = append([]grpc.ServerOption{grpc.ChainUnaryInterceptor(p.serverInterceptors...)}, opts...)
		}
		s := grpc.NewServer(opts...)
		if p.host != nil {
			p.host.Register(s)
		}
		return s
	})

	return &Client{
		Handshake: handshakev1.NewHandshakeServiceClient(conn),
		Tool:      toolv1.NewToolServiceClient(conn),
		Channel:   channelv1.NewChannelServiceClient(conn),
		Trigger:   triggerv1.NewTriggerServiceClient(conn),
		Bootstrap: bootstrapv1.NewBootstrapServiceClient(conn),
		conn:      conn,
	}, nil
}

// PluginMap is the go-plugin plugin set used by both the host and plugin
// subprocess. Both sides must declare the same entry name ("gleipnir").
//
// This package-level map uses a placeholder instance; Launch creates a fresh
// gleipnirPlugin per call and passes it in a per-call PluginSet so the host
// field is correctly scoped.
var PluginMap = plugin.PluginSet{
	"gleipnir": &gleipnirPlugin{},
}

// safeEnvKeys is the allowlist of environment variable names that are safe to
// pass from the host process into a plugin subprocess. Only system-level vars
// needed for basic process operation are included. Gleipnir secrets
// (GLEIPNIR_ENCRYPTION_KEY, GLEIPNIR_DB_PATH, provider API keys, etc.) are
// never in this list. The three Gleipnir instance vars
// (GLEIPNIR_INSTANCE_ID, GLEIPNIR_PLUGIN_ID, GLEIPNIR_INSTANCE_TOKEN) are
// injected separately via opts.Env by the caller (internal/plugin/process).
var safeEnvKeys = []string{
	"PATH",   // required for subprocess tool resolution
	"HOME",   // expected by most Unix programs
	"USER",   // expected by some runtimes
	"TZ",     // timezone — affects log timestamps inside the plugin
	"TMPDIR", // standard override for temp file location
	"LANG",   // locale — affects string handling in some libraries
	"LC_ALL", // locale override — takes precedence over LANG
}

// safeEnv builds a minimal environment for plugin subprocesses by extracting
// only the allowlisted system vars from the host's os.Environ(). This prevents
// host secrets (GLEIPNIR_ENCRYPTION_KEY, provider API keys, etc.) from leaking
// into untrusted plugin processes. The go-plugin library appends its own
// protocol vars (PLUGIN_MIN_PORT, PLUGIN_PROTOCOL_VERSIONS, etc.) after this
// base, so those do not need to be listed here.
func safeEnv() []string {
	// Build a lookup map from the current environment for O(1) key access.
	hostEnv := make(map[string]string, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, val, _ := strings.Cut(entry, "=")
		hostEnv[key] = val
	}

	result := make([]string, 0, len(safeEnvKeys))
	for _, key := range safeEnvKeys {
		if val, ok := hostEnv[key]; ok {
			result = append(result, key+"="+val)
		}
	}
	return result
}

// Launch starts a plugin subprocess at binaryPath, performs the go-plugin
// handshake, registers the host's HostService on the broker, and calls
// Bootstrap.Bind so the plugin knows which stream ID to Dial.
//
// Returns a typed Client for calling into the plugin, a teardown function that
// kills the subprocess (call it via defer), and any error.
//
// Callers are responsible for calling Handshake.Negotiate after Launch returns
// to verify the plugin's declared capabilities match the manifest.
func Launch(ctx context.Context, binaryPath string, host HostServer, opts Options) (*Client, func(), error) {
	if opts.StartupTimeout == 0 {
		opts.StartupTimeout = 10 * time.Second
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Create a fresh gleipnirPlugin for this launch. It must not be shared with
	// PluginMap — that is only exported so callers (runfixture, production
	// loader) can reference the plugin set for plugin.Serve on the plugin side.
	p := newGleipnirPlugin(host, opts.ServerInterceptors)

	cmd := exec.CommandContext(ctx, binaryPath)

	// Build the subprocess environment from a strict allowlist rather than
	// inheriting os.Environ(). This prevents GLEIPNIR_ENCRYPTION_KEY and other
	// host secrets from leaking to untrusted plugin processes.
	//
	// SkipHostEnv=true tells go-plugin not to append os.Environ() on top of
	// what we set here. go-plugin still appends its own protocol vars
	// (PLUGIN_MIN_PORT, PLUGIN_PROTOCOL_VERSIONS, the magic cookie, etc.) which
	// the subprocess needs to complete the handshake — those are safe to pass.
	cmd.Env = append(safeEnv(), opts.Env...)

	clientConfig := &plugin.ClientConfig{
		HandshakeConfig:     HandshakeConfig,
		Plugins:             plugin.PluginSet{"gleipnir": p},
		AllowedProtocols:    []plugin.Protocol{plugin.ProtocolGRPC},
		Cmd:                 cmd,
		StartTimeout:        opts.StartupTimeout,
		Logger:              newHCLogger(logger),
		GRPCBrokerMultiplex: true,
		SkipHostEnv:         true,
	}

	if opts.Stderr != nil {
		clientConfig.Stderr = opts.Stderr
	}

	c := plugin.NewClient(clientConfig)

	rpcClient, err := c.Client()
	if err != nil {
		c.Kill()
		return nil, nil, fmt.Errorf("hostwire: connect to plugin: %w", err)
	}

	// Dispense triggers GRPCClient, which allocates the broker ID and stashes
	// it in p.allocatedBrokerID.
	raw, err := rpcClient.Dispense("gleipnir")
	if err != nil {
		c.Kill()
		return nil, nil, fmt.Errorf("hostwire: dispense plugin: %w", err)
	}

	pluginClient, ok := raw.(*Client)
	if !ok {
		c.Kill()
		return nil, nil, fmt.Errorf("hostwire: unexpected dispensed type %T", raw)
	}

	brokerId := atomic.LoadUint32(&p.allocatedBrokerID)

	// Inform the plugin of the broker stream ID so it can Dial HostService.
	bindResp, err := pluginClient.Bootstrap.Bind(ctx, &bootstrapv1.BindRequest{
		HostBrokerId: brokerId,
	})
	if err != nil {
		c.Kill()
		return nil, nil, fmt.Errorf("hostwire: bootstrap bind: %w", err)
	}
	if !bindResp.GetOk() {
		c.Kill()
		return nil, nil, fmt.Errorf("hostwire: bootstrap bind rejected by plugin: %s", bindResp.GetErrorDetail())
	}

	// Start a goroutine that polls c.Exited() and fires opts.OnProcessExited
	// when the subprocess has exited and all go-plugin goroutines have cleaned
	// up. This is the mechanism the host process package uses to detect crashes
	// without having to call Kill() first. When Kill() is called explicitly, the
	// goroutine also unblocks because Kill() sets c.exited = true.
	//
	// The goroutine also exits on ctx cancellation as a defence-in-depth measure
	// to avoid goroutine leaks when the caller abandons the launch context.
	if opts.OnProcessExited != nil {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
				if c.Exited() {
					opts.OnProcessExited()
					return
				}
			}
		}()
	}

	// Extract the subprocess PID from go-plugin's reattach config so the host
	// can sample /proc/<pid>/statm for memory accounting. ReattachConfig() is
	// the only stable API on go-plugin's *plugin.Client that exposes the PID.
	if rc := c.ReattachConfig(); rc != nil {
		pluginClient.Pid = rc.Pid
	}

	teardown := func() { c.Kill() }
	return pluginClient, teardown, nil
}

// newHCLogger creates a minimal hclog.Logger that discards output. go-plugin
// requires an hclog.Logger; at this layer we let plugin subprocess stderr flow
// through opts.Stderr (set in ClientConfig) rather than duplicating it via the
// logger. Callers that want structured host-side logging should bridge via a
// custom hclog → slog adapter.
func newHCLogger(_ *slog.Logger) hclog.Logger {
	return hclog.New(&hclog.LoggerOptions{
		Name:   "gleipnir-plugin",
		Level:  hclog.Warn,
		Output: io.Discard,
	})
}
