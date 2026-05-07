package serve_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"

	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

// incomingCtx builds a context that looks like the server side of a gRPC call
// with the given key/value pairs in the incoming metadata.
func incomingCtx(kv ...string) context.Context {
	md := metadata.Pairs(kv...)
	return metadata.NewIncomingContext(context.Background(), md)
}

// outgoingVals returns all values stored in the outgoing metadata for key.
func outgoingVals(ctx context.Context, key string) []string {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return nil
	}
	return md.Get(key)
}

func TestWithCallContext(t *testing.T) {
	t.Parallel()

	t.Run("inbound MD with call ID propagates to outgoing and context", func(t *testing.T) {
		ctx := incomingCtx(sdkproto.CallIDMetadataKey, "call-abc-123")
		ctx = serve.WithCallContext(ctx)

		// Outgoing metadata must carry the call ID.
		vals := outgoingVals(ctx, sdkproto.CallIDMetadataKey)
		if len(vals) == 0 || vals[0] != "call-abc-123" {
			t.Errorf("outgoing metadata[%s] = %v, want [call-abc-123]", sdkproto.CallIDMetadataKey, vals)
		}

		// CallIDFromContext must surface the value.
		id, ok := serve.CallIDFromContext(ctx)
		if !ok || id != "call-abc-123" {
			t.Errorf("CallIDFromContext = (%q, %v), want (\"call-abc-123\", true)", id, ok)
		}
	})

	t.Run("no inbound MD returns context unchanged", func(t *testing.T) {
		ctx := serve.WithCallContext(context.Background())

		vals := outgoingVals(ctx, sdkproto.CallIDMetadataKey)
		if len(vals) != 0 {
			t.Errorf("outgoing metadata[%s] = %v, want empty", sdkproto.CallIDMetadataKey, vals)
		}

		id, ok := serve.CallIDFromContext(ctx)
		if ok || id != "" {
			t.Errorf("CallIDFromContext = (%q, %v), want (\"\", false)", id, ok)
		}
	})

	t.Run("empty-string value treated as absent", func(t *testing.T) {
		ctx := incomingCtx(sdkproto.CallIDMetadataKey, "")
		ctx = serve.WithCallContext(ctx)

		vals := outgoingVals(ctx, sdkproto.CallIDMetadataKey)
		if len(vals) != 0 {
			t.Errorf("outgoing metadata[%s] = %v, want empty for empty-string call ID", sdkproto.CallIDMetadataKey, vals)
		}

		id, ok := serve.CallIDFromContext(ctx)
		if ok || id != "" {
			t.Errorf("CallIDFromContext = (%q, %v), want (\"\", false) for empty-string call ID", id, ok)
		}
	})
}

func TestDetachContext(t *testing.T) {
	t.Parallel()

	t.Run("DetachContext after WithCallContext strips call ID and hides it from CallIDFromContext", func(t *testing.T) {
		ctx := incomingCtx(sdkproto.CallIDMetadataKey, "call-xyz-999")
		ctx = serve.WithCallContext(ctx)

		// Confirm we have a call ID before detaching.
		if _, ok := serve.CallIDFromContext(ctx); !ok {
			t.Fatal("precondition failed: WithCallContext did not store call ID")
		}

		ctx = serve.DetachContext(ctx)

		// Outgoing metadata must not carry the call ID.
		vals := outgoingVals(ctx, sdkproto.CallIDMetadataKey)
		if len(vals) != 0 {
			t.Errorf("outgoing metadata[%s] = %v after DetachContext, want empty", sdkproto.CallIDMetadataKey, vals)
		}

		// CallIDFromContext must return ("", false) — detached sentinel wins.
		id, ok := serve.CallIDFromContext(ctx)
		if ok || id != "" {
			t.Errorf("CallIDFromContext = (%q, %v) after DetachContext, want (\"\", false)", id, ok)
		}
	})
}
