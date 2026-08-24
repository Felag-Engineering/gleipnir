package hostsvc

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// EventTypeUnauthorizedCallContext is the plugin_audit_events.event_type value
// written when a plugin calls a call-scoped Host RPC from outside a valid call
// scope (i.e. from a background goroutine or after the call context was
// detached). It slots into the same event taxonomy as
// "plugin_tool_namespace_conflict".
const EventTypeUnauthorizedCallContext = "unauthorized_call_context"

// EventTypeUnauthorizedTier2Call is the plugin_audit_events.event_type value
// written when a plugin calls a Tier-2 RPC without declaring the corresponding
// capability in its manifest (spec §8.2).
const EventTypeUnauthorizedTier2Call = "unauthorized_tier2_call"

// EventTypeUnauthorizedRequestID is the plugin_audit_events.event_type value
// written when a plugin resolves a call_id that was not routed to the calling
// instance (spec §8.4), e.g. GetRunContext. Severity is always "high".
const EventTypeUnauthorizedRequestID = "unauthorized_request_id"

// EventTypeEventRateLimited is the plugin_audit_events.event_type value
// written (at most once per minute per instance) when the host-side EmitEvent
// rate limiter drops excess events from a plugin instance. The payload carries
// "drop_count" and "window_secs" so operators can assess misbehavior severity.
// Severity is "warning".
const EventTypeEventRateLimited = "event_rate_limited"

// EventTypeEmitEventRetiredProfile is the plugin_audit_events.event_type
// value written when EmitEvent refuses a call from an instance whose plugin
// carries a v2 manifest declaring profiles.event_source (issue #906): that
// plugin's events are supposed to ride io.gleipnir/events (events/listen),
// so a call here means its contract and its behavior disagree. Severity is
// always "high" — mirrors EventTypeUnauthorizedTier2Call's "a plugin doing
// something its manifest says it shouldn't is a record, not a log line"
// precedent. Coalesced at most once per auditFlushInterval per instance, the
// same rationale and window as EventTypeEventRateLimited.
const EventTypeEmitEventRetiredProfile = "emit_event_retired_profile"

// AuditQuerier is the narrow DB interface this package needs. A *db.Queries
// value satisfies it; the narrow interface makes tests cheaper to write.
type AuditQuerier interface {
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
}

// RejectIfDetached checks whether ctx carries a valid gleipnir-call-id. If it
// does, the function returns nil immediately.
//
// If the call ID is absent (no metadata, empty string, or the context was
// produced by serve.DetachContext on the plugin side), the function:
//  1. Inserts a plugin_audit_events row with event_type
//     "unauthorized_call_context" and severity "high".
//  2. Returns a gRPC PermissionDenied error with detail
//     "unauthorized_call_context".
//
// An audit-insert failure is logged at Warn but does not change the returned
// error — the call is still rejected.
//
// instanceID and rpcMethod are used to populate the audit row. They should be
// the plugin instance's database ID and the gRPC method name (e.g.
// "/gleipnir.plugin.host.v1.HostService/SomeRPC").
//
// Note: this helper is reserved for future RPCs that require call-scope
// binding (its former caller, WriteAuditStep, authenticated via
// request-ownership instead and left the Tier-1 surface entirely — #880). As
// of this PR it has zero callers in the codebase but is intentionally
// preserved.
//
// Spec reference: plugin-system-spec.md §8.5.
func RejectIfDetached(ctx context.Context, q AuditQuerier, instanceID, rpcMethod string) error {
	if _, present := CallIDFromContext(ctx); present {
		return nil
	}

	// No valid call ID — record the attempt and deny.
	payload, marshalErr := json.Marshal(map[string]string{"rpc_method": rpcMethod})
	if marshalErr != nil {
		payload = []byte("{}")
	}

	iid := instanceID
	_, insertErr := q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &iid,
		EventType:        EventTypeUnauthorizedCallContext,
		Severity:         "high",
		PayloadJson:      string(payload),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	})
	if insertErr != nil {
		// Non-fatal: the call is still denied, but operators should know the
		// audit trail has a gap — mirror the pattern in internal/plugin/tools/registrar.go.
		slog.WarnContext(ctx, "unauthorized_call_context: audit event insert failed",
			"instance_id", instanceID,
			"rpc_method", rpcMethod,
			"err", insertErr,
		)
	}

	return status.Error(codes.PermissionDenied, "unauthorized_call_context")
}

// RejectIfTier2NotDeclared enforces the Tier-2 capability gate (spec §8.2).
// It returns nil immediately when hasCapability is true.
//
// When hasCapability is false the function:
//  1. Inserts a plugin_audit_events row with event_type
//     "unauthorized_tier2_call" and severity "high".
//  2. Returns a gRPC PermissionDenied error with detail
//     "unauthorized_tier2_call".
//
// An audit-insert failure is logged at Warn but does not change the returned
// error — the call is still rejected (mirrors RejectIfDetached).
func RejectIfTier2NotDeclared(ctx context.Context, q AuditQuerier, instanceID, capability, rpcMethod string, hasCapability bool) error {
	if hasCapability {
		return nil
	}

	payload, marshalErr := json.Marshal(map[string]string{
		"rpc_method": rpcMethod,
		"capability": capability,
	})
	if marshalErr != nil {
		payload = []byte("{}")
	}

	iid := instanceID
	_, insertErr := q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &iid,
		EventType:        EventTypeUnauthorizedTier2Call,
		Severity:         "high",
		PayloadJson:      string(payload),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	})
	if insertErr != nil {
		slog.WarnContext(ctx, "unauthorized_tier2_call: audit event insert failed",
			"instance_id", instanceID,
			"rpc_method", rpcMethod,
			"capability", capability,
			"err", insertErr,
		)
	}

	return status.Error(codes.PermissionDenied, "unauthorized_tier2_call")
}
