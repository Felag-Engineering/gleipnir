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
// written when a plugin calls WriteAuditStep from outside a valid call scope
// (i.e. from a background goroutine or after the call context was detached).
// It slots into the same event taxonomy as "plugin_tool_namespace_conflict".
const EventTypeUnauthorizedCallContext = "unauthorized_call_context"

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
// "/gleipnir.plugin.host.v1.HostService/WriteAuditStep").
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
