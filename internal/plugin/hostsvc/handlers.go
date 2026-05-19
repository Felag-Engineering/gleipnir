package hostsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
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

// WriteAuditStep accepts only `feedback_response`. It MUST carry a request_id.
// Authorization is by request-ownership (spec §8.5 exemption): the request_id
// row's plugin_instance_id (plugin substrate path) or the policy's tool grants
// matching `<instance_name>.` (native path) must match the calling instance.
// The detached-context check does not apply — request-ownership is the §8.5
// alternative auth primitive.
func (s *Server) WriteAuditStep(ctx context.Context, req *hostv1.WriteAuditStepRequest) (*hostv1.WriteAuditStepResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	const rpcMethod = "/gleipnir.plugin.host.v1.HostService/WriteAuditStep"

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

	requestID := req.GetRequestId()
	payloadJSON := req.GetPayloadJson()

	// Plugin-substrate path: look up plugin_pending_requests first. On
	// sql.ErrNoRows, fall through to the native feedback_requests path below.
	pendingReq, perr := s.q.GetPluginPendingRequest(ctx, requestID)
	if perr == nil {
		// Row exists — this is a plugin-substrate feedback_response.
		if pendingReq.PluginInstanceID != inst.ID {
			// Wrong instance: ownership violation.
			s.writeAuditEvent(ctx, inst.ID, EventTypeUnauthorizedRequestID, "high", map[string]string{
				"request_id": requestID,
				"run_id":     pendingReq.RunID,
				"rpc_method": rpcMethod,
			})
			return nil, status.Error(codes.PermissionDenied, "unauthorized_request_id")
		}

		if s.channels == nil {
			// Resolver not wired — treat as late callback to avoid silent drop.
			slog.WarnContext(ctx, "plugin channel resolver not wired; treating as late callback",
				"request_id", requestID,
			)
			s.writeAuditEvent(ctx, inst.ID, EventTypeFeedbackResponseLate, "warning", map[string]string{
				"request_id": requestID,
				"reason":     "resolver_unwired",
				"substrate":  "plugin",
				"status":     pendingReq.Status,
			})
			return &hostv1.WriteAuditStepResponse{
				Ok: false,
				Error: &commonv1.ErrorEnvelope{
					Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
					Message: EventTypeFeedbackResponseLate,
				},
			}, nil
		}

		resolved, rerr := s.channels.Resolve(ctx, requestID, payloadJSON)
		if rerr != nil && !errors.Is(rerr, dispatch.ErrUnknownRequestID) {
			return nil, status.Errorf(codes.Internal, "resolve plugin request: %v", rerr)
		}

		if !resolved {
			reason := "late"
			if errors.Is(rerr, dispatch.ErrUnknownRequestID) {
				// The in-memory waiter was evicted (server restart or scanner race)
				// but the DB row exists, so the request is no longer actionable.
				reason = "evicted_waiter"
			}
			s.writeAuditEvent(ctx, inst.ID, EventTypeFeedbackResponseLate, "warning", map[string]string{
				"request_id": requestID,
				"reason":     reason,
				"substrate":  "plugin",
				"status":     pendingReq.Status,
			})
			return &hostv1.WriteAuditStepResponse{
				Ok: false,
				Error: &commonv1.ErrorEnvelope{
					Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
					Message: EventTypeFeedbackResponseLate,
				},
			}, nil
		}

		// CAS won: publish SSE event and return ok=true. The agent loop's
		// Dispatcher.Wait writes its own audit step; we do not duplicate it here.
		if eventData, marshalErr := json.Marshal(map[string]string{
			"run_id":      pendingReq.RunID,
			"request_id":  requestID,
			"instance_id": inst.ID,
			"step_type":   "feedback_response",
		}); marshalErr == nil {
			s.publisher.Publish("plugin.feedback_response_written", eventData)
		}
		return &hostv1.WriteAuditStepResponse{Ok: true}, nil
	}
	if !errors.Is(perr, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "get plugin pending request: %v", perr)
	}
	// sql.ErrNoRows: not a plugin-substrate request — fall through to native path.

	// Look up the feedback request; reject late responses without mutating state.
	fr, err := s.q.GetFeedbackRequest(ctx, requestID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get feedback request: %v", err)
	}
	if fr.Status != "pending" {
		// Spec §4.2 line 106: record the late attempt and return ok=false.
		s.writeAuditEvent(ctx, inst.ID, EventTypeFeedbackResponseLate, "warning", map[string]string{
			"request_id": requestID,
			"status":     fr.Status,
		})
		return &hostv1.WriteAuditStepResponse{
			Ok: false,
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
				Message: EventTypeFeedbackResponseLate,
			},
		}, nil
	}

	// Verify that the feedback_request's run belongs to a policy that grants
	// tools to the calling instance (i.e. has at least one capabilities.tools
	// entry with the prefix "<instanceName>."). This is the heuristic available
	// today; audience-based routing (audiences.plugin_instance per spec §4.2/§6)
	// is a follow-up — no audiences DB schema exists yet.
	run, err := s.q.GetRun(ctx, fr.RunID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get run for request_id scope check: %v", err)
	}
	policy, err := s.q.GetPolicy(ctx, run.PolicyID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get policy for request_id scope check: %v", err)
	}
	if !policyGrantsInstance(policy.Yaml, inst.InstanceName) {
		s.writeAuditEvent(ctx, inst.ID, EventTypeUnauthorizedRequestID, "high", map[string]string{
			"request_id": requestID,
			"run_id":     fr.RunID,
			"rpc_method": rpcMethod,
		})
		return nil, status.Error(codes.PermissionDenied, "unauthorized_request_id")
	}

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
		Content:    payloadJSON,
		TokenCost:  0,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create run step: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.q.UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
		Status:     "resolved",
		Response:   &payloadJSON,
		ResolvedAt: &now,
		ID:         requestID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update feedback request status: %v", err)
	}

	// Publish so SSE subscribers and tests can observe the step.
	if eventData, marshalErr := json.Marshal(map[string]string{
		"run_id":      fr.RunID,
		"request_id":  requestID,
		"instance_id": inst.ID,
		"step_type":   "feedback_response",
	}); marshalErr == nil {
		s.publisher.Publish("plugin.feedback_response_written", eventData)
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

	// Rate-limit gate: drop excess events before running any per-policy match
	// scan so a runaway plugin cannot exhaust host resources. Returns Ok=false
	// (not a gRPC error) so the plugin does not retry-storm (spec §4.3).
	allowed, flushCount := s.eventLimiter.Allow(inst.PluginID, inst.ID)
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

// ── Tier-2 helpers ────────────────────────────────────────────────────────────

// hasTier2Capability checks whether the plugin manifest for inst's parent
// plugin declares the given Tier-2 capability. It reads manifest_snapshot fresh
// per call — no caching — so hot-reload invalidation (spec §5.4) is automatic.
//
// Returns (false, Internal) on manifest parse error.
func (s *Server) hasTier2Capability(ctx context.Context, inst db.PluginInstance, capability string) (bool, error) {
	plugin, err := s.q.GetPluginByID(ctx, inst.PluginID)
	if err != nil {
		return false, status.Errorf(codes.Internal, "fetch plugin: %v", err)
	}

	var m sdkmanifest.Manifest
	if err := yaml.Unmarshal([]byte(plugin.ManifestSnapshot), &m); err != nil {
		return false, status.Errorf(codes.Internal, "parse manifest snapshot: %v", err)
	}

	return m.HasTier2(capability), nil
}

// scopeProbe is a minimal struct for scanning the fields out of a policy YAML
// blob that determine whether the policy references the calling instance.
// We extract only what is needed to avoid a dependency on internal/policy,
// which would create an import cycle.
type scopeProbe struct {
	Capabilities struct {
		Tools []struct {
			Tool string `yaml:"tool"`
		} `yaml:"tools"`
	} `yaml:"capabilities"`
	Trigger struct {
		Type      string `yaml:"type"`
		Source    string `yaml:"source"`
		EventKind string `yaml:"event_kind"`
	} `yaml:"trigger"`
}

// policyGrantsInstance reports whether the policy YAML blob grants at least one
// tool whose name begins with "<instanceName>.". Used to verify that a
// feedback_request's run belongs to a policy scoped to the calling instance.
func policyGrantsInstance(policyYAML, instanceName string) bool {
	var probe scopeProbe
	if err := yaml.Unmarshal([]byte(policyYAML), &probe); err != nil {
		// Unparseable policy YAML → treat as no match (reject).
		return false
	}
	prefix := instanceName + "."
	for _, t := range probe.Capabilities.Tools {
		if strings.HasPrefix(t.Tool, prefix) {
			return true
		}
	}
	return false
}

// policyIDsForInstance returns the IDs of policies that reference inst via
// tool grants (capabilities.tools contains an entry with the prefix
// "<instanceName>.") OR via a subscribed trigger (trigger.type == "subscribed"
// and trigger.source == instanceName). A policy reachable through both paths
// appears exactly once.
func (s *Server) policyIDsForInstance(ctx context.Context, inst db.PluginInstance) ([]string, error) {
	policies, err := s.q.ListPolicies(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list policies: %v", err)
	}

	prefix := inst.InstanceName + "."
	var ids []string
	for _, pol := range policies {
		var probe scopeProbe
		if err := yaml.Unmarshal([]byte(pol.Yaml), &probe); err != nil {
			// Corrupt policy YAML is not a reason to fail the RPC — skip it.
			continue
		}

		matched := false
		for _, t := range probe.Capabilities.Tools {
			if strings.HasPrefix(t.Tool, prefix) {
				matched = true
				break
			}
		}
		if !matched &&
			probe.Trigger.Type == string(model.TriggerTypeSubscribed) &&
			probe.Trigger.Source == inst.InstanceName {
			matched = true
		}
		if matched {
			ids = append(ids, pol.ID)
		}
	}
	return ids, nil
}

// ── Tier-2 RPCs ───────────────────────────────────────────────────────────────

// RunHistoryRead returns past runs for policies associated with the calling
// plugin instance. Requires the "run_history_read" Tier-2 capability declared
// in the plugin manifest (spec §8.2).
func (s *Server) RunHistoryRead(ctx context.Context, req *hostv1.RunHistoryReadRequest) (*hostv1.RunHistoryReadResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	const rpcMethod = "/gleipnir.plugin.host.v1.HostService/RunHistoryRead"

	hasCap, err := s.hasTier2Capability(ctx, inst, sdkmanifest.Tier2RunHistoryRead)
	if err != nil {
		return nil, err
	}
	if err := RejectIfTier2NotDeclared(ctx, s.q, inst.ID, sdkmanifest.Tier2RunHistoryRead, rpcMethod, hasCap); err != nil {
		return nil, err
	}

	scopedIDs, err := s.policyIDsForInstance(ctx, inst)
	if err != nil {
		return nil, err
	}

	// If the caller requested a specific policy, intersect with the scoped set.
	// Return an empty list — not an error — when the policy is not in scope so we
	// do not leak the existence of policies the instance doesn't own.
	if req.GetPolicyId() != "" {
		var filtered []string
		for _, id := range scopedIDs {
			if id == req.GetPolicyId() {
				filtered = append(filtered, id)
				break
			}
		}
		scopedIDs = filtered
	}

	// Clamp limit: default to 100 when ≤ 0, hard cap at 100.
	limit := int64(req.GetLimit())
	if limit <= 0 || limit > 100 {
		limit = 100
	}

	// Fetch per-policy rows and merge them. Because each SQL call returns rows
	// ordered by created_at DESC, we merge all slices and sort once at the end.
	// Worst case: len(scopedIDs)*100 rows in memory before truncation.
	var merged []db.ListRunsByPolicyRow
	for _, policyID := range scopedIDs {
		rows, err := s.q.ListRunsByPolicy(ctx, db.ListRunsByPolicyParams{
			PolicyID: policyID,
			Limit:    limit,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list runs for policy %s: %v", policyID, err)
		}
		merged = append(merged, rows...)
	}

	// Sort merged results by created_at DESC, matching the SQL ORDER BY.
	// RFC3339 strings sort lexicographically so string comparison is correct.
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].CreatedAt > merged[j].CreatedAt
	})

	if int64(len(merged)) > limit {
		merged = merged[:limit]
	}

	summaries := make([]*hostv1.RunSummary, 0, len(merged))
	for _, r := range merged {
		finishedAt := ""
		if r.CompletedAt != nil {
			finishedAt = *r.CompletedAt
		}
		summaries = append(summaries, &hostv1.RunSummary{
			RunId:      r.ID,
			PolicyId:   r.PolicyID,
			Status:     r.Status,
			StartedAt:  r.StartedAt,
			FinishedAt: finishedAt,
		})
	}

	return &hostv1.RunHistoryReadResponse{Runs: summaries}, nil
}

// UserDirectoryRead returns user and role information for all active users.
// Requires the "user_directory_read" Tier-2 capability declared in the plugin
// manifest (spec §8.2). Only id, username, and role are returned — no
// passwords, session tokens, or deactivation metadata.
func (s *Server) UserDirectoryRead(ctx context.Context, req *hostv1.UserDirectoryReadRequest) (*hostv1.UserDirectoryReadResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	const rpcMethod = "/gleipnir.plugin.host.v1.HostService/UserDirectoryRead"

	hasCap, err := s.hasTier2Capability(ctx, inst, sdkmanifest.Tier2UserDirectoryRead)
	if err != nil {
		return nil, err
	}
	if err := RejectIfTier2NotDeclared(ctx, s.q, inst.ID, sdkmanifest.Tier2UserDirectoryRead, rpcMethod, hasCap); err != nil {
		return nil, err
	}

	var entries []*hostv1.UserEntry

	if req.GetRoleFilter() == "" {
		rows, err := s.q.ListAllActiveUsersWithRoles(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list users: %v", err)
		}
		for _, r := range rows {
			entries = append(entries, &hostv1.UserEntry{
				UserId:   r.UserID,
				Username: r.Username,
				Role:     r.Role,
			})
		}
	} else {
		rows, err := s.q.ListActiveUsersByRole(ctx, req.GetRoleFilter())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list users by role: %v", err)
		}
		// ListActiveUsersByRoleRow has no Role field, so we stamp it from the
		// request (the WHERE clause already filters to this role).
		for _, r := range rows {
			entries = append(entries, &hostv1.UserEntry{
				UserId:   r.UserID,
				Username: r.Username,
				Role:     req.GetRoleFilter(),
			})
		}
	}

	return &hostv1.UserDirectoryReadResponse{Users: entries}, nil
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
