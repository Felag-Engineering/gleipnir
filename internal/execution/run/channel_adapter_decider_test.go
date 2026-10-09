package run_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
)

type fakeLinkedUser struct {
	id    string
	roles []model.Role
}

// mapActorResolver is a fixed external-id -> linked-user directory. An id that
// is absent resolves as not found, which is also what the real directory does
// for a deactivated user (its query excludes them).
type mapActorResolver struct {
	users map[string]fakeLinkedUser
	err   error
}

func (m mapActorResolver) ResolveActor(_ context.Context, actorExternalID string) (string, []model.Role, bool, error) {
	if m.err != nil {
		return "", nil, false, m.err
	}
	u, ok := m.users[actorExternalID]
	return u.id, u.roles, ok, nil
}

// defaultTestActors links the actor ids the shared channel-adapter tests use
// to an approver, so tests not about verification can still approve.
func defaultTestActors() run.ActorUserResolver {
	approver := fakeLinkedUser{id: "u-approver", roles: []model.Role{model.RoleApprover}}
	return mapActorResolver{users: map[string]fakeLinkedUser{
		"U123": approver, "U9": approver, "U1": approver, "U7": approver,
	}}
}

// #1028: an approval is only honored when the named actor is linked to a
// Gleipnir user holding approver or admin; anything else is refused, audited
// at high severity, and never reported approved.
func TestTaskChannelAdapters_DispatchApproval_VerifiesApprovingActor(t *testing.T) {
	cases := []struct {
		name         string
		resolver     run.ActorUserResolver
		optionID     string
		wantApproved bool
		wantRefused  bool
		wantUser     string
		wantLink     decision.LinkMethod
	}{
		{
			name:     "linked approver",
			resolver: mapActorResolver{users: map[string]fakeLinkedUser{"U123": {"u-alice", []model.Role{model.RoleApprover}}}},
			optionID: "approve", wantApproved: true, wantUser: "u-alice", wantLink: decision.LinkDirectory,
		},
		{
			name:     "linked admin",
			resolver: mapActorResolver{users: map[string]fakeLinkedUser{"U123": {"u-root", []model.Role{model.RoleAdmin}}}},
			optionID: "approve", wantApproved: true, wantUser: "u-root", wantLink: decision.LinkDirectory,
		},
		{
			name:     "unknown or deactivated actor",
			resolver: mapActorResolver{users: map[string]fakeLinkedUser{}},
			optionID: "approve", wantRefused: true,
		},
		{
			name:     "linked operator lacks the approver role",
			resolver: mapActorResolver{users: map[string]fakeLinkedUser{"U123": {"u-op", []model.Role{model.RoleOperator}}}},
			optionID: "approve", wantRefused: true,
		},
		{
			name:     "linked auditor",
			resolver: mapActorResolver{users: map[string]fakeLinkedUser{"U123": {"u-aud", []model.Role{model.RoleAuditor}}}},
			optionID: "approve", wantRefused: true,
		},
		{
			name:     "directory lookup failure fails closed",
			resolver: mapActorResolver{err: errors.New("db down")},
			optionID: "approve", wantRefused: true,
		},
		{
			name:     "no directory configured fails closed",
			resolver: nil,
			optionID: "approve", wantRefused: true,
		},
		{
			name:     "unverified rejection is still honored as a claim",
			resolver: mapActorResolver{users: map[string]fakeLinkedUser{}},
			optionID: "reject", wantApproved: false, wantLink: decision.LinkUnverified,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := mcp.NewFakeChannelServer()
			f := newChannelAdapterFixture(t, stub)
			serverID := f.newRun(t, "r-verify")
			insertInstance(t, f.store, "inst-1")
			adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})
			// A nil interface (not a typed nil) is what "no directory" means.
			if tc.resolver != nil {
				adapters.WithActorResolver(tc.resolver)
			} else {
				adapters.WithActorResolver(nil)
			}

			expiresAt := time.Now().Add(time.Minute)
			resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
				return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
					AudienceID: "aud-1", RunID: "r-verify", ToolName: "some.tool",
					Prompt: "Approve?", ExpiresAt: &expiresAt,
				})
			})
			waitForPersistedTask(t, f, "r-verify")
			completeTask(t, f, stub, "task-1", tc.optionID, "U123", nil)
			settlement, err := awaitResult(t, resultCh)

			if tc.wantRefused {
				if err == nil {
					t.Fatal("expected the approval to be refused")
				}
				if settlement.Approved {
					t.Fatal("a refused approval must never report approved=true")
				}
				records, rerr := f.decisions.ForRun(context.Background(), "r-verify")
				if rerr != nil {
					t.Fatalf("ForRun: %v", rerr)
				}
				if len(records) != 1 || records[0].Outcome != decision.OutcomeRejected {
					t.Fatalf("records = %+v, want one rejected record", records)
				}
				if records[0].ActorUserID != "" || records[0].LinkMethod.Verified() {
					t.Errorf("a refusal must not claim a verified user: %+v", records[0])
				}
				var found bool
				for _, a := range auditEventsFor(t, f, "r-verify") {
					if a.EventType == "unauthorized_approval_attempt" && a.Severity == "high" {
						found = true
					}
				}
				if !found {
					t.Error("want a high-severity unauthorized_approval_attempt audit event")
				}
				return
			}

			if err != nil {
				t.Fatalf("DispatchApproval: %v", err)
			}
			if settlement.Approved != tc.wantApproved {
				t.Errorf("Approved = %v, want %v", settlement.Approved, tc.wantApproved)
			}
			if tc.wantUser == "" {
				if settlement.DeciderUserID != nil {
					t.Errorf("DeciderUserID = %q, want nil", *settlement.DeciderUserID)
				}
			} else if settlement.DeciderUserID == nil || *settlement.DeciderUserID != tc.wantUser {
				t.Errorf("DeciderUserID = %v, want %q", settlement.DeciderUserID, tc.wantUser)
			}

			settle(t, approvalSettlement(settlement), true)
			records, err := f.decisions.ForRun(context.Background(), "r-verify")
			if err != nil {
				t.Fatalf("ForRun: %v", err)
			}
			if len(records) != 1 {
				t.Fatalf("len(records) = %d, want 1", len(records))
			}
			if records[0].LinkMethod != tc.wantLink || records[0].ActorUserID != tc.wantUser {
				t.Errorf("LinkMethod = %q, ActorUserID = %q, want %q and %q",
					records[0].LinkMethod, records[0].ActorUserID, tc.wantLink, tc.wantUser)
			}
		})
	}
}

func TestTaskChannelAdapters_DispatchFeedback_StampsMappedResponder(t *testing.T) {
	stub := mcp.NewFakeChannelServer()
	f := newChannelAdapterFixture(t, stub)
	serverID := f.newRun(t, "r-responder")
	insertInstance(t, f.store, "inst-1")
	adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})
	adapters.WithActorResolver(mapActorResolver{users: map[string]fakeLinkedUser{"U7": {"u-bob", []model.Role{model.RoleOperator}}}})

	expiresAt := time.Now().Add(time.Minute)
	resultCh := runAsync(func() (agent.FeedbackSettlement, error) {
		return adapters.DispatchFeedback(context.Background(), agent.FeedbackDispatchRequest{
			AudienceID: "aud-1", RunID: "r-responder", ToolName: "gleipnir.ask_operator",
			Prompt: "What region?", ExpiresAt: &expiresAt,
		})
	})
	waitForPersistedTask(t, f, "r-responder")
	completeTask(t, f, stub, "task-1", "", "U7", []byte(`{"text":"us-east-1"}`))
	settlement, err := awaitResult(t, resultCh)
	if err != nil {
		t.Fatalf("DispatchFeedback: %v", err)
	}
	if settlement.ResponderUserID == nil || *settlement.ResponderUserID != "u-bob" {
		t.Errorf("ResponderUserID = %v, want u-bob", settlement.ResponderUserID)
	}
}
