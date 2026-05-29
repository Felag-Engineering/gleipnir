package main

// pluginruntime.go extracts the plugin subsystem bring-up from run() in main.go
// into a single constructor. run() calls startPluginRuntime once (or not at all
// when GLEIPNIR_PLUGINS_ENABLED=false) and then reads the fields it needs.
//
// Motivation: ADR-reference for plugin system spec §15.2; issue #351.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	pluginpkg "github.com/felag-engineering/gleipnir/internal/plugin"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	pluginoauth "github.com/felag-engineering/gleipnir/internal/plugin/oauth"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
	plugintools "github.com/felag-engineering/gleipnir/internal/plugin/tools"
	plugintrigger "github.com/felag-engineering/gleipnir/internal/plugin/trigger"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
	"google.golang.org/grpc"
)

// pluginRuntime holds the handles that run() needs after the plugin subsystem
// has been started. All fields are non-nil when plugins are enabled.
type pluginRuntime struct {
	// Pool is the dispatch pool. run() passes it to runManager.WithPluginCanceller
	// and reads it for the SetInflightCounter wiring and the shutdown Close call.
	Pool *dispatch.Pool

	// DispatchAdapter satisfies agent.PluginToolDispatcher for RunLauncherConfig.
	DispatchAdapter agent.PluginToolDispatcher

	// ApprovalAdapter satisfies agent.ApprovalChannelDispatcher for RunLauncherConfig.
	ApprovalAdapter agent.ApprovalChannelDispatcher

	// FeedbackAdapter satisfies agent.FeedbackChannelDispatcher for RunLauncherConfig.
	FeedbackAdapter agent.FeedbackChannelDispatcher

	// ToolRegistrar is the plugin tool dot-name registrar; wired into RunLauncherConfig.
	ToolRegistrar *plugintools.Registrar

	// ToolResolver resolves plugin tool grants into agent-ready PluginToolEntry values.
	ToolResolver runpkg.PluginToolResolver

	// ToolClassifier determines whether a dot-name tool grant belongs to a plugin.
	ToolClassifier runpkg.ToolSourceClassifier

	// ManifestSnap caches plugin manifests by content hash. Shared between the
	// launcher, audience handler, binding test handler, and policy service.
	ManifestSnap *configvalidate.Snapshotter

	// HostSvc is the hostsvc.Server. run() calls SetTriggerSink on it after
	// the launcher is constructed (late-bind pattern; see wireTriggerSupervisor).
	HostSvc *hostsvc.Server

	// TriggerSupervisor manages per-instance trigger stream goroutines. Populated
	// by wireTriggerSupervisor; nil until that method has been called.
	TriggerSupervisor *plugintrigger.Supervisor

	// OAuthHandler handles plugin OAuth2 token flows. nil when encryptionKey is absent.
	OAuthHandler *admin.PluginOAuthHandler

	// CredentialsHandler handles plugin credential reads. nil when encryptionKey is absent.
	CredentialsHandler *admin.PluginCredentialsHandler

	// OnPublicURLChanged is the hook run() assigns to adminHandler.OnPublicURLChanged
	// so that a public_url change triggers a callback-URL rescan. nil when
	// encryptionKey is absent (rescan requires encrypted storage).
	OnPublicURLChanged func(ctx context.Context, oldURL, newURL string)

	// loader is the Loader constructed by startPluginRuntime. run() reads
	// Manager() and Installer() from it.
	loader *pluginpkg.Loader
}

// Manager returns the process.Manager started by the Loader. Callers that
// need the manager should use this rather than reaching into loader directly.
func (rt *pluginRuntime) Manager() *process.Manager { return rt.loader.Manager() }

// Loader returns the underlying plugin Loader. run() reads Installer() and
// Manager() from it via the Loader's own accessor methods.
func (rt *pluginRuntime) Loader() *pluginpkg.Loader { return rt.loader }

// startPluginRuntime brings up the plugin subsystem and returns a populated
// *pluginRuntime. Returns nil (not an error) when cfg.PluginsEnabled is false,
// which lets callers use a simple nil-guard instead of an explicit
// cfg.PluginsEnabled check.
//
// arbiter is the shared cross-source tool namespace registry; it must already
// be constructed before this call.
//
// systemSettings is needed here only for the OAuth getPublicURL closure; it is
// not stored on the returned struct.
func startPluginRuntime(
	ctx context.Context,
	cfg config.Config,
	store *db.Store,
	broadcaster event.Publisher,
	encryptionKey []byte,
	arbiter *toolregistry.Registry,
	systemSettings *settings.Service,
) (*pluginRuntime, error) {
	if !cfg.PluginsEnabled {
		return nil, nil
	}

	loader := pluginpkg.NewLoader()
	if err := loader.Init(ctx, cfg); err != nil {
		return nil, fmt.Errorf("init plugin loader: %w", err)
	}

	if err := loader.StartWatcher(ctx, store.Queries(), store.DB(), cfg.PluginsDir, broadcaster); err != nil {
		return nil, fmt.Errorf("start plugin watcher: %w", err)
	}

	// connFactory bridges the dispatch layer to the process.Manager. The manager
	// is injected via setManager below, after StartManager succeeds. Before that,
	// Connect returns ErrManagerUnavailable — correct because no plugin subprocess
	// is reachable until StartManager completes.
	connFactory := &managerConnFactory{}

	pool := dispatch.New(dispatch.Config{
		CallTimeout:          cfg.MCPTimeout,
		CancelTimeout:        5 * time.Second,
		DefaultMaxConcurrent: 50,
		DefaultMaxQueueDepth: 50,
		Connect:              connFactory.Connect,
	})

	dispatchAdapter := &pluginDispatchAdapter{pool: pool}

	pluginToolRegistrar := plugintools.New(arbiter, store.Queries(), broadcaster)
	pluginManifestSnap := configvalidate.NewSnapshotter(store.Queries())

	identityReg := identity.New()
	genCtrl := generation.New()

	pluginDispatcher := dispatch.NewDispatcher(dispatch.DispatcherConfig{
		Queries: store.Queries(),
		Connect: connFactory.Connect,
		// TODO(#NNN): Wire WriteRunStep here for audit trail completeness.
		// Requires constructing an AuditWriter outside the agent package,
		// which is deferred to a follow-up issue.
	})

	approvalAdapter := runpkg.NewApprovalChannelAdapter(pluginDispatcher)
	feedbackAdapter := runpkg.NewFeedbackChannelAdapter(pluginDispatcher)

	hostSvc := hostsvc.NewServer(
		store.Queries(),
		store.DB(),
		encryptionKey,
		pool,
		hostsvc.NewContextBinder(),
		broadcaster,
		pluginDispatcher,
	)

	// Chain order: token MUST be first because UnaryGenerationRefcountInterceptor
	// reads the instance ID from the context populated by UnaryInstanceTokenInterceptor.
	// UnaryCallIDInterceptor is last so it only attaches to authenticated,
	// generation-tracked calls.
	interceptors := []grpc.UnaryServerInterceptor{
		hostsvc.UnaryInstanceTokenInterceptor(identityReg),
		hostsvc.UnaryGenerationRefcountInterceptor(genCtrl),
		hostsvc.UnaryCallIDInterceptor(),
	}

	if err := loader.StartManager(ctx, pluginpkg.StartManagerConfig{
		Querier:              store.Queries(),
		Publisher:            broadcaster,
		HostServer:           hostSvc,
		IdentityRegistry:     identityReg,
		GenerationController: genCtrl,
		ServerInterceptors:   interceptors,
		ToolRegistrar:        pluginToolRegistrar,
	}); err != nil {
		return nil, fmt.Errorf("start plugin manager: %w", err)
	}

	// Publish the manager to the factory so Connect calls can now resolve running
	// instances. This runs after StartManager so the manager is fully initialised
	// before any call can reach it.
	connFactory.setManager(loader.Manager())

	toolResolver := &pluginToolResolverAdapter{
		snap:      pluginManifestSnap,
		registrar: pluginToolRegistrar,
		q:         store.Queries(),
	}
	toolClassifier := &manifestClassifier{snap: pluginManifestSnap, q: store.Queries()}

	rt := &pluginRuntime{
		Pool:            pool,
		DispatchAdapter: dispatchAdapter,
		ApprovalAdapter: approvalAdapter,
		FeedbackAdapter: feedbackAdapter,
		ToolRegistrar:   pluginToolRegistrar,
		ToolResolver:    toolResolver,
		ToolClassifier:  toolClassifier,
		ManifestSnap:    pluginManifestSnap,
		HostSvc:         hostSvc,
		loader:          loader,
	}

	// Wire OAuth handlers when an encryption key is available. The rescan hook
	// (OnPublicURLChanged) is returned as a field so run() can attach it to
	// adminHandler without this file importing admin.Handler directly.
	if encryptionKey != nil {
		enc := func(p string) (string, error) { return admin.Encrypt(encryptionKey, p) }
		dec := func(c string) (string, error) { return admin.Decrypt(encryptionKey, c) }
		oauthStore := pluginoauth.NewDBStore(store.Queries(), enc, dec, store.Queries(), time.Now)
		oauthNonces := pluginoauth.NewDBNonceStore(store.Queries(), time.Now)
		go oauthNonces.StartJanitor(ctx, time.Minute)
		oauthHMACKey := pluginoauth.DeriveHMACKey(encryptionKey)
		getPublicURL := func() string {
			u, _ := systemSettings.GetPublicURL(context.Background())
			return u
		}
		oauthMgr := pluginoauth.NewManager(oauthStore, oauthNonces, time.Now, oauthHMACKey, getPublicURL)
		oauthScanner := pluginoauth.NewRefreshScanner(
			oauthStore, store.Queries(),
			getPublicURL,
			cfg.OAuthRefreshInterval,
			cfg.OAuthRefreshLead,
		)
		oauthScanner.Start(ctx)
		rt.OAuthHandler = admin.NewPluginOAuthHandler(store.Queries(), oauthMgr, getPublicURL)
		rt.CredentialsHandler = admin.NewPluginCredentialsHandler(store.Queries(), oauthStore)

		callbackRescanner := pluginoauth.NewCallbackRescanner(
			store.Queries(), store.Queries(), getPublicURL, time.Now,
		)
		rt.OnPublicURLChanged = func(hookCtx context.Context, _, _ string) {
			if _, err := callbackRescanner.Scan(hookCtx); err != nil {
				slog.ErrorContext(hookCtx, "callback rescan failed after public_url change", "err", err)
			}
		}
	}

	return rt, nil
}

// wireTriggerSupervisor constructs the trigger dispatcher and supervisor and
// starts the supervisor's stream goroutines. It must be called after the
// RunLauncher is constructed (launcher depends on the runtime, and the trigger
// dispatcher depends on the launcher — this two-phase split is intentional).
//
// wireTriggerSupervisor is a no-op when rt is nil.
func (rt *pluginRuntime) wireTriggerSupervisor(
	ctx context.Context,
	launcher *runpkg.RunLauncher,
	store *db.Store,
	broadcaster event.Publisher,
	systemSettings *settings.Service,
) {
	if rt == nil || rt.HostSvc == nil {
		return
	}

	triggerDispatcher := plugintrigger.NewDispatcher(plugintrigger.DispatcherConfig{
		Launcher:      launcher,
		Querier:       store.Queries(),
		Dedup:         dedup.Noop{}, // #215 will swap in a rolling-window store
		Publisher:     broadcaster,
		ModelResolver: systemSettings,
		Logger:        slog.Default(),
	})
	rt.HostSvc.SetTriggerSink(plugintrigger.NewSinkAdapter(triggerDispatcher))

	supervisor := plugintrigger.NewSupervisor(plugintrigger.Config{
		Manager:      rt.loader.Manager(),
		Querier:      store.Queries(),
		Dispatcher:   triggerDispatcher,
		HealthSetter: rt.loader.Manager().HealthSetter(),
		Logger:       slog.Default(),
		// long-lived server ctx; parents stream goroutines so per-request
		// callers of Restart cannot cancel them (#401).
		RootCtx: ctx,
	})
	rt.TriggerSupervisor = supervisor

	go func() {
		if err := supervisor.StartAll(ctx); err != nil {
			slog.Error("trigger supervisor StartAll failed", "err", err)
		}
	}()
}

// shutdown stops the trigger supervisor, the plugin manager subprocesses, and
// finally the dispatch pool, in that order. This order matches the original
// main.go shutdown sequence and must be preserved.
//
// shutdown is a no-op when rt is nil.
func (rt *pluginRuntime) shutdown() {
	if rt == nil {
		return
	}

	// Stop trigger stream goroutines before tearing down plugin subprocesses.
	if rt.TriggerSupervisor != nil {
		rt.TriggerSupervisor.StopAll()
	}

	// Stop all plugin subprocesses before closing the dispatch pool. This order
	// matters: any in-flight cancel RPCs from the pool (#292/#198) must still
	// have live transport while subprocesses are stopping.
	if mgr := rt.loader.Manager(); mgr != nil {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 15*time.Second)
		if err := mgr.StopAll(stopCtx); err != nil {
			slog.Warn("plugin StopAll error", "err", err)
		}
		cancelStop()
	}

	// Close the plugin dispatch pool after all runs have drained so no new
	// Cancel RPCs are issued after the connections are torn down.
	// Note: StopAll above already tore down each subprocess's gRPC transport
	// via go-plugin's Kill(). Pool.Close() may therefore hit already-closed
	// connections; grpc.ErrClientConnClosing is swallowed by Pool.Close's
	// firstErr capture and is not surfaced here. This re-close is benign
	// post-StopAll.
	if err := rt.Pool.Close(); err != nil {
		slog.Warn("plugin dispatch pool close error", "err", err)
	}
}

