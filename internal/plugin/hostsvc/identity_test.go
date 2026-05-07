package hostsvc_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// incomingCtxWithToken builds a context that looks like the server side of a
// gRPC call with the given instance token in the incoming metadata.
func incomingCtxWithToken(token string) context.Context {
	md := metadata.Pairs(sdkproto.InstanceTokenMetadataKey, token)
	return metadata.NewIncomingContext(context.Background(), md)
}

// runTokenInterceptor runs UnaryInstanceTokenInterceptor against a context and
// captures the context seen by the handler.
func runTokenInterceptor(reg *identity.Registry, ctx context.Context) (handlerCtx context.Context, grpcErr error) {
	interceptor := hostsvc.UnaryInstanceTokenInterceptor(reg)
	_, grpcErr = interceptor(ctx, nil, nil, func(c context.Context, _ any) (any, error) {
		handlerCtx = c
		return nil, nil
	})
	return handlerCtx, grpcErr
}

func TestUnaryInstanceTokenInterceptor_PassesValidToken(t *testing.T) {
	t.Parallel()

	reg := identity.New()
	token, err := reg.Issue("inst-valid")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	ctx := incomingCtxWithToken(token)
	handlerCtx, grpcErr := runTokenInterceptor(reg, ctx)
	if grpcErr != nil {
		t.Fatalf("expected nil error for valid token, got: %v", grpcErr)
	}

	gotID, ok := hostsvc.InstanceIDFromTokenContext(handlerCtx)
	if !ok {
		t.Fatal("InstanceIDFromTokenContext returned ok=false after valid token")
	}
	if gotID != "inst-valid" {
		t.Errorf("instance ID = %q, want inst-valid", gotID)
	}
}

func TestUnaryInstanceTokenInterceptor_RejectsMissingToken(t *testing.T) {
	t.Parallel()

	reg := identity.New()

	// No metadata at all.
	_, grpcErr := runTokenInterceptor(reg, context.Background())
	if grpcErr == nil {
		t.Fatal("expected Unauthenticated error, got nil")
	}
	st, ok := status.FromError(grpcErr)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", grpcErr)
	}
}

func TestUnaryInstanceTokenInterceptor_RejectsUnknownToken(t *testing.T) {
	t.Parallel()

	reg := identity.New()

	ctx := incomingCtxWithToken("bogus-token-that-was-never-issued")
	_, grpcErr := runTokenInterceptor(reg, ctx)
	if grpcErr == nil {
		t.Fatal("expected Unauthenticated error, got nil")
	}
	st, ok := status.FromError(grpcErr)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", grpcErr)
	}
}

func TestUnaryInstanceTokenInterceptor_RejectsRevokedToken(t *testing.T) {
	t.Parallel()

	reg := identity.New()

	// Issue T1 for instance-revoke.
	t1, err := reg.Issue("instance-revoke")
	if err != nil {
		t.Fatalf("Issue t1: %v", err)
	}

	// Issue T2 for the same instance — this auto-revokes T1.
	_, err = reg.Issue("instance-revoke")
	if err != nil {
		t.Fatalf("Issue t2: %v", err)
	}

	// T1 must now be rejected.
	ctx := incomingCtxWithToken(t1)
	_, grpcErr := runTokenInterceptor(reg, ctx)
	if grpcErr == nil {
		t.Fatal("expected Unauthenticated for revoked token, got nil")
	}
	st, ok := status.FromError(grpcErr)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated for revoked token, got %v", grpcErr)
	}
}
