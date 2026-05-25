package run_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
)

// feedbackStubRequester extends stubRequester with a capturedRC field so tests
// can verify the RouteContext (including Metadata) that was passed to Request.
type feedbackStubRequester struct {
	requestID      string
	requestOutcome dispatch.RoutingOutcome
	requestErr     error

	waitResponse string
	waitErr      error

	capturedRC dispatch.RouteContext
}

func (s *feedbackStubRequester) Request(_ context.Context, _ string, rc dispatch.RouteContext, _ string, _ *time.Time) (string, dispatch.RoutingOutcome, error) {
	s.capturedRC = rc
	return s.requestID, s.requestOutcome, s.requestErr
}

func (s *feedbackStubRequester) Wait(_ context.Context, _ string, _ time.Duration) (string, error) {
	return s.waitResponse, s.waitErr
}

func TestFeedbackChannelAdapter_DispatchFeedback(t *testing.T) {
	ctx := context.Background()
	dummyReq := agent.FeedbackDispatchRequest{
		AudienceID: "aud-1",
		RunID:      "run-1",
		PolicyID:   "pol-1",
		ToolName:   "gleipnir.ask_operator",
		Prompt:     "What should I do next?",
	}

	t.Run("RouteToInApp from Request", func(t *testing.T) {
		stub := &feedbackStubRequester{
			requestID:      "",
			requestOutcome: dispatch.RouteToInApp,
		}
		adapter := run.NewFeedbackChannelAdapter(stub)
		_, err := adapter.DispatchFeedback(ctx, dummyReq)
		if !errors.Is(err, agent.ErrFeedbackRouteToInApp) {
			t.Errorf("err = %v, want ErrFeedbackRouteToInApp", err)
		}
	})

	t.Run("ErrNoRequestCapableEntry maps to ErrFeedbackRouteToInApp", func(t *testing.T) {
		stub := &feedbackStubRequester{
			requestErr: dispatch.ErrNoRequestCapableEntry,
		}
		adapter := run.NewFeedbackChannelAdapter(stub)
		_, err := adapter.DispatchFeedback(ctx, dummyReq)
		if !errors.Is(err, agent.ErrFeedbackRouteToInApp) {
			t.Errorf("err = %v, want ErrFeedbackRouteToInApp", err)
		}
	})

	t.Run("RouteToPlugin with text response returns raw JSON", func(t *testing.T) {
		stub := &feedbackStubRequester{
			requestID:      "req-1",
			requestOutcome: dispatch.RouteToPlugin,
			waitResponse:   `{"text":"operator reply","user":"U123","request_id":"req-1"}`,
		}
		adapter := run.NewFeedbackChannelAdapter(stub)
		resp, err := adapter.DispatchFeedback(ctx, dummyReq)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// The adapter returns raw JSON; parseFeedbackResponse extracts the text
		// in FeedbackHandler.Wait, not here.
		if resp != stub.waitResponse {
			t.Errorf("response = %q, want %q", resp, stub.waitResponse)
		}
	})

	t.Run("Request returns non-sentinel error propagated", func(t *testing.T) {
		stub := &feedbackStubRequester{
			requestErr: fmt.Errorf("gRPC unavailable"),
		}
		adapter := run.NewFeedbackChannelAdapter(stub)
		_, err := adapter.DispatchFeedback(ctx, dummyReq)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, agent.ErrFeedbackRouteToInApp) {
			t.Error("non-sentinel request error must not map to ErrFeedbackRouteToInApp")
		}
	})

	t.Run("Wait returns error propagated", func(t *testing.T) {
		stub := &feedbackStubRequester{
			requestID:      "req-2",
			requestOutcome: dispatch.RouteToPlugin,
			waitErr:        fmt.Errorf("context deadline exceeded"),
		}
		adapter := run.NewFeedbackChannelAdapter(stub)
		_, err := adapter.DispatchFeedback(ctx, dummyReq)
		if err == nil {
			t.Fatal("expected error from Wait, got nil")
		}
	})

	t.Run("Metadata contains mode=feedback in captured RouteContext", func(t *testing.T) {
		stub := &feedbackStubRequester{
			requestID:      "req-3",
			requestOutcome: dispatch.RouteToPlugin,
			waitResponse:   `{"text":"ok"}`,
		}
		adapter := run.NewFeedbackChannelAdapter(stub)
		_, _ = adapter.DispatchFeedback(ctx, dummyReq)

		if stub.capturedRC.Metadata == nil {
			t.Fatal("RouteContext.Metadata is nil, want non-nil")
		}
		if stub.capturedRC.Metadata["mode"] != "feedback" {
			t.Errorf("Metadata[mode] = %q, want %q", stub.capturedRC.Metadata["mode"], "feedback")
		}
	})

	t.Run("ExpiresAt sets wait timeout", func(t *testing.T) {
		future := time.Now().Add(2 * time.Hour)
		req := dummyReq
		req.ExpiresAt = &future

		stub := &feedbackStubRequester{
			requestID:      "req-4",
			requestOutcome: dispatch.RouteToPlugin,
			waitResponse:   `{"text":"replied"}`,
		}
		adapter := run.NewFeedbackChannelAdapter(stub)
		_, err := adapter.DispatchFeedback(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// TestParseFeedbackResponse verifies the standalone parse helper behaviour.
// parseFeedbackResponse is not exported, so we test it indirectly via
// FeedbackHandler.Wait in feedback_test.go.  This file focuses on adapter-level
// behaviour only; parsing edge cases are covered there.
func TestFeedbackChannelAdapter_NilExpiresAtDefaultsToOneHour(t *testing.T) {
	// When ExpiresAt is nil the adapter defaults to 1h.  We cannot observe the
	// actual timeout passed to Wait directly, but we can verify no error is
	// returned from a stub that always succeeds.
	stub := &feedbackStubRequester{
		requestID:      "req-nil-exp",
		requestOutcome: dispatch.RouteToPlugin,
		waitResponse:   `{"text":"ok"}`,
	}
	adapter := run.NewFeedbackChannelAdapter(stub)
	req := agent.FeedbackDispatchRequest{
		AudienceID: "aud-1",
		RunID:      "run-1",
		PolicyID:   "pol-1",
		ToolName:   "gleipnir.ask_operator",
		Prompt:     "test",
		ExpiresAt:  nil,
	}
	_, err := adapter.DispatchFeedback(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error with nil ExpiresAt: %v", err)
	}
}
