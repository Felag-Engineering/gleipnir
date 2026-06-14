package e2e_test

// hostrpc_harness_test.go provides shared scaffolding for the generation-drain
// and identity-rotation e2e tests. It wires the production
// UnaryInstanceTokenInterceptor → UnaryGenerationRefcountInterceptor chain onto
// a real in-process gRPC server so both tests exercise the full wire path, not
// just in-process interceptor invocations.
//
// Why this is separate from composition_test.go: composition_test.go is
// trigger-stream specific (it wires TriggerService, Supervisor, Dispatcher, DB,
// etc.). The host-RPC tests here need HostService, not TriggerService, and carry
// no DB dependency. Keeping them separate avoids bloating the composition file
// and makes the intent of each file immediately clear.

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// ── stub server ───────────────────────────────────────────────────────────────

// stubHostServer is a minimal HostService implementation that only handles
// GetRunContext. It is dependency-free: no DB, no encryption key, no metrics
// registry. That makes it ideal for isolating the interceptor chain under test.
//
// onCall, when non-nil, is invoked inside the handler body. The drain test uses
// this to block the handler in-flight while BeginDrain waits, and to simulate
// the force-cancel path by blocking on ctx.Done().
type stubHostServer struct {
	hostv1.UnimplementedHostServiceServer
	onCall func(ctx context.Context)
}

// GetRunContext implements hostv1.HostServiceServer.GetRunContext.
//
// The ctx.Err() propagation after onCall is REQUIRED for the force-cancel
// sub-scenario: the generation interceptor passes a wrapped cancellable context
// to the handler (generation_interceptor.go:59). When BeginDrain's grace elapses
// it invokes entry.cancel() which cancels that wrapped context. The handler must
// observe that cancellation and return an error — otherwise the caller sees
// success even when force-cancel fires, which would be a false positive.
func (s *stubHostServer) GetRunContext(ctx context.Context, _ *hostv1.GetRunContextRequest) (*hostv1.GetRunContextResponse, error) {
	if s.onCall != nil {
		s.onCall(ctx)
	}
	// Propagate context cancellation as an error so the gRPC runtime converts it
	// to codes.Canceled on the wire. Without this the force-cancel test is a
	// false positive (server returns nil err → client sees success).
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return &hostv1.GetRunContextResponse{}, nil
}

// ── server stand-up ───────────────────────────────────────────────────────────

// startHostServer stands up a real in-process gRPC server with the production
// interceptor chain and returns a typed HostServiceClient pointing at it.
//
// Chain order: UnaryInstanceTokenInterceptor (token FIRST) →
// UnaryGenerationRefcountInterceptor. This matches the production wiring in
// generation_interceptor.go:15-16 and the plan's constraint.
//
// The returned cleanup func stops the server and closes the client connection;
// callers should defer it.
func startHostServer(
	t *testing.T,
	reg *identity.Registry,
	ctrl *generation.Controller,
	srv hostv1.HostServiceServer,
) (hostv1.HostServiceClient, func()) {
	t.Helper()

	// Bind on a random free port — same pattern as composition_test.go:56.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startHostServer: listen: %v", err)
	}

	gs := grpc.NewServer(grpc.ChainUnaryInterceptor(
		hostsvc.UnaryInstanceTokenInterceptor(reg),
		hostsvc.UnaryGenerationRefcountInterceptor(ctrl),
	))
	hostv1.RegisterHostServiceServer(gs, srv)
	go gs.Serve(lis) //nolint:errcheck

	// grpc.NewClient returns (*ClientConn, error); the two-step pattern mirrors
	// composition_test.go:64-69 and end_to_end_integration_test.go:148.
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		gs.Stop()
		t.Fatalf("startHostServer: dial: %v", err)
	}

	client := hostv1.NewHostServiceClient(conn)
	return client, func() {
		gs.Stop()
		conn.Close()
	}
}

// ── metadata helper ───────────────────────────────────────────────────────────

// callCtxWithToken returns a context that carries the given instance token as
// OUTGOING gRPC metadata. The gRPC client transport converts outgoing metadata
// to incoming metadata on the server side, where UnaryInstanceTokenInterceptor
// reads it via metadata.FromIncomingContext.
//
// This is the key difference from the in-process interceptor tests in
// hostsvc/generation_interceptor_test.go and hostsvc/process_token_integration_test.go,
// which fabricate an INCOMING context (metadata.NewIncomingContext) because they
// bypass the wire. Using OUTGOING metadata here makes the tests genuinely e2e:
// rejection travels back as a gRPC status code over the network.
func callCtxWithToken(token string) context.Context {
	md := metadata.Pairs(sdkproto.InstanceTokenMetadataKey, token)
	return metadata.NewOutgoingContext(context.Background(), md)
}
