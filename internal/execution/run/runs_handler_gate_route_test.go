package run_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func seedPluginInstance(t *testing.T, store *db.Store, pluginID, instanceID, instanceName string) {
	t.Helper()
	q := db.New(store.DB())
	now := "2026-01-01T00:00:00Z"
	if _, err := q.CreatePlugin(context.Background(), db.CreatePluginParams{
		ID:               pluginID,
		Name:             pluginID,
		PluginVersion:    "1.0.0",
		ManifestSnapshot: "{}",
		TrustedPubkey:    "",
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := q.CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID:                    instanceID,
		PluginID:              pluginID,
		InstanceName:          instanceName,
		ConfigJson:            "{}",
		SubscriptionScopeJson: "{}",
		HandshakeVersions:     "{}",
		HealthState:           "healthy",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
}

// gateRouteScenario names how the run's open gate is routed when the UI submits.
type gateRouteScenario struct {
	name string
	// route records the gate's route on the manager; nil leaves the default
	// (in-app) in place.
	route func(m *run.RunManager, runID string)
	// wantStatus / wantBody are the expected response.
	wantStatus int
	wantBody   string
}

func gateRouteScenarios() []gateRouteScenario {
	return []gateRouteScenario{
		{
			name:       "no recorded route is answerable in-app",
			wantStatus: http.StatusAccepted,
		},
		{
			name: "plugin route naming the channel is refused",
			route: func(m *run.RunManager, runID string) {
				m.RecordGateRoute(runID, agent.GateRoute{Plugin: true, InstanceID: "inst-slack"})
			},
			wantStatus: http.StatusConflict,
			wantBody:   `the \"team-slack\" plugin channel`,
		},
		{
			name: "plugin route still being resolved is refused",
			route: func(m *run.RunManager, runID string) {
				m.RecordGateRoute(runID, agent.GateRoute{Plugin: true})
			},
			wantStatus: http.StatusConflict,
			wantBody:   "a plugin channel",
		},
		{
			name: "plugin route that fell back to in-app is answerable",
			route: func(m *run.RunManager, runID string) {
				m.RecordGateRoute(runID, agent.GateRoute{Plugin: true, InstanceID: "inst-slack"})
				m.ClearGateRoute(runID)
			},
			wantStatus: http.StatusAccepted,
		},
	}
}

func TestRunsHandler_SubmitApproval_GateRoute(t *testing.T) {
	for _, tt := range gateRouteScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			manager := run.NewRunManager()
			t.Cleanup(manager.Wait)

			seedPluginInstance(t, store, "plug-slack", "inst-slack", "team-slack")
			testutil.InsertPolicy(t, store, "p1", "policy-p1", "webhook", audienceRoutedPolicy)
			testutil.InsertRun(t, store, "r1", "p1", model.RunStatusWaitingForApproval)
			testutil.InsertApprovalRequest(t, store, "ar1", "r1", "some_tool")

			ch := make(chan bool, 1)
			manager.Register("r1", func() {}, ch)
			t.Cleanup(func() { manager.Deregister("r1") })
			if tt.route != nil {
				tt.route(manager, "r1")
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/r1/approval", strings.NewReader(`{"decision":"approved"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			newRunsRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tt.wantStatus, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Errorf("body = %s, want it to contain %q", w.Body.String(), tt.wantBody)
			}

			pending, err := store.GetPendingApprovalRequestsByRun(context.Background(), "r1")
			if err != nil {
				t.Fatalf("GetPendingApprovalRequestsByRun: %v", err)
			}
			refused := tt.wantStatus == http.StatusConflict
			if refused && len(pending) != 1 {
				t.Errorf("a refused submit must leave the approval pending for the plugin; pending rows = %d", len(pending))
			}
			if refused && len(ch) != 0 {
				t.Error("a refused submit must not wake the agent")
			}
		})
	}
}

func TestRunsHandler_SubmitFeedback_GateRoute(t *testing.T) {
	for _, tt := range gateRouteScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			manager := run.NewRunManager()
			t.Cleanup(manager.Wait)

			seedPluginInstance(t, store, "plug-slack", "inst-slack", "team-slack")
			testutil.InsertPolicy(t, store, "p1", "policy-p1", "webhook", audienceRoutedPolicy)
			testutil.InsertRun(t, store, "r1", "p1", model.RunStatusWaitingForFeedback)
			insertFeedbackRequest(t, store, "fr1", "r1")

			delivered := false
			resolver := &stubResolver{ResolveFunc: func(_, _ string) error {
				delivered = true
				return nil
			}}
			manager.Register("r1", func() {}, make(chan bool, 1))
			manager.RegisterFeedbackResolver("r1", resolver)
			t.Cleanup(func() { manager.Deregister("r1") })
			if tt.route != nil {
				tt.route(manager, "r1")
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/r1/feedback", strings.NewReader(`{"response":"go ahead"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			newRunsRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tt.wantStatus, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Errorf("body = %s, want it to contain %q", w.Body.String(), tt.wantBody)
			}
			if wantDelivered := tt.wantStatus == http.StatusAccepted; delivered != wantDelivered {
				t.Errorf("response delivered = %v, want %v", delivered, wantDelivered)
			}
		})
	}
}
