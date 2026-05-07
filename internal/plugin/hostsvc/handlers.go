package hostsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// resolveInstance resolves the calling plugin instance ID from the connection
// context and fetches the corresponding DB row. Returns Unauthenticated when
// the binder reports no identity, Internal on DB errors.
func (s *Server) resolveInstance(ctx context.Context) (db.PluginInstance, error) {
	iid, ok := s.binder.InstanceIDFromContext(ctx)
	if !ok {
		return db.PluginInstance{}, status.Error(codes.Unauthenticated, "no plugin instance identity on connection")
	}
	inst, err := s.q.GetPluginInstanceByID(ctx, iid)
	if err != nil {
		return db.PluginInstance{}, status.Errorf(codes.Internal, "fetch instance: %v", err)
	}
	return inst, nil
}

// latestStepNumber returns the step_number of the most recently inserted step
// for runID, or -1 when there are no steps (sql.ErrNoRows treated as 0 steps).
func (s *Server) latestStepNumber(ctx context.Context, runID string) (int64, error) {
	step, err := s.q.GetLatestRunStep(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No steps yet — the next step to be inserted is step 0.
			return -1, nil
		}
		return 0, fmt.Errorf("get latest run step: %w", err)
	}
	return step.StepNumber, nil
}

// writeAuditEvent inserts a plugin_audit_events row. Non-fatal: logs at Warn
// on failure (mirrors the pattern in audit_guard.go).
func (s *Server) writeAuditEvent(ctx context.Context, iid, eventType, severity string, payload map[string]string) {
	p, err := json.Marshal(payload)
	if err != nil {
		p = []byte("{}")
	}
	_, insertErr := s.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &iid,
		EventType:        eventType,
		Severity:         severity,
		PayloadJson:      string(p),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	})
	if insertErr != nil {
		slog.WarnContext(ctx, "audit event insert failed",
			"event_type", eventType,
			"instance_id", iid,
			"err", insertErr,
		)
	}
}

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
// mutation events are written by the admin credential lifecycle code (out of
// scope for this issue).
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

	plaintext, decryptErr := admin.Decrypt(s.encryptionKey, *inst.CredentialsEncrypted)
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

	_ = inst // identity already verified; not needed in response payload

	return &hostv1.GetRunContextResponse{
		RunId:     info.RunID,
		PolicyId:  info.PolicyID,
		StartedAt: run.StartedAt,
		StepIndex: stepIndex,
	}, nil
}

// WriteAuditStep writes a feedback_response step for the active run. All other
// step types are rejected (unauthorized_step_type, spec §8.1). The
// detached-context check (RejectIfDetached) runs first because it is the more
// fundamental guard.
func (s *Server) WriteAuditStep(ctx context.Context, req *hostv1.WriteAuditStepRequest) (*hostv1.WriteAuditStepResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	const rpcMethod = "/gleipnir.plugin.host.v1.HostService/WriteAuditStep"

	if err := RejectIfDetached(ctx, s.q, inst.ID, rpcMethod); err != nil {
		return nil, err
	}

	if req.GetStepType() != "feedback_response" {
		s.writeAuditEvent(ctx, inst.ID, "unauthorized_step_type", "high", map[string]string{
			"rpc_method": rpcMethod,
			"step_type":  req.GetStepType(),
		})
		return nil, status.Errorf(codes.PermissionDenied,
			"step_type %q is not permitted in v1; only feedback_response is allowed", req.GetStepType())
	}

	if req.GetRequestId() == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id must be set for feedback_response steps")
	}

	// Look up the feedback request; reject late responses without mutating state.
	fr, err := s.q.GetFeedbackRequest(ctx, req.GetRequestId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get feedback request: %v", err)
	}
	if fr.Status != "pending" {
		// Spec §4.2 line 106: record the late attempt and return ok=false.
		s.writeAuditEvent(ctx, inst.ID, "feedback_response_late", "medium", map[string]string{
			"request_id": req.GetRequestId(),
			"status":     fr.Status,
		})
		return &hostv1.WriteAuditStepResponse{
			Ok: false,
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
				Message: "feedback_response_late",
			},
		}, nil
	}

	// TODO (v1 security gap): validate that feedback_request.run_id →
	// runs.policy_id → policies.plugin_instance_id == inst.ID to confirm the
	// request belongs to a run owned by the calling instance. Any plugin knowing
	// a request_id can currently resolve another instance's request. Tracking
	// issue: TBD.

	// Determine the next step number.
	latestStep, err := s.latestStepNumber(ctx, fr.RunID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve step index: %v", err)
	}
	nextStep := latestStep + 1

	// Insert the run step and resolve the feedback request. A race with the
	// agent loop is acceptable under ADR-003 audit-write serialization — the
	// feedback resolution path in the main loop also writes a step and resolves
	// the request; whichever writer lands first wins (UpdateFeedbackRequestStatus
	// is guarded by AND status='pending').
	_, err = s.q.CreateRunStep(ctx, db.CreateRunStepParams{
		ID:         model.NewULID(),
		RunID:      fr.RunID,
		StepNumber: nextStep,
		Type:       "feedback_response",
		Content:    req.GetPayloadJson(),
		TokenCost:  0,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create run step: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	payloadJSON := req.GetPayloadJson()
	_, err = s.q.UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
		Status:     "responded",
		Response:   &payloadJSON,
		ResolvedAt: &now,
		ID:         req.GetRequestId(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update feedback request status: %v", err)
	}

	return &hostv1.WriteAuditStepResponse{Ok: true}, nil
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

	errCode, metricErr := s.metrics.set(req.GetName(), req.GetValue(), req.GetLabels(), inst.PluginID, inst.ID)
	if metricErr != nil {
		grpcCode := codes.InvalidArgument
		if errCode == "cardinality_cap_exceeded" {
			grpcCode = codes.ResourceExhausted
		}
		return nil, status.Errorf(grpcCode, "%s: %v", errCode, metricErr)
	}

	return &hostv1.EmitMetricResponse{Ok: true}, nil
}

// EmitEvent validates and publishes a plugin substrate event to the host's
// internal pub/sub bus. The event surfaces on the SSE stream for downstream
// consumers. Actual trigger-dispatch routing is deferred to the follow-up that
// owns the trigger dispatcher (parent #158).
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

	// TODO (#158): route the event to the trigger dispatcher so plugin Trigger
	// streams can fire policy runs. For now, publish on the SSE bus only so the
	// event is observable and the wire format is exercised.
	s.publisher.Publish("plugin.event_emitted", payload)

	logctx.Logger(ctx).InfoContext(ctx, "plugin event emitted",
		"plugin", inst.PluginID,
		"instance", inst.ID,
		"event_id", req.GetEventId(),
		"event_kind", req.GetEventKind(),
	)

	return &hostv1.EmitEventResponse{Ok: true}, nil
}

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
