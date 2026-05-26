package state

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// capturePublisher records every Publish call for assertion in tests.
type capturePublisher struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	eventType string
	data      json.RawMessage
}

func (p *capturePublisher) Publish(eventType string, data json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, capturedEvent{eventType: eventType, data: data})
}

func (p *capturePublisher) all() []capturedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]capturedEvent, len(p.events))
	copy(out, p.events)
	return out
}

// seedInstance inserts a plugin + plugin_instance row and returns the instance ID.
func seedInstance(tb testing.TB, s *db.Store, instanceID string, state model.PluginHealthState) {
	tb.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	pluginID := instanceID + "-plugin"
	_, err := s.Queries().CreatePlugin(ctx, db.CreatePluginParams{
		ID:               pluginID,
		Name:             pluginID,
		PluginVersion:    "1.0.0",
		ManifestSnapshot: "{}",
		TrustedPubkey:    "pubkey",
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		tb.Fatalf("CreatePlugin: %v", err)
	}

	_, err = s.Queries().CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID:                instanceID,
		PluginID:          pluginID,
		InstanceName:      "test-instance",
		ConfigJson:        "{}",
		HandshakeVersions: "{}",
		HealthState:       string(state),
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if err != nil {
		tb.Fatalf("CreatePluginInstance: %v", err)
	}
}

func TestIsLegalTransition(t *testing.T) {
	legal := [][2]model.PluginHealthState{
		{model.PluginHealthStatePendingKeyApproval, model.PluginHealthStateHealthy},
		{model.PluginHealthStatePendingKeyApproval, model.PluginHealthStateUnsignedPermissive},
		{model.PluginHealthStatePendingKeyApproval, model.PluginHealthStateSignatureInvalid},
		{model.PluginHealthStatePendingKeyApproval, model.PluginHealthStateVerificationError},
		{model.PluginHealthStatePendingKeyApproval, model.PluginHealthStatePendingManifestApproval},
		{model.PluginHealthStatePendingManifestApproval, model.PluginHealthStateHealthy},
		{model.PluginHealthStatePendingManifestApproval, model.PluginHealthStateVerificationError},
		{model.PluginHealthStatePendingManifestApproval, model.PluginHealthStatePendingConfigMigration},
		// #194: tool-namespace conflict must drive unhealthy from any pending_* state.
		{model.PluginHealthStatePendingKeyApproval, model.PluginHealthStateUnhealthy},
		{model.PluginHealthStatePendingManifestApproval, model.PluginHealthStateUnhealthy},
		{model.PluginHealthStatePendingConfigMigration, model.PluginHealthStateUnhealthy},
		{model.PluginHealthStatePendingConfigMigration, model.PluginHealthStateHealthy},
		{model.PluginHealthStatePendingConfigMigration, model.PluginHealthStateVerificationError},
		{model.PluginHealthStateHealthy, model.PluginHealthStateUnhealthy},
		{model.PluginHealthStateHealthy, model.PluginHealthStateCrashed},
		{model.PluginHealthStateHealthy, model.PluginHealthStateCircuitBroken},
		{model.PluginHealthStateHealthy, model.PluginHealthStatePendingManifestApproval},
		// New edges: pending_key_approval is reachable from non-terminal states (#188).
		{model.PluginHealthStateHealthy, model.PluginHealthStatePendingKeyApproval},
		{model.PluginHealthStateUnsignedPermissive, model.PluginHealthStatePendingKeyApproval},
		{model.PluginHealthStateUnhealthy, model.PluginHealthStatePendingKeyApproval},
		{model.PluginHealthStateCircuitBroken, model.PluginHealthStatePendingKeyApproval},
		{model.PluginHealthStateCrashed, model.PluginHealthStatePendingKeyApproval},
		{model.PluginHealthStateUnsignedPermissive, model.PluginHealthStateUnhealthy},
		{model.PluginHealthStateUnsignedPermissive, model.PluginHealthStateCrashed},
		{model.PluginHealthStateUnhealthy, model.PluginHealthStateHealthy},
		{model.PluginHealthStateUnhealthy, model.PluginHealthStateCrashed},
		{model.PluginHealthStateUnhealthy, model.PluginHealthStateCircuitBroken},
		{model.PluginHealthStateCircuitBroken, model.PluginHealthStateHealthy},
		{model.PluginHealthStateCircuitBroken, model.PluginHealthStateUnhealthy},
		{model.PluginHealthStateCircuitBroken, model.PluginHealthStateCrashed},
		{model.PluginHealthStateCrashed, model.PluginHealthStateHealthy},
		{model.PluginHealthStateCrashed, model.PluginHealthStateUnhealthy},
		// #230: pending_reauthorize is reachable from operationally active states.
		{model.PluginHealthStateHealthy, model.PluginHealthStatePendingReauthorize},
		{model.PluginHealthStateUnsignedPermissive, model.PluginHealthStatePendingReauthorize},
		{model.PluginHealthStateCrashed, model.PluginHealthStatePendingReauthorize},
		{model.PluginHealthStateCircuitBroken, model.PluginHealthStatePendingReauthorize},
		// #230: pending_reauthorize exits toward healthy and real-failure states.
		{model.PluginHealthStatePendingReauthorize, model.PluginHealthStateHealthy},
		{model.PluginHealthStatePendingReauthorize, model.PluginHealthStateUnhealthy},
		{model.PluginHealthStatePendingReauthorize, model.PluginHealthStateCrashed},
		{model.PluginHealthStatePendingReauthorize, model.PluginHealthStateCircuitBroken},
		// #243: inactive is reachable from every non-terminal state.
		{model.PluginHealthStatePendingKeyApproval, model.PluginHealthStateInactive},
		{model.PluginHealthStatePendingManifestApproval, model.PluginHealthStateInactive},
		{model.PluginHealthStatePendingConfigMigration, model.PluginHealthStateInactive},
		{model.PluginHealthStateHealthy, model.PluginHealthStateInactive},
		{model.PluginHealthStateUnsignedPermissive, model.PluginHealthStateInactive},
		{model.PluginHealthStatePendingReauthorize, model.PluginHealthStateInactive},
		{model.PluginHealthStateUnhealthy, model.PluginHealthStateInactive},
		{model.PluginHealthStateCircuitBroken, model.PluginHealthStateInactive},
		{model.PluginHealthStateCrashed, model.PluginHealthStateInactive},
		// #243: inactive exits to healthy or unhealthy on re-activation.
		{model.PluginHealthStateInactive, model.PluginHealthStateHealthy},
		{model.PluginHealthStateInactive, model.PluginHealthStateUnhealthy},
	}
	for _, pair := range legal {
		if !IsLegalTransition(pair[0], pair[1]) {
			t.Errorf("IsLegalTransition(%s, %s) = false, want true", pair[0], pair[1])
		}
	}

	illegal := [][2]model.PluginHealthState{
		{model.PluginHealthStateSignatureInvalid, model.PluginHealthStateHealthy},
		{model.PluginHealthStateVerificationError, model.PluginHealthStateHealthy},
		// signature_invalid and verification_error are terminal — no outgoing edges.
		{model.PluginHealthStateCrashed, model.PluginHealthStateSignatureInvalid},
		// #230: pending_reauthorize cannot jump to other pending_* states or
		// to terminal states.
		{model.PluginHealthStatePendingReauthorize, model.PluginHealthStateSignatureInvalid},
		{model.PluginHealthStatePendingReauthorize, model.PluginHealthStateVerificationError},
		{model.PluginHealthStatePendingReauthorize, model.PluginHealthStatePendingKeyApproval},
		// pending_key_approval cannot jump to pending_reauthorize (#230).
		{model.PluginHealthStatePendingKeyApproval, model.PluginHealthStatePendingReauthorize},
		// #243: terminal states cannot transition to inactive.
		{model.PluginHealthStateSignatureInvalid, model.PluginHealthStateInactive},
		{model.PluginHealthStateVerificationError, model.PluginHealthStateInactive},
	}
	for _, pair := range illegal {
		if IsLegalTransition(pair[0], pair[1]) {
			t.Errorf("IsLegalTransition(%s, %s) = true, want false", pair[0], pair[1])
		}
	}
}

func TestPendingReauthorizeSeverity(t *testing.T) {
	// pending_reauthorize must sit at rank 2 (operator-action-pending tier),
	// matching the other pending_* states.
	got := Severity(model.PluginHealthStatePendingReauthorize)
	if got != 2 {
		t.Errorf("Severity(pending_reauthorize) = %d, want 2", got)
	}
}

func TestInactiveSeverity(t *testing.T) {
	// inactive must sit at rank 8, above crashed (7), because it is a deliberate
	// and total disable — worse than any runtime failure from an availability standpoint.
	got := Severity(model.PluginHealthStateInactive)
	if got != 8 {
		t.Errorf("Severity(inactive) = %d, want 8", got)
	}
	// inactive must dominate crashed in WorstHealth.
	worst := WorstHealth([]model.PluginHealthState{model.PluginHealthStateCrashed, model.PluginHealthStateInactive})
	if worst != model.PluginHealthStateInactive {
		t.Errorf("WorstHealth([crashed, inactive]) = %q, want inactive", worst)
	}
}

func TestSetHealthState_LegalTransitions(t *testing.T) {
	legal := [][2]model.PluginHealthState{
		{model.PluginHealthStatePendingKeyApproval, model.PluginHealthStateHealthy},
		{model.PluginHealthStateHealthy, model.PluginHealthStateUnhealthy},
		{model.PluginHealthStateUnhealthy, model.PluginHealthStateCrashed},
		{model.PluginHealthStateCrashed, model.PluginHealthStateHealthy},
	}

	for _, pair := range legal {
		from, to := pair[0], pair[1]
		t.Run(string(from)+"→"+string(to), func(t *testing.T) {
			s := testutil.NewTestStore(t)
			iid := "inst-" + string(from)
			seedInstance(t, s, iid, from)

			if err := SetHealthState(context.Background(), s.Queries(), nil, iid, OriginHost, to, ""); err != nil {
				t.Fatalf("SetHealthState(%s → %s): unexpected error: %v", from, to, err)
			}

			row, err := s.Queries().GetPluginInstanceByID(context.Background(), iid)
			if err != nil {
				t.Fatalf("GetPluginInstanceByID: %v", err)
			}
			if row.HealthState != string(to) {
				t.Errorf("health_state = %q, want %q", row.HealthState, to)
			}
		})
	}
}

func TestSetHealthState_IllegalTransition(t *testing.T) {
	s := testutil.NewTestStore(t)
	seedInstance(t, s, "inst1", model.PluginHealthStateSignatureInvalid)

	err := SetHealthState(context.Background(), s.Queries(), nil, "inst1", OriginHost, model.PluginHealthStateHealthy, "")
	if err == nil {
		t.Fatal("expected error for illegal transition, got nil")
	}
	if !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("error = %v, want errors.Is(err, ErrIllegalTransition)", err)
	}

	// State must remain unchanged.
	row, err := s.Queries().GetPluginInstanceByID(context.Background(), "inst1")
	if err != nil {
		t.Fatalf("GetPluginInstanceByID: %v", err)
	}
	if row.HealthState != string(model.PluginHealthStateSignatureInvalid) {
		t.Errorf("health_state changed to %q after illegal transition", row.HealthState)
	}
}

func TestSetHealthState_PluginSelfDroppedWhenNotWorse(t *testing.T) {
	// Plugin self-reporting healthy when already healthy → dropped (no write).
	s := testutil.NewTestStore(t)
	seedInstance(t, s, "inst1", model.PluginHealthStateUnhealthy)

	// Plugin reports a less-severe state — should be silently dropped.
	err := SetHealthState(context.Background(), s.Queries(), nil, "inst1", OriginPluginSelf, model.PluginHealthStateHealthy, "")
	if err != nil {
		t.Fatalf("SetHealthState: unexpected error: %v", err)
	}

	row, err := s.Queries().GetPluginInstanceByID(context.Background(), "inst1")
	if err != nil {
		t.Fatalf("GetPluginInstanceByID: %v", err)
	}
	if row.HealthState != string(model.PluginHealthStateUnhealthy) {
		t.Errorf("health_state = %q, want unhealthy (plugin-self improvement must be dropped)", row.HealthState)
	}
	if row.Version != 0 {
		t.Errorf("version = %d, want 0 (no DB write expected)", row.Version)
	}
}

func TestSetHealthState_PluginSelfCanWorsen(t *testing.T) {
	// Plugin self-reporting crashed when currently healthy → allowed (worsens).
	s := testutil.NewTestStore(t)
	seedInstance(t, s, "inst1", model.PluginHealthStateHealthy)

	err := SetHealthState(context.Background(), s.Queries(), nil, "inst1", OriginPluginSelf, model.PluginHealthStateCrashed, "OOM")
	if err != nil {
		t.Fatalf("SetHealthState: unexpected error: %v", err)
	}

	row, err := s.Queries().GetPluginInstanceByID(context.Background(), "inst1")
	if err != nil {
		t.Fatalf("GetPluginInstanceByID: %v", err)
	}
	if row.HealthState != string(model.PluginHealthStateCrashed) {
		t.Errorf("health_state = %q, want crashed", row.HealthState)
	}
}

func TestSetHealthState_HostCanImprove(t *testing.T) {
	// Host reporting healthy when currently unhealthy → allowed (host not restricted).
	s := testutil.NewTestStore(t)
	seedInstance(t, s, "inst1", model.PluginHealthStateUnhealthy)

	err := SetHealthState(context.Background(), s.Queries(), nil, "inst1", OriginHost, model.PluginHealthStateHealthy, "")
	if err != nil {
		t.Fatalf("SetHealthState: unexpected error: %v", err)
	}

	row, err := s.Queries().GetPluginInstanceByID(context.Background(), "inst1")
	if err != nil {
		t.Fatalf("GetPluginInstanceByID: %v", err)
	}
	if row.HealthState != string(model.PluginHealthStateHealthy) {
		t.Errorf("health_state = %q, want healthy", row.HealthState)
	}
}

func TestSetHealthState_PublishesEvent(t *testing.T) {
	s := testutil.NewTestStore(t)
	seedInstance(t, s, "inst1", model.PluginHealthStatePendingKeyApproval)

	pub := &capturePublisher{}
	if err := SetHealthState(context.Background(), s.Queries(), pub, "inst1", OriginHost, model.PluginHealthStateHealthy, "verified"); err != nil {
		t.Fatalf("SetHealthState: %v", err)
	}

	events := pub.all()
	if len(events) != 1 {
		t.Fatalf("got %d published events, want 1", len(events))
	}
	if events[0].eventType != "plugin.health_changed" {
		t.Errorf("event type = %q, want plugin.health_changed", events[0].eventType)
	}

	var payload map[string]string
	if err := json.Unmarshal(events[0].data, &payload); err != nil {
		t.Fatalf("unmarshal event data: %v", err)
	}
	if payload["instance_id"] != "inst1" {
		t.Errorf("instance_id = %q, want inst1", payload["instance_id"])
	}
	if payload["state"] != "healthy" {
		t.Errorf("state = %q, want healthy", payload["state"])
	}
}

func TestSetHealthState_NilPublisher(t *testing.T) {
	s := testutil.NewTestStore(t)
	seedInstance(t, s, "inst1", model.PluginHealthStatePendingKeyApproval)

	// nil publisher must not panic.
	if err := SetHealthState(context.Background(), s.Queries(), nil, "inst1", OriginHost, model.PluginHealthStateHealthy, ""); err != nil {
		t.Fatalf("SetHealthState with nil publisher: %v", err)
	}
}

func TestSetHealthState_CASConflict(t *testing.T) {
	s := testutil.NewTestStore(t)
	seedInstance(t, s, "inst1", model.PluginHealthStatePendingKeyApproval)

	// Bump the version externally to simulate a concurrent write.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB().Exec(
		`UPDATE plugin_instances SET version = 5, updated_at = ? WHERE id = 'inst1'`,
		now,
	)
	if err != nil {
		t.Fatalf("bump version: %v", err)
	}

	// A SetHealthState that reads version=5 and writes with expected_version=5 should succeed.
	if err := SetHealthState(context.Background(), s.Queries(), nil, "inst1", OriginHost, model.PluginHealthStateHealthy, ""); err != nil {
		t.Fatalf("SetHealthState after version bump: unexpected error: %v", err)
	}

	// Now use UpdatePluginInstanceHealth directly with a stale expected_version to
	// verify the CAS semantics work — mirrors the runstate CAS test pattern.
	reason := "stale"
	rows, updateErr := s.Queries().UpdatePluginInstanceHealth(context.Background(), db.UpdatePluginInstanceHealthParams{
		HealthState:     string(model.PluginHealthStateUnhealthy),
		HealthDetail:    &reason,
		UpdatedAt:       now,
		ID:              "inst1",
		ExpectedVersion: 3, // stale — real version is now 6
	})
	if updateErr != nil {
		t.Fatalf("UpdatePluginInstanceHealth: %v", updateErr)
	}
	if rows != 0 {
		t.Errorf("rows = %d for stale version CAS, want 0", rows)
	}

	row, err := s.Queries().GetPluginInstanceByID(context.Background(), "inst1")
	if err != nil {
		t.Fatalf("GetPluginInstanceByID after CAS miss: %v", err)
	}
	if row.HealthState != string(model.PluginHealthStateHealthy) {
		t.Errorf("health_state = %q after CAS miss, want healthy (unchanged)", row.HealthState)
	}
}

func TestSetHealthState_InstanceNotFound(t *testing.T) {
	s := testutil.NewTestStore(t)

	err := SetHealthState(context.Background(), s.Queries(), nil, "nonexistent-instance", OriginHost, model.PluginHealthStateHealthy, "")
	if err == nil {
		t.Fatal("expected error for nonexistent instance, got nil")
	}
}

func TestWorstHealth(t *testing.T) {
	tests := []struct {
		name   string
		states []model.PluginHealthState
		want   model.PluginHealthState
	}{
		{
			name:   "empty returns healthy",
			states: nil,
			want:   model.PluginHealthStateHealthy,
		},
		{
			name:   "single healthy",
			states: []model.PluginHealthState{model.PluginHealthStateHealthy},
			want:   model.PluginHealthStateHealthy,
		},
		{
			name: "picks worst",
			states: []model.PluginHealthState{
				model.PluginHealthStateHealthy,
				model.PluginHealthStateUnhealthy,
				model.PluginHealthStateCrashed,
			},
			want: model.PluginHealthStateCrashed,
		},
		{
			name: "all equal",
			states: []model.PluginHealthState{
				model.PluginHealthStateUnhealthy,
				model.PluginHealthStateUnhealthy,
			},
			want: model.PluginHealthStateUnhealthy,
		},
		{
			name: "crashed dominates signature_invalid and verification_error",
			states: []model.PluginHealthState{
				model.PluginHealthStateCrashed,
				model.PluginHealthStateSignatureInvalid,
				model.PluginHealthStateVerificationError,
			},
			want: model.PluginHealthStateCrashed, // severity 7 > 6 > 5
		},
		{
			name: "inactive dominates all other states",
			states: []model.PluginHealthState{
				model.PluginHealthStateCrashed,
				model.PluginHealthStateInactive,
				model.PluginHealthStateSignatureInvalid,
			},
			want: model.PluginHealthStateInactive, // severity 8 > 7 > 6
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WorstHealth(tt.states)
			if got != tt.want {
				t.Errorf("WorstHealth(%v) = %q, want %q", tt.states, got, tt.want)
			}
		})
	}
}
