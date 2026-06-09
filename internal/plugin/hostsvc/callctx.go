// Package hostsvc provides building blocks for the gRPC host service server
// that plugins call into. It handles call-ID propagation (spec §8.5) and the
// audit guard that rejects WriteAuditStep calls made outside a valid call scope.
//
// This package is a leaf: it imports internal/db and the plugin-sdk proto, but
// nothing from internal/execution or internal/mcp.
package hostsvc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// callIDCtxKey is the unexported context key used to store a gleipnir-call-id
// value extracted from gRPC incoming metadata by UnaryCallIDInterceptor.
type callIDCtxKey struct{}

// CallIDFromContext returns the gleipnir-call-id attached to ctx by
// UnaryCallIDInterceptor, along with a boolean indicating whether a valid ID
// is present.
//
// Returns ("", false) when:
//   - the interceptor found no call-ID in the incoming metadata, or
//   - the value was empty, or
//   - multiple values were present (which is suspicious and treated as absent).
func CallIDFromContext(ctx context.Context) (string, bool) {
	v := ctx.Value(callIDCtxKey{})
	if v == nil {
		return "", false
	}
	id, ok := v.(string)
	return id, ok
}

// UnaryCallIDInterceptor is a gRPC unary server interceptor that reads the
// gleipnir-call-id from incoming metadata and, when exactly one non-empty value
// is found, attaches it to the request context for downstream handlers.
//
// The interceptor never rejects calls on its own — a missing or ambiguous call
// ID simply means handlers will see ("", false) from CallIDFromContext. The
// WriteAuditStep handler is responsible for enforcing the call-scope requirement
// via RejectIfDetached.
//
// Spec reference: plugin-system-spec.md §8.5.
func UnaryCallIDInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if ok {
			vals := md.Get(sdkproto.CallIDMetadataKey)
			// Accept exactly one non-empty value. Multiple values are ambiguous
			// and we refuse to guess which one is authoritative.
			if len(vals) == 1 && vals[0] != "" {
				ctx = context.WithValue(ctx, callIDCtxKey{}, vals[0])
			}
		}
		return handler(ctx, req)
	}
}
