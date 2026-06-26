package hostsvc_test

// Tests for the external-actor authorization gate in WriteAuditStep
// (issue #624). The gate fires AFTER §8.5 ownership and BEFORE any state
// mutation on BOTH the plugin-substrate path and the native feedback_requests
// path. An empty actor_external_id bypasses the gate (backward-compat).

import (
	"context"
	"database/sql"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// pendingFeedback is a minimal db.FeedbackRequest in the pending state.
func pendingFeedback(id, runID string) db.FeedbackRequest {
	return db.FeedbackRequest{
		ID:    id,
		RunID: runID,
		// Status "pending" passes the fr.Status actionability check.
		Status: "pending",
	}
}

// policyWithInstance returns a db.Policy whose YAML grants the given instanceName.
// The scopeProbe struct in policy_scope.go expects capabilities.tools as a list
// of objects with a "tool" key, not bare strings.
func policyWithInstance(instanceName string) db.Policy {
	return db.Policy{
		Yaml: "capabilities:\n  tools:\n  - tool: " + instanceName + ".some_tool\n",
	}
}

// slackIDRows returns GetUserBySlackUserIDRow slices for testing.
func slackIDRows(roles ...string) []db.GetUserBySlackUserIDRow {
	rows := make([]db.GetUserBySlackUserIDRow, len(roles))
	for i, r := range roles {
		rows[i] = db.GetUserBySlackUserIDRow{
			ID:       "user-" + r,
			Username: r + "-user",
			Role:     r,
		}
	}
	return rows
}

// buildSubstrateQuerier returns a fakeQuerier wired for the substrate path
// (GetPluginPendingRequest returns the given pending request).
func buildSubstrateQuerier(inst db.PluginInstance, pendingReq db.PluginPendingRequest, slackRows []db.GetUserBySlackUserIDRow) *fakeQuerier {
	return &fakeQuerier{
		instance:                 inst,
		pluginPendingRequest:     pendingReq,
		userBySlackUserID:        slackRows,
		updateFeedbackStatusRows: 1,
	}
}

// buildNativeQuerier returns a fakeQuerier wired for the native feedback_requests path.
func buildNativeQuerier(inst db.PluginInstance, fr db.FeedbackRequest, policy db.Policy, slackRows []db.GetUserBySlackUserIDRow) *fakeQuerier {
	return &fakeQuerier{
		instance: inst,
		// sql.ErrNoRows on GetPluginPendingRequest → fall through to native path.
		pluginPendingRequestErr:  sql.ErrNoRows,
		feedbackRequest:          fr,
		policy:                   policy,
		run:                      db.Run{ID: fr.RunID, PolicyID: policy.ID},
		userBySlackUserID:        slackRows,
		updateFeedbackStatusRows: 1,
	}
}

// TestWriteAuditStep_ActorAuthzSubstratePath exercises the substrate
// (plugin_pending_requests) path of the external-actor authorization gate.
func TestWriteAuditStep_ActorAuthzSubstratePath(t *testing.T) {
	const instanceID = "inst-1"
	const instanceName = "my-plugin"
	const requestID = "req-1"
	const runID = "run-1"

	inst := db.PluginInstance{ID: instanceID, InstanceName: instanceName}
	pendingReq := db.PluginPendingRequest{
		ID:               requestID,
		RunID:            runID,
		PluginInstanceID: instanceID,
	}

	cases := []struct {
		name              string
		actorID           string
		slackRows         []db.GetUserBySlackUserIDRow
		wantOK            bool
		wantUnauthorized  bool
		wantResolveCalled bool // tracks whether the ChannelResolver was reached
	}{
		{
			name:              "empty actor_external_id passthrough — authorized",
			actorID:           "",
			slackRows:         nil,
			wantOK:            true,
			wantUnauthorized:  false,
			wantResolveCalled: true,
		},
		{
			name:              "approver role — authorized",
			actorID:           "U-approver",
			slackRows:         slackIDRows("approver"),
			wantOK:            true,
			wantUnauthorized:  false,
			wantResolveCalled: true,
		},
		{
			name:              "operator role — authorized",
			actorID:           "U-operator",
			slackRows:         slackIDRows("operator"),
			wantOK:            true,
			wantUnauthorized:  false,
			wantResolveCalled: true,
		},
		{
			name:              "admin role — authorized",
			actorID:           "U-admin",
			slackRows:         slackIDRows("admin"),
			wantOK:            true,
			wantUnauthorized:  false,
			wantResolveCalled: true,
		},
		{
			name:              "auditor only — unauthorized",
			actorID:           "U-auditor",
			slackRows:         slackIDRows("auditor"),
			wantOK:            false,
			wantUnauthorized:  true,
			wantResolveCalled: false,
		},
		{
			name:              "unknown Slack id — unauthorized",
			actorID:           "U-nobody",
			slackRows:         nil,
			wantOK:            false,
			wantUnauthorized:  true,
			wantResolveCalled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := buildSubstrateQuerier(inst, pendingReq, tc.slackRows)

			// Track whether Resolve was called.
			resolveCalled := false
			resolver := &countingChannelResolver{
				resolved:    true,
				onResolveFn: func() { resolveCalled = true },
			}

			srv := hostsvc.NewServer(q, nil, testEncryptionKey,
				&fakeResolver{}, &fakeInstanceBinder{id: instanceID, ok: true},
				&fakePublisher{}, resolver,
			)

			resp, err := srv.WriteAuditStep(context.Background(), &hostv1.WriteAuditStepRequest{
				StepType:        "feedback_response",
				RequestId:       requestID,
				PayloadJson:     `{"text":"ok"}`,
				ActorExternalId: tc.actorID,
			})
			if err != nil {
				t.Fatalf("unexpected gRPC error: %v", err)
			}

			if resp.GetOk() != tc.wantOK {
				t.Errorf("ok = %v, want %v", resp.GetOk(), tc.wantOK)
			}
			if resp.GetUnauthorized() != tc.wantUnauthorized {
				t.Errorf("unauthorized = %v, want %v", resp.GetUnauthorized(), tc.wantUnauthorized)
			}
			if resolveCalled != tc.wantResolveCalled {
				t.Errorf("Resolve called = %v, want %v", resolveCalled, tc.wantResolveCalled)
			}

			// Unauthorized path must produce a high-severity audit event.
			if tc.wantUnauthorized {
				events := q.fakeAuditQuerier.all()
				if len(events) == 0 {
					t.Fatal("expected audit event for unauthorized actor, got none")
				}
				found := false
				for _, ev := range events {
					if ev.EventType == hostsvc.EventTypeUnauthorizedApproval && ev.Severity == "high" {
						found = true
					}
				}
				if !found {
					t.Errorf("no high-severity %q audit event found; got %v", hostsvc.EventTypeUnauthorizedApproval, events)
				}
			}
		})
	}
}

// TestWriteAuditStep_ActorAuthzNativePath exercises the native
// (feedback_requests) path of the external-actor authorization gate.
func TestWriteAuditStep_ActorAuthzNativePath(t *testing.T) {
	const instanceID = "inst-2"
	const instanceName = "my-plugin"
	const requestID = "req-2"
	const runID = "run-2"

	inst := db.PluginInstance{ID: instanceID, InstanceName: instanceName}
	fr := pendingFeedback(requestID, runID)
	policy := policyWithInstance(instanceName)

	cases := []struct {
		name                   string
		actorID                string
		slackRows              []db.GetUserBySlackUserIDRow
		wantOK                 bool
		wantUnauthorized       bool
		wantUpdateStatusCalled bool
	}{
		{
			name:                   "empty actor_external_id passthrough",
			actorID:                "",
			slackRows:              nil,
			wantOK:                 true,
			wantUnauthorized:       false,
			wantUpdateStatusCalled: true,
		},
		{
			name:                   "approver — authorized",
			actorID:                "U-approver",
			slackRows:              slackIDRows("approver"),
			wantOK:                 true,
			wantUnauthorized:       false,
			wantUpdateStatusCalled: true,
		},
		{
			name:                   "auditor only — unauthorized",
			actorID:                "U-auditor",
			slackRows:              slackIDRows("auditor"),
			wantOK:                 false,
			wantUnauthorized:       true,
			wantUpdateStatusCalled: false,
		},
		{
			name:                   "unknown id — unauthorized",
			actorID:                "U-nobody",
			slackRows:              nil,
			wantOK:                 false,
			wantUnauthorized:       true,
			wantUpdateStatusCalled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := buildNativeQuerier(inst, fr, policy, tc.slackRows)
			// Reset updateFeedbackStatusRows to track whether UpdateFeedbackRequestStatus was called.
			q.updateFeedbackStatusRows = 1

			srv := hostsvc.NewServer(q, nil, testEncryptionKey,
				&fakeResolver{}, &fakeInstanceBinder{id: instanceID, ok: true},
				&fakePublisher{}, nil,
			)

			resp, err := srv.WriteAuditStep(context.Background(), &hostv1.WriteAuditStepRequest{
				StepType:        "feedback_response",
				RequestId:       requestID,
				PayloadJson:     `{"text":"ok"}`,
				ActorExternalId: tc.actorID,
			})
			if err != nil {
				t.Fatalf("unexpected gRPC error: %v", err)
			}

			if resp.GetOk() != tc.wantOK {
				t.Errorf("ok = %v, want %v", resp.GetOk(), tc.wantOK)
			}
			if resp.GetUnauthorized() != tc.wantUnauthorized {
				t.Errorf("unauthorized = %v, want %v", resp.GetUnauthorized(), tc.wantUnauthorized)
			}
			// If UpdateFeedbackRequestStatus should not have been called, check
			// that CreateRunStep was also not called (proxy: createRunStepCalls).
			if !tc.wantUpdateStatusCalled && q.createRunStepCalls != 0 {
				t.Errorf("CreateRunStep called %d times on unauthorized path, want 0", q.createRunStepCalls)
			}

			if tc.wantUnauthorized {
				events := q.fakeAuditQuerier.all()
				found := false
				for _, ev := range events {
					if ev.EventType == hostsvc.EventTypeUnauthorizedApproval && ev.Severity == "high" {
						found = true
					}
				}
				if !found {
					t.Errorf("no high-severity %q audit event found; got %v", hostsvc.EventTypeUnauthorizedApproval, events)
				}
			}
		})
	}
}

// countingChannelResolver wraps fakeChannelResolver and records resolve calls.
type countingChannelResolver struct {
	resolved    bool
	err         error
	onResolveFn func()
}

func (c *countingChannelResolver) Resolve(ctx context.Context, requestID, responseJSON string) (bool, error) {
	if c.onResolveFn != nil {
		c.onResolveFn()
	}
	return c.resolved, c.err
}
