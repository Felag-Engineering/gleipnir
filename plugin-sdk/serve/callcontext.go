package serve

import (
	"context"

	"google.golang.org/grpc/metadata"

	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// callIDKey is the context key for the call ID value stored by WithCallContext.
type callIDKey struct{}

// detachedKey is the context key used as a sentinel by DetachContext. Its
// presence means "no call ID should be surfaced from this context", even if a
// callIDKey value is also present.
type detachedKey struct{}

// WithCallContext reads the gleipnir-call-id from the incoming gRPC metadata
// and, if present and non-empty, propagates it in two ways:
//
//  1. It appends the value to the outgoing metadata so that host RPCs (Log,
//     EmitMetric, etc.) carry the same call ID back.
//  2. It stores the value under an unexported context key so that background
//     goroutines that inherit the context can retrieve it via CallIDFromContext.
//
// If no gleipnir-call-id is found in the incoming metadata, the context is
// returned unchanged.
//
// Spec reference: plugin-system-spec.md §8.5.
// See also: sdkproto.CallIDMetadataKey.
func WithCallContext(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}

	vals := md.Get(sdkproto.CallIDMetadataKey)
	if len(vals) == 0 || vals[0] == "" {
		return ctx
	}

	callID := vals[0]

	// Attach to outgoing metadata so every host RPC made from this context
	// carries the call ID without the plugin author needing to thread it manually.
	ctx = metadata.AppendToOutgoingContext(ctx, sdkproto.CallIDMetadataKey, callID)

	// Also store under the private key so background goroutines can retrieve it
	// via CallIDFromContext without going through gRPC metadata.
	ctx = context.WithValue(ctx, callIDKey{}, callID)
	return ctx
}

// DetachContext marks a context as call-detached. Any host RPC made with the
// returned context will not carry a gleipnir-call-id, and CallIDFromContext
// will return ("", false) even if the parent context had a call ID.
//
// Plugin authors should use this when spawning background goroutines that
// outlive a single tool Call invocation. The detached-context sentinel takes
// precedence over any call ID stored by WithCallContext — the guard is intentional
// and irrevocable on the returned context.
//
// Spec reference: plugin-system-spec.md §8.5.
func DetachContext(ctx context.Context) context.Context {
	// Copy the outgoing metadata without the call ID key so the host cannot be
	// fooled by a stale ID from an earlier call that happened to flow through.
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		stripped := md.Copy()
		stripped.Delete(sdkproto.CallIDMetadataKey)
		ctx = metadata.NewOutgoingContext(ctx, stripped)
	}

	// Set the sentinel so CallIDFromContext returns ("", false) unconditionally.
	ctx = context.WithValue(ctx, detachedKey{}, true)
	return ctx
}

// CallIDFromContext returns the gleipnir-call-id stored on ctx by WithCallContext,
// along with a boolean indicating whether a call ID is present.
//
// The detached-context sentinel (set by DetachContext) takes priority: if ctx
// was produced by DetachContext, this function always returns ("", false).
func CallIDFromContext(ctx context.Context) (string, bool) {
	// Detached sentinel wins — once a context is marked detached, no call ID
	// should be surfaced, even if one was stored earlier.
	if ctx.Value(detachedKey{}) != nil {
		return "", false
	}

	v := ctx.Value(callIDKey{})
	if v == nil {
		return "", false
	}
	id, ok := v.(string)
	return id, ok
}
