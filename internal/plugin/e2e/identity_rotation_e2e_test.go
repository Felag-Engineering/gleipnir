package e2e_test

// identity_rotation_e2e_test.go exercises the identity-token rotation guarantee
// over a real gRPC wire with the production interceptor chain.
//
// Coverage gap this fills: internal/plugin/hostsvc/process_token_integration_test.go
// (TestProcessTokenIntegration_OldTokenRejectedAfterReissue) covers rotation by
// spawning a real subprocess, but it calls UnaryInstanceTokenInterceptor
// in-process with a fabricated INCOMING context and does NOT assert that the
// handler did not run on stale-token rejection. This test:
//  1. Sends tokens through a real client→server gRPC call (outgoing metadata →
//     wire → server-side incoming metadata), confirming the rejection is a real
//     gRPC error, not just an in-process interceptor return value.
//  2. Uses an atomic counter inside the stub handler to assert that the handler
//     was NOT invoked when the stale token was presented (issue #508 AC line 47).

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// TestHostRPC_IdentityRotation_StaleTokenRejected_FreshAccepted verifies the
// per-generation token rotation guarantee end-to-end over a live gRPC connection:
//
//  1. An instance token works immediately after being issued (sanity baseline).
//  2. Re-issuing a new token auto-revokes the old one (registry.go:74-79).
//  3. A Host RPC carrying the stale (revoked) token is rejected with
//     codes.Unauthenticated and the handler does NOT run.
//  4. A Host RPC carrying the fresh token is accepted and the handler runs.
//
// Synchronization: all assertions are on the synchronous gRPC return value and
// an atomic counter. No wall-clock sleeps are needed.
func TestHostRPC_IdentityRotation_StaleTokenRejected_FreshAccepted(t *testing.T) {
	t.Parallel()

	const instanceID = "e2e-identity-rotation-inst"

	reg := identity.New()
	ctrl := generation.New()
	ctrl.RegisterInstance(instanceID)

	// handlerCalls counts how many times the stub's GetRunContext body runs.
	// We assert the count does NOT increase when a stale token is presented
	// (the interceptor must short-circuit before the handler is invoked).
	var handlerCalls atomic.Int64

	stub := &stubHostServer{
		onCall: func(_ context.Context) {
			handlerCalls.Add(1)
		},
	}

	client, stop := startHostServer(t, reg, ctrl, stub)
	defer stop()

	// Issue the first token and verify it works (sanity baseline).
	oldToken, err := reg.Issue(instanceID)
	if err != nil {
		t.Fatalf("Issue (old): %v", err)
	}

	ctx1 := callCtxWithToken(oldToken)
	if _, err := client.GetRunContext(ctx1, &hostv1.GetRunContextRequest{}); err != nil {
		t.Fatalf("old token should be accepted before rotation; got: %v", err)
	}
	if handlerCalls.Load() != 1 {
		t.Fatalf("handler call count after old-token call: want 1, got %d", handlerCalls.Load())
	}

	// Re-issue a new token. This atomically revokes oldToken inside the registry
	// (identity/registry.go:74-79).
	newToken, err := reg.Issue(instanceID)
	if err != nil {
		t.Fatalf("Issue (new): %v", err)
	}

	// --- Assert: stale token is rejected ---
	//
	// Record the handler call count before the stale-token attempt; it must not
	// increase, proving rejection happened inside the interceptor before the
	// handler ran.
	countBefore := handlerCalls.Load()

	staleCtx := callCtxWithToken(oldToken)
	_, staleErr := client.GetRunContext(staleCtx, &hostv1.GetRunContextRequest{})
	if staleErr == nil {
		t.Fatal("stale token should be rejected but RPC succeeded")
	}
	st, ok := status.FromError(staleErr)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("stale token rejection: want codes.Unauthenticated, got %v", staleErr)
	}
	if got := handlerCalls.Load(); got != countBefore {
		t.Errorf("handler ran on stale-token call: count before=%d after=%d (interceptor must reject before handler)", countBefore, got)
	}

	// --- Assert: fresh token is accepted ---
	freshCtx := callCtxWithToken(newToken)
	if _, err := client.GetRunContext(freshCtx, &hostv1.GetRunContextRequest{}); err != nil {
		t.Errorf("fresh token should be accepted after rotation; got: %v", err)
	}
	if got := handlerCalls.Load(); got != countBefore+1 {
		t.Errorf("handler call count after fresh-token call: want %d, got %d", countBefore+1, got)
	}
}
