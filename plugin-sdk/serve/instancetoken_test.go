package serve_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

// outgoingTokenVals returns all values for InstanceTokenMetadataKey in the
// outgoing metadata of ctx.
func outgoingTokenVals(ctx context.Context) []string {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return nil
	}
	return md.Get(sdkproto.InstanceTokenMetadataKey)
}

// captureInvoker is a fake grpc.UnaryInvoker that records the context it was
// called with and the method name. It does not make a real RPC.
type captureInvoker struct {
	ctx    context.Context
	method string
	called bool
}

func (c *captureInvoker) invoke(ctx context.Context, method string, _ any, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
	c.ctx = ctx
	c.method = method
	c.called = true
	return nil
}

func TestTokenInterceptor_AddsMetadata(t *testing.T) {
	t.Parallel()

	const token = "test-token-abc123"
	interceptor := serve.TokenInterceptor(token)

	cap := &captureInvoker{}
	err := interceptor(context.Background(), "/svc/Method", nil, nil, nil, cap.invoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cap.called {
		t.Fatal("invoker was not called")
	}

	vals := outgoingTokenVals(cap.ctx)
	if len(vals) != 1 || vals[0] != token {
		t.Errorf("outgoing metadata[%s] = %v, want [%q]", sdkproto.InstanceTokenMetadataKey, vals, token)
	}
}

func TestTokenInterceptor_EmptyTokenIsPassThrough(t *testing.T) {
	t.Parallel()

	// Empty token must not add any metadata key at all.
	interceptor := serve.TokenInterceptor("")

	cap := &captureInvoker{}
	err := interceptor(context.Background(), "/svc/Method", nil, nil, nil, cap.invoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cap.called {
		t.Fatal("invoker was not called")
	}

	vals := outgoingTokenVals(cap.ctx)
	if len(vals) != 0 {
		t.Errorf("outgoing metadata[%s] = %v, want empty for empty token", sdkproto.InstanceTokenMetadataKey, vals)
	}
}
