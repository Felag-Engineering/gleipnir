// Package tools handles plugin tool registration into the cross-source
// namespace arbiter. When a plugin instance starts, it calls RegisterInstanceTools
// to claim its dot-names. A conflict transitions the instance to unhealthy and
// records a plugin_audit_events row so operators can diagnose the issue.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// PluginAuditQuerier is the narrow DB interface required by this package. It
// combines the state-machine queries (from pluginstate.Querier) with the audit
// event insert so a single *db.Queries value satisfies both — mirroring how
// the installer in internal/plugin/loader/install.go uses *db.Queries directly.
type PluginAuditQuerier interface {
	pluginstate.Querier
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
}

// Registrar manages plugin tool namespace reservations on behalf of a plugin
// instance. It is safe for concurrent use (the arbiter handles its own locking;
// generation tracking uses its own mu).
type Registrar struct {
	arbiter     *toolregistry.Registry
	q           PluginAuditQuerier
	pub         event.Publisher
	mu          sync.Mutex
	generations map[string]int64 // keyed by instanceName; process-local (not persisted)
}

// New returns a Registrar wired to the given arbiter, DB querier, and event
// publisher. The publisher may be nil; audit events are written to DB regardless.
func New(arbiter *toolregistry.Registry, q PluginAuditQuerier, pub event.Publisher) *Registrar {
	return &Registrar{
		arbiter:     arbiter,
		q:           q,
		pub:         pub,
		generations: make(map[string]int64),
	}
}

// RegisterInstanceTools attempts to claim each "instanceName.toolName" dot-name
// in the arbiter for the given plugin instance. If any name is already owned by
// a different source, the instance is transitioned to unhealthy, a
// plugin_audit_events row is inserted with event_type
// "plugin_tool_namespace_conflict", and the conflict error is returned.
//
// On success, the arbiter holds all reservations until UnregisterInstance is called,
// and the provided generation is recorded so agent runners can detect stale captures
// via Generation().
func (r *Registrar) RegisterInstanceTools(ctx context.Context, instanceID, instanceName string, toolNames []string, generation int64) error {
	if len(toolNames) == 0 {
		return nil
	}

	src := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: instanceName}
	entries := make([]toolregistry.Reservation, len(toolNames))
	for i, name := range toolNames {
		entries[i] = toolregistry.Reservation{
			DotName: toolregistry.DotName(instanceName, name),
			Owner:   src,
		}
	}

	if err := r.arbiter.ReserveBulk(entries); err != nil {
		var ce *toolregistry.ConflictError
		if !errors.As(err, &ce) {
			return fmt.Errorf("reserve tool namespace for plugin %q: %w", instanceName, err)
		}

		// Audit the conflict so operators can see which plugin's start was blocked.
		payload := conflictPayload{
			DotName:       ce.DotName,
			ExistingOwner: ce.Existing.String(),
		}
		payloadJSON, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			payloadJSON = []byte("{}")
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		iid := instanceID
		if _, auditErr := r.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
			PluginInstanceID: &iid,
			EventType:        "plugin_tool_namespace_conflict",
			Severity:         "high",
			PayloadJson:      string(payloadJSON),
			CreatedAt:        now,
		}); auditErr != nil {
			// Non-fatal — we still report the conflict — but log so operators know the audit trail has a gap.
			slog.WarnContext(ctx, "plugin tool namespace conflict: audit event insert failed", "instance_id", instanceID, "err", auditErr)
		}

		// Drive the instance to unhealthy. This is now a legal transition from any
		// pending_* state (see internal/plugin/state/pluginstate.go, #194).
		detail := fmt.Sprintf("tool namespace conflict: %s is already registered to %s",
			ce.DotName, ce.Existing.String())
		if stateErr := pluginstate.SetHealthState(
			ctx, r.q, r.pub, instanceID,
			pluginstate.OriginHost,
			model.PluginHealthStateUnhealthy,
			detail,
		); stateErr != nil {
			// Non-fatal for the caller — the audit row is the primary record.
			slog.WarnContext(ctx, "plugin tool namespace conflict: failed to transition instance to unhealthy", "instance_id", instanceID, "err", stateErr)
		}

		return fmt.Errorf("plugin %q: %w", instanceName, err)
	}

	r.mu.Lock()
	r.generations[instanceName] = generation
	r.mu.Unlock()

	return nil
}

// Generation returns the active generation for the named plugin instance, or
// (0, false) if the instance is not currently registered. Agent runners call
// this at tool-call time to detect when a plugin has been replaced since the
// capability snapshot was taken.
func (r *Registrar) Generation(instanceName string) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gen, ok := r.generations[instanceName]
	return gen, ok
}

// UnregisterInstance releases all arbiter reservations owned by the given
// instance name and removes its generation record. Safe to call even if the
// instance was never registered or already unregistered.
func (r *Registrar) UnregisterInstance(_ context.Context, instanceName string) {
	r.arbiter.ReleaseAllFor(toolregistry.Source{Kind: toolregistry.KindPlugin, Name: instanceName})

	r.mu.Lock()
	delete(r.generations, instanceName)
	r.mu.Unlock()
}

// conflictPayload is the JSON structure written to plugin_audit_events.payload_json
// for a "plugin_tool_namespace_conflict" event.
type conflictPayload struct {
	DotName       string `json:"dot_name"`
	ExistingOwner string `json:"existing_owner"`
}
