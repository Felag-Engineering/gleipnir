package run_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
)

// mapActorResolver is a fixed external-id -> user-id directory.
type mapActorResolver struct {
	users map[string]string
	err   error
}

func (m mapActorResolver) ResolveUserID(_ context.Context, actorExternalID string) (string, bool, error) {
	if m.err != nil {
		return "", false, m.err
	}
	id, ok := m.users[actorExternalID]
	return id, ok, nil
}

// #684: a settled approval carries the Gleipnir user the channel's actor maps
// to, and carries nobody when there is no mapping or the lookup fails.
func TestTaskChannelAdapters_DispatchApproval_StampsMappedDecider(t *testing.T) {
	cases := []struct {
		name     string
		resolver run.ActorUserResolver
		want     string // "" = nil
	}{
		{"mapped actor", mapActorResolver{users: map[string]string{"U123": "u-alice"}}, "u-alice"},
		{"unmapped actor", mapActorResolver{users: map[string]string{}}, ""},
		{"lookup failure", mapActorResolver{err: errors.New("db down")}, ""},
		{"no resolver configured", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := mcp.NewFakeChannelServer()
			f := newChannelAdapterFixture(t, stub)
			serverID := f.newRun(t, "r-decider")
			insertInstance(t, f.store, "inst-1")
			adapters := newApprovalAdapters(t, f, []hitl.Entry{pluginEntry("inst-1", serverID)})
			if tc.resolver != nil {
				adapters.WithActorResolver(tc.resolver)
			}

			expiresAt := time.Now().Add(time.Minute)
			resultCh := runAsync(func() (agent.ApprovalSettlement, error) {
				return adapters.DispatchApproval(context.Background(), agent.ApprovalDispatchRequest{
					AudienceID: "aud-1", RunID: "r-decider", ToolName: "some.tool",
					Prompt: "Approve?", ExpiresAt: &expiresAt,
				})
			})
			waitForPersistedTask(t, f, "r-decider")
			completeTask(t, f, stub, "task-1", "approve", "U123", nil)
			settlement, err := awaitResult(t, resultCh)
			if err != nil {
				t.Fatalf("DispatchApproval: %v", err)
			}

			if tc.want == "" {
				if settlement.DeciderUserID != nil {
					t.Errorf("DeciderUserID = %q, want nil", *settlement.DeciderUserID)
				}
				return
			}
			if settlement.DeciderUserID == nil || *settlement.DeciderUserID != tc.want {
				t.Errorf("DeciderUserID = %v, want %q", settlement.DeciderUserID, tc.want)
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
	adapters.WithActorResolver(mapActorResolver{users: map[string]string{"U7": "u-bob"}})

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
