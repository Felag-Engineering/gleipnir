package hostsvc_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
	"google.golang.org/grpc/metadata"
)

// ctxWithInstanceID builds a context that carries the instance ID as if
// UnaryInstanceTokenInterceptor had already resolved it.
func ctxWithInstanceID(instanceID string) context.Context {
	// We set the value directly via the public interface (round-trip through
	// a real token registry) so that the test exercises the same context key
	// that production uses.
	reg := identity.New()
	token, err := reg.Issue(instanceID)
	if err != nil {
		panic("ctxWithInstanceID: Issue: " + err.Error())
	}
	md := metadata.Pairs(sdkproto.InstanceTokenMetadataKey, token)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	// Run the token interceptor to attach the resolved instance ID.
	interceptor := hostsvc.UnaryInstanceTokenInterceptor(reg)
	var innerCtx context.Context
	_, _ = interceptor(ctx, nil, nil, func(c context.Context, _ any) (any, error) {
		innerCtx = c
		return nil, nil
	})
	return innerCtx
}

// runGenerationInterceptor runs UnaryGenerationRefcountInterceptor and returns
// the error it produces. handler is the fake handler the interceptor delegates to.
func runGenerationInterceptor(
	c *generation.Controller,
	ctx context.Context,
	handler func(context.Context, any) (any, error),
) error {
	interceptor := hostsvc.UnaryGenerationRefcountInterceptor(c)
	_, err := interceptor(ctx, nil, nil, handler)
	return err
}

// TestGenerationInterceptor_HappyPath verifies that a normal RPC acquires a
// refcount, passes a live ctx to the handler, and releases cleanly on return.
func TestGenerationInterceptor_HappyPath(t *testing.T) {
	t.Parallel()

	c := generation.New()
	c.RegisterInstance("inst-gp")

	ctx := ctxWithInstanceID("inst-gp")
	var handlerCtx context.Context
	var ctxErrDuringCall error

	err := runGenerationInterceptor(c, ctx, func(hctx context.Context, _ any) (any, error) {
		handlerCtx = hctx
		// The handler must run under a live (un-cancelled) ctx. Capture the error
		// here, during the call — not after the interceptor returns, because the
		// deferred release() cancels the derived ctx on the way out (issue #498).
		ctxErrDuringCall = hctx.Err()
		return nil, nil
	})
	if err != nil {
		t.Fatalf("expected nil error from interceptor, got: %v", err)
	}
	if handlerCtx == nil {
		t.Fatal("handler was not called")
	}
	if ctxErrDuringCall != nil {
		t.Fatalf("handler ctx was already cancelled during the call: %v", ctxErrDuringCall)
	}
	// After the interceptor returns, release() has run and cancels the derived
	// context — that is the issue #498 fix (release must not leak the cancel
	// func). The ctx being Canceled here is the fix working, not a force-cancel.
	if handlerCtx.Err() != context.Canceled {
		t.Fatalf("handler ctx should be Canceled after release() returned; got: %v", handlerCtx.Err())
	}

	// After the interceptor returns the release func must have been called, so
	// a subsequent BeginDrain with no in-flight calls must drain immediately.
	_, drained, drainErr := c.BeginDrain(context.Background(), "inst-gp", time.Second)
	if drainErr != nil {
		t.Fatalf("BeginDrain: %v", drainErr)
	}
	if !drained {
		t.Fatal("expected drained=true (refcount should be zero after interceptor returned)")
	}
}

// TestGenerationInterceptor_ForceCancelPropagation verifies that when
// BeginDrain force-cancels an in-flight call (grace=0), the handler's ctx
// becomes Done and the interceptor returns the handler's error (not Unavailable).
func TestGenerationInterceptor_ForceCancelPropagation(t *testing.T) {
	t.Parallel()

	c := generation.New()
	c.RegisterInstance("inst-fc")

	ctx := ctxWithInstanceID("inst-fc")

	// handlerBlocked lets the test synchronise: handler blocks until its ctx is
	// cancelled, then returns.
	handlerStarted := make(chan struct{})
	handlerDone := make(chan error, 1)

	go func() {
		err := runGenerationInterceptor(c, ctx, func(hctx context.Context, _ any) (any, error) {
			close(handlerStarted)
			// Block until force-cancel.
			<-hctx.Done()
			return nil, hctx.Err()
		})
		handlerDone <- err
	}()

	// Wait for the handler to be inside the interceptor.
	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}

	// Drain with zero grace → immediate force-cancel.
	_, _, err := c.BeginDrain(context.Background(), "inst-fc", 0)
	if err != nil {
		t.Fatalf("BeginDrain: %v", err)
	}

	// The interceptor must return the handler's own error (context.Canceled),
	// not codes.Unavailable — force-cancel is runtime cancellation, not a
	// pause-rejection.
	select {
	case handlerErr := <-handlerDone:
		if handlerErr == nil {
			t.Fatal("expected non-nil error from interceptor after force-cancel")
		}
		// The error should be context.Canceled, not Unavailable.
		if s, ok := status.FromError(handlerErr); ok && s.Code() == codes.Unavailable {
			t.Fatalf("interceptor returned Unavailable for a force-cancelled in-flight call; want context.Canceled")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("interceptor did not return after force-cancel")
	}
}

// TestGenerationInterceptor_PauseDeadline verifies that when the instance is
// paused (drain in progress) and a new RPC's own context expires before the
// drain completes, the interceptor returns codes.Unavailable.
func TestGenerationInterceptor_PauseDeadline(t *testing.T) {
	t.Parallel()

	c := generation.New()
	c.RegisterInstance("inst-pd")

	// Hold a refcount to keep BeginDrain blocked.
	holdCtx := ctxWithInstanceID("inst-pd")
	holdStarted := make(chan struct{})
	holdRelease := make(chan struct{})

	go func() {
		runGenerationInterceptor(c, holdCtx, func(hctx context.Context, _ any) (any, error) { //nolint:errcheck
			close(holdStarted)
			<-holdRelease
			return nil, nil
		})
	}()

	<-holdStarted

	// Start BeginDrain in the background — it will pause traffic.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		c.BeginDrain(context.Background(), "inst-pd", 10*time.Second) //nolint:errcheck
	}()

	// Give BeginDrain a moment to enter the paused state.
	time.Sleep(20 * time.Millisecond)

	// A new RPC arrives with a context that expires quickly.
	shortCtx, cancel := context.WithTimeout(ctxWithInstanceID("inst-pd"), 50*time.Millisecond)
	defer cancel()

	err := runGenerationInterceptor(c, shortCtx, func(_ context.Context, _ any) (any, error) {
		t.Error("handler must not be called: new RPC should have been rejected")
		return nil, nil
	})

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected codes.Unavailable while paused + deadline exceeded, got %v", err)
	}

	// Release the held call so BeginDrain can finish and the test can exit.
	close(holdRelease)
	select {
	case <-drainDone:
	case <-time.After(15 * time.Second):
		t.Fatal("BeginDrain did not complete after holdRelease")
	}
}

// TestGenerationInterceptor_MissingInstanceID verifies that when no instance ID
// is present in the context (i.e. the token interceptor didn't run), the
// generation interceptor passes the request through unchanged without error.
func TestGenerationInterceptor_MissingInstanceID(t *testing.T) {
	t.Parallel()

	c := generation.New()
	// No instances registered — and the ctx has no instance ID.

	handlerCalled := false
	err := runGenerationInterceptor(c, context.Background(), func(_ context.Context, _ any) (any, error) {
		handlerCalled = true
		return nil, nil
	})

	if err != nil {
		t.Fatalf("interceptor returned error for missing instance ID: %v", err)
	}
	if !handlerCalled {
		t.Fatal("handler was not called when instance ID was missing (should pass through)")
	}
}
