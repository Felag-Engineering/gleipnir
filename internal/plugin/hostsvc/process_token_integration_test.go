//go:build unix

// Integration tests that exercise the end-to-end path from process.Start
// (which injects the token via env var) through UnaryInstanceTokenInterceptor
// (which validates it on incoming Host RPCs). The test does not bring up a full
// hostsvc.Server — that requires DB and encryption key wiring (scope of #295).
// Only the interceptor primitive and the env-delivered token are exercised.

package hostsvc_test

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	bootstrapv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/bootstrap/v1"
	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// TestMain intercepts the test binary in fixture mode so it acts as a plugin
// subprocess rather than running the test suite. The integration tests in this
// file re-exec os.Args[0] (this binary) with GLEIPNIR_TEST_FIXTURE set to a
// fixture mode string. This is the standard Go re-exec pattern for testing code
// that spawns subprocesses.
//
// Modes:
//   - "serve-and-block"           — used by process_token_integration_test.go
//   - "serve-and-writeauditstep"  — used by end_to_end_integration_test.go
func TestMain(m *testing.M) {
	switch os.Getenv("GLEIPNIR_TEST_FIXTURE") {
	case "serve-and-block":
		runHostsvcFixtureServePlugin()
		os.Exit(0)
	case "serve-and-writeauditstep":
		runHostsvcFixtureWriteAuditStep()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runHostsvcFixtureServePlugin starts a go-plugin subprocess listener and
// blocks until SIGTERM. It mirrors the same fixture in internal/plugin/process/
// but lives here so the hostsvc integration tests can re-exec this binary.
func runHostsvcFixtureServePlugin() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM)

	impl := &hostsvcFixtureImpl{}
	done := make(chan struct{})
	go func() {
		goplugin.Serve(&goplugin.ServeConfig{
			HandshakeConfig: hostwire.HandshakeConfig,
			Plugins: goplugin.PluginSet{
				"gleipnir": &hostsvcFixtureGRPCPlugin{impl: impl},
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

// hostsvcFixtureGRPCPlugin adapts hostsvcFixtureImpl to go-plugin's GRPCPlugin
// interface.
type hostsvcFixtureGRPCPlugin struct {
	goplugin.Plugin
	impl *hostsvcFixtureImpl
}

func (p *hostsvcFixtureGRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	handshakev1.RegisterHandshakeServiceServer(s, p.impl)
	bootstrapv1.RegisterBootstrapServiceServer(s, p.impl)
	return nil
}

func (p *hostsvcFixtureGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, _ *grpc.ClientConn) (interface{}, error) {
	return nil, status.Error(codes.Unimplemented, "GRPCClient not used on plugin side")
}

// hostsvcFixtureImpl provides the minimum gRPC surface to pass the go-plugin
// handshake and hostwire.Launch's Bootstrap.Bind call.
type hostsvcFixtureImpl struct {
	handshakev1.UnimplementedHandshakeServiceServer
	bootstrapv1.UnimplementedBootstrapServiceServer
}

func (f *hostsvcFixtureImpl) Negotiate(_ context.Context, _ *handshakev1.NegotiateRequest) (*handshakev1.NegotiateResponse, error) {
	return &handshakev1.NegotiateResponse{
		SdkVersion:    "0.0.0-fixture",
		PluginVersion: "0.1.0",
		Ok:            true,
	}, nil
}

func (f *hostsvcFixtureImpl) Bind(_ context.Context, req *bootstrapv1.BindRequest) (*bootstrapv1.BindResponse, error) {
	_ = req.GetHostBrokerId()
	return &bootstrapv1.BindResponse{Ok: true}, nil
}

// startIntegrationFixture launches a real plugin subprocess using the test
// binary re-exec pattern (same approach as process_test.go). It sets
// GLEIPNIR_TEST_FIXTURE so the binary acts as a plugin subprocess rather than
// running the test suite.
//
// The returned Instance must be stopped by the caller.
func startIntegrationFixture(t *testing.T, reg *identity.Registry, instanceID string) *process.Instance {
	t.Helper()

	cfg := process.Config{
		BinaryPath:     os.Args[0],
		InstanceID:     instanceID,
		PluginID:       "integration-plugin",
		InstanceName:   "integration-instance",
		StartupTimeout: 30 * time.Second,
		StopGrace:      10 * time.Second,
		IdentityIssuer: reg,
		// Inject GLEIPNIR_TEST_FIXTURE so the test binary acts as a plugin subprocess.
		Launch: func(ctx context.Context, binaryPath string, host hostwire.HostServer, opts hostwire.Options) (*hostwire.Client, func(), error) {
			os.Setenv("GLEIPNIR_TEST_FIXTURE", "serve-and-block")
			client, teardown, err := hostwire.Launch(ctx, binaryPath, host, opts)
			os.Unsetenv("GLEIPNIR_TEST_FIXTURE")
			return client, teardown, err
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("process.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = inst.Stop(stopCtx)
		<-inst.Done()
	})
	return inst
}

// incomingCtxWithRawToken builds a context that looks like the server side of a
// gRPC call with the given raw string as the instance-token metadata value.
func incomingCtxWithRawToken(token string) context.Context {
	md := metadata.Pairs(sdkproto.InstanceTokenMetadataKey, token)
	return metadata.NewIncomingContext(context.Background(), md)
}

// runTokenInterceptorWithCtx calls UnaryInstanceTokenInterceptor and captures the resolved
// instance ID from the handler context. If the interceptor rejects the call,
// handlerCtx is nil.
func runTokenInterceptorWithCtx(reg *identity.Registry, ctx context.Context) (handlerCtx context.Context, grpcErr error) {
	interceptor := hostsvc.UnaryInstanceTokenInterceptor(reg)
	_, grpcErr = interceptor(ctx, nil, nil, func(c context.Context, _ any) (any, error) {
		handlerCtx = c
		return nil, nil
	})
	return handlerCtx, grpcErr
}

// TestProcessTokenIntegration_ValidTokenAccepted verifies that the token
// injected by process.Start into the subprocess env is accepted by
// UnaryInstanceTokenInterceptor and resolves to the correct instance ID.
func TestProcessTokenIntegration_ValidTokenAccepted(t *testing.T) {
	reg := identity.New()
	const instanceID = "integration-valid"
	inst := startIntegrationFixture(t, reg, instanceID)

	token := inst.Token()
	ctx := incomingCtxWithRawToken(token)
	handlerCtx, grpcErr := runTokenInterceptorWithCtx(reg, ctx)
	if grpcErr != nil {
		t.Fatalf("expected nil error for valid token, got: %v", grpcErr)
	}

	gotID, ok := hostsvc.InstanceIDFromTokenContext(handlerCtx)
	if !ok {
		t.Fatal("InstanceIDFromTokenContext returned ok=false after valid token")
	}
	if gotID != instanceID {
		t.Errorf("resolved instance ID = %q, want %q", gotID, instanceID)
	}
}

// TestProcessTokenIntegration_MissingMetadataRejected verifies that a Host RPC
// with no instance-token metadata is rejected with Unauthenticated.
func TestProcessTokenIntegration_MissingMetadataRejected(t *testing.T) {
	reg := identity.New()
	startIntegrationFixture(t, reg, "integration-missing")

	// No metadata on the context at all.
	_, grpcErr := runTokenInterceptorWithCtx(reg, context.Background())
	if grpcErr == nil {
		t.Fatal("expected Unauthenticated error for missing metadata, got nil")
	}
	st, ok := status.FromError(grpcErr)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", grpcErr)
	}
}

// TestProcessTokenIntegration_MalformedTokenRejected verifies that a garbage
// token value is rejected with Unauthenticated.
func TestProcessTokenIntegration_MalformedTokenRejected(t *testing.T) {
	reg := identity.New()
	startIntegrationFixture(t, reg, "integration-malformed")

	ctx := incomingCtxWithRawToken("not-a-real-token-!!garbage!!")
	_, grpcErr := runTokenInterceptorWithCtx(reg, ctx)
	if grpcErr == nil {
		t.Fatal("expected Unauthenticated error for malformed token, got nil")
	}
	st, ok := status.FromError(grpcErr)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", grpcErr)
	}
}

// TestProcessTokenIntegration_OldTokenRejectedAfterReissue verifies the
// per-generation rotation guarantee end-to-end: after stopping an instance and
// restarting it, the old token is rejected and the new token is accepted.
//
// We must wait on <-inst.Done() before the second Start so the Revoke in
// waitForExit has run; skipping the wait creates a race between Revoke and
// the new Issue call.
func TestProcessTokenIntegration_OldTokenRejectedAfterReissue(t *testing.T) {
	reg := identity.New()
	const instanceID = "integration-rotation"

	// Build a config directly so we can start and stop two generations. We do
	// not use startIntegrationFixture because we need explicit control over the
	// cleanup order.
	baseCfg := process.Config{
		BinaryPath:     os.Args[0],
		InstanceID:     instanceID,
		PluginID:       "integration-plugin",
		InstanceName:   "integration-instance",
		StartupTimeout: 30 * time.Second,
		StopGrace:      10 * time.Second,
		IdentityIssuer: reg,
		HealthSetter: func(_ context.Context, _ string, _ model.PluginHealthState, _ string) {},
		Launch: func(ctx context.Context, binaryPath string, host hostwire.HostServer, opts hostwire.Options) (*hostwire.Client, func(), error) {
			os.Setenv("GLEIPNIR_TEST_FIXTURE", "serve-and-block")
			client, teardown, err := hostwire.Launch(ctx, binaryPath, host, opts)
			os.Unsetenv("GLEIPNIR_TEST_FIXTURE")
			return client, teardown, err
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// First generation.
	inst1, err := process.Start(ctx, baseCfg)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	oldToken := inst1.Token()

	stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
	defer stopCancel()
	if err := inst1.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Wait for waitForExit to complete so the Revoke inside it runs before
	// the second Issue.
	select {
	case <-inst1.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("first instance done channel did not close within 10s")
	}

	// Second generation.
	inst2, err := process.Start(ctx, baseCfg)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	newToken := inst2.Token()
	defer func() {
		stopCtx2, stopCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel2()
		_ = inst2.Stop(stopCtx2)
		<-inst2.Done()
	}()

	// Old token must be rejected.
	oldCtx := incomingCtxWithRawToken(oldToken)
	if _, grpcErr := runTokenInterceptorWithCtx(reg, oldCtx); grpcErr == nil {
		t.Error("old token accepted after reissue — per-generation rotation broken")
	} else {
		st, ok := status.FromError(grpcErr)
		if !ok || st.Code() != codes.Unauthenticated {
			t.Errorf("old token rejection: expected Unauthenticated, got %v", grpcErr)
		}
	}

	// New token must be accepted and resolve to the correct instance.
	newCtx := incomingCtxWithRawToken(newToken)
	handlerCtx, grpcErr := runTokenInterceptorWithCtx(reg, newCtx)
	if grpcErr != nil {
		t.Fatalf("new token rejected after reissue: %v", grpcErr)
	}
	if gotID, ok := hostsvc.InstanceIDFromTokenContext(handlerCtx); !ok {
		t.Error("InstanceIDFromTokenContext returned ok=false for new token")
	} else if gotID != instanceID {
		t.Errorf("new token resolved to %q, want %q", gotID, instanceID)
	}
}
