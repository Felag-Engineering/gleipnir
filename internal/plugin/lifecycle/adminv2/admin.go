package adminv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
)

// This file holds the v2 (container substrate) implementations of the narrow
// interfaces internal/admin declares for the v1 subprocess substrate:
// PluginProcessManager, TriggerRestarter, InflightCounter, ToolConnEvictor and
// RSSAggregator. Each takes the collaborator it drives as a small interface so
// tests use fakes; the v2 assembly injects the real reconciler, events
// supervisor, MCP registry and stats collector. Nothing here is wired into
// main.go yet.

// Kicker nudges the reconciler to run a pass now. *reconciler.Reconciler
// satisfies it. Kick is non-blocking and coalescing, so it is safe on an
// admin request path.
type Kicker interface {
	Kick()
}

// desiredStateWriter is the slice of *DesiredState the process manager uses.
type desiredStateWriter interface {
	SetDesired(ctx context.Context, q *db.Queries, instanceID, state string) error
	Remove(ctx context.Context, q *db.Queries, instanceID string) error
}

// EndpointDeregistrar removes an instance's managed MCP endpoint.
// *mcp.ManagedRegistrar satisfies it.
type EndpointDeregistrar interface {
	Deregister(ctx context.Context, instanceID string) error
}

// ProcessManager implements admin.PluginProcessManager over desired state: it
// never starts or stops anything itself. It records what the operator wants in
// plugin_containers and kicks the reconciler, which converges the world.
type ProcessManager struct {
	q         *db.Queries
	desired   desiredStateWriter
	kicker    Kicker
	registrar EndpointDeregistrar
}

// NewProcessManager constructs a ProcessManager. All arguments are required.
func NewProcessManager(q *db.Queries, desired desiredStateWriter, kicker Kicker, registrar EndpointDeregistrar) *ProcessManager {
	return &ProcessManager{q: q, desired: desired, kicker: kicker, registrar: registrar}
}

// Kick asks the reconciler for a pass now instead of at the next tick, for a
// change that was written to desired state outside this type (an accepted
// manifest that moved the desired image).
func (p *ProcessManager) Kick() { p.kicker.Kick() }

// Stop marks the instance's container desired_state=stopped and kicks the
// reconciler. An instance with no container row is a no-op success.
func (p *ProcessManager) Stop(ctx context.Context, instanceID string) error {
	if err := p.desired.SetDesired(ctx, p.q, instanceID, lifecycle.DesiredStateStopped); err != nil {
		return fmt.Errorf("stop instance %q: %w", instanceID, err)
	}
	p.kicker.Kick()
	return nil
}

// StartByPluginID marks every non-deactivated instance of pluginID
// desired_state=running and kicks the reconciler. Deactivated instances are
// skipped: activating one sibling must not resurrect another the operator
// turned off. Per-instance failures are joined so one bad instance does not
// hide the others.
func (p *ProcessManager) StartByPluginID(ctx context.Context, pluginID string) error {
	instances, err := p.q.ListPluginInstancesByPlugin(ctx, pluginID)
	if err != nil {
		return fmt.Errorf("start plugin %q: list instances: %w", pluginID, err)
	}

	var errs []error
	for _, inst := range instances {
		if model.PluginHealthState(inst.HealthState) == model.PluginHealthStateInactive {
			continue
		}
		if err := p.desired.SetDesired(ctx, p.q, inst.ID, lifecycle.DesiredStateRunning); err != nil {
			errs = append(errs, fmt.Errorf("start instance %q: %w", inst.ID, err))
		}
	}
	p.kicker.Kick()
	return errors.Join(errs...)
}

// StopByPluginID is the uninstall hook: it removes the desired-state row of
// every instance of pluginID (the reconciler then sees an orphan container and
// stops and removes it) and then deregisters each instance's managed MCP
// endpoint so its tool names are released. Per-instance failures are joined.
func (p *ProcessManager) StopByPluginID(ctx context.Context, pluginID string) error {
	instances, err := p.q.ListPluginInstancesByPlugin(ctx, pluginID)
	if err != nil {
		return fmt.Errorf("uninstall plugin %q: list instances: %w", pluginID, err)
	}

	var errs []error
	for _, inst := range instances {
		if err := p.desired.Remove(ctx, p.q, inst.ID); err != nil {
			errs = append(errs, fmt.Errorf("remove desired state for instance %q: %w", inst.ID, err))
			continue
		}
		if err := p.registrar.Deregister(ctx, inst.ID); err != nil {
			errs = append(errs, fmt.Errorf("deregister endpoint for instance %q: %w", inst.ID, err))
		}
	}
	p.kicker.Kick()
	return errors.Join(errs...)
}

// EventSupervisor is the slice of *events.Supervisor the admin surface drives.
type EventSupervisor interface {
	Start(ctx context.Context, instanceID string)
	Restart(ctx context.Context, instanceID string)
	Stop(instanceID string)
}

// TriggerRestarter implements admin.TriggerRestarter over the events
// supervisor. The subscription scope travels as events/listen params, so a
// Restart after a scope PUT reconnects under the new scope; the changed
// scopeHash makes the stored cursor miss, so the stream starts from a fresh
// cursor rather than resuming a position that belonged to the old scope.
type TriggerRestarter struct {
	supervisor EventSupervisor
}

// NewTriggerRestarter constructs a TriggerRestarter.
func NewTriggerRestarter(supervisor EventSupervisor) *TriggerRestarter {
	return &TriggerRestarter{supervisor: supervisor}
}

func (t *TriggerRestarter) Start(ctx context.Context, instanceID string) {
	t.supervisor.Start(ctx, instanceID)
}

func (t *TriggerRestarter) Restart(ctx context.Context, instanceID string) {
	t.supervisor.Restart(ctx, instanceID)
}

func (t *TriggerRestarter) Stop(instanceID string) { t.supervisor.Stop(instanceID) }

// InstanceByNameReader and ManagedServerReader are the two lookups the
// in-flight counter needs to get from an instance name to its MCP server row.
// *db.Queries satisfies both.
type InstanceByNameReader interface {
	GetPluginInstanceByGlobalName(ctx context.Context, instanceName string) (db.PluginInstance, error)
}

type ManagedServerReader interface {
	GetMCPServerByPluginInstance(ctx context.Context, pluginInstanceID *string) (db.McpServer, error)
}

// InFlightSource reports in-flight tool calls per MCP server ID.
// *mcp.Registry satisfies it.
type InFlightSource interface {
	InFlight(serverID string) int
}

// InflightCounter implements admin.InflightCounter from the per-server call
// gate in internal/mcp. admin gates deactivate and delete on this count.
type InflightCounter struct {
	instances InstanceByNameReader
	servers   ManagedServerReader
	source    InFlightSource
}

// NewInflightCounter constructs an InflightCounter.
func NewInflightCounter(instances InstanceByNameReader, servers ManagedServerReader, source InFlightSource) *InflightCounter {
	return &InflightCounter{instances: instances, servers: servers, source: source}
}

// InflightCountByInstance returns the number of tool calls executing or queued
// against the instance's managed endpoint. An instance with no endpoint has no
// calls. The interface cannot return an error, so a lookup fault is logged and
// read as zero; the delete or deactivate that follows hits the same database
// and fails there if it is really down.
func (c *InflightCounter) InflightCountByInstance(instanceName string) int {
	ctx := context.Background()
	inst, err := c.instances.GetPluginInstanceByGlobalName(ctx, instanceName)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("in-flight count: look up instance", "instance_name", instanceName, "err", err)
		}
		return 0
	}
	srv, err := c.servers.GetMCPServerByPluginInstance(ctx, &inst.ID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("in-flight count: look up managed endpoint", "instance_id", inst.ID, "err", err)
		}
		return 0
	}
	return c.source.InFlight(srv.ID)
}

// ToolConnEvictor implements admin.ToolConnEvictor as a deliberate no-op. The
// v1 pool cached a gRPC connection per instance that went stale across a
// respawn. Under the container substrate a rotation repoints the managed
// endpoint's url, and url is part of the MCP client cache's invalidation key,
// so the next resolve builds a client against the new address by itself.
type ToolConnEvictor struct{}

func (ToolConnEvictor) EvictInstance(string) {}

// SampleSource is the slice of *resources.Collector the RSS endpoint reads.
type SampleSource interface {
	Samples() []resources.Sample
}

// RSSAggregator implements admin.RSSAggregator from the collector's last
// container stats sample. The endpoint keeps its v1 JSON shape; "rss" now
// reports container memory usage.
type RSSAggregator struct {
	source SampleSource
}

// NewRSSAggregator constructs an RSSAggregator.
func NewRSSAggregator(source SampleSource) *RSSAggregator {
	return &RSSAggregator{source: source}
}

// Aggregate returns the total, instance count and per-instance samples sorted
// by memory descending. SampledAt is the time of the stats read.
func (a *RSSAggregator) Aggregate() (uint64, int, []admin.RSSSample) {
	samples := a.source.Samples()
	out := make([]admin.RSSSample, len(samples))
	var total uint64
	for i, s := range samples {
		out[i] = admin.RSSSample{
			InstanceID:   s.InstanceID,
			InstanceName: s.InstanceName,
			PluginID:     s.PluginID,
			Bytes:        s.MemoryUsageBytes,
			SampledAt:    s.At,
		}
		total += s.MemoryUsageBytes
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return total, len(out), out
}
