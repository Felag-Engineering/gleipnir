// Package assembly is the v2 plugin substrate body — the composition root for
// the MCP-realignment substrate (signed containerized MCP servers, ADR-053…
// ADR-060). It exists as its own importable package (rather than living
// directly in the tagged plugins_v2.go package-main file) so test packages —
// most notably the DooD suite (#985) — can construct the exact same runtime
// the binary does.
//
// This package must only be imported by plugins_v2.go (the substratev2
// package-main shim) and by tests. Nothing else in the tree should depend on
// it: importing it from an untagged file would pull v2-only packages into the
// default build, which is exactly what the substratev2 build tag exists to
// prevent (main_linkage_test.go asserts this holds).
//
// New returns an Assembly with no plugins today: nil-safe deps, and
// Quiesce/Shutdown are no-ops. That is enough for the tagged binary to boot
// and serve /api/v1/health without starting any plugin machinery. #962 fills
// in the real wiring (attach/observe passes, task-backed channels, the
// reconciler, …) behind these same methods.
package assembly

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// Deps carries everything New needs to bring up the v2 plugin substrate. It
// mirrors the constructor inputs plugins_v1.go's startPluginRuntime takes,
// since the assembly is wired from the exact same call site in run().
type Deps struct {
	Config         config.Config
	Store          *db.Store
	Publisher      event.Publisher
	EncryptionKey  []byte
	Arbiter        *toolregistry.Registry
	SystemSettings *settings.Service
}

// LauncherDeps mirrors package main's unexported launcherPluginDeps: the
// plugin-only inputs run() needs to finish building the RunLauncher and wire
// RunManager's plugin canceller. Defined again here (rather than shared)
// because package main's type is unexported and this package cannot import
// package main — plugins_v2.go converts between the two field-by-field.
type LauncherDeps struct {
	Registrar          agent.PluginGenerationLookup
	Dispatcher         agent.PluginToolDispatcher
	ApprovalDispatcher agent.ApprovalChannelDispatcher
	FeedbackDispatcher agent.FeedbackChannelDispatcher
	ToolClassifier     runpkg.ToolSourceClassifier
	ToolResolver       runpkg.PluginToolResolver
	Canceller          interface{ CancelRun(runID string) }
}

// AdminDeps mirrors package main's unexported adminPluginDeps. See LauncherDeps.
type AdminDeps struct {
	Installer interface {
		admin.PluginInstaller
		OnInstalled(func(ctx context.Context, pluginID string))
	}
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
	// endpoint. nil today.
	CapabilityHealth any
}

// Assembly is the v2 plugin substrate. Every method is nil-safe and a no-op
// until #962 fills in the real composition.
type Assembly struct {
	// manifestSnap is real (non-nil whenever deps.Store is set), even though
	// no plugin ever populates it with rows today. *configvalidate.Snapshotter
	// panics on a nil receiver (it dereferences its DB querier field), and
	// ManifestSnap is handed to request-time handlers (the audience handler,
	// the binding-test handler, the subscribed-binding validator) that call
	// it — an admin exercising any of those endpoints against the tagged
	// build must get a normal "not found" style error, not a 500 crash.
	manifestSnap *configvalidate.Snapshotter
}

// New brings up the v2 plugin substrate. It returns an Assembly with no
// plugins today — #962 is the issue that starts wiring the attach/observe
// passes, task-backed channels, and the reconciler behind it.
func New(ctx context.Context, deps Deps) (*Assembly, error) {
	var snap *configvalidate.Snapshotter
	if deps.Store != nil {
		snap = configvalidate.NewSnapshotter(deps.Store.Queries())
	}
	return &Assembly{manifestSnap: snap}, nil
}

// LauncherDeps returns the plugin-only inputs run() needs to finish building
// the RunLauncher. Every field is nil today — no plugins are started.
func (a *Assembly) LauncherDeps() LauncherDeps {
	return LauncherDeps{}
}

// BindLauncher completes the two-phase wiring once the RunLauncher exists.
// A no-op today.
func (a *Assembly) BindLauncher(ctx context.Context, launcher *runpkg.RunLauncher) {}

// AdminDeps returns everything the admin plugin-instance surface needs.
// ManifestSnap is real (see the Assembly.manifestSnap doc); every other
// field is nil today — no plugins are started.
func (a *Assembly) AdminDeps() AdminDeps {
	return AdminDeps{ManifestSnap: a.manifestSnap}
}

// Quiesce stops every avenue by which a plugin trigger event can reach
// RunLauncher.Launch. A no-op today.
func (a *Assembly) Quiesce() {}

// Shutdown stops the assembly's background goroutines and subprocesses.
// A no-op today.
func (a *Assembly) Shutdown() {}
