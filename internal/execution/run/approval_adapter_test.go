package run_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
)

// TestParseApprovalDecision is a table-driven test for the exported
// ParseApprovalDecision helper.  It is in the external test package so the
// helper must be exported — see plan §6.
func TestParseApprovalDecision(t *testing.T) {
	cases := []struct {
		name         string
		input        string
		wantApproved bool
		wantErrText  string // non-empty means we expect an error containing this string
	}{
		{
			name:         "decision approved",
			input:        `{"decision":"approved"}`,
			wantApproved: true,
		},
		{
			name:         "decision denied",
			input:        `{"decision":"denied"}`,
			wantApproved: false,
		},
		{
			name:         "slack option_id approve",
			input:        `{"option_id":"approve","value":"approve","request_id":"req-1","user":"U123"}`,
			wantApproved: true,
		},
		{
			name:         "slack option_id reject",
			input:        `{"option_id":"reject","value":"reject","request_id":"req-1","user":"U123"}`,
			wantApproved: false,
		},
		{
			name:        "unknown option_id",
			input:       `{"option_id":"maybe"}`,
			wantErrText: "unknown",
		},
		{
			name:        "unknown decision",
			input:       `{"decision":"unknown"}`,
			wantErrText: "unknown",
		},
		{
			name:        "empty body",
			input:       `{}`,
			wantErrText: "missing both",
		},
		{
			name:        "invalid json",
			input:       `invalid json`,
			wantErrText: "invalid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			approved, err := run.ParseApprovalDecision(tc.input)
			if tc.wantErrText != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrText)
				}
				if !strings.Contains(err.Error(), tc.wantErrText) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tc.wantErrText)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if approved != tc.wantApproved {
				t.Errorf("approved = %v, want %v", approved, tc.wantApproved)
			}
		})
	}
}

// stubRequester is a mock for the approvalChannelRequester interface used by
// NewApprovalChannelAdapter.  Tests configure return values per-call.
type stubRequester struct {
	requestID      string
	requestOutcome dispatch.RoutingOutcome
	requestErr     error

	waitResponse string
	waitErr      error
}

func (s *stubRequester) Request(_ context.Context, _ string, _ dispatch.RouteContext, _ string, _ *time.Time) (string, dispatch.RoutingOutcome, error) {
	return s.requestID, s.requestOutcome, s.requestErr
}

func (s *stubRequester) Wait(_ context.Context, _ string, _ time.Duration) (string, error) {
	return s.waitResponse, s.waitErr
}

func TestApprovalChannelAdapter_DispatchApproval(t *testing.T) {
	ctx := context.Background()
	dummyReq := agent.ApprovalDispatchRequest{
		AudienceID: "aud-1",
		RunID:      "run-1",
		PolicyID:   "pol-1",
		ToolName:   "some.tool",
		Prompt:     "Approve?",
	}

	t.Run("RouteToInApp from Request", func(t *testing.T) {
		stub := &stubRequester{
			requestID:      "",
			requestOutcome: dispatch.RouteToInApp,
		}
		adapter := run.NewApprovalChannelAdapter(stub)
		approved, err := adapter.DispatchApproval(ctx, dummyReq)
		if !errors.Is(err, agent.ErrApprovalRouteToInApp) {
			t.Errorf("err = %v, want ErrApprovalRouteToInApp", err)
		}
		if approved {
			t.Error("approved must be false on in-app route")
		}
	})

	t.Run("ErrNoRequestCapableEntry maps to ErrApprovalRouteToInApp", func(t *testing.T) {
		stub := &stubRequester{
			requestErr: dispatch.ErrNoRequestCapableEntry,
		}
		adapter := run.NewApprovalChannelAdapter(stub)
		approved, err := adapter.DispatchApproval(ctx, dummyReq)
		if !errors.Is(err, agent.ErrApprovalRouteToInApp) {
			t.Errorf("err = %v, want ErrApprovalRouteToInApp", err)
		}
		if approved {
			t.Error("approved must be false on no-entry route")
		}
	})

	t.Run("RouteToPlugin + approved response", func(t *testing.T) {
		stub := &stubRequester{
			requestID:      "req-1",
			requestOutcome: dispatch.RouteToPlugin,
			waitResponse:   `{"decision":"approved"}`,
		}
		adapter := run.NewApprovalChannelAdapter(stub)
		approved, err := adapter.DispatchApproval(ctx, dummyReq)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !approved {
			t.Error("expected approved=true")
		}
	})

	t.Run("RouteToPlugin + denied response", func(t *testing.T) {
		stub := &stubRequester{
			requestID:      "req-2",
			requestOutcome: dispatch.RouteToPlugin,
			waitResponse:   `{"decision":"denied"}`,
		}
		adapter := run.NewApprovalChannelAdapter(stub)
		approved, err := adapter.DispatchApproval(ctx, dummyReq)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if approved {
			t.Error("expected approved=false on denied response")
		}
	})

	t.Run("Request returns non-sentinel error", func(t *testing.T) {
		stub := &stubRequester{
			requestErr: fmt.Errorf("gRPC unavailable"),
		}
		adapter := run.NewApprovalChannelAdapter(stub)
		_, err := adapter.DispatchApproval(ctx, dummyReq)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, agent.ErrApprovalRouteToInApp) {
			t.Error("non-sentinel request error must not map to ErrApprovalRouteToInApp")
		}
	})

	t.Run("Wait returns error", func(t *testing.T) {
		stub := &stubRequester{
			requestID:      "req-3",
			requestOutcome: dispatch.RouteToPlugin,
			waitErr:        fmt.Errorf("context deadline exceeded"),
		}
		adapter := run.NewApprovalChannelAdapter(stub)
		_, err := adapter.DispatchApproval(ctx, dummyReq)
		if err == nil {
			t.Fatal("expected error from Wait, got nil")
		}
	})
}
