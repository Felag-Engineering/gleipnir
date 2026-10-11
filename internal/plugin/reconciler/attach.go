package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/manifest"
)

// The attach pass (spec §7 / §3; issue #956) is what makes a healthy
// generation REACHABLE: it turns the generation the rotation machinery has
// switched to into the ordinary `mcp_servers` row every consumer (hitl, the
// events supervisor, caphealth) resolves an *mcp.Client through.
//
// It is level-triggered like everything else here. There is no callback from
// the generation switch: each pass recomputes the whole answer from the
// current generations, containers and rows, so a crash between "switch" and
// "register" costs one interval and the next pass finishes the job.

// AttachActionKind is the single step the attach planner decides for one
// instance.
type AttachActionKind string

const (
	// AttachNone means the instance's managed row already matches reality, or
	// the instance is live but has nothing routable right now (see
	// attachInstance.HoldReason).
	AttachNone AttachActionKind = "none"

	// AttachRegister creates the instance's managed row, or repoints an
	// existing one at a new URL. Repointing is the routing flip of a rotation:
	// the row ID, and so the tool-namespace prefix, stays stable.
	AttachRegister AttachActionKind = "register"

	// AttachDeregister removes the row of an instance with nothing left to
	// route to. The delete cascades to mcp_tools, which releases the
	// instance's tool names.
	AttachDeregister AttachActionKind = "deregister"
)

// AttachAction is a planned step plus the instance it applies to.
type AttachAction struct {
	Kind       AttachActionKind
	InstanceID string
	// Endpoint is set for AttachRegister.
	Endpoint mcp.ManagedEndpoint
	// Reason is a short operator-facing explanation, carried into the log line.
	Reason string
}

// attachInstance is what the planner needs to know about one instance that
// still has something live. An instance with no live generation (or, in manual
// posture, no matched container) is simply absent from the planner's input,
// which is what makes any managed row it still owns an orphan.
type attachInstance struct {
	InstanceID   string
	InstanceName string

	// URL is the active generation's endpoint, empty when HoldReason is set.
	URL string

	// HoldReason is set when the instance is live but cannot be routed to this
	// pass: a generation that has not passed its gate yet, a missing container
	// address, an unreadable manifest. The planner leaves the existing row
	// alone — dropping a working route over a transient inspect failure would
	// churn the instance's tool names for nothing.
	HoldReason string
}

// attachListener is the seam #957 fills with per-instance events-listener
// state. planAttach accepts it now so that issue adds listener actions to this
// planner rather than growing a second one; nothing reads it yet.
type attachListener struct {
	InstanceID string
}

// planAttach decides the one managed-row step per instance. It is a pure
// function of (live instances, existing managed rows, listeners), in the style
// of planFor, so the whole table is testable without a runtime or a store.
//
// Listener actions are NOT planned here yet (#957); listeners is accepted and
// ignored. The result is sorted by instance ID so two passes over an unchanged
// world read identically.
func planAttach(instances []attachInstance, managedRows []db.McpServer, listeners []attachListener) []AttachAction {
	_ = listeners

	rowByInstance := make(map[string]db.McpServer, len(managedRows))
	for _, row := range managedRows {
		if row.PluginInstanceID != nil {
			rowByInstance[*row.PluginInstanceID] = row
		}
	}

	actions := make([]AttachAction, 0, len(instances)+len(managedRows))
	live := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		live[inst.InstanceID] = struct{}{}
		actions = append(actions, planAttachInstance(inst, rowByInstance))
	}

	for instanceID := range rowByInstance {
		if _, ok := live[instanceID]; ok {
			continue
		}
		actions = append(actions, AttachAction{
			Kind:       AttachDeregister,
			InstanceID: instanceID,
			Reason:     "no live generation serves this instance",
		})
	}

	sort.Slice(actions, func(i, j int) bool { return actions[i].InstanceID < actions[j].InstanceID })
	return actions
}

func planAttachInstance(inst attachInstance, rowByInstance map[string]db.McpServer) AttachAction {
	if inst.HoldReason != "" {
		return AttachAction{Kind: AttachNone, InstanceID: inst.InstanceID, Reason: inst.HoldReason}
	}

	row, hasRow := rowByInstance[inst.InstanceID]
	if hasRow && row.Url == inst.URL {
		return AttachAction{Kind: AttachNone, InstanceID: inst.InstanceID}
	}

	reason := "active generation has no managed endpoint yet"
	if hasRow {
		reason = "active generation serves from a different address; repointing the endpoint"
	}
	return AttachAction{
		Kind:       AttachRegister,
		InstanceID: inst.InstanceID,
		Endpoint: mcp.ManagedEndpoint{
			InstanceID:   inst.InstanceID,
			InstanceName: inst.InstanceName,
			URL:          inst.URL,
		},
		Reason: reason,
	}
}

// ManagedEndpoints is the registry side of the attach pass.
// *mcp.ManagedRegistrar satisfies it.
type ManagedEndpoints interface {
	Register(ctx context.Context, ep mcp.ManagedEndpoint) (db.McpServer, error)
	Deregister(ctx context.Context, instanceID string) error
}

// AttachStore is the read side the attach pass needs beyond the desired-state
// and generation stores. *db.Queries satisfies it.
type AttachStore interface {
	ListPluginInstances(ctx context.Context) ([]db.PluginInstance, error)
	ListPlugins(ctx context.Context) ([]db.Plugin, error)
	ListManagedMCPServers(ctx context.Context) ([]db.McpServer, error)
}

// AttachDeps configures the attach pass. Optional in Config: nil disables it,
// so the loop never touches the registry.
type AttachDeps struct {
	Store     AttachStore
	Endpoints ManagedEndpoints
}

// AttachPass makes the managed `mcp_servers` rows match the live generations.
//
// In the managed postures the input is the generation table: an `active`
// generation is one that passed its health gate and was switched to, which is
// the only thing worth routing to. In manual posture there are no generations,
// so the input is Discover's verdict instead — `matched` attaches, and every
// other state (notably `wrong_generation`, where the running plugin is not the
// approved one) does not, and withdraws a row an earlier pass created. Nothing
// is injected into the operator's containers in manual posture; their compose
// file sets whatever the plugin needs.
//
// Callers: the run loop calls it after the rotation half of every pass. In
// manual posture the loop is inert, so whatever drives discovery calls it.
func (r *Reconciler) AttachPass(ctx context.Context) ([]AttachAction, error) {
	r.rotationMu.Lock()
	defer r.rotationMu.Unlock()
	return r.attachPassLocked(ctx)
}

// attachPassLocked is AttachPass for callers already holding rotationMu
// (runPass). Reading generations mid-way through another rotation pass would
// show a half-applied switch, so the lock is shared with the rotation half.
func (r *Reconciler) attachPassLocked(ctx context.Context) ([]AttachAction, error) {
	if r.attach == nil {
		return nil, nil
	}

	instances, err := r.attach.Store.ListPluginInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconciler: attach: listing instances: %w", err)
	}
	plugins, err := r.attach.Store.ListPlugins(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconciler: attach: listing plugins: %w", err)
	}
	managedRows, err := r.attach.Store.ListManagedMCPServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconciler: attach: listing managed endpoints: %w", err)
	}

	servingContainers, err := r.servingContainers(ctx)
	if err != nil {
		return nil, err
	}

	instanceByID := make(map[string]db.PluginInstance, len(instances))
	for _, inst := range instances {
		instanceByID[inst.ID] = inst
	}
	pluginByID := make(map[string]db.Plugin, len(plugins))
	for _, p := range plugins {
		pluginByID[p.ID] = p
	}

	candidates := make([]attachInstance, 0, len(servingContainers))
	for _, sc := range servingContainers {
		candidates = append(candidates, r.resolveAttachInstance(ctx, sc, instanceByID, pluginByID))
	}

	actions := planAttach(candidates, managedRows, nil)

	var applied []AttachAction
	var errs []error
	for _, action := range actions {
		if action.Kind == AttachNone {
			if action.Reason != "" {
				logctx.Logger(ctx).InfoContext(ctx, "reconciler: attach: holding existing endpoint",
					"instance_id", action.InstanceID, "reason", action.Reason)
			}
			continue
		}
		if err := r.applyAttach(ctx, action); err != nil {
			errs = append(errs, err)
			continue
		}
		applied = append(applied, action)
	}
	return applied, errors.Join(errs...)
}

func (r *Reconciler) applyAttach(ctx context.Context, action AttachAction) error {
	switch action.Kind {
	case AttachRegister:
		if _, err := r.attach.Endpoints.Register(ctx, action.Endpoint); err != nil {
			return fmt.Errorf("reconciler: attach: registering instance %s: %w", action.InstanceID, err)
		}
	case AttachDeregister:
		if err := r.attach.Endpoints.Deregister(ctx, action.InstanceID); err != nil {
			return fmt.Errorf("reconciler: attach: deregistering instance %s: %w", action.InstanceID, err)
		}
	default:
		return fmt.Errorf("reconciler: attach: unknown action %q for instance %s", action.Kind, action.InstanceID)
	}
	logctx.Logger(ctx).InfoContext(ctx, "reconciler: attach: "+string(action.Kind),
		"instance_id", action.InstanceID, "reason", action.Reason)
	return nil
}

// servingContainer is an instance with something live, and the container that
// would serve it if there is one.
type servingContainer struct {
	InstanceID string
	// ContainerID is empty when the instance is live but has no container to
	// route to yet (no active generation, or an active row without a container).
	ContainerID container.ContainerID
	// HoldReason explains an empty ContainerID.
	HoldReason string
}

// servingContainers answers "which instances are live, and which container
// serves each" from the posture's source of truth.
func (r *Reconciler) servingContainers(ctx context.Context) ([]servingContainer, error) {
	if r.posture == container.PostureManual {
		discovered, err := r.DiscoverPass(ctx)
		if err != nil {
			return nil, fmt.Errorf("reconciler: attach: %w", err)
		}
		var out []servingContainer
		for _, d := range discovered {
			// Only a matched container is the approved plugin. Every other
			// state is deliberately left out so a previously attached row is
			// withdrawn.
			if d.State == DiscoveryMatched {
				out = append(out, servingContainer{InstanceID: d.InstanceID, ContainerID: d.ContainerID})
			}
		}
		return out, nil
	}

	if r.rotations == nil {
		return nil, errors.New("reconciler: attach: generation tracking (Rotations) is required outside manual posture")
	}
	generations, err := r.rotations.ListLiveContainerGenerations(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconciler: attach: listing live generations: %w", err)
	}
	return servingFromGenerations(generations), nil
}

// servingFromGenerations reduces an instance's live generations to the
// container of its active one. An instance whose generations are all still
// pending/starting/healthy/draining is live but not yet routable.
func servingFromGenerations(generations []db.PluginContainerGeneration) []servingContainer {
	byInstance := make(map[string]servingContainer)
	for _, gen := range generations {
		sc, seen := byInstance[gen.PluginInstanceID]
		if !seen {
			sc = servingContainer{
				InstanceID: gen.PluginInstanceID,
				HoldReason: "no generation has passed its health gate and been switched to",
			}
		}
		if gen.Status == GenActive {
			switch {
			case gen.ContainerID == nil || *gen.ContainerID == "":
				sc.HoldReason = "active generation has no container recorded"
			default:
				sc.ContainerID = container.ContainerID(*gen.ContainerID)
				sc.HoldReason = ""
			}
		}
		byInstance[gen.PluginInstanceID] = sc
	}

	out := make([]servingContainer, 0, len(byInstance))
	for _, sc := range byInstance {
		out = append(out, sc)
	}
	return out
}

// resolveAttachInstance turns a serving container into the planner's input:
// the instance's name, and the URL built from the container's address on its
// instance network and the port the consented manifest declares. Any failure
// becomes a HoldReason rather than an error, because a pass that cannot
// resolve one instance must still converge the rest.
func (r *Reconciler) resolveAttachInstance(ctx context.Context, sc servingContainer, instanceByID map[string]db.PluginInstance, pluginByID map[string]db.Plugin) attachInstance {
	out := attachInstance{InstanceID: sc.InstanceID}

	instance, ok := instanceByID[sc.InstanceID]
	if !ok {
		out.HoldReason = "instance row not found"
		return out
	}
	out.InstanceName = instance.InstanceName

	if sc.HoldReason != "" {
		out.HoldReason = sc.HoldReason
		return out
	}

	plugin, ok := pluginByID[instance.PluginID]
	if !ok {
		out.HoldReason = "plugin row not found"
		return out
	}
	// The consented snapshot, not whatever the container claims: the port is
	// part of what the admin approved, and ADR-045's material-change block
	// means a plugin cannot move it by shipping an update.
	snapshot, err := manifest.Read([]byte(plugin.ManifestSnapshot))
	if err != nil {
		out.HoldReason = fmt.Sprintf("reading manifest snapshot: %v", err)
		return out
	}
	port := snapshot.Transport.Port
	if snapshot.Version != 2 || port <= 0 {
		out.HoldReason = "manifest declares no package.transport.port"
		return out
	}

	info, err := r.runtime.Inspect(ctx, sc.ContainerID)
	if err != nil {
		out.HoldReason = fmt.Sprintf("inspecting container: %v", err)
		return out
	}
	addr, err := netip.ParseAddr(info.IPAddress)
	if err != nil {
		out.HoldReason = fmt.Sprintf("container has no usable address on its instance network (%q)", info.IPAddress)
		return out
	}

	out.URL = "http://" + net.JoinHostPort(addr.String(), strconv.Itoa(port)) + "/"
	return out
}
