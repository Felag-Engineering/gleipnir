package serve

import (
	"context"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// InstanceTokenEnvVar is the environment variable name used to deliver the
// per-generation identity token to a plugin subprocess at spawn time. The host
// writes this value before exec; the plugin reads it once at startup via
// TokenInterceptorFromEnv. Both sides reference this constant so the env var
// name has a single source of truth (ADR-042).
const InstanceTokenEnvVar = "GLEIPNIR_INSTANCE_TOKEN"

// TokenInterceptorFromEnv reads the identity token from the
// GLEIPNIR_INSTANCE_TOKEN environment variable and returns a gRPC unary client
// interceptor that attaches it to every outgoing Host RPC.
//
// This is the standard wiring for plugin authors: call TokenInterceptorFromEnv()
// once at startup and register the result as a dial option on the host-API
// connection. If the env var is absent or empty, the interceptor is a
// pass-through (same semantics as TokenInterceptor("")).
func TokenInterceptorFromEnv() grpc.UnaryClientInterceptor {
	return TokenInterceptor(os.Getenv(InstanceTokenEnvVar))
}

// TokenInterceptor returns a gRPC unary client interceptor that appends the
// instance token to the outgoing metadata of every Host RPC. The host uses this
// token to verify the caller's identity (spec §8.4).
//
// When token is empty the interceptor is a pass-through: the call proceeds
// without any token header. The host will then reject the call with
// Unauthenticated, which is the correct behavior when the subprocess was not
// launched through the standard host-broker path.
//
// Wired into the SDK's host-API client connection by the subprocess launch path
// (issue #158); not yet referenced from run_capture.go.
func TokenInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, sdkproto.InstanceTokenMetadataKey, token)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
