package assembly

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// lateKicker is the reconciler wake-up the admin surface holds before the
// reconciler exists. Kick before OperatorListenerReady is a no-op: the boot
// pass reads the same desired state the caller just wrote.
type lateKicker struct {
	mu     sync.Mutex
	target interface{ Kick() }
}

func (k *lateKicker) set(target interface{ Kick() }) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.target = target
}

func (k *lateKicker) Kick() {
	k.mu.Lock()
	target := k.target
	k.mu.Unlock()
	if target != nil {
		target.Kick()
	}
}

// lateEvents is the events supervisor as the admin surface sees it. The
// supervisor is built in BindLauncher, which may run after the admin surface
// was handed this, so it is resolved per call; with no supervisor bound every
// call is a no-op.
type lateEvents struct{ a *Assembly }

func (l lateEvents) current() eventStreams {
	l.a.mu.Lock()
	defer l.a.mu.Unlock()
	return l.a.events
}

func (l lateEvents) Start(ctx context.Context, instanceID string) {
	if s := l.current(); s != nil {
		s.Start(ctx, instanceID)
	}
}

func (l lateEvents) Restart(ctx context.Context, instanceID string) {
	if s := l.current(); s != nil {
		s.Restart(ctx, instanceID)
	}
}

func (l lateEvents) Stop(instanceID string) {
	if s := l.current(); s != nil {
		s.Stop(instanceID)
	}
}

// toolDiscoverer discovers a registered server's tools into mcp_tools.
// *mcp.Registry satisfies it.
type toolDiscoverer interface {
	RefreshTools(ctx context.Context, serverID string) (mcp.ToolDiff, error)
}

// managedServerStore is the read the endpoint wrapper needs to learn a row's
// name before the row is deleted. *db.Queries satisfies it.
type managedServerStore interface {
	GetMCPServerByPluginInstance(ctx context.Context, pluginInstanceID *string) (db.McpServer, error)
}

// managedEndpoints is the attach pass's view of the managed registry
// (reconciler.ManagedEndpoints). It adds to *mcp.ManagedRegistrar the two
// things that registrar leaves to its caller:
//
//   - Register discovers the endpoint's tools. A registered row with no
//     mcp_tools is a plugin whose every grant fails at run start, and the
//     attach pass never registers the same URL twice, so this is the only
//     moment discovery happens without an operator pressing refresh.
//   - Deregister releases the instance's tool-namespace reservations. Deleting
//     the row cascades to mcp_tools in the database, but the arbiter is
//     in-memory and nothing else tells it.
type managedEndpoints struct {
	registrar *mcp.ManagedRegistrar
	servers   managedServerStore
	tools     toolDiscoverer
	arbiter   *toolregistry.Registry
}

func (m *managedEndpoints) Register(ctx context.Context, ep mcp.ManagedEndpoint) (db.McpServer, error) {
	srv, err := m.registrar.Register(ctx, ep)
	if err != nil {
		return db.McpServer{}, err
	}
	// The row is already the routing flip; a plugin that cannot list its tools
	// yet is a fault to surface, not a reason to withdraw the endpoint.
	if _, err := m.tools.RefreshTools(ctx, srv.ID); err != nil {
		slog.WarnContext(ctx, "managed endpoint registered but tool discovery failed; refresh it from the MCP server page",
			"instance_id", ep.InstanceID, "server_id", srv.ID, "err", err)
	}
	return srv, nil
}

func (m *managedEndpoints) Deregister(ctx context.Context, instanceID string) error {
	name, found, err := m.nameOf(ctx, instanceID)
	if err != nil {
		return err
	}
	if err := m.registrar.Deregister(ctx, instanceID); err != nil {
		return err
	}
	if found && m.arbiter != nil {
		m.arbiter.ReleaseAllFor(toolregistry.Source{Kind: toolregistry.KindMCP, Name: name})
	}
	return nil
}

func (m *managedEndpoints) nameOf(ctx context.Context, instanceID string) (string, bool, error) {
	srv, err := m.servers.GetMCPServerByPluginInstance(ctx, &instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("looking up managed endpoint for instance %s: %w", instanceID, err)
	}
	return srv.Name, true, nil
}

// namespaceReleaser is admin.ToolUnregistrar for the container substrate.
// InstanceLifecycle.Delete calls it after the instance row is gone. By then
// the managed mcp_servers row and its mcp_tools have been removed by the
// foreign-key cascade, so all that is left to release is the in-memory
// reservation, which is keyed by the instance name.
type namespaceReleaser struct{ arbiter *toolregistry.Registry }

func (r namespaceReleaser) UnregisterInstance(_ context.Context, instanceName string) {
	if r.arbiter == nil {
		return
	}
	r.arbiter.ReleaseAllFor(toolregistry.Source{Kind: toolregistry.KindMCP, Name: instanceName})
}

// startupFaultStore is the generation history an instance's startup fault is
// derived from. *db.Queries satisfies it.
type startupFaultStore interface {
	GetPluginContainerByInstance(ctx context.Context, pluginInstanceID string) (db.PluginContainer, error)
	ListContainerGenerationsByInstance(ctx context.Context, pluginInstanceID string) ([]db.PluginContainerGeneration, error)
}

// startupFaults answers the admin instance detail's `startup` field from the
// same durable rows the reconciler's mint backoff reads, so what an operator is
// shown is what the reconciler is acting on. It writes nothing: instance health
// keeps its one writer, the capability prober.
type startupFaults struct{ store startupFaultStore }

func (s startupFaults) InstanceStartup(ctx context.Context, instanceID string) (*admin.InstanceStartupFault, error) {
	desired, err := s.store.GetPluginContainerByInstance(ctx, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		// No desired row means no container is wanted, so none can fail to
		// start.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading desired container for instance %s: %w", instanceID, err)
	}
	history, err := s.store.ListContainerGenerationsByInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("listing generations for instance %s: %w", instanceID, err)
	}

	fault, ok := reconciler.DescribeStartupFault(history, desired)
	if !ok {
		return nil, nil
	}
	out := &admin.InstanceStartupFault{
		Failures:          fault.Failures,
		LastError:         fault.LastError,
		LastGeneration:    fault.LastGeneration,
		LastFailedAt:      fault.LastFailedAt,
		NextAttemptAt:     fault.NextAttemptAt,
		AttemptInProgress: fault.AttemptInProgress,
	}
	if fault.ServingGeneration != 0 {
		serving := fault.ServingGeneration
		out.ServingGeneration = &serving
	}
	return out, nil
}
