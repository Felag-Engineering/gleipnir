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
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
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

	// ChannelDispatcher routes channel Notify/Request RPCs. shutdown() closes its
	// cached gRPC connections (channel-path analogue of Pool.Close).
	ChannelDispatcher *dispatch.Dispatcher

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

	// bgWG joins the long-lived OAuth background goroutines (nonce janitor +
	// refresh scanner) so shutdown() can wait for them to exit rather than
	// abandoning them mid-DB-write. Both goroutines are parented to the root
	// ctx; shutdown() relies on main.go having cancelled that ctx first, then
	// joins bgWG under a bounded deadline (#500).
	bgWG sync.WaitGroup
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
		Pool:              pool,
		ChannelDispatcher: pluginDispatcher,
		DispatchAdapter:   dispatchAdapter,
		ApprovalAdapter:   approvalAdapter,
		FeedbackAdapter:   feedbackAdapter,
		ToolRegistrar:     pluginToolRegistrar,
		ToolResolver:      toolResolver,
		ToolClassifier:    toolClassifier,
		ManifestSnap:      pluginManifestSnap,
		HostSvc:           hostSvc,
		loader:            loader,
	}

	// Wire OAuth handlers when an encryption key is available. The rescan hook
	// (OnPublicURLChanged) is returned as a field so run() can attach it to
	// adminHandler without this file importing admin.Handler directly.
	if encryptionKey != nil {
		enc := func(p string) (string, error) { return crypto.Encrypt(encryptionKey, p) }
		dec := func(c string) (string, error) { return crypto.Decrypt(encryptionKey, c) }
		oauthStore := pluginoauth.NewDBStore(store.Queries(), enc, dec, store.Queries(), time.Now)
		oauthNonces := pluginoauth.NewDBNonceStore(store.Queries(), time.Now)
		// Own the janitor goroutine under bgWG so shutdown() can join it rather
		// than abandoning a mid-Prune DB write (#500). The janitor selects on
		// ctx.Done() and returns promptly once the root ctx is cancelled.
		rt.bgWG.Add(1)
		go func() {
			defer rt.bgWG.Done()
			oauthNonces.StartJanitor(ctx, time.Minute)
		}()
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
		// Own the refresh-scanner goroutine under bgWG (same rationale as the
		// janitor above): Run blocks until ctx is cancelled, after the in-flight
		// scan tick finishes, so shutdown() can join it instead of racing a
		// mid-refresh token write (#500).
		rt.bgWG.Add(1)
		go func() {
			defer rt.bgWG.Done()
			oauthScanner.Run(ctx)
		}()
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

// quiesceTriggers stops every avenue by which a plugin trigger event can reach
// RunLauncher.Launch, synchronously, and MUST be called before the run-drain
// wait in main.go.
//
// Why before the drain: a trigger event that reaches Launch registers the run
// with RunManager (wg.Add) only inside the call. An event arriving at Launch
// AFTER runManager.Wait() returns but BEFORE trigger ingress is stopped is a
// WaitGroup add-after-Wait: that run is never awaited and races teardown of the
// dispatch pool it depends on. The root ctx cancel only *signals* goroutines
// cooperatively — it does not close the window atomically.
//
// There are two trigger-event ingresses, and this closes both:
//
//  1. The supervisor's per-instance Start streams. StopAll cancels each stream's
//     child ctx and joins (<-doneCh) every goroutine, so once it returns no
//     stream goroutine is alive to call Launch.
//
//  2. The substrate-initiated hostsvc.EmitEvent RPC (e.g. Slack Socket Mode),
//     which forwards to the trigger Dispatcher via the trigger sink. Clearing
//     the sink (SetTriggerSink(nil)) makes EmitEvent fall through to its
//     publisher-only (SSE) path so it no longer reaches Launch. SetTriggerSink
//     is RWMutex-guarded and safe to call concurrently with EmitEvent; at most
//     one EmitEvent that already read a non-nil sink can still be mid-Launch,
//     which is the same bounded-Launch property as case 1 (Launch returns after
//     registering + spawning the agent goroutine; it does not block on the run).
//
// quiesceTriggers is a no-op when rt is nil, tolerates a nil supervisor / nil
// HostSvc, and is safe to call more than once (StopAll on the now-empty instance
// map is a no-op; clearing an already-nil sink is a no-op) (#500).
func (rt *pluginRuntime) quiesceTriggers() {
	if rt == nil {
		return
	}
	// Clear the EmitEvent → Dispatcher sink first so no new substrate event can
	// be forwarded to Launch while we are joining the stream goroutines.
	if rt.HostSvc != nil {
		rt.HostSvc.SetTriggerSink(nil)
	}
	if rt.TriggerSupervisor != nil {
		rt.TriggerSupervisor.StopAll()
	}
}

// shutdown stops the trigger supervisor, joins the OAuth background goroutines,
// the plugin manager subprocesses, and finally the dispatch pool, in that order.
//
// shutdown is a no-op when rt is nil.
func (rt *pluginRuntime) shutdown() {
	if rt == nil {
		return
	}

	// Stop trigger stream goroutines before tearing down plugin subprocesses.
	// Normally main.go has already called quiesceTriggers before the drain wait;
	// this call is the idempotent backstop (StopAll on an empty map is a no-op)
	// for any path that shuts the runtime down without a prior quiesce.
	if rt.TriggerSupervisor != nil {
		rt.TriggerSupervisor.StopAll()
	}

	// Join the OAuth background goroutines (nonce janitor + refresh scanner). The
	// root ctx was cancelled in main.go before shutdown() runs, so both loops have
	// already been signalled to exit; this join just confirms they have returned
	// rather than abandoning them mid-DB-write. Bound the wait so a goroutine stuck
	// inside an in-flight DB call cannot hang shutdown indefinitely.
	bgDone := make(chan struct{})
	go func() {
		rt.bgWG.Wait()
		close(bgDone)
	}()
	select {
	case <-bgDone:
	case <-time.After(5 * time.Second):
		slog.Warn("plugin OAuth background goroutines did not exit within 5s; proceeding with shutdown")
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

	// Close the channel dispatcher's cached gRPC connections. StopAll above may
	// have already torn down the underlying transports; grpc.ErrClientConnClosing
	// from a re-close here is benign — warn-log only, do not fail shutdown.
	if rt.ChannelDispatcher != nil {
		if err := rt.ChannelDispatcher.Close(); err != nil {
			slog.Warn("plugin channel dispatcher close error", "err", err)
		}
	}
}
