package serve

import (
	"log/slog"
	"os"
	"syscall"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// config holds the resolved options for a Serve or EmitManifest call.
type config struct {
	channelFactory func(hostv1.HostServiceClient) channelv1.ChannelServiceServer
	toolFactory    func(hostv1.HostServiceClient) toolv1.ToolServiceServer
	triggerFactory func(hostv1.HostServiceClient) triggerv1.TriggerServiceServer
	manifest       *manifest.Manifest
	stopSignals    []os.Signal
	logger         *slog.Logger
}

// newConfig applies opts in order and fills in defaults for any unset field.
func newConfig(opts []Option) *config {
	cfg := &config{
		stopSignals: []os.Signal{syscall.SIGTERM, syscall.SIGINT},
		logger:      slog.Default(),
	}
	for _, o := range opts {
		o(cfg)
	}
	return cfg
}

// Option is a functional option for Serve.
type Option func(*config)

// WithChannelService registers a ChannelService factory. The factory receives
// the typed HostServiceClient after Bootstrap.Bind completes, so it has access
// to GetInstanceConfig, GetCredentials, and other host RPCs from the start of
// the first call.
func WithChannelService(f func(hostv1.HostServiceClient) channelv1.ChannelServiceServer) Option {
	return func(c *config) { c.channelFactory = f }
}

// WithToolService registers a ToolService factory. The factory contract is the
// same as WithChannelService: the HostServiceClient is available from the first
// RPC call onward.
//
// Phase 6 wires this for real tool plugins; shipping the option now avoids an
// API bump later.
func WithToolService(f func(hostv1.HostServiceClient) toolv1.ToolServiceServer) Option {
	return func(c *config) { c.toolFactory = f }
}

// WithTriggerService registers a TriggerService factory. Mirrors WithChannelService
// semantically; real wiring lands in Phase 6/8.
func WithTriggerService(f func(hostv1.HostServiceClient) triggerv1.TriggerServiceServer) Option {
	return func(c *config) { c.triggerFactory = f }
}

// WithManifest supplies the plugin's manifest so that Serve can advertise the
// correct plugin version in Handshake.Negotiate and so that --emit-manifest can
// write the manifest JSON to stdout.
//
// Required for --emit-manifest to succeed; optional for normal serve mode.
func WithManifest(m manifest.Manifest) Option {
	return func(c *config) { c.manifest = &m }
}

// WithLogger overrides the logger used for internal serve diagnostics. The
// default is slog.Default(). Useful in tests to capture log output.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}
