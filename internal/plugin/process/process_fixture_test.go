//go:build unix

package process_test

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bootstrapv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/bootstrap/v1"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

// runFixtureServePlugin starts a go-plugin subprocess listener and blocks until
// SIGTERM or SIGKILL. It is called from TestMain when GLEIPNIR_TEST_FIXTURE is
// set to one of the fixture modes.
//
// We call goplugin.Serve directly (the same way runfixture/main.go does) so the
// magic-cookie handshake matches hostwire.HandshakeConfig exactly. The fixture
// implements only Bootstrap.Bind and Handshake.Negotiate — the minimum surface
// needed for hostwire.Launch to succeed.
func runFixtureServePlugin() {
	// Block until SIGTERM so the host can Stop us cleanly.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM)

	impl := &fixtureImpl{}
	done := make(chan struct{})
	go func() {
		goplugin.Serve(&goplugin.ServeConfig{
			HandshakeConfig: hostwire.HandshakeConfig,
			Plugins: goplugin.PluginSet{
				"gleipnir": &fixtureGRPCPlugin{impl: impl},
			},
			GRPCServer: goplugin.DefaultGRPCServer,
		})
		close(done)
	}()

	select {
	case <-quit:
	case <-done:
	}
}

// runFixtureServeThenCrash starts the go-plugin server in a goroutine, then
// waits for a host-driven crash signal before exiting the process. This
// simulates a crash: the subprocess exits unexpectedly while the host still
// holds a live connection.
//
// The signal is a file path delivered via GLEIPNIR_TEST_CRASH_TRIGGER: the
// fixture polls for that file's existence (cheap subprocess-internal poll,
// 10ms interval) and exits as soon as it appears. Tests only create the file
// after process.Start has returned successfully, so the crash can never race
// the go-plugin handshake — unlike a fixed sleep, which could be too short
// for Bootstrap.Bind to complete under -race on a loaded CI runner. A
// generous overall guard bounds how long an orphaned fixture can linger if
// the trigger var is unset or never touched.
func runFixtureServeThenCrash() {
	impl := &fixtureImpl{}
	// Serve in the background so we can exit on the crash signal regardless
	// of whether the parent has called Kill.
	go goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: hostwire.HandshakeConfig,
		Plugins: goplugin.PluginSet{
			"gleipnir": &fixtureGRPCPlugin{impl: impl},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
	})

	triggerPath := os.Getenv("GLEIPNIR_TEST_CRASH_TRIGGER")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if triggerPath != "" {
			if _, err := os.Stat(triggerPath); err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(17)
}

// runFixtureServeTrapSigterm starts the plugin protocol then ignores SIGTERM,
// forcing go-plugin to escalate to SIGKILL after its internal grace period.
// Used by TestStop_KillOnGraceTimeout.
func runFixtureServeTrapSigterm() {
	// Ignore SIGTERM so the host's graceful-stop signal has no effect. go-plugin
	// will send SIGKILL after its own internal grace (default 2s).
	signal.Ignore(syscall.SIGTERM)

	impl := &fixtureImpl{}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: hostwire.HandshakeConfig,
		Plugins: goplugin.PluginSet{
			"gleipnir": &fixtureGRPCPlugin{impl: impl},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
	})
}

// runFixtureServeEchoEnv starts the plugin protocol, then writes the values of
// GLEIPNIR_INSTANCE_ID and GLEIPNIR_PLUGIN_ID to stderr so the host-side log
// pipe can capture them. Used by TestStart_EnvInjected.
func runFixtureServeEchoEnv() {
	// Echo the env vars to stderr before serving so the host pipe sees them.
	// The values are written as "KEY=VALUE" lines so the test can grep for them.
	fmt.Fprintf(os.Stderr, "GLEIPNIR_INSTANCE_ID=%s\n", os.Getenv("GLEIPNIR_INSTANCE_ID"))
	fmt.Fprintf(os.Stderr, "GLEIPNIR_PLUGIN_ID=%s\n", os.Getenv("GLEIPNIR_PLUGIN_ID"))

	// Block until killed so the host has time to read the log lines.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM)

	impl := &fixtureImpl{}
	done := make(chan struct{})
	go func() {
		goplugin.Serve(&goplugin.ServeConfig{
			HandshakeConfig: hostwire.HandshakeConfig,
			Plugins: goplugin.PluginSet{
				"gleipnir": &fixtureGRPCPlugin{impl: impl},
			},
			GRPCServer: goplugin.DefaultGRPCServer,
		})
		close(done)
	}()

	select {
	case <-quit:
	case <-done:
	}
}

// runFixtureServeEchoToken starts the plugin protocol, then writes the value of
// GLEIPNIR_INSTANCE_TOKEN to stderr so the host-side log pipe can capture it.
// Used by TestStart_TokenInjectedIntoEnv.
func runFixtureServeEchoToken() {
	// Echo the token to stderr before serving so the host pipe sees it. Written
	// as "KEY=VALUE" so the test can grep for the exact pair.
	fmt.Fprintf(os.Stderr, "GLEIPNIR_INSTANCE_TOKEN=%s\n", os.Getenv("GLEIPNIR_INSTANCE_TOKEN"))

	// Block until killed so the host has time to read the log line.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM)

	impl := &fixtureImpl{}
	done := make(chan struct{})
	go func() {
		goplugin.Serve(&goplugin.ServeConfig{
			HandshakeConfig: hostwire.HandshakeConfig,
			Plugins: goplugin.PluginSet{
				"gleipnir": &fixtureGRPCPlugin{impl: impl},
			},
			GRPCServer: goplugin.DefaultGRPCServer,
		})
		close(done)
	}()

	select {
	case <-quit:
	case <-done:
	}
}

// runFixtureServeViaSDK calls serve.Serve with a trivial ChannelService stub so
// the host-side process tests exercise the real SDK Serve path rather than a
// hand-rolled go-plugin listener. This catches regressions where the SDK's
// serve.Serve is broken but the hand-rolled fixture still passes.
func runFixtureServeViaSDK() {
	serve.Serve(
		serve.WithChannelService(func(_ hostv1.HostServiceClient) channelv1.ChannelServiceServer {
			return &sdkFixtureChannelService{}
		}),
	)
}

// sdkFixtureChannelService is a trivial ChannelService that returns Ok=true on
// every Notify call. Used only by runFixtureServeViaSDK.
type sdkFixtureChannelService struct {
	channelv1.UnimplementedChannelServiceServer
}

func (s *sdkFixtureChannelService) Notify(_ context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
	return &channelv1.NotifyResponse{Ok: true}, nil
}

// ── go-plugin GRPCPlugin implementation ─────────────────────────────────────

// fixtureGRPCPlugin adapts fixtureImpl to go-plugin's GRPCPlugin interface.
type fixtureGRPCPlugin struct {
	goplugin.Plugin
	impl *fixtureImpl
}

func (p *fixtureGRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	handshakev1.RegisterHandshakeServiceServer(s, p.impl)
	bootstrapv1.RegisterBootstrapServiceServer(s, p.impl)
	return nil
}

func (p *fixtureGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, _ *grpc.ClientConn) (interface{}, error) {
	return nil, status.Error(codes.Unimplemented, "GRPCClient not used on plugin side")
}

// ── minimal service implementations ─────────────────────────────────────────

// fixtureImpl provides the minimum gRPC service surface to pass the go-plugin
// handshake and hostwire.Launch's Bootstrap.Bind call.
type fixtureImpl struct {
	handshakev1.UnimplementedHandshakeServiceServer
	bootstrapv1.UnimplementedBootstrapServiceServer
}

func (f *fixtureImpl) Negotiate(_ context.Context, _ *handshakev1.NegotiateRequest) (*handshakev1.NegotiateResponse, error) {
	return &handshakev1.NegotiateResponse{
		SdkVersion:    "0.0.0-fixture",
		PluginVersion: "0.1.0",
		Ok:            true,
	}, nil
}

func (f *fixtureImpl) Bind(_ context.Context, req *bootstrapv1.BindRequest) (*bootstrapv1.BindResponse, error) {
	_ = req.GetHostBrokerId()
	return &bootstrapv1.BindResponse{Ok: true}, nil
}
