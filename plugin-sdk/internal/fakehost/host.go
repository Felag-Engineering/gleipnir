// Package fakehost provides an in-process implementation of HostService for
// use during local plugin development and testing.
//
// The fake host records all inbound RPC calls under a mutex so tests can
// inspect what the plugin sent. It implements all Tier-1 HostService RPCs with
// configurable responses; Tier-2 RPCs return codes.Unimplemented.
//
// Both the `gleipnir-plugin run` CLI and the unit-test library (#172) share
// this implementation. It deliberately does NOT simulate signature
// verification, version mismatch, or a real LLM — it is a pure dev convenience
// tool, not a conformance harness.
package fakehost

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// Options configures a fake host instance.
type Options struct {
	// InstanceConfigJSON is returned verbatim by GetInstanceConfig.
	// Defaults to "{}".
	InstanceConfigJSON string

	// CredentialsJSON is returned verbatim by GetCredentials.
	// Defaults to "{}".
	CredentialsJSON string

	// RunContext is the run context returned by GetRunContext. If the zero
	// value is provided, sensible dev defaults are used (run_id="fake-run",
	// policy_id="fake-policy", step_index=0, started_at=now).
	RunContext RunContext

	// OnEmitEvent is an optional callback invoked synchronously on every
	// EmitEvent call before the response is returned. The capture runner uses
	// this to write JSONL to disk. The argument is the full EmitEventRequest.
	OnEmitEvent func(req *hostv1.EmitEventRequest)

	// Logger receives forwarded Log RPCs from the plugin. If nil, the default
	// slog logger is used.
	Logger *slog.Logger
}

// RunContext holds the fields returned by GetRunContext.
type RunContext struct {
	RunID      string
	PolicyID   string
	StepIndex  int64
	StartedAt  time.Time
}

// Host is an in-process implementation of hostv1.HostServiceServer. All
// recorded fields are guarded by mu. Create with New.
type Host struct {
	hostv1.UnimplementedHostServiceServer

	opts Options
	mu   sync.Mutex

	auditSteps  []*hostv1.WriteAuditStepRequest
	metrics     []*hostv1.EmitMetricRequest
	events      []*hostv1.EmitEventRequest
	logs        []*hostv1.LogRequest
	healthState *hostv1.SetHealthStateRequest
}

// New creates a new fake Host with the given options. Default values are
// applied for any zero-valued option fields.
func New(opts Options) *Host {
	if opts.InstanceConfigJSON == "" {
		opts.InstanceConfigJSON = "{}"
	}
	if opts.CredentialsJSON == "" {
		opts.CredentialsJSON = "{}"
	}
	if opts.RunContext.RunID == "" {
		opts.RunContext.RunID = "fake-run"
	}
	if opts.RunContext.PolicyID == "" {
		opts.RunContext.PolicyID = "fake-policy"
	}
	if opts.RunContext.StartedAt.IsZero() {
		opts.RunContext.StartedAt = time.Now()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Host{opts: opts}
}

// Register implements hostwire.HostServer. It registers this Host as the
// HostService implementation on the given gRPC server.
func (h *Host) Register(srv *grpc.Server) {
	hostv1.RegisterHostServiceServer(srv, h)
}

// ── Accessors ───────────────────────────────────────────────────────────────

// AuditSteps returns a copy of all WriteAuditStep requests received.
func (h *Host) AuditSteps() []*hostv1.WriteAuditStepRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*hostv1.WriteAuditStepRequest, len(h.auditSteps))
	copy(out, h.auditSteps)
	return out
}

// Metrics returns a copy of all EmitMetric requests received.
func (h *Host) Metrics() []*hostv1.EmitMetricRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*hostv1.EmitMetricRequest, len(h.metrics))
	copy(out, h.metrics)
	return out
}

// Events returns a copy of all EmitEvent requests received. This is the
// capture sink used by the --capture CLI mode.
func (h *Host) Events() []*hostv1.EmitEventRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*hostv1.EmitEventRequest, len(h.events))
	copy(out, h.events)
	return out
}

// Logs returns a copy of all Log requests received.
func (h *Host) Logs() []*hostv1.LogRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*hostv1.LogRequest, len(h.logs))
	copy(out, h.logs)
	return out
}

// HealthStates returns the most recent SetHealthState request, or nil if none
// has been received.
func (h *Host) HealthStates() *hostv1.SetHealthStateRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.healthState
}

// ── Tier-1 RPC implementations ───────────────────────────────────────────────

// GetInstanceConfig returns opts.InstanceConfigJSON.
func (h *Host) GetInstanceConfig(_ context.Context, _ *hostv1.GetInstanceConfigRequest) (*hostv1.GetInstanceConfigResponse, error) {
	return &hostv1.GetInstanceConfigResponse{
		ConfigJson: h.opts.InstanceConfigJSON,
	}, nil
}

// GetCredentials returns opts.CredentialsJSON.
func (h *Host) GetCredentials(_ context.Context, _ *hostv1.GetCredentialsRequest) (*hostv1.GetCredentialsResponse, error) {
	return &hostv1.GetCredentialsResponse{
		CredentialsJson: h.opts.CredentialsJSON,
	}, nil
}

// GetRunContext returns opts.RunContext formatted as a GetRunContextResponse.
func (h *Host) GetRunContext(_ context.Context, _ *hostv1.GetRunContextRequest) (*hostv1.GetRunContextResponse, error) {
	rc := h.opts.RunContext
	return &hostv1.GetRunContextResponse{
		RunId:     rc.RunID,
		PolicyId:  rc.PolicyID,
		StepIndex: rc.StepIndex,
		StartedAt: rc.StartedAt.Format(time.RFC3339),
	}, nil
}

// WriteAuditStep validates that step_type is "feedback_response". Any other
// value is rejected with codes.PermissionDenied / detail
// "unauthorized_step_type". Production hosts MUST mirror this contract.
func (h *Host) WriteAuditStep(_ context.Context, req *hostv1.WriteAuditStepRequest) (*hostv1.WriteAuditStepResponse, error) {
	if req.GetStepType() != "feedback_response" {
		return nil, status.Error(codes.PermissionDenied, "unauthorized_step_type")
	}
	h.mu.Lock()
	h.auditSteps = append(h.auditSteps, req)
	h.mu.Unlock()
	return &hostv1.WriteAuditStepResponse{Ok: true}, nil
}

// EmitMetric records the metric request under the mutex.
func (h *Host) EmitMetric(_ context.Context, req *hostv1.EmitMetricRequest) (*hostv1.EmitMetricResponse, error) {
	h.mu.Lock()
	h.metrics = append(h.metrics, req)
	h.mu.Unlock()
	return &hostv1.EmitMetricResponse{Ok: true}, nil
}

// EmitEvent records the event request under the mutex. If opts.OnEmitEvent is
// set it is invoked synchronously before the response is returned. This is the
// tap point for --capture mode.
func (h *Host) EmitEvent(_ context.Context, req *hostv1.EmitEventRequest) (*hostv1.EmitEventResponse, error) {
	h.mu.Lock()
	h.events = append(h.events, req)
	cb := h.opts.OnEmitEvent
	h.mu.Unlock()

	if cb != nil {
		cb(req)
	}

	return &hostv1.EmitEventResponse{Ok: true}, nil
}

// Log forwards the log request to opts.Logger and records it.
func (h *Host) Log(_ context.Context, req *hostv1.LogRequest) (*hostv1.LogResponse, error) {
	h.mu.Lock()
	h.logs = append(h.logs, req)
	h.mu.Unlock()

	level := slogLevel(req.GetLevel())
	h.opts.Logger.Log(context.Background(), level, req.GetMsg())

	return &hostv1.LogResponse{Ok: true}, nil
}

// SetHealthState records the most recent health state.
func (h *Host) SetHealthState(_ context.Context, req *hostv1.SetHealthStateRequest) (*hostv1.SetHealthStateResponse, error) {
	h.mu.Lock()
	h.healthState = req
	h.mu.Unlock()
	return &hostv1.SetHealthStateResponse{Ok: true}, nil
}

// ── Tier-2 RPC stubs ─────────────────────────────────────────────────────────

// RunHistoryRead returns Unimplemented. Tier-2 RPCs require manifest
// declaration and admin approval; the fake host does not implement them.
func (h *Host) RunHistoryRead(_ context.Context, _ *hostv1.RunHistoryReadRequest) (*hostv1.RunHistoryReadResponse, error) {
	return nil, status.Error(codes.Unimplemented, "fake host: tier-2 stubbed")
}

// UserDirectoryRead returns Unimplemented. Tier-2 RPCs require manifest
// declaration and admin approval; the fake host does not implement them.
func (h *Host) UserDirectoryRead(_ context.Context, _ *hostv1.UserDirectoryReadRequest) (*hostv1.UserDirectoryReadResponse, error) {
	return nil, status.Error(codes.Unimplemented, "fake host: tier-2 stubbed")
}

// ── helpers ──────────────────────────────────────────────────────────────────

// slogLevel converts a hostv1.LogLevel to the corresponding slog.Level.
func slogLevel(l hostv1.LogLevel) slog.Level {
	switch l {
	case hostv1.LogLevel_LOG_LEVEL_DEBUG:
		return slog.LevelDebug
	case hostv1.LogLevel_LOG_LEVEL_INFO:
		return slog.LevelInfo
	case hostv1.LogLevel_LOG_LEVEL_WARN:
		return slog.LevelWarn
	case hostv1.LogLevel_LOG_LEVEL_ERROR:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
