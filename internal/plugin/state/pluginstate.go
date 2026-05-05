// Package state provides the plugin instance health-state machine and CAS
// transition helpers. It mirrors the shape of internal/execution/runstate
// (ADR-038 atomic transitions) for consistency.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// ErrIllegalTransition is returned when a requested health-state transition is
// not permitted by the plugin health state machine graph.
var ErrIllegalTransition = errors.New("illegal plugin health state transition")

// ErrTransitionConflict is returned when a CAS update finds that another writer
// already advanced the instance's version. The instance is in a valid state in
// the DB — the caller must not treat this as fatal, only as a signal that its
// write was lost.
var ErrTransitionConflict = errors.New("plugin health state transition lost to concurrent writer")

// Origin identifies who is reporting a health-state change. The §8.1 rule
// ("plugin can only mark itself worse than host-detected state") is encoded
// inside SetHealthState based on this value.
type Origin int

const (
	// OriginHost means the Gleipnir host runtime is reporting the state.
	OriginHost Origin = iota
	// OriginPluginSelf means the plugin process itself reported the state via
	// the host RPC SetHealthState call.
	OriginPluginSelf
)

// Querier is the narrow DB interface required by this package. Accepting an
// interface (not *db.Queries) keeps the package testable with a fake querier
// and mirrors the admin.Handler pattern.
type Querier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	UpdatePluginInstanceHealth(ctx context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error)
}

// severity maps each health state to a numeric rank used to compare states.
// Higher rank = worse health. The total ordering is required by the §8.1 merge
// rule ("plugin-self can only worsen; worst across plugin-self and host wins").
//
// This ordering is a design decision introduced in issue #191: the spec §8.1
// specifies the merge rule but not the full total ordering across all 10 states.
// The ranking here reflects the operational impact of each state:
//
//	0 = fully operational
//	1 = degraded-but-allowed (unsigned is intentional operator choice)
//	2 = waiting for operator action (pending states)
//	3 = runtime auth/availability problem (unhealthy)
//	4 = circuit breaker tripped (requests not routed)
//	5 = cryptographic verification failed
//	6 = manifest signature rejected
//	7 = process crash
var severity = map[model.PluginHealthState]int{
	model.PluginHealthStateHealthy:                 0,
	model.PluginHealthStateUnsignedPermissive:      1,
	model.PluginHealthStatePendingKeyApproval:      2,
	model.PluginHealthStatePendingManifestApproval: 2,
	model.PluginHealthStatePendingConfigMigration:  2,
	model.PluginHealthStateUnhealthy:               3,
	model.PluginHealthStateCircuitBroken:           4,
	model.PluginHealthStateVerificationError:       5,
	model.PluginHealthStateSignatureInvalid:        6,
	model.PluginHealthStateCrashed:                 7,
}

// Severity returns the numeric severity rank for a state. Lower is better.
// Unknown states return -1 so callers can detect them if needed.
func Severity(s model.PluginHealthState) int {
	v, ok := severity[s]
	if !ok {
		return -1
	}
	return v
}

// legalTransitions defines the plugin health state machine graph. Each key is
// a state that may transition; its value lists every state it may move to.
//
// The graph encodes the operational lifecycle:
//   - Startup states (pending_*) resolve toward healthy or an error state.
//   - healthy/unsigned_permissive may degrade at runtime (unhealthy, crashed,
//     circuit_broken) and can recover back to healthy.
//   - Error states (signature_invalid, verification_error) are terminal from the
//     plugin's own perspective; only the host can re-install / re-approve.
var legalTransitions = map[model.PluginHealthState][]model.PluginHealthState{
	model.PluginHealthStatePendingKeyApproval: {
		model.PluginHealthStateHealthy,
		model.PluginHealthStateUnsignedPermissive,
		model.PluginHealthStateSignatureInvalid,
		model.PluginHealthStateVerificationError,
		model.PluginHealthStatePendingManifestApproval,
	},
	model.PluginHealthStatePendingManifestApproval: {
		model.PluginHealthStateHealthy,
		model.PluginHealthStateVerificationError,
		model.PluginHealthStatePendingConfigMigration,
	},
	model.PluginHealthStatePendingConfigMigration: {
		model.PluginHealthStateHealthy,
		model.PluginHealthStateVerificationError,
	},
	model.PluginHealthStateHealthy: {
		model.PluginHealthStateUnhealthy,
		model.PluginHealthStateCrashed,
		model.PluginHealthStateCircuitBroken,
		model.PluginHealthStatePendingManifestApproval, // hot-reload detects manifest change
	},
	model.PluginHealthStateUnsignedPermissive: {
		model.PluginHealthStateUnhealthy,
		model.PluginHealthStateCrashed,
		model.PluginHealthStateCircuitBroken,
		model.PluginHealthStatePendingManifestApproval,
	},
	model.PluginHealthStateUnhealthy: {
		model.PluginHealthStateHealthy,
		model.PluginHealthStateCrashed,
		model.PluginHealthStateCircuitBroken,
	},
	model.PluginHealthStateCircuitBroken: {
		model.PluginHealthStateHealthy,
		model.PluginHealthStateUnhealthy,
		model.PluginHealthStateCrashed,
	},
	model.PluginHealthStateCrashed: {
		model.PluginHealthStateHealthy,
		model.PluginHealthStateUnhealthy,
	},
	// signature_invalid and verification_error are terminal — no outgoing edges.
	// A re-install or admin key-rotation creates a fresh instance row.
}

// IsLegalTransition reports whether transitioning from → to is permitted by the
// plugin health state machine graph.
func IsLegalTransition(from, to model.PluginHealthState) bool {
	for _, allowed := range legalTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// SetHealthState transitions a plugin instance to a new health state, enforcing:
//  1. §8.1 plugin-self merge rule: if origin is OriginPluginSelf and the
//     reported state is not worse than the current state, the call is a no-op.
//  2. The state machine graph (legalTransitions): an illegal edge returns
//     ErrIllegalTransition.
//  3. ADR-038 CAS guard: if another writer advanced the version concurrently,
//     returns ErrTransitionConflict.
//
// On success, the transition is recorded as a metric, logged, and published as
// a "plugin.health_changed" event (if publisher is non-nil).
func SetHealthState(ctx context.Context, q Querier, publisher event.Publisher, instanceID string, origin Origin, reported model.PluginHealthState, detail string) error {
	row, err := q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("get plugin instance: %w", err)
	}

	current := model.PluginHealthState(row.HealthState)

	// §8.1: plugin-self reports are dropped when they do not worsen the current
	// state. The host is never restricted — it may improve or worsen freely.
	if origin == OriginPluginSelf && Severity(reported) <= Severity(current) {
		return nil
	}

	if !IsLegalTransition(current, reported) {
		return fmt.Errorf("%w: %s → %s", ErrIllegalTransition, current, reported)
	}

	detailPtr := (*string)(nil)
	if detail != "" {
		detailPtr = &detail
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := q.UpdatePluginInstanceHealth(ctx, db.UpdatePluginInstanceHealthParams{
		HealthState:     string(reported),
		HealthDetail:    detailPtr,
		UpdatedAt:       now,
		ID:              instanceID,
		ExpectedVersion: row.Version,
	})
	if err != nil {
		return fmt.Errorf("update plugin instance health: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: instance %s", ErrTransitionConflict, instanceID)
	}

	RecordTransition(current, reported)

	logctx.Logger(ctx).InfoContext(ctx, "plugin health state transition",
		"instance_id", instanceID,
		"plugin_id", row.PluginID,
		"from", string(current),
		"to", string(reported),
	)

	if publisher != nil {
		if data, err := json.Marshal(map[string]string{
			"instance_id": instanceID,
			"plugin_id":   row.PluginID,
			"state":       string(reported),
		}); err == nil {
			publisher.Publish("plugin.health_changed", data)
		}
	}

	return nil
}

// WorstHealth returns the most severe PluginHealthState from the given slice.
// If the slice is empty, it returns PluginHealthStateHealthy.
func WorstHealth(states []model.PluginHealthState) model.PluginHealthState {
	worst := model.PluginHealthStateHealthy
	for _, s := range states {
		if Severity(s) > Severity(worst) {
			worst = s
		}
	}
	return worst
}
