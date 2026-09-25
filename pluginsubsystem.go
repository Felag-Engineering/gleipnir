package main

// pluginsubsystem.go defines the narrow contract run() depends on instead of
// reaching into the plugin runtime's concrete fields directly (the old
// rt.Pool, rt.TriggerSupervisor, rt.Loader() style reach-through). Exactly one
// implementation of pluginSubsystem is compiled into any given binary:
//
//   - plugins_v1.go (default build, no tag): the live HashiCorp go-plugin
//     substrate, wrapping the existing *pluginRuntime (pluginruntime.go).
//   - plugins_v2.go (-tags substratev2): a thin adapter over the importable
//     internal/plugin/assembly package, so test packages (the DooD suite,
//     #985) can construct the same runtime the binary does.
//
// What is actually enforced, and by what:
//   - the default build never STARTS v2 machinery: main_linkage_test.go and
//     main_direct_imports_test.go assert internal/plugin/assembly is absent
//     from both the default build's transitive closure AND package main's own
//     direct imports, and that no untagged file calls a v2-substrate-starting
//     constructor (reconciler.New*, hostendpoint.NewListenerSet, egress.*,
//     loader.NewOCIInstaller) directly. It is NOT a claim that every v2-era
//     package is absent from the default binary's link — internal/plugin/
//     {reconciler,container,egress} are reachable transitively today, through
//     hostendpoint and loader, both of which know about both substrate eras
//     without starting either.
//   - the tagged build never links the v1.1 runtime packages
//     (process/hostsvc/tools/hashicorp's go-plugin): main_linkage_v2_test.go.
//     internal/plugin/dispatch and internal/plugin/identity are the two
//     documented exceptions there (inherited transitively via
//     internal/execution/run and internal/plugin/hostendpoint, which both
//     substrates need); main_linkage_v2_test.go additionally asserts neither
//     is imported DIRECTLY by the tagged files or by internal/plugin/assembly.
//
// #1005 (G-59, the flip) deletes this file's v1 half — plugins_v1.go,
// pluginruntime.go, and the adapters they own — and plugins_v2.go becomes the
// only implementation, at which point this interface (and the deps structs
// below) may collapse back into a single concrete type if nothing else needs
// the seam.

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
)

// pluginSubsystem is the contract both startPluginSubsystem implementations
// satisfy. run() drives it in this order: launcherDeps() (to finish wiring
// RunManager and RunLauncherConfig) → bindLauncher() (once the RunLauncher
// exists) → adminDeps() (to build the admin plugin-instance surface) →
// quiesce() (before the run-drain wait) → shutdown() (after it).
type pluginSubsystem interface {
	// launcherDeps returns the plugin-only inputs run() needs before it can
	// finish building RunLauncherConfig and wire RunManager's plugin canceller.
	launcherDeps() launcherPluginDeps
	// bindLauncher completes the two-phase wiring: the trigger dispatcher (v1)
	// or its v2 equivalent needs the launcher to exist before it can be built,
	// so this runs once, immediately after the RunLauncher is constructed.
	bindLauncher(ctx context.Context, launcher *runpkg.RunLauncher)
	// adminDeps returns everything the admin plugin-instance surface, the
	// audience/binding-test handlers, and the OAuth/credentials/options
	// handlers need.
	adminDeps() adminPluginDeps
	// quiesce stops every avenue by which a plugin trigger event can reach
	// RunLauncher.Launch. Must be called before the run-drain wait in run()
	// (see quiesceTriggers's doc comment in pluginruntime.go for why the
	// ordering matters).
	quiesce()
	// shutdown stops the subsystem's background goroutines and subprocesses.
	// Called after the run-drain wait (or its timeout) in run().
	shutdown()
}

// pluginCanceller is the interface RunManager.WithPluginCanceller accepts.
// Named again here (rather than imported) because RunManager keeps its own
// unexported interface of the same shape specifically so it does not need to
// import internal/plugin/dispatch — see internal/execution/run/manager.go.
type pluginCanceller interface {
	CancelRun(runID string)
}

// pluginInstallNotifier extends admin.PluginInstaller with the OnInstalled
// hook registration run() uses to spawn a subprocess immediately after a
// fresh install (#386) — no server restart required. Kept as its own
// interface (rather than widening admin.PluginInstaller) because the admin
// package itself has no need of OnInstalled; only this wiring does.
type pluginInstallNotifier interface {
	admin.PluginInstaller
	OnInstalled(func(ctx context.Context, pluginID string))
}

// launcherPluginDeps carries the plugin-only inputs run() needs to finish
// building the RunLauncher: RunLauncherConfig's plugin fields (Registrar,
// Dispatcher, ApprovalDispatcher, FeedbackDispatcher), the classifier/resolver
// pair runpkg.NewDefaultToolResolver combines with the MCP registry into
// RunLauncherConfig.Resolver, and the RunManager canceller. A subsystem that
// starts no plugins (assembly.New today) leaves every field at its nil zero
// value, which is exactly what "plugins disabled" already means to both
// RunLauncherConfig and RunManager.
type launcherPluginDeps struct {
	Registrar          agent.PluginGenerationLookup
	Dispatcher         agent.PluginToolDispatcher
	ApprovalDispatcher agent.ApprovalChannelDispatcher
	FeedbackDispatcher agent.FeedbackChannelDispatcher
	ToolClassifier     runpkg.ToolSourceClassifier
	ToolResolver       runpkg.PluginToolResolver
	Canceller          pluginCanceller
}

// adminPluginDeps carries everything the admin plugin-instance surface
// (InstanceLifecycle, InstanceConfig, PluginHandler), the audience/binding-test
// handlers, and the OAuth/credentials/options handlers need. Every field is
// nil-safe: admin.NewInstanceLifecycle and friends already tolerate an absent
// collaborator (DB-only cleanup still works), which is what keeps them
// test-injectable and safe against a subsystem that starts no plugins.
type adminPluginDeps struct {
	Installer          pluginInstallNotifier
	ProcMgr            admin.PluginProcessManager
	Trigger            admin.TriggerRestarter
	Inflight           admin.InflightCounter
	Evictor            admin.ToolConnEvictor
	PluginsDir         string
	RSSAggregator      admin.RSSAggregator
	Unregistrar        admin.ToolUnregistrar
	OAuthHandler       *admin.PluginOAuthHandler
	CredentialsHandler *admin.PluginCredentialsHandler
	OptionsHandler     *admin.PluginOptionsHandler
	CredentialSeeder   admin.CredentialSeeder
	ManifestSnap       *configvalidate.Snapshotter
	OnPublicURLChanged func(ctx context.Context, oldURL, newURL string)

	// CapabilityHealth is reserved for #1001's per-capability health read
	// endpoint. nil in v1; a future issue populates it on both assemblies.
	CapabilityHealth any
}
