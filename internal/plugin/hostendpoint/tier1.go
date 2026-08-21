package hostendpoint

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/pluginmetrics"
)

// The six kept Tier-1 host methods (spec §8 inventory), ported from
// internal/plugin/hostsvc's gRPC handlers (#877). WriteAuditStep and
// EmitEvent are deliberately absent — they are removed from the inventory,
// and their retirement sequencing is #880's decision, not this port's.

// Log caps, carried over from hostsvc unchanged: 4 KiB covers any reasonable
// structured line; 32 attrs is well above legitimate use (unbounded attrs
// would let a plugin bury the correlation keys); 256 bytes per key/value
// accommodates ULIDs and short metadata.
const (
	maxLogMsgBytes  = 4 * 1024
	maxLogAttrs     = 32
	maxLogAttrBytes = 256
)

// EventTypeUnauthorizedRequestID mirrors hostsvc's audit event of the same
// name: a plugin presented a call id that resolves to another instance's
// in-flight call.
const EventTypeUnauthorizedRequestID = "unauthorized_request_id"

// Tier1Querier is the slice of the sqlc surface the Tier-1 tools need.
// *db.Queries satisfies it.
type Tier1Querier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	GetRun(ctx context.Context, id string) (db.Run, error)
	GetLatestRunStep(ctx context.Context, runID string) (db.RunStep, error)
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
}

// CallResolver resolves (run_id, policy_id, instance_name) from a call id.
// Satisfied by *dispatch.Pool.
type CallResolver interface {
	LookupCall(callID string) (dispatch.CallInfo, bool)
}

// Tier1Deps carries the collaborators the six handlers share.
type Tier1Deps struct {
	Querier Tier1Querier

	// EncryptionKey decrypts plugin_instances.credentials_encrypted for
	// host/get_credentials. May be nil on hosts with no encryption key
	// configured; get_credentials then fails rather than returning
	// ciphertext.
	EncryptionKey []byte

	// Calls resolves run context from the Gleipnir-Call-Id the middleware
	// attached (#876). Optional: when nil, host/get_run_context reports no
	// in-flight call and host/log skips run correlation.
	Calls CallResolver

	// Metrics is the shared ADR-047 guard behind host/emit_metric.
	Metrics *pluginmetrics.Metrics

	// Health is the per-capability registry host/set_health_state records
	// into (#814): a capability fault narrows routing, nothing else.
	Health *caphealth.Registry
}

// Tier1Tools returns the six kept Tier-1 methods as host-endpoint tool
// definitions, ready for Server.Register.
func Tier1Tools(deps Tier1Deps) []ToolDef {
	return []ToolDef{
		{
			Name:        ToolGetInstanceConfig,
			Description: "Return the calling instance's config_json verbatim.",
			Handler:     deps.getInstanceConfig,
		},
		{
			Name:        ToolGetCredentials,
			Description: "Return the calling instance's decrypted credentials JSON.",
			Handler:     deps.getCredentials,
		},
		{
			Name:        ToolGetRunContext,
			Description: "Return run/policy/step context for the call identified by Gleipnir-Call-Id.",
			Handler:     deps.getRunContext,
		},
		{
			Name:        ToolEmitMetric,
			Description: "Record a plugin-emitted gauge metric (gleipnir_plugin_ prefixed, plugin/instance labels auto-injected).",
			Handler:     deps.emitMetric,
		},
		{
			Name:        ToolLog,
			Description: "Route a structured log line through the host's logging pipeline with run correlation when available.",
			Handler:     deps.log,
		},
		{
			Name:        ToolSetHealthState,
			Description: "Self-report health for one declared capability (profile plus optional name).",
			Handler:     deps.setHealthState,
		},
	}
}

// resolveInstance fetches the authenticated caller's instance row. Identity
// is established by the middleware chain (#876); a missing identity here
// means the Server was mounted without Chain, which is a wiring bug worth a
// distinct message.
func (d Tier1Deps) resolveInstance(ctx context.Context) (db.PluginInstance, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return db.PluginInstance{}, &ToolError{Code: "unauthenticated", Message: "no plugin instance identity on request"}
	}
	inst, err := d.Querier.GetPluginInstanceByID(ctx, id.InstanceID)
	if err != nil {
		return db.PluginInstance{}, &ToolError{Code: "internal", Message: fmt.Sprintf("fetch instance: %v", err)}
	}
	return inst, nil
}

// getInstanceConfig returns config_json verbatim. No audit event; reads are
// logged at Debug only (spec §8.1 "no audit").
func (d Tier1Deps) getInstanceConfig(ctx context.Context, _ json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	logctx.Logger(ctx).DebugContext(ctx, "host/get_instance_config called",
		"plugin", inst.PluginID, "instance", inst.ID)
	return map[string]any{"config_json": inst.ConfigJson}, nil
}

// getCredentials returns the decrypted credentials blob. It exists — rather
// than being subsumed by the egress proxy's header injection — because
// header injection cannot cover streams: a plugin holding a long-lived
// substrate connection (a Slack Socket Mode websocket, an IMAP session)
// needs the standing credential itself, not a header the host attaches to
// each individual request. That is the question a future reader will ask
// before deleting this, so it is answered here (spec §8).
//
// No in-process cache — the DB is hit on every call per spec §9.4
// (pull-only). No audit event; credential mutations are audited by the admin
// credential lifecycle code.
func (d Tier1Deps) getCredentials(ctx context.Context, _ json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	if inst.CredentialsEncrypted == nil {
		// No credentials configured is a valid state, not an error.
		return map[string]any{"credentials_json": ""}, nil
	}
	if len(d.EncryptionKey) == 0 {
		return nil, &ToolError{Code: "internal", Message: "host has no encryption key configured; cannot decrypt credentials"}
	}
	plaintext, decryptErr := crypto.Decrypt(d.EncryptionKey, *inst.CredentialsEncrypted)
	if decryptErr != nil {
		return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("decrypt credentials: %v", decryptErr)}
	}
	return map[string]any{"credentials_json": plaintext}, nil
}

// getRunContext resolves the caller's active call into run/policy/step
// context. The ownership check is the load-bearing part: without it, any
// authenticated plugin could supply a foreign call id and read another
// instance's run context — so a foreign call id is refused AND audited at
// high severity, exactly as the gRPC handler did.
func (d Tier1Deps) getRunContext(ctx context.Context, _ json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	callID, ok := CallIDFromContext(ctx)
	if !ok {
		return nil, &ToolError{Code: "failed_precondition", Message: "host/get_run_context requires a Gleipnir-Call-Id header"}
	}
	if d.Calls == nil {
		return nil, &ToolError{Code: "failed_precondition", Message: fmt.Sprintf("call_id %q is not currently in-flight", callID)}
	}
	info, ok := d.Calls.LookupCall(callID)
	if !ok {
		return nil, &ToolError{Code: "failed_precondition", Message: fmt.Sprintf("call_id %q is not currently in-flight", callID)}
	}
	if info.InstanceName != inst.InstanceName {
		d.writeAuditEvent(ctx, inst.ID, EventTypeUnauthorizedRequestID, "high", map[string]string{
			"call_id": callID,
			"run_id":  info.RunID,
			"tool":    ToolGetRunContext,
		})
		return nil, &ToolError{Code: "permission_denied", Message: EventTypeUnauthorizedRequestID}
	}

	latestStep, err := d.latestStepNumber(ctx, info.RunID)
	if err != nil {
		return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("resolve step index: %v", err)}
	}
	run, err := d.Querier.GetRun(ctx, info.RunID)
	if err != nil {
		return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("fetch run: %v", err)}
	}
	return map[string]any{
		"run_id":     info.RunID,
		"policy_id":  info.PolicyID,
		"started_at": run.StartedAt,
		"step_index": latestStep + 1,
	}, nil
}

type emitMetricArgs struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels"`
}

// emitMetric records a plugin-emitted gauge through the shared ADR-047 guard
// (internal/plugin/pluginmetrics): forced gleipnir_plugin_ prefix,
// auto-injected plugin/instance labels, the 100-distinct-value cardinality
// cap, and inconsistent-label-set rejection all live there, shared with the
// gRPC path until the cutover deletes it.
func (d Tier1Deps) emitMetric(ctx context.Context, args json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	var a emitMetricArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if errCode, metricErr := d.Metrics.Set(a.Name, a.Value, a.Labels, inst.PluginID, inst.ID); metricErr != nil {
		return nil, &ToolError{Code: errCode, Message: metricErr.Error()}
	}
	return map[string]any{"ok": true}, nil
}

type logArgs struct {
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs"`
}

// log routes a structured line through the host's slog pipeline. Run
// correlation attaches when the request's call id resolves; otherwise the
// record carries plugin+instance only — ADR-047's
// structured-host-RPC-not-stdout rationale, preserved across the transport.
func (d Tier1Deps) log(ctx context.Context, args json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	var a logArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if len(a.Msg) > maxLogMsgBytes {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("msg exceeds maximum size of %d bytes", maxLogMsgBytes)}
	}
	if len(a.Attrs) > maxLogAttrs {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("attrs map exceeds maximum of %d entries", maxLogAttrs)}
	}
	for k, v := range a.Attrs {
		if len(k) > maxLogAttrBytes || len(v) > maxLogAttrBytes {
			return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("attr key/value exceeds maximum size of %d bytes", maxLogAttrBytes)}
		}
	}

	attrs := []slog.Attr{
		slog.String("plugin", inst.PluginID),
		slog.String("instance", inst.ID),
	}
	logCtx := ctx
	if callID, hasCallID := CallIDFromContext(ctx); hasCallID && d.Calls != nil {
		if info, ok := d.Calls.LookupCall(callID); ok {
			logCtx = logctx.WithRunCorrelation(ctx, info.RunID, info.PolicyID)
			if latestStep, stepErr := d.latestStepNumber(ctx, info.RunID); stepErr == nil {
				attrs = append(attrs, slog.Int64("step_index", latestStep+1))
			}
			attrs = append(attrs, slog.String("call_id", callID))
		}
	}
	for k, v := range a.Attrs {
		attrs = append(attrs, slog.String(k, v))
	}
	logctx.Logger(logCtx).LogAttrs(logCtx, logLevel(a.Level), a.Msg, attrs...)
	return map[string]any{"ok": true}, nil
}

type setHealthStateArgs struct {
	Profile    string `json:"profile"`
	Capability string `json:"capability"`
	State      string `json:"state"`
	Detail     string `json:"detail"`
}

// setHealthState is where the port changes behaviour on purpose: health is
// per CAPABILITY, not per instance. The v1.1 RPC marked the whole instance,
// so one missing OAuth scope took every other capability out of routing —
// this is the fix for that defect, not a port of it (#814). A report names a
// declared profile plus an optional capability name below it, and narrows
// routing for exactly that surface.
//
// The §8.1 "plugin can only mark itself worse" rule carries over, applied
// per capability inside caphealth.SelfReportCapability: an improvement
// report is accepted as a no-op ({ok:true, applied:false}) rather than
// written — recovery is the host's observation to make, because a plugin
// that could self-clear a fault could mask one.
func (d Tier1Deps) setHealthState(ctx context.Context, args json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	var a setHealthStateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	profile := caphealth.Profile(a.Profile)
	if !profile.Valid() {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("unknown capability profile %q", a.Profile)}
	}
	var state model.PluginHealthState
	switch a.State {
	case string(model.PluginHealthStateHealthy):
		state = model.PluginHealthStateHealthy
	case string(model.PluginHealthStateUnhealthy):
		state = model.PluginHealthStateUnhealthy
	default:
		// The plugin-reportable vocabulary is deliberately the two states a
		// plugin can honestly claim about itself. Everything else in the
		// model vocabulary (circuit_broken, the pending_* family, inactive)
		// is a HOST or ADMIN verdict a plugin must not be able to assert.
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("state %q is not plugin-reportable (healthy|unhealthy)", a.State)}
	}

	applied := d.Health.SelfReportCapability(inst.ID, caphealth.Entry{
		Capability: caphealth.Capability{Profile: profile, Name: a.Capability},
		State:      state,
		Detail:     a.Detail,
	})
	return map[string]any{"ok": true, "applied": applied}, nil
}

// latestStepNumber mirrors hostsvc: the latest step_number, or -1 with no
// steps yet (so the next index is 0).
func (d Tier1Deps) latestStepNumber(ctx context.Context, runID string) (int64, error) {
	step, err := d.Querier.GetLatestRunStep(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return -1, nil
		}
		return 0, fmt.Errorf("get latest run step: %w", err)
	}
	return step.StepNumber, nil
}

// writeAuditEvent inserts a plugin_audit_events row, non-fatally: an audit
// insert failure is logged, never allowed to mask the rejection it records.
func (d Tier1Deps) writeAuditEvent(ctx context.Context, iid, eventType, severity string, payload map[string]string) {
	p, err := json.Marshal(payload)
	if err != nil {
		p = []byte("{}")
	}
	if _, insertErr := d.Querier.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &iid,
		EventType:        eventType,
		Severity:         severity,
		PayloadJson:      string(p),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}); insertErr != nil {
		slog.WarnContext(ctx, "audit event insert failed",
			"event_type", eventType, "instance_id", iid, "err", insertErr)
	}
}

// logLevel maps the wire level string to slog. Unknown and empty default to
// Info, mirroring the gRPC enum mapping.
func logLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
