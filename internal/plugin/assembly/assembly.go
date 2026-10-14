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
// New composes the substrate in construction order (posture, runtime, subnet
// allocator, task poller, egress proxy, host endpoint, managed registrar,
// capability health, log capture, resource collector, task-backed channels,
// tool classifier) but does not start the reconciler, or anything else: every
// loop, including the background scanners, starts in OperatorListenerReady.
// The events supervisor is the one piece built later, in BindLauncher, because
// its sink launches runs and the launcher does not exist yet.
// Self-attach joins Gleipnir's own container to every plugin network, which is
// only safe once the operator API listener is wrapped by netguard, and main.go
// wraps that listener after the assembly is constructed.
// OperatorListenerReady is the gate: it takes the proof and starts the boot
// sequence.
//
// With no container-runtime socket and GLEIPNIR_CONTAINER_RUNTIME_MODE=auto
// the assembly comes up with plugins disabled: every dependency is nil-safe
// and every lifecycle method is a no-op, which is enough for the tagged
// binary to boot and serve /api/v1/health.
package assembly

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
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

	// MCPRegistry is the one registry the whole process shares. The discover
	// probe, the events supervisor, the task poller and the channel clients all
	// resolve through it, so every consumer reasons about the same cached
	// *mcp.Client for an instance — a second registry would hold a second
	// client, and after a generation rotation the two would disagree about
	// which URL is current. Required whenever plugins are enabled.
	MCPRegistry *mcp.Registry
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

	// Provisioner writes and removes an instance's plugin_containers desired
	// state in the same transaction as the instance row. nil while plugins are
	// disabled, which makes create and delete touch only plugin_instances.
	Provisioner admin.InstanceProvisioner

	// CapabilityHealth backs the per-capability health read endpoint (#1001).
	// nil while plugins are disabled.
	CapabilityHealth *caphealth.Registry
}
