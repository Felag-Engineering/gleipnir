package hostsvc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// instanceIDCtxKey is the unexported context key used to store the resolved
// plugin instance ID attached by UnaryInstanceTokenInterceptor.
type instanceIDCtxKey struct{}

// InstanceIDFromTokenContext returns the plugin instance ID resolved from the
// gleipnir-instance-token metadata by UnaryInstanceTokenInterceptor, along with
// a boolean indicating whether a valid ID is present.
//
// Returns ("", false) when the interceptor did not find a valid token, or when
// ctx was not produced by the interceptor at all.
func InstanceIDFromTokenContext(ctx context.Context) (string, bool) {
	v := ctx.Value(instanceIDCtxKey{})
	if v == nil {
		return "", false
	}
	id, ok := v.(string)
	return id, ok
}

// UnaryInstanceTokenInterceptor returns a gRPC unary server interceptor that
// authenticates every incoming Host RPC by reading the gleipnir-instance-token
// metadata key and resolving it against reg.
//
// On success, the resolved instance ID is attached to the request context under
// instanceIDCtxKey so that InstanceIDFromTokenContext can retrieve it downstream.
//
// On missing or unknown token, the interceptor returns codes.Unauthenticated
// and the handler is not invoked. No audit event is written here because the
// instance identity is unknown at this stage — writing an audit row would
// require a NULL plugin_instance_id, which is not useful. The
// unauthorized_request_id audit event (spec §8.4) is written by the
// WriteAuditStep handler when request_id ownership fails.
func UnaryInstanceTokenInterceptor(reg *identity.Registry) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing instance token")
		}

		vals := md.Get(sdkproto.InstanceTokenMetadataKey)
		// Accept exactly one non-empty value — same convention as UnaryCallIDInterceptor.
		if len(vals) != 1 || vals[0] == "" {
			return nil, status.Error(codes.Unauthenticated, "missing instance token")
		}

		instanceID, found := reg.Lookup(vals[0])
		if !found {
			return nil, status.Error(codes.Unauthenticated, "unknown or revoked instance token")
		}

		ctx = context.WithValue(ctx, instanceIDCtxKey{}, instanceID)
		return handler(ctx, req)
	}
}
