package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// routeEventRecorder is a GateRouteRecorder that publishes every route change
// on a channel, so a test synchronizes on the change instead of polling.
type routeEventRecorder struct {
	events chan GateRoute
}

func newRouteEventRecorder() *routeEventRecorder {
	return &routeEventRecorder{events: make(chan GateRoute, 16)}
}

func (r *routeEventRecorder) RecordGateRoute(_ string, route GateRoute) { r.events <- route }
func (r *routeEventRecorder) ClearGateRoute(_ string)                   { r.events <- GateRoute{} }

func (r *routeEventRecorder) next(t *testing.T) GateRoute {
	t.Helper()
	select {
	case route := <-r.events:
		return route
	case <-time.After(30 * time.Second):
		t.Fatal("no gate route change recorded")
		return GateRoute{}
	}
}

// drain returns every route change recorded so far.
func (r *routeEventRecorder) drain() []GateRoute {
	var out []GateRoute
	for {
		select {
		case route := <-r.events:
			out = append(out, route)
		default:
			return out
		}
	}
}

func assertRouteSequence(t *testing.T, got, want []GateRoute) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("route changes = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route change %d = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
}

type funcApprovalDispatcher func(ctx context.Context, req ApprovalDispatchRequest) (ApprovalSettlement, error)

func (f funcApprovalDispatcher) DispatchApproval(ctx context.Context, req ApprovalDispatchRequest) (ApprovalSettlement, error) {
	return f(ctx, req)
}

type funcFeedbackDispatcher func(ctx context.Context, req FeedbackDispatchRequest) (FeedbackSettlement, error)

func (f funcFeedbackDispatcher) DispatchFeedback(ctx context.Context, req FeedbackDispatchRequest) (FeedbackSettlement, error) {
	return f(ctx, req)
}

func newGateRouteApprovalHandler(t *testing.T, d ApprovalChannelDispatcher, rec GateRouteRecorder, approvalCh chan bool) *ApprovalHandler {
	t.Helper()
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { w.Close() }) //nolint:errcheck
	return NewApprovalHandler(w, sm, (<-chan bool)(approvalCh),
		WithApprovalChannelDispatch(d, "audience-1", "p1"),
		WithApprovalGateRoutes(rec),
	)
}

func TestApprovalHandler_Wait_GateRoute(t *testing.T) {
	pluginPending := GateRoute{Plugin: true}
	pluginOnSlack := GateRoute{Plugin: true, InstanceID: "inst-slack"}

	tests := []struct {
		name     string
		dispatch funcApprovalDispatcher
		wantErr  bool
		want     []GateRoute
	}{
		{
			name: "plugin answers",
			dispatch: func(_ context.Context, req ApprovalDispatchRequest) (ApprovalSettlement, error) {
				req.OnPluginRoute("inst-slack")
				return ApprovalSettlement{Approved: true}, nil
			},
			want: []GateRoute{pluginPending, pluginOnSlack, {}},
		},
		{
			name: "dispatch fails after routing to a plugin",
			dispatch: func(_ context.Context, req ApprovalDispatchRequest) (ApprovalSettlement, error) {
				req.OnPluginRoute("inst-slack")
				return ApprovalSettlement{}, errors.New("plugin unreachable")
			},
			wantErr: true,
			want:    []GateRoute{pluginPending, pluginOnSlack, {}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRouteEventRecorder()
			h := newGateRouteApprovalHandler(t, tt.dispatch, rec, make(chan bool, 1))

			err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Wait error = %v, wantErr %v", err, tt.wantErr)
			}
			assertRouteSequence(t, rec.drain(), tt.want)
		})
	}
}

// A request that falls back to in-app must stop being plugin-owned while the
// in-app wait is still open, or the UI could never answer it.
func TestApprovalHandler_Wait_GateRoute_InAppFallbackIsAnswerableWhileWaiting(t *testing.T) {
	rec := newRouteEventRecorder()
	dispatcher := funcApprovalDispatcher(func(context.Context, ApprovalDispatchRequest) (ApprovalSettlement, error) {
		return ApprovalSettlement{}, ErrApprovalRouteToInApp
	})
	approvalCh := make(chan bool, 1)
	h := newGateRouteApprovalHandler(t, dispatcher, rec, approvalCh)

	done := make(chan error, 1)
	go func() {
		done <- h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	}()

	if got := rec.next(t); got != (GateRoute{Plugin: true}) {
		t.Fatalf("first route = %+v, want plugin pending", got)
	}
	// Wait has no approval yet, so it is parked in the in-app select: the
	// route observed here is the one a UI submit would see.
	if got := rec.next(t); got != (GateRoute{}) {
		t.Fatalf("route while waiting in-app = %+v, want cleared", got)
	}

	approvalCh <- true
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Wait did not return after the in-app approval")
	}
}

func TestFeedbackHandler_Wait_GateRoute(t *testing.T) {
	newHandler := func(t *testing.T, d FeedbackChannelDispatcher, rec GateRouteRecorder) (*FeedbackHandler, *capturePublisher) {
		t.Helper()
		s := testutil.NewTestStore(t)
		testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
		testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)
		pub := &capturePublisher{}
		sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
		w := NewAuditWriter(s.Queries())
		t.Cleanup(func() { w.Close() }) //nolint:errcheck
		h := NewFeedbackHandler(w, sm, time.Minute,
			WithFeedbackChannelDispatch(d, "aud-1", "pol-1"),
			WithFeedbackGateRoutes(rec),
		)
		return h, pub
	}

	t.Run("plugin answers", func(t *testing.T) {
		rec := newRouteEventRecorder()
		h, _ := newHandler(t, funcFeedbackDispatcher(func(_ context.Context, req FeedbackDispatchRequest) (FeedbackSettlement, error) {
			req.OnPluginRoute("inst-slack")
			return FeedbackSettlement{Response: `{"text":"ok"}`}, nil
		}), rec)

		if _, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "q", time.Minute); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		assertRouteSequence(t, rec.drain(), []GateRoute{{Plugin: true}, {Plugin: true, InstanceID: "inst-slack"}, {}})
	})

	t.Run("dispatch fails", func(t *testing.T) {
		rec := newRouteEventRecorder()
		h, _ := newHandler(t, funcFeedbackDispatcher(func(context.Context, FeedbackDispatchRequest) (FeedbackSettlement, error) {
			return FeedbackSettlement{}, errors.New("plugin unreachable")
		}), rec)

		if _, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "q", time.Minute); err == nil {
			t.Fatal("Wait: expected error")
		}
		assertRouteSequence(t, rec.drain(), []GateRoute{{Plugin: true}, {}})
	})

	t.Run("in-app fallback with no timeout is answerable while waiting", func(t *testing.T) {
		rec := newRouteEventRecorder()
		h, pub := newHandler(t, funcFeedbackDispatcher(func(context.Context, FeedbackDispatchRequest) (FeedbackSettlement, error) {
			return FeedbackSettlement{}, ErrFeedbackRouteToInApp
		}), rec)

		done := make(chan error, 1)
		go func() {
			_, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "q", 0)
			done <- err
		}()

		if got := rec.next(t); got != (GateRoute{Plugin: true}) {
			t.Fatalf("first route = %+v, want plugin pending", got)
		}
		if got := rec.next(t); got != (GateRoute{}) {
			t.Fatalf("route while waiting in-app = %+v, want cleared", got)
		}

		pub.waitForEvent(t, "feedback.created", 30*time.Second)
		rows, err := h.sm.Queries().GetPendingFeedbackRequestsByRun(context.Background(), "run1")
		if err != nil || len(rows) == 0 {
			t.Fatalf("pending feedback rows = %v, err %v", rows, err)
		}
		if err := h.Resolve(rows[0].ID, "answer"); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("Wait did not return after the in-app answer")
		}
	})
}
