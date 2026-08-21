package hostsvc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// maxLogMsgBytes is the per-RPC hard cap on the Log msg field. 4 KiB covers
// any reasonable structured log line including context and stack excerpts.
const maxLogMsgBytes = 4 * 1024

// maxLogAttrs is the maximum number of key/value pairs a single Log RPC may
// carry. Unbounded attrs let a plugin override correlation keys such as run_id
// by appending many attrs; 32 is well above any legitimate use.
const maxLogAttrs = 32

// maxLogAttrBytes is the per-key and per-value byte cap inside a Log attrs
// map. 256 bytes accommodates ULIDs, UUIDs, short messages, and most
// structured metadata. Values that exceed this are signs of misuse.
const maxLogAttrBytes = 256

// GetInstanceConfig returns the instance's config_json verbatim. No audit event;
// reads are logged at Debug via slog only (spec §8.1 "no audit").
func (s *Server) GetInstanceConfig(ctx context.Context, _ *hostv1.GetInstanceConfigRequest) (*hostv1.GetInstanceConfigResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	logctx.Logger(ctx).DebugContext(ctx, "GetInstanceConfig called",
		"plugin", inst.PluginID,
		"instance", inst.ID,
	)
	return &hostv1.GetInstanceConfigResponse{ConfigJson: inst.ConfigJson}, nil
}

// GetCredentials returns decrypted credentials. No in-process cache — the DB
// is hit on every call per spec §9.4 (pull-only). No audit event; credential
// mutation events are written by the admin credential lifecycle code.
//
// Returns the encrypted-column plaintext as-is; plugins decode the JSON
// against their declared Strategy (see plugin-sdk/credentials for typed
// helpers — #226).
func (s *Server) GetCredentials(ctx context.Context, _ *hostv1.GetCredentialsRequest) (*hostv1.GetCredentialsResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	if inst.CredentialsEncrypted == nil {
		// Empty credentials is a valid state (instance has no credentials configured).
		logctx.Logger(ctx).DebugContext(ctx, "GetCredentials: no credentials configured",
			"plugin", inst.PluginID,
			"instance", inst.ID,
		)
		return &hostv1.GetCredentialsResponse{}, nil
	}

	plaintext, decryptErr := crypto.Decrypt(s.encryptionKey, *inst.CredentialsEncrypted)
	if decryptErr != nil {
		return nil, status.Errorf(codes.Internal, "decrypt credentials: %v", decryptErr)
	}

	logctx.Logger(ctx).DebugContext(ctx, "GetCredentials called",
		"plugin", inst.PluginID,
		"instance", inst.ID,
	)
	return &hostv1.GetCredentialsResponse{CredentialsJson: plaintext}, nil
}

// GetRunContext returns run/policy/step context for the active call. Requires a
// valid call_id in context (set by UnaryCallIDInterceptor).
func (s *Server) GetRunContext(ctx context.Context, _ *hostv1.GetRunContextRequest) (*hostv1.GetRunContextResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	callID, ok := CallIDFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "GetRunContext requires a valid call_id in metadata")
	}

	info, ok := s.resolver.LookupCall(callID)
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "call_id %q is not currently in-flight", callID)
	}

	// Verify the resolved call belongs to the authenticated instance. Without
	// this check any authenticated plugin could supply a foreign call_id and
	// read run/policy context belonging to a different instance.
	if info.InstanceName != inst.InstanceName {
		const rpcMethod = "/gleipnir.plugin.host.v1.HostService/GetRunContext"
		s.writeAuditEvent(ctx, inst.ID, EventTypeUnauthorizedRequestID, "high", map[string]string{
			"call_id":    callID,
			"run_id":     info.RunID,
			"rpc_method": rpcMethod,
		})
		return nil, status.Error(codes.PermissionDenied, "unauthorized_request_id")
	}

	latestStep, err := s.latestStepNumber(ctx, info.RunID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve step index: %v", err)
	}
	// latestStep == -1 means no steps yet; next index = 0.
	stepIndex := latestStep + 1

	run, err := s.q.GetRun(ctx, info.RunID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "fetch run: %v", err)
	}

	return &hostv1.GetRunContextResponse{
		RunId:     info.RunID,
		PolicyId:  info.PolicyID,
		StartedAt: run.StartedAt,
		StepIndex: stepIndex,
	}, nil
}

// EmitMetric records a plugin-emitted metric in Prometheus. The host
// force-prefixes "gleipnir_plugin_" and auto-injects "plugin" and "instance"
// labels. Label cardinality beyond cardinalityCap causes loud rejection with
// codes.ResourceExhausted (spec §8.1).
func (s *Server) EmitMetric(ctx context.Context, req *hostv1.EmitMetricRequest) (*hostv1.EmitMetricResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	errCode, metricErr := s.metrics.Set(req.GetName(), req.GetValue(), req.GetLabels(), inst.PluginID, inst.ID)
	if metricErr != nil {
		grpcCode := codes.InvalidArgument
		if errCode == "cardinality_cap_exceeded" || errCode == "metric_name_cap_exceeded" {
			grpcCode = codes.ResourceExhausted
		}
		return nil, status.Errorf(grpcCode, "%s: %v", errCode, metricErr)
	}

	return &hostv1.EmitMetricResponse{Ok: true}, nil
}

// EmitEvent validates and publishes a plugin substrate event to the host's
// internal pub/sub bus and forwards it to the trigger dispatcher.
//
// Context handling note (spec §8.5): EmitEvent does NOT require a call_id.
// Trigger streams are not scoped to a particular run; the interceptor chain
// already accepts requests without gleipnir-call-id metadata, and only
// WriteAuditStep calls RejectIfDetached. Host RPCs lacking a valid call_id
// are accepted and logged with plugin/instance labels only.
//
// Identity is still enforced via UnaryInstanceTokenInterceptor and generation
// drain semantics still apply via UnaryGenerationRefcountInterceptor.
func (s *Server) EmitEvent(ctx context.Context, req *hostv1.EmitEventRequest) (*hostv1.EmitEventResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	if req.GetEventId() == "" {
		return nil, status.Error(codes.InvalidArgument, "event_id must not be empty")
	}
	if req.GetEventKind() == "" {
		return nil, status.Error(codes.InvalidArgument, "event_kind must not be empty")
	}
	if len(req.GetPayloadJson()) > maxPayloadJSONBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"payload_json exceeds maximum size of %d bytes", maxPayloadJSONBytes)
	}

	// Rate-limit gate: drop excess events before running any per-policy match
	// scan so a runaway plugin cannot exhaust host resources. Returns Ok=false
	// (not a gRPC error) so the plugin does not retry-storm (spec §4.3).
	allowed, flushCount := s.eventLimiter.Allow(inst.PluginID, inst.ID, inst.HostEventRatePerSec, inst.HostEventBurst)
	if !allowed {
		eventDroppedCounter.WithLabelValues(inst.PluginID, inst.ID, metrics.ReasonRateLimit).Inc()
		if flushCount > 0 {
			// Coalesced audit row: at most one per minute per instance.
			s.writeAuditEvent(ctx, inst.ID, EventTypeEventRateLimited, "warning", map[string]string{
				"plugin_id":   inst.PluginID,
				"reason":      metrics.ReasonRateLimit,
				"drop_count":  strconv.FormatUint(flushCount, 10),
				"window_secs": strconv.FormatFloat(auditFlushInterval.Seconds(), 'f', 0, 64),
			})
		}
		logctx.Logger(ctx).WarnContext(ctx, "EmitEvent: dropped by rate limiter",
			"plugin", inst.PluginID,
			"instance", inst.ID,
			"event_id", req.GetEventId(),
		)
		return &hostv1.EmitEventResponse{Ok: false}, nil
	}

	payload, marshalErr := json.Marshal(map[string]string{
		"event_id":    req.GetEventId(),
		"event_kind":  req.GetEventKind(),
		"plugin_id":   inst.PluginID,
		"instance_id": inst.ID,
		"payload":     req.GetPayloadJson(),
	})
	if marshalErr != nil {
		return nil, status.Errorf(codes.Internal, "marshal event payload: %v", marshalErr)
	}

	// Unconditionally publish to the SSE bus so real-time consumers always
	// see plugin.event_emitted regardless of trigger-sink wiring.
	s.publisher.Publish("plugin.event_emitted", payload)

	// Forward to the trigger dispatcher when one has been wired via
	// SetTriggerSink. When nil (the startup gap before RunLauncher is ready),
	// fall through to publisher-only behavior.
	if sink := s.getTriggerSink(); sink != nil {
		evt := EmittedEvent{
			InstanceID:  inst.ID,
			PluginID:    inst.PluginID,
			EventKind:   req.GetEventKind(),
			EventID:     req.GetEventId(),
			PayloadJSON: []byte(req.GetPayloadJson()),
			ObservedAt:  timeNow(),
		}
		if handleErr := sink.Handle(ctx, evt); handleErr != nil {
			// Log at Warn but do not fail the RPC — the plugin's emit should
			// succeed even if one dispatch cycle errors (e.g. DB unavailable).
			logctx.Logger(ctx).WarnContext(ctx, "EmitEvent: trigger sink Handle error",
				"plugin", inst.PluginID,
				"instance", inst.ID,
				"event_id", req.GetEventId(),
				"err", handleErr,
			)
		}
	}

	logctx.Logger(ctx).InfoContext(ctx, "plugin event emitted",
		"plugin", inst.PluginID,
		"instance", inst.ID,
		"event_id", req.GetEventId(),
		"event_kind", req.GetEventKind(),
	)

	return &hostv1.EmitEventResponse{Ok: true}, nil
}

// timeNow is a package-level variable so tests can substitute a fixed clock.
var timeNow = func() time.Time { return time.Now() }

// Log routes a structured log line through the host's slog pipeline. When the
// request carries a resolvable call_id, the log record is enriched with
// run_id and policy_id via logctx so it correlates with the run's trace. When
// call_id is absent or unresolvable, the record carries plugin+instance only
// (spec §8.5).
func (s *Server) Log(ctx context.Context, req *hostv1.LogRequest) (*hostv1.LogResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	if len(req.GetMsg()) > maxLogMsgBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"msg exceeds maximum size of %d bytes", maxLogMsgBytes)
	}
	if len(req.GetAttrs()) > maxLogAttrs {
		return nil, status.Errorf(codes.InvalidArgument,
			"attrs map exceeds maximum of %d entries", maxLogAttrs)
	}
	for k, v := range req.GetAttrs() {
		if len(k) > maxLogAttrBytes {
			return nil, status.Errorf(codes.InvalidArgument,
				"attr key exceeds maximum size of %d bytes", maxLogAttrBytes)
		}
		if len(v) > maxLogAttrBytes {
			return nil, status.Errorf(codes.InvalidArgument,
				"attr value exceeds maximum size of %d bytes", maxLogAttrBytes)
		}
	}

	level := protoLevelToSlog(req.GetLevel())

	// Build base attrs that are always present.
	attrs := []slog.Attr{
		slog.String("plugin", inst.PluginID),
		slog.String("instance", inst.ID),
	}

	// Enrich with run correlation when call_id resolves.
	logCtx := ctx
	callID, hasCallID := CallIDFromContext(ctx)
	if hasCallID {
		if info, ok := s.resolver.LookupCall(callID); ok {
			logCtx = logctx.WithRunCorrelation(ctx, info.RunID, info.PolicyID)

			// Determine current step index for the log record.
			latestStep, stepErr := s.latestStepNumber(ctx, info.RunID)
			if stepErr == nil {
				attrs = append(attrs, slog.Int64("step_index", latestStep+1))
			}
			attrs = append(attrs, slog.String("call_id", callID))
		}
	}

	// Append plugin-supplied key/value pairs (string-only, per proto).
	for k, v := range req.GetAttrs() {
		attrs = append(attrs, slog.String(k, v))
	}

	logctx.Logger(logCtx).LogAttrs(logCtx, level, req.GetMsg(), attrs...)

	return &hostv1.LogResponse{Ok: true}, nil
}

// SetHealthState lets the plugin self-report its health. The host enforces the
// §8.1 "plugin can only worsen itself" rule inside state.SetHealthState — a
// no-op report (plugin tries to improve its own state) returns nil from
// SetHealthState and the handler returns success without writing to the DB.
func (s *Server) SetHealthState(ctx context.Context, req *hostv1.SetHealthStateRequest) (*hostv1.SetHealthStateResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	mapped, mapErr := mapProtoHealthState(req.GetState())
	if mapErr != nil {
		return nil, mapErr
	}

	stateErr := pluginstate.SetHealthState(
		ctx,
		s.q,
		s.publisher,
		inst.ID,
		pluginstate.OriginPluginSelf,
		mapped,
		req.GetDetail(),
	)
	if stateErr != nil {
		if errors.Is(stateErr, pluginstate.ErrIllegalTransition) {
			return nil, status.Errorf(codes.InvalidArgument, "illegal health state transition: %v", stateErr)
		}
		if errors.Is(stateErr, pluginstate.ErrTransitionConflict) {
			return nil, status.Errorf(codes.Aborted, "health state transition conflict (retry): %v", stateErr)
		}
		return nil, status.Errorf(codes.Internal, "set health state: %v", stateErr)
	}

	return &hostv1.SetHealthStateResponse{Ok: true}, nil
}

// mapProtoHealthState converts the proto PluginHealthState enum to the model
// domain type. UNSPECIFIED is rejected; UNAVAILABLE maps to Unhealthy because
// CircuitBroken is a host-detected concept (spec §8.1; the proto comment says
// "transient, will auto-recover" which is closest to Unhealthy in model terms).
func mapProtoHealthState(s hostv1.PluginHealthState) (model.PluginHealthState, error) {
	switch s {
	case hostv1.PluginHealthState_PLUGIN_HEALTH_STATE_HEALTHY:
		return model.PluginHealthStateHealthy, nil
	case hostv1.PluginHealthState_PLUGIN_HEALTH_STATE_UNAVAILABLE:
		// UNAVAILABLE = "transient; will auto-recover". The closest model state
		// is Unhealthy (severity 3). CircuitBroken (severity 4) is host-detected;
		// a plugin reporting UNAVAILABLE does not trip the circuit breaker itself.
		return model.PluginHealthStateUnhealthy, nil
	case hostv1.PluginHealthState_PLUGIN_HEALTH_STATE_UNHEALTHY:
		return model.PluginHealthStateUnhealthy, nil
	default:
		// UNSPECIFIED and any unknown future values are rejected.
		return "", status.Errorf(codes.InvalidArgument, "health state UNSPECIFIED is not reportable by plugins")
	}
}

// protoLevelToSlog converts the proto LogLevel enum to the slog.Level that the
// host routes through. Unspecified levels default to Info.
func protoLevelToSlog(l hostv1.LogLevel) slog.Level {
	switch l {
	case hostv1.LogLevel_LOG_LEVEL_DEBUG:
		return slog.LevelDebug
	case hostv1.LogLevel_LOG_LEVEL_WARN:
		return slog.LevelWarn
	case hostv1.LogLevel_LOG_LEVEL_ERROR:
		return slog.LevelError
	default:
		// INFO and UNSPECIFIED both route to Info.
		return slog.LevelInfo
	}
}
