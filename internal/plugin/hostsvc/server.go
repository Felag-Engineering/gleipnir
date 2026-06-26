package hostsvc

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// EmittedEvent is a plugin-emitted substrate event forwarded from the EmitEvent
// Host RPC to the trigger pipeline. It is declared in this package (not in
// internal/plugin/trigger) to avoid an import cycle: hostsvc cannot import the
// trigger package if trigger ends up importing hostsvc.
//
// The SinkAdapter in internal/plugin/trigger/sink.go converts EmittedEvent to
// the package-local trigger.Event when it calls Dispatcher.Handle.
type EmittedEvent struct {
	InstanceID  string
	PluginID    string
	EventKind   string
	EventID     string
	PayloadJSON []byte
	ObservedAt  time.Time
}

// TriggerSink receives plugin-emitted events forwarded from the EmitEvent Host
// RPC. Implemented by *trigger.SinkAdapter in internal/plugin/trigger.
//
// When the trigger sink is nil (default until SetTriggerSink is called),
// EmitEvent falls back to publisher-only behavior (SSE bus only). This nil
// window is correct: no plugin subprocess can emit events before
// Supervisor.StartAll is called, which happens after SetTriggerSink.
type TriggerSink interface {
	Handle(ctx context.Context, evt EmittedEvent) error
}

// Querier is the narrow DB interface required by Server. A *db.Queries value
// (or *db.InstrumentedQueries) satisfies it.
//
// The interface is local to the package (mirrors the AuditQuerier pattern in
// audit_guard.go) so tests can inject a cheap fake without wiring a real DB.
type Querier interface {
	AuditQuerier
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	UpdatePluginInstanceHealth(ctx context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error)
	CreateRunStep(ctx context.Context, arg db.CreateRunStepParams) (db.RunStep, error)
	GetLatestRunStep(ctx context.Context, runID string) (db.RunStep, error)
	GetFeedbackRequest(ctx context.Context, id string) (db.FeedbackRequest, error)
	UpdateFeedbackRequestStatus(ctx context.Context, arg db.UpdateFeedbackRequestStatusParams) (int64, error)
	GetRun(ctx context.Context, id string) (db.Run, error)
	GetPolicy(ctx context.Context, id string) (db.Policy, error)
	GetPluginPendingRequest(ctx context.Context, id string) (db.PluginPendingRequest, error)
	// Tier-2 RPC support
	GetPluginByID(ctx context.Context, id string) (db.Plugin, error)
	ListPolicies(ctx context.Context) ([]db.Policy, error)
	ListRunsByPolicy(ctx context.Context, arg db.ListRunsByPolicyParams) ([]db.ListRunsByPolicyRow, error)
	ListRunsByPolicies(ctx context.Context, arg db.ListRunsByPoliciesParams) ([]db.ListRunsByPoliciesRow, error)
	ListAllActiveUsersWithRoles(ctx context.Context) ([]db.ListAllActiveUsersWithRolesRow, error)
	ListActiveUsersByRole(ctx context.Context, role string) ([]db.ListActiveUsersByRoleRow, error)
	// Authz support
	GetUserBySlackUserID(ctx context.Context, slackUserID *string) ([]db.GetUserBySlackUserIDRow, error)
}

// CallContextResolver resolves (run_id, policy_id, instance_name) from a
// call_id. Satisfied by *dispatch.Pool via its LookupCall method.
//
// The hostsvc → dispatch import direction is safe: dispatch does not import
// hostsvc, so there is no import cycle.
type CallContextResolver interface {
	LookupCall(callID string) (dispatch.CallInfo, bool)
}

// InstanceBinder resolves the calling plugin instance ID from the gRPC
// request context. In production, the context value is set by
// UnaryInstanceTokenInterceptor, which verifies the gleipnir-instance-token
// metadata key against the in-memory identity.Registry on every incoming RPC
// (spec §8.4). NewContextBinder() returns the standard implementation.
//
// Tests may inject a fakeInstanceBinder to avoid needing a real registry.
// A nil binder causes NewServer to panic — preferred over silent
// zero-instance behavior.
type InstanceBinder interface {
	InstanceIDFromContext(ctx context.Context) (instanceID string, ok bool)
}

// ChannelResolver delivers a plugin-substrate feedback response to the
// in-flight Request waiter identified by requestID.  Satisfied by
// *dispatch.Dispatcher.  nil is a valid value for NewServer (see docs there).
type ChannelResolver interface {
	Resolve(ctx context.Context, requestID, responseJSON string) (resolved bool, err error)
}

// Server implements hostv1.HostServiceServer for all Host RPCs. Tier-1 RPCs
// are always available; Tier-2 RPCs (RunHistoryRead, UserDirectoryRead) require
// a matching capability declaration in the plugin manifest (spec §8.2).
type Server struct {
	hostv1.UnimplementedHostServiceServer

	q             Querier
	sqlDB         *sql.DB // used to open transactions in WriteAuditStep (native path)
	encryptionKey []byte
	resolver      CallContextResolver
	binder        InstanceBinder
	publisher     event.Publisher
	metrics       *pluginMetrics
	// channels is nil when the plugin substrate is disabled; WriteAuditStep
	// treats nil as "resolver unwired" and collapses into the late-callback path.
	channels ChannelResolver

	// triggerSink is set via SetTriggerSink after RunLauncher is constructed.
	// Protected by triggerSinkMu so the late-bind write from main.go and
	// concurrent EmitEvent reads do not race.
	// Nil means EmitEvent falls back to publisher-only (correct during the
	// startup gap before Supervisor.StartAll fires).
	triggerSinkMu sync.RWMutex
	triggerSink   TriggerSink

	// eventLimiter enforces the per-instance token-bucket rate limit on incoming
	// plugin events and coalesces "event_rate_limited" audit rows.
	eventLimiter *eventRateLimiter
}

// Register implements hostwire.HostServer by registering *Server as the
// HostService implementation on srv. This lets *Server satisfy the
// hostwire.HostServer interface so it can be returned directly from
// Manager.HostServerFor without a wrapper.
func (s *Server) Register(srv *grpc.Server) {
	hostv1.RegisterHostServiceServer(srv, s)
}

// SetTriggerSink wires the trigger dispatch path into EmitEvent. It is called
// once after RunLauncher is constructed (see main.go, analogous to
// connFactory.setManager). Until this call, EmitEvent falls back to
// publisher-only behavior, which is correct because no plugin subprocess has
// started a trigger stream yet.
//
// SetTriggerSink is safe to call concurrently with EmitEvent (protected by
// triggerSinkMu); it is single-use by convention.
func (s *Server) SetTriggerSink(sink TriggerSink) {
	s.triggerSinkMu.Lock()
	s.triggerSink = sink
	s.triggerSinkMu.Unlock()
}

// getTriggerSink returns the current trigger sink under a read lock. Returns
// nil if SetTriggerSink has not been called yet.
func (s *Server) getTriggerSink() TriggerSink {
	s.triggerSinkMu.RLock()
	defer s.triggerSinkMu.RUnlock()
	return s.triggerSink
}

// NewServer constructs a Server ready to be registered with a gRPC server.
// binder must be non-nil; pass a concrete implementation that resolves the
// caller's plugin instance ID from the request context (production wiring uses
// NewContextBinder paired with UnaryInstanceTokenInterceptor — see issue #202).
//
// sqlDB is the underlying *sql.DB used to open transactions in the native
// feedback path of WriteAuditStep (fixes #348). Pass nil only in unit tests
// that use a fake Querier and do not exercise concurrent writes — the
// transactional path is skipped when sqlDB is nil.
//
// channels may be nil (e.g. in tests that only exercise the native
// feedback_requests path). When nil and a plugin_pending_requests row is found
// for a WriteAuditStep request_id, the call is treated as a late callback with
// reason="resolver_unwired" and a warning is logged. Production wiring passes
// the *dispatch.Dispatcher.
func NewServer(
	q Querier,
	sqlDB *sql.DB,
	encryptionKey []byte,
	resolver CallContextResolver,
	binder InstanceBinder,
	publisher event.Publisher,
	channels ChannelResolver,
) *Server {
	if binder == nil {
		panic("hostsvc.NewServer: binder must not be nil")
	}
	return &Server{
		q:             q,
		sqlDB:         sqlDB,
		encryptionKey: encryptionKey,
		resolver:      resolver,
		binder:        binder,
		publisher:     publisher,
		metrics:       newPluginMetrics(),
		channels:      channels,
		eventLimiter:  newEventRateLimiter(),
	}
}
