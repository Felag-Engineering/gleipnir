// Package fixtures holds golden data that must outlive the code that wrote it.
//
// MUST-STAY-GREEN COMPAT FIXTURE for the v1 -> v2 plugin cutover (#963). It
// seeds run history exactly as the v1 gRPC plugin runtime recorded it, so the
// tests that read it back (internal/execution/run/history_compat_test.go and
// the RunDetail Vitest fixture) prove that history written under the old
// architecture still renders once that code is gone. The #1004 purge of the v1
// plugin packages must re-run those tests; a failure there means the purge
// broke operators' existing audit trail.
//
// This package deliberately imports only internal/db. It must never import a
// v1 plugin package (hostsvc, process, dispatch, identity, tools, trigger,
// decision, ...), or deleting them would delete the fixture with them. Every
// literal below (step shapes, event_type strings, the "plugin:<instance>@<gen>"
// source) is therefore copied, not referenced; do not "tidy" them into
// constants from live code, and do not update them when live code changes.
// They are frozen records of what production wrote.
package fixtures

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// GrpcEraRuns identifies the rows SeedGrpcEraRuns inserted.
type GrpcEraRuns struct {
	PolicyID string
	RunID    string

	InstanceID   string
	InstanceName string
	AudienceID   string

	ApprovalID string
	FeedbackID string

	// StepTypes lists the run_steps.type of the run, in step_number order.
	StepTypes []string

	// OrphanAuditEventType is the event type of the audit row whose
	// plugin_instance_id is NULL (the instance was later deleted).
	OrphanAuditEventType string

	// AuditEventCount is the number of plugin_audit_events rows inserted;
	// RunScopedAuditEventCount is how many of them carry the run's id.
	AuditEventCount          int
	RunScopedAuditEventCount int
}

const (
	grpcEraPolicyYAML = `
name: grpc-era-policy
trigger:
  type: webhook
  auth: none
agent:
  model: claude-opus-4-5
  task: "test task"
`
	grpcEraPluginID     = "plg-grpc-era"
	grpcEraInstanceID   = "pin-grpc-era"
	grpcEraInstanceName = "slack-ops"
	grpcEraAudienceID   = "aud-grpc-era"
	grpcEraEntryID      = "aen-grpc-era"
	grpcEraPolicyID     = "pol-grpc-era"
	grpcEraRunID        = "run-grpc-era"
	grpcEraApprovalID   = "apr-grpc-era"
	grpcEraFeedbackID   = "fbk-grpc-era"
	grpcEraPendingReqID = "ppr-grpc-era"
	grpcEraTimeoutReqID = "ppr-grpc-era-timeout"
)

type grpcEraStep struct {
	typ     string
	content any
}

// SeedGrpcEraRuns inserts one completed run whose history was written by the
// v1 gRPC plugin runtime, plus the plugin rows and plugin_audit_events that
// accompanied it. Raw SQL only: the INSERTs name columns as they stood at v1
// so a later refactor of an sqlc query cannot quietly change the fixture.
func SeedGrpcEraRuns(tb testing.TB, s *db.Store) GrpcEraRuns {
	tb.Helper()
	ctx := context.Background()
	const ts = "2026-03-10T12:00:00Z"

	exec := func(query string, args ...any) {
		tb.Helper()
		if _, err := s.DB().ExecContext(ctx, query, args...); err != nil {
			tb.Fatalf("grpc-era fixture: %v\nquery: %s", err, query)
		}
	}

	exec(`INSERT INTO policies(id, name, trigger_type, yaml, created_at, updated_at) VALUES (?, ?, 'webhook', ?, ?, ?)`,
		grpcEraPolicyID, "grpc-era-policy", grpcEraPolicyYAML, ts, ts)
	exec(`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, completed_at, created_at, model)
	      VALUES (?, ?, 'complete', 'webhook', '{"alert":"disk full"}', ?, ?, ?, 'claude-opus-4-5')`,
		grpcEraRunID, grpcEraPolicyID, ts, "2026-03-10T12:05:00Z", ts)

	exec(`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, created_at, updated_at)
	      VALUES (?, 'slack', '1.0.0', '{}', 'RWTfixturekey', 'active', ?, ?)`, grpcEraPluginID, ts, ts)
	exec(`INSERT INTO plugin_instances(id, plugin_id, instance_name, health_state, created_at, updated_at)
	      VALUES (?, ?, ?, 'healthy', ?, ?)`, grpcEraInstanceID, grpcEraPluginID, grpcEraInstanceName, ts, ts)
	exec(`INSERT INTO plugin_audiences(id, name, created_at, updated_at) VALUES (?, 'oncall', ?, ?)`,
		grpcEraAudienceID, ts, ts)
	exec(`INSERT INTO audience_entries(id, audience_id, plugin_instance_id, position, notify, request)
	      VALUES (?, ?, ?, 0, 1, 1)`, grpcEraEntryID, grpcEraAudienceID, grpcEraInstanceID)

	// The approval_requests row has no routing column; the plugin audience
	// route lives on plugin_pending_requests (one per dispatched Request).
	exec(`INSERT INTO approval_requests(id, run_id, tool_name, proposed_input, reasoning_summary, status, decided_at, expires_at, created_at)
	      VALUES (?, ?, 'slack-ops.delete_channel', '{"channel":"old-alerts"}', 'cleanup', 'approved', ?, ?, ?)`,
		grpcEraApprovalID, grpcEraRunID, "2026-03-10T12:02:00Z", "2026-03-10T13:00:00Z", ts)
	exec(`INSERT INTO plugin_pending_requests(id, plugin_instance_id, run_id, audience_entry_id, tool_name, status, response, expires_at, resolved_at, created_at)
	      VALUES (?, ?, ?, ?, 'slack-ops.delete_channel', 'resolved', 'approved', ?, ?, ?)`,
		grpcEraPendingReqID, grpcEraInstanceID, grpcEraRunID, grpcEraEntryID, "2026-03-10T13:00:00Z", "2026-03-10T12:02:00Z", ts)
	exec(`INSERT INTO feedback_requests(id, run_id, tool_name, proposed_input, message, status, response, resolved_at, expires_at, created_at)
	      VALUES (?, ?, 'gleipnir.ask_operator', '{}', 'Which channel should I archive?', 'resolved', 'old-alerts', ?, ?, ?)`,
		grpcEraFeedbackID, grpcEraRunID, "2026-03-10T12:03:00Z", "2026-03-10T12:33:00Z", ts)
	exec(`INSERT INTO plugin_pending_requests(id, plugin_instance_id, run_id, audience_entry_id, tool_name, status, expires_at, resolved_at, created_at)
	      VALUES (?, ?, ?, ?, 'gleipnir.ask_operator', 'timed_out', ?, ?, ?)`,
		grpcEraTimeoutReqID, grpcEraInstanceID, grpcEraRunID, grpcEraEntryID, "2026-03-10T12:04:00Z", "2026-03-10T12:04:00Z", ts)

	// The dispatcher's step writer stored every dispatcher failure as an
	// "error" step with the original step type folded into content.kind.
	steps := []grpcEraStep{
		{"capability_snapshot", map[string]any{
			"provider": "anthropic",
			"model":    "claude-opus-4-5",
			"tools": []map[string]any{
				{"server_name": "slack-ops", "tool_name": "post_message", "approval": "none", "timeout": 0, "on_timeout": "", "source": "plugin:slack-ops@7"},
				{"server_name": "slack-ops", "tool_name": "delete_channel", "approval": "required", "timeout": 3600000000000, "on_timeout": "fail", "source": "plugin:slack-ops@7"},
				{"server_name": "gleipnir", "tool_name": "ask_operator", "approval": "none", "timeout": 0, "on_timeout": ""},
			},
		}},
		{"thought", map[string]string{"text": "I will post a notice, then clean up the channel."}},
		{"tool_call", map[string]any{"tool_name": "slack-ops.post_message", "server_id": "slack-ops", "input": map[string]any{"channel": "ops", "text": "disk full"}}},
		{"tool_result", map[string]any{"tool_name": "slack-ops.post_message", "output": `{"ok":true,"ts":"1710072000.000100"}`, "is_error": false}},
		{"approval_request", map[string]any{"approval_id": grpcEraApprovalID, "tool": "slack-ops.delete_channel", "input": map[string]any{"channel": "old-alerts"}}},
		{"tool_call", map[string]any{"tool_name": "slack-ops.delete_channel", "server_id": "slack-ops", "input": map[string]any{"channel": "old-alerts"}}},
		{"tool_result", map[string]any{"tool_name": "slack-ops.delete_channel", "output": "channel archived", "is_error": false}},
		{"feedback_request", map[string]any{"feedback_id": grpcEraFeedbackID, "tool": "gleipnir.ask_operator", "message": "Which channel should I archive?", "expires_at": "2026-03-10T12:33:00Z"}},
		{"feedback_response", map[string]any{"feedback_id": grpcEraFeedbackID, "response": "old-alerts"}},
		{"error", map[string]any{
			"message":    "plugin pre-ack failed: instance unreachable",
			"code":       "feedback_dispatch_error",
			"kind":       "feedback_dispatch_error",
			"instance":   grpcEraInstanceName,
			"request_id": "ppr-grpc-era-preack",
		}},
		{"error", map[string]any{
			"message":    "plugin request timed out after 1m0s",
			"code":       "plugin_request_timeout",
			"kind":       "plugin_request_timeout",
			"request_id": grpcEraTimeoutReqID,
			"tool_name":  "gleipnir.ask_operator",
		}},
		{"complete", map[string]string{"message": "agent completed task"}},
	}

	stepTypes := make([]string, 0, len(steps))
	for i, st := range steps {
		content, err := json.Marshal(st.content)
		if err != nil {
			tb.Fatalf("grpc-era fixture: marshal %s step: %v", st.typ, err)
		}
		exec(`INSERT INTO run_steps(id, run_id, step_number, type, content, token_cost, created_at) VALUES (?, ?, ?, ?, ?, 0, ?)`,
			fmt.Sprintf("stp-grpc-era-%02d", i), grpcEraRunID, i, st.typ, string(content), ts)
		stepTypes = append(stepTypes, st.typ)
	}

	// event_type strings are the literals v1 code wrote. The last row has a
	// NULL instance (ON DELETE SET NULL after the instance was removed); the
	// run-scoped row is not a decision record, so the decisions endpoint must
	// skip it rather than choke on it.
	audits := []struct {
		instanceID any
		runID      any
		eventType  string
		severity   string
		payload    string
	}{
		{grpcEraInstanceID, nil, "plugin_installed", "info", `{"plugin_id":"plg-grpc-era","name":"slack","version":"1.0.0"}`},
		{grpcEraInstanceID, nil, "plugin_crashed", "high", `{"plugin_id":"plg-grpc-era","instance_id":"pin-grpc-era","error":"exit status 2","stderr_excerpt":"panic: boom"}`},
		{grpcEraInstanceID, nil, "plugin_tool_namespace_conflict", "warning", `{"tool":"slack-ops.post_message"}`},
		{grpcEraInstanceID, nil, "unauthorized_tier2_call", "high", `{"rpc":"EmitEvent"}`},
		{grpcEraInstanceID, nil, "event_rate_limited", "warning", `{"drop_count":12,"window_secs":60}`},
		{grpcEraInstanceID, grpcEraRunID, "unauthorized_call_context", "high", `{"rpc":"GetRunContext"}`},
		{nil, nil, "plugin_uninstalled", "info", `{"plugin_id":"plg-grpc-era-gone","name":"retired"}`},
	}
	runScoped := 0
	for i, a := range audits {
		if a.runID != nil {
			runScoped++
		}
		exec(`INSERT INTO plugin_audit_events(plugin_instance_id, event_type, severity, actor_user_id, payload_json, created_at, run_id)
		      VALUES (?, ?, ?, NULL, ?, ?, ?)`,
			a.instanceID, a.eventType, a.severity, a.payload, fmt.Sprintf("2026-03-10T12:%02d:30Z", i), a.runID)
	}

	return GrpcEraRuns{
		PolicyID:                 grpcEraPolicyID,
		RunID:                    grpcEraRunID,
		InstanceID:               grpcEraInstanceID,
		InstanceName:             grpcEraInstanceName,
		AudienceID:               grpcEraAudienceID,
		ApprovalID:               grpcEraApprovalID,
		FeedbackID:               grpcEraFeedbackID,
		StepTypes:                stepTypes,
		OrphanAuditEventType:     "plugin_uninstalled",
		AuditEventCount:          len(audits),
		RunScopedAuditEventCount: runScoped,
	}
}
