package hostsvc

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

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
// connection context. In production, each plugin instance connects over a
// dedicated Unix domain socket so the 1:1 connection ↔ instance binding
// provides identity without per-RPC credentials (spec §8.4).
//
// Wiring is deferred to the loader/subprocess follow-up (#158). For now,
// NewServer accepts the interface so callers (and tests) can inject a fixed
// identity. A nil binder causes NewServer to panic — preferred over silent
// zero-instance behavior.
type InstanceBinder interface {
	InstanceIDFromContext(ctx context.Context) (instanceID string, ok bool)
}

// Server implements hostv1.HostServiceServer for the eight Tier-1 Host RPCs.
// Tier-2 RPCs (RunHistoryRead, UserDirectoryRead) fall through to the embedded
// UnimplementedHostServiceServer until parent issue #158 routes Tier-2 work.
type Server struct {
	hostv1.UnimplementedHostServiceServer

	q             Querier
	encryptionKey []byte
	resolver      CallContextResolver
	binder        InstanceBinder
	publisher     event.Publisher
	metrics       *pluginMetrics
}

// NewServer constructs a Server ready to be registered with a gRPC server.
// binder must be non-nil; pass a concrete implementation that maps UDS peer
// identity to plugin instance ID.
func NewServer(
	q Querier,
	encryptionKey []byte,
	resolver CallContextResolver,
	binder InstanceBinder,
	publisher event.Publisher,
) *Server {
	if binder == nil {
		panic("hostsvc.NewServer: binder must not be nil")
	}
	return &Server{
		q:             q,
		encryptionKey: encryptionKey,
		resolver:      resolver,
		binder:        binder,
		publisher:     publisher,
		metrics:       newPluginMetrics(),
	}
}
