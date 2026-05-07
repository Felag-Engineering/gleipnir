package hostsvc_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// runInterceptor simulates a gRPC server by calling the interceptor with an
// incoming context and a no-op handler. It returns the context the handler
// received and the call error.
func runInterceptor(t *testing.T, incomingCtx context.Context) context.Context {
	t.Helper()

	interceptor := hostsvc.UnaryCallIDInterceptor()
	var handlerCtx context.Context

	_, err := interceptor(incomingCtx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
		handlerCtx = ctx
		return nil, nil
	})
	if err != nil {
		t.Fatalf("interceptor returned unexpected error: %v", err)
	}
	return handlerCtx
}

func TestUnaryCallIDInterceptor(t *testing.T) {
	t.Parallel()

	t.Run("single non-empty call ID is attached to handler context", func(t *testing.T) {
		md := metadata.Pairs(sdkproto.CallIDMetadataKey, "abc-123")
		ctx := metadata.NewIncomingContext(context.Background(), md)

		handlerCtx := runInterceptor(t, ctx)

		id, ok := hostsvc.CallIDFromContext(handlerCtx)
		if !ok || id != "abc-123" {
			t.Errorf("CallIDFromContext = (%q, %v), want (\"abc-123\", true)", id, ok)
		}
	})

	t.Run("no incoming metadata — call ID absent", func(t *testing.T) {
		handlerCtx := runInterceptor(t, context.Background())

		id, ok := hostsvc.CallIDFromContext(handlerCtx)
		if ok || id != "" {
			t.Errorf("CallIDFromContext = (%q, %v), want (\"\", false)", id, ok)
		}
	})

	t.Run("metadata present but key missing — call ID absent", func(t *testing.T) {
		md := metadata.Pairs("some-other-key", "val")
		ctx := metadata.NewIncomingContext(context.Background(), md)

		handlerCtx := runInterceptor(t, ctx)

		id, ok := hostsvc.CallIDFromContext(handlerCtx)
		if ok || id != "" {
			t.Errorf("CallIDFromContext = (%q, %v), want (\"\", false)", id, ok)
		}
	})

	t.Run("multiple values for call ID key — treated as absent", func(t *testing.T) {
		// metadata.Pairs interleaves keys and values; we need to set two separate
		// values for the same key. Build the MD manually.
		md := metadata.MD{
			sdkproto.CallIDMetadataKey: {"val-1", "val-2"},
		}
		ctx := metadata.NewIncomingContext(context.Background(), md)

		handlerCtx := runInterceptor(t, ctx)

		id, ok := hostsvc.CallIDFromContext(handlerCtx)
		if ok || id != "" {
			t.Errorf("CallIDFromContext = (%q, %v), want (\"\", false) for multiple values", id, ok)
		}
	})

	t.Run("empty-string value treated as absent", func(t *testing.T) {
		md := metadata.Pairs(sdkproto.CallIDMetadataKey, "")
		ctx := metadata.NewIncomingContext(context.Background(), md)

		handlerCtx := runInterceptor(t, ctx)

		id, ok := hostsvc.CallIDFromContext(handlerCtx)
		if ok || id != "" {
			t.Errorf("CallIDFromContext = (%q, %v), want (\"\", false) for empty-string value", id, ok)
		}
	})
}
