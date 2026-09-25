//go:build !substratev2

package main

// plugins_v1.go is the default build's pluginSubsystem implementation: the
// live HashiCorp go-plugin substrate (pluginruntime.go's startPluginRuntime),
// wrapped to satisfy the interface pluginsubsystem.go declares. It also holds
// every v1-only adapter that used to live directly in main.go — moved here,
// unchanged, so main.go itself no longer imports any v1-specific package.
//
// #1005 (G-59, the flip) deletes this file along with pluginruntime.go,
// conn_factory_test.go, and audit_writestep_adapter_test.go.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"google.golang.org/grpc"
)

// startPluginSubsystem brings up the v1 plugin substrate and wraps it to
// satisfy pluginSubsystem. store, broadcaster, and systemSettings are kept on
// the returned wrapper (rather than only passed to startPluginRuntime)
// because bindLauncher needs them later, when wireTriggerSupervisor runs.
func startPluginSubsystem(
	ctx context.Context,
	cfg config.Config,
	store *db.Store,
	broadcaster event.Publisher,
	encryptionKey []byte,
	arbiter *toolregistry.Registry,
	systemSettings *settings.Service,
) (pluginSubsystem, error) {
	rt, err := startPluginRuntime(ctx, cfg, store, broadcaster, encryptionKey, arbiter, systemSettings)
	if err != nil {
		return nil, err
	}
	return &v1PluginSubsystem{
		ctx:            ctx,
		cfg:            cfg,
		rt:             rt,
		store:          store,
		broadcaster:    broadcaster,
		systemSettings: systemSettings,
	}, nil
}

// v1PluginSubsystem adapts *pluginRuntime to pluginSubsystem.
type v1PluginSubsystem struct {
	ctx            context.Context
	cfg            config.Config
	rt             *pluginRuntime
	store          *db.Store
	broadcaster    event.Publisher
	systemSettings *settings.Service
}

func (s *v1PluginSubsystem) launcherDeps() launcherPluginDeps {
	return launcherPluginDeps{
		Registrar:          s.rt.ToolRegistrar,
		Dispatcher:         s.rt.DispatchAdapter,
		ApprovalDispatcher: s.rt.ApprovalAdapter,
		FeedbackDispatcher: s.rt.FeedbackAdapter,
		ToolClassifier:     s.rt.ToolClassifier,
		ToolResolver:       s.rt.ToolResolver,
		Canceller:          s.rt.Pool,
	}
}

func (s *v1PluginSubsystem) bindLauncher(ctx context.Context, launcher *runpkg.RunLauncher) {
	s.rt.wireTriggerSupervisor(ctx, launcher, s.store, s.broadcaster, s.systemSettings)
}

// adminDeps mirrors the collaborator wiring that used to live inline in
// main.go's run() (formerly lines ~386-500). The nested nil-guards are
// unchanged from that code: they exist because the admin helpers are designed
// to tolerate absent deps (DB-only cleanup still works), which keeps them
// test-injectable and safe against a partially-initialized runtime.
func (s *v1PluginSubsystem) adminDeps() adminPluginDeps {
	rt := s.rt
	deps := adminPluginDeps{
		ManifestSnap:       rt.ManifestSnap,
		OAuthHandler:       rt.OAuthHandler,
		CredentialsHandler: rt.CredentialsHandler,
		OptionsHandler:     rt.OptionsHandler,
		OnPublicURLChanged: rt.OnPublicURLChanged,
		Unregistrar:        rt.ToolRegistrar,
	}

	if rt.TriggerSupervisor != nil {
		deps.Trigger = rt.TriggerSupervisor
	}
	if rt.Loader().Installer() != nil {
		deps.Installer = rt.Loader().Installer()
		if mgr := rt.Manager(); mgr != nil {
			deps.ProcMgr = mgr
			deps.PluginsDir = s.cfg.PluginsDir
			deps.Inflight = rt.Pool
			deps.Evictor = rt.Pool
		}
	}
	if mgr := rt.Manager(); mgr != nil {
		rssSampler := process.NewRSSSampler(mgr.Snapshot)
		rssSampler.Start(s.ctx, 30*time.Second)
		deps.RSSAggregator = rssAggregatorAdapter{sampler: rssSampler}
	}
	if rt.CredStore != nil {
		deps.CredentialSeeder = rt.CredStore
	}

	return deps
}

func (s *v1PluginSubsystem) quiesce()  { s.rt.quiesceTriggers() }
func (s *v1PluginSubsystem) shutdown() { s.rt.shutdown() }

// pluginDispatchAdapter wraps *dispatch.Pool to satisfy agent.PluginToolDispatcher.
// dispatch.ErrCallTimeout and dispatch.ErrQueueFull are the same sentinel values
// as agent.ErrPluginCallTimeout and agent.ErrPluginQueueFull (both alias
// internal/plugin/pluginerr), so the adapter is now a pure interface bridge —
// no error translation needed.
type pluginDispatchAdapter struct {
	pool *dispatch.Pool
}

func (a *pluginDispatchAdapter) Call(ctx context.Context, runID, policyID, instanceName, toolName, inputJSON string) (string, bool, error) {
	return a.pool.Call(ctx, runID, policyID, instanceName, toolName, inputJSON)
}

// manifestClassifier decides whether a dot-name tool grant belongs to a plugin
// instance by consulting the installed instance row and its manifest snapshot —
// NOT the in-memory namespace arbiter. This makes classification static: a tool's
// source does not change when its plugin subprocess starts or stops (see #399).
// The arbiter remains the spawn-time uniqueness enforcer; it is simply no longer
// the classification oracle. This is the production implementation of
// runpkg.ToolSourceClassifier.
//
// It shares lookupPluginInstanceTool with pluginToolResolverAdapter so the two
// can never disagree about what is a plugin tool.
type manifestClassifier struct {
	snap *configvalidate.Snapshotter
	q    pluginInstanceLookup
}

func (c *manifestClassifier) IsPluginTool(ctx context.Context, dotName string) (bool, error) {
	_, decl, instanceFound, err := lookupPluginInstanceTool(ctx, c.snap, c.q, dotName)
	if err != nil {
		return false, err
	}
	// A grant is a plugin tool only when an installed instance exists AND its
	// manifest declares the tool. Otherwise it routes to the MCP path.
	return instanceFound && decl != nil, nil
}

// lookupPluginInstanceTool resolves dotName ("<instance>.<tool>") to the installed
// plugin instance and the manifest ToolDecl it declares. It is the single source
// of truth shared by the classifier (routing) and the resolver (materialization),
// so the two cannot diverge. The lookup is independent of subprocess liveness.
//
// Return contract:
//   - bad dot-form, or no installed instance with that name: instanceFound=false,
//     decl=nil, err=nil — "not a plugin tool", route to MCP.
//   - instance exists but its manifest does not declare the tool: instanceFound=true,
//     decl=nil, err=nil.
//   - instance exists and declares the tool: instanceFound=true, decl!=nil, err=nil.
//   - a lookup that should have succeeded failed (DB error other than no-rows, or
//     manifest snapshot unreadable): err!=nil — the caller should fail loudly.
func lookupPluginInstanceTool(ctx context.Context, snap *configvalidate.Snapshotter, q pluginInstanceLookup, dotName string) (inst db.PluginInstance, decl *sdkmanifest.ToolDecl, instanceFound bool, err error) {
	instanceName, toolName, splitErr := splitDotName(dotName)
	if splitErr != nil {
		// Not in instance.tool form — cannot be a plugin tool.
		return db.PluginInstance{}, nil, false, nil
	}

	inst, err = q.GetPluginInstanceByGlobalName(ctx, instanceName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No installed instance by that name — route to MCP.
			return db.PluginInstance{}, nil, false, nil
		}
		return db.PluginInstance{}, nil, false, fmt.Errorf("lookup plugin instance %q: %w", instanceName, err)
	}

	manifest, err := snap.ForPluginID(ctx, inst.PluginID)
	if err != nil {
		return db.PluginInstance{}, nil, true, fmt.Errorf("manifest lookup for instance %q: %w", instanceName, err)
	}

	for i := range manifest.Tools {
		if manifest.Tools[i].Name == toolName {
			return inst, &manifest.Tools[i], true, nil
		}
	}
	return inst, nil, true, nil
}

// pluginToolGenerationLookup is the narrow interface that pluginToolResolverAdapter
// needs from *plugintools.Registrar. Only the Generation method is required —
// narrowing to an interface avoids importing the tools package in tests that
// use stub implementations.
type pluginToolGenerationLookup interface {
	Generation(instanceName string) (int64, bool)
}

// pluginInstanceLookup is the narrow DB interface that pluginToolResolverAdapter
// needs. Only GetPluginInstanceByGlobalName is required.
type pluginInstanceLookup interface {
	GetPluginInstanceByGlobalName(ctx context.Context, instanceName string) (db.PluginInstance, error)
}

// pluginToolResolverAdapter implements runpkg.PluginToolResolver by looking up
// each plugin tool grant in the manifest and the registrar. It is constructed in
// startPluginRuntime (not in a separate package) because it wires together
// multiple internal packages that must not import each other — the same
// pattern as pluginDispatchAdapter.
type pluginToolResolverAdapter struct {
	snap      *configvalidate.Snapshotter
	registrar pluginToolGenerationLookup
	q         pluginInstanceLookup
}

// ResolvePluginTools resolves a list of plugin tool grants into agent-ready
// PluginToolEntry values. For each grant it:
//  1. Splits the "instance.tool" dot-name.
//  2. Looks up the plugin instance in the DB to get its plugin_id.
//  3. Fetches the manifest snapshot for that plugin to read the tool's
//     description and JSON schema.
//  4. Reads the current generation from the registrar so the agent can detect
//     stale calls after a generation rotation.
func (r *pluginToolResolverAdapter) ResolvePluginTools(ctx context.Context, grants []model.ToolCapability) ([]agent.PluginToolEntry, error) {
	result := make([]agent.PluginToolEntry, 0, len(grants))
	for _, g := range grants {
		instanceName, toolName, err := splitDotName(g.Tool)
		if err != nil {
			return nil, fmt.Errorf("resolve plugin tool %q: %w", g.Tool, err)
		}

		// lookupPluginInstanceTool is the same lookup the classifier uses, so
		// routing and resolution can never disagree. The launcher only sends
		// already-classified plugin grants here, so a missing instance or
		// undeclared tool is a genuine error at this point (not a route-to-MCP
		// signal as it is for the classifier). The instance row itself is not
		// needed here — the registrar is keyed by instance name below.
		_, toolDecl, instanceFound, err := lookupPluginInstanceTool(ctx, r.snap, r.q, g.Tool)
		if err != nil {
			return nil, fmt.Errorf("plugin tool %q: %w", g.Tool, err)
		}
		if !instanceFound {
			return nil, fmt.Errorf("plugin tool %q: instance %q not found", g.Tool, instanceName)
		}
		if toolDecl == nil {
			return nil, fmt.Errorf("plugin tool %q: tool %q not declared in manifest", g.Tool, toolName)
		}

		var schema map[string]any
		if toolDecl.InputSchema != nil {
			if err := toolDecl.InputSchema.Decode(&schema); err != nil {
				return nil, fmt.Errorf("plugin tool %q: decode input schema: %w", g.Tool, err)
			}
		}

		gen, registered := r.registrar.Generation(instanceName)
		if !registered {
			// The DB lookup above confirmed the instance exists in the DB; the
			// registrar not knowing about it means its subprocess is not running.
			return nil, fmt.Errorf("plugin tool %q: instance %q subprocess is not running", g.Tool, instanceName)
		}

		// Approval mode passes through from the policy grant unchanged. The parser
		// normalizes empty approval to "none" (parser.go:209-211), so g.Approval is
		// always "none" or "required" by this point. The manifest's ApprovalRequired
		// is advisory metadata for the policy author; the policy controls at runtime.
		approval := g.Approval

		var timeout time.Duration
		if g.Timeout != "" {
			timeout, err = time.ParseDuration(g.Timeout)
			if err != nil {
				return nil, fmt.Errorf("plugin tool %q: parse timeout: %w", g.Tool, err)
			}
		}

		result = append(result, agent.PluginToolEntry{
			InstanceName: instanceName,
			ToolName:     toolName,
			Generation:   gen,
			Description:  toolDecl.Description,
			Schema:       schema,
			Approval:     approval,
			Timeout:      timeout,
			Params:       g.Params,
		})
	}
	return result, nil
}

// splitDotName splits a "source.tool" dot-name into its two parts. Returns an
// error when the name is missing the dot or has empty parts on either side.
// Same 3-line logic as internal/mcp's unexported splitToolName.
func splitDotName(dotName string) (source, tool string, err error) {
	parts := strings.SplitN(dotName, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("tool name %q must be in source.tool dot-notation", dotName)
	}
	return parts[0], parts[1], nil
}

// rssAggregatorAdapter bridges *process.RSSSampler to admin.RSSAggregator.
//
// The admin package defines its own RSSSample type with primitive fields only
// so it does not need to import internal/plugin/process. This adapter converts
// between the two types at wiring time, keeping the package boundary clean.
// The pattern mirrors managerConnFactory and PluginProcessManager.
type rssAggregatorAdapter struct {
	sampler *process.RSSSampler
}

func (a rssAggregatorAdapter) Aggregate() (uint64, int, []admin.RSSSample) {
	total, count, samples := a.sampler.Aggregate()
	out := make([]admin.RSSSample, len(samples))
	for i, s := range samples {
		out[i] = admin.RSSSample{
			InstanceID:   s.InstanceID,
			InstanceName: s.InstanceName,
			PluginID:     s.PluginID,
			Bytes:        s.Bytes,
			SampledAt:    s.SampledAt,
		}
	}
	return total, count, out
}

// managerConnFactory resolves a *grpc.ClientConn for a named plugin instance by
// looking it up in the host's process.Manager. It is the production ConnFactory
// that replaces the old stubConnFactory.
//
// The argument is the human-readable instance_name (matching
// dispatch.ConnFactory's contract and the plugin_instances.instance_name
// column), NOT the ULID. We therefore call Manager.LookupByName, not Lookup.
//
// The manager is set via setManager after loader.StartManager succeeds. Until
// then (or when plugins are disabled), Connect returns ErrManagerUnavailable.
// The atomic.Pointer lets connFactory be wired into dispatch.New and
// dispatch.NewDispatcher before StartManager runs; late-binding is safe because
// no plugin subprocess is reachable until StartManager completes anyway.
type managerConnFactory struct {
	mgr atomic.Pointer[process.Manager]
}

func (f *managerConnFactory) setManager(m *process.Manager) { f.mgr.Store(m) }

func (f *managerConnFactory) Connect(instanceName string) (*grpc.ClientConn, error) {
	m := f.mgr.Load()
	if m == nil {
		return nil, fmt.Errorf("%w: %q", dispatch.ErrManagerUnavailable, instanceName)
	}
	inst := m.LookupByName(instanceName)
	if inst == nil {
		return nil, fmt.Errorf("%w: %q", dispatch.ErrInstanceNotRunning, instanceName)
	}
	conn := inst.Client().Conn()
	if conn == nil {
		// Defence in depth: Client.Conn() should always be non-nil for instances
		// returned by the real process.Start path (hostwire.GRPCClient sets conn).
		return nil, fmt.Errorf("%w: %q (nil conn)", dispatch.ErrInstanceNotRunning, instanceName)
	}
	return conn, nil
}
