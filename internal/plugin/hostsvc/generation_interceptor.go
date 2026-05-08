package hostsvc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
)

// UnaryGenerationRefcountInterceptor returns a grpc.UnaryServerInterceptor
// that, for each incoming Host RPC:
//   - Looks up the instance ID from the context (set by
//     UnaryInstanceTokenInterceptor — chain order: token FIRST, then this
//     interceptor).
//   - Calls controller.Acquire with the request ctx; the handler executes
//     under the wrapped (cancellable) ctx.
//   - Defers release().
//
// Behaviour while a hot-reload drain is in progress:
//   - In-flight RPCs continue under the cancellable ctx and are
//     force-cancelled (ctx.Done) only after the grace period elapses.
//   - NEW RPCs arriving during the pause window block in Acquire until the
//     new generation is current, OR return codes.Unavailable if their own
//     request ctx expires first.
//
// (i.e. "pauses traffic" is short-hand: in-flight = cancellable; new =
// Unavailable on deadline.)
//
// If the instance ID is absent from ctx (the token interceptor would have
// rejected in production), the request passes through untouched so the auth
// layer's error is not masked.
//
// See issue #294, spec §8.4 + §13.8.
func UnaryGenerationRefcountInterceptor(c *generation.Controller) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		instanceID, ok := InstanceIDFromTokenContext(ctx)
		if !ok {
			// No instance ID in context — the token interceptor must not have run
			// (or rejected). Pass through so the auth error is surfaced normally.
			return handler(ctx, req)
		}

		wrappedCtx, release, _, err := c.Acquire(ctx, instanceID)
		if err != nil {
			// Acquire fails when the instance is unregistered or the request ctx
			// expired while waiting for a drain to complete.
			return nil, status.Error(codes.Unavailable, "plugin generation draining")
		}
		defer release()

		return handler(wrappedCtx, req)
	}
}
