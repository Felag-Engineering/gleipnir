package serve

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

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
