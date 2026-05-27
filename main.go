package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/http/sse"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/version"
	"github.com/felag-engineering/gleipnir/internal/llm"
	llmfactory "github.com/felag-engineering/gleipnir/internal/llm/factory"
	openaicompatllm "github.com/felag-engineering/gleipnir/internal/llm/openaicompat"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
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
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/timeout"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
	"github.com/felag-engineering/gleipnir/internal/trigger"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"google.golang.org/grpc"
)

// knownProviders is the list of LLM providers the system supports.
var knownProviders = []string{"anthropic", "google", "openai"}

const (
	// shutdownTimeout is the time budget for the HTTP server's graceful
	// shutdown after agent runs have drained (or timed out).
	shutdownTimeout = 5 * time.Second
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Use plain stderr here — the structured logger is not set up yet.
		fmt.Fprintf(os.Stderr, "FATAL: %s\n", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if err := run(cfg); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfg config.Config) error {
	startTime := time.Now()

	// Phase 1: background services and infrastructure.

	// Root context cancelled on shutdown so background components (Scheduler) can stop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Plugin loader: Init sets up the Verifier; StartWatcher starts the fsnotify
	// watcher after the DB is migrated. Both are no-ops when GLEIPNIR_PLUGINS_ENABLED
	// is false (the default for this release; spec §15.2).
	loader := pluginpkg.NewLoader()
	if err := loader.Init(ctx, cfg); err != nil {
		return fmt.Errorf("init plugin loader: %w", err)
	}

	store, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Plugin watcher start is deferred until after broadcaster is initialized;
	// see below after sse.NewBroadcaster().

	// Mark any in-flight runs as interrupted (ADR-011).
	if err := store.ScanOrphanedRuns(ctx, slog.Default()); err != nil {
		return fmt.Errorf("scan orphaned runs: %w", err)
	}

	// Write the PID file so gleipnirctl (and operators) can signal the process.
	// A write failure is non-fatal — log a warning and continue.
	pidContent := fmt.Sprintf("%d\n", os.Getpid())
	if err := os.WriteFile(cfg.PIDFile, []byte(pidContent), 0644); err != nil {
		slog.Warn("could not write PID file", "path", cfg.PIDFile, "err", err)
	} else {
		defer os.Remove(cfg.PIDFile)
	}

	broadcaster := sse.NewBroadcaster()
	sseHandler := sse.NewHandler(broadcaster)

	// Start the plugin watcher after broadcaster is initialized so pubkey-mismatch
	// transitions can emit plugin.health_changed SSE events. Schema is already
	// migrated at this point. No-op when GLEIPNIR_PLUGINS_ENABLED=false.
	if cfg.PluginsEnabled {
		if err := loader.StartWatcher(ctx, store.Queries(), store.DB(), cfg.PluginsDir, broadcaster); err != nil {
			return fmt.Errorf("start plugin watcher: %w", err)
		}
		// StartManager is called below, after pluginPool and encryptionKey are
		// in scope, so the hostsvc.Server can be constructed with full dependencies.
	}

	approvalScanner := timeout.NewApprovalScanner(
		store,
		cfg.ApprovalScanInterval,
		timeout.WithPublisher(broadcaster),
	)
	approvalScanner.Start(ctx)

	feedbackScanner := timeout.NewFeedbackScanner(
		store,
		cfg.FeedbackScanInterval,
		timeout.WithPublisher(broadcaster),
	)
	feedbackScanner.Start(ctx)

	// connFactory bridges the dispatch layer to the host's process.Manager.
	// It is constructed now (before pluginPool and pluginDispatcher) but the
	// manager is injected later via setManager, after loader.StartManager
	// succeeds. Using an atomic pointer lets the factory be captured by value
	// in both call sites while still seeing the manager once it is available.
	// Before setManager runs, Connect returns ErrManagerUnavailable — correct
	// because no plugin subprocesses are running yet.
	connFactory := &managerConnFactory{}

	// Plugin dispatch pool: routes agent tool calls to plugin instances via gRPC.
	pluginPool := dispatch.New(dispatch.Config{
		CallTimeout:          cfg.MCPTimeout,
		CancelTimeout:        5 * time.Second,
		DefaultMaxConcurrent: 50,
		DefaultMaxQueueDepth: 50,
		Connect:              connFactory.Connect,
	})

	// pluginDispatchAdapter wraps *dispatch.Pool to satisfy agent.PluginToolDispatcher,
	// translating dispatch-package sentinel errors to agent-package sentinels so the
	// agent package does not need to import internal/plugin/dispatch.
	pluginDispatchAdapter := &pluginDispatchAdapter{pool: pluginPool}

	runManager := runpkg.NewRunManager()
	runManager.WithPluginCanceller(pluginPool)
	providerRegistry := llm.NewProviderRegistry()

	// Parse the encryption key for admin API key storage.
	var encryptionKey []byte
	if raw := cfg.EncryptionKey; raw != "" {
		var err error
		encryptionKey, err = admin.ParseEncryptionKey(raw)
		if err != nil {
			return fmt.Errorf("parse GLEIPNIR_ENCRYPTION_KEY: %w", err)
		}
	}

	if encryptionKey == nil {
		slog.Warn("GLEIPNIR_ENCRYPTION_KEY not set — admin API key management will be unavailable")
	}

	// Cross-source tool namespace arbiter: a single in-memory registry shared
	// by the MCP server creation path and (once plugin start lands) the plugin
	// tool registrar. Constructed once here so both sides see the same state.
	arbiter := toolregistry.New()

	// Plugin tool registrar: claims <instance>.<tool> dot-names in the shared
	// arbiter when a plugin instance starts, and releases them on stop. Constructed
	// unconditionally so the reference is available regardless of PluginsEnabled;
	// it is only injected into StartManagerConfig inside the if-block below.
	pluginToolRegistrar := plugintools.New(arbiter, store.Queries(), broadcaster)

	// Plugin manifest snapshotter: parses and caches plugin manifests by content
	// hash. Hoisted here (before the launcher) so pluginToolResolverAdapter can
	// use it. The same instance is reused at line ~442 where the audience handler
	// and binding test handler are constructed — shared cache hits benefit both.
	pluginManifestSnap := configvalidate.NewSnapshotter(store.Queries())

	// Activate the hostsvc.Server and start the process.Manager when plugins are
	// enabled. This block runs after pluginPool (needed as CallContextResolver),
	// encryptionKey (needed for GetCredentials), and arbiter are all in scope.
	//
	// Chain order: token MUST be first because UnaryGenerationRefcountInterceptor
	// reads the instance ID from the context populated by UnaryInstanceTokenInterceptor.
	// UnaryCallIDInterceptor is last so it only attaches to authenticated,
	// generation-tracked calls.
	// hostSvc and approvalAdapter are declared outside the if block so that
	// post-launcher wiring (SetTriggerSink, triggerSupervisor) can reach hostSvc,
	// and the RunLauncherConfig can reference approvalAdapter, without either
	// variable going out of scope.  Analogous to connFactory being declared at
	// line ~155 so setManager can be called at line 237.
	var hostSvc *hostsvc.Server
	var approvalAdapter agent.ApprovalChannelDispatcher
	var feedbackAdapter agent.FeedbackChannelDispatcher
	if cfg.PluginsEnabled {
		identityReg := identity.New()
		genCtrl := generation.New()

		pluginDispatcher := dispatch.NewDispatcher(dispatch.DispatcherConfig{
			Queries: store.Queries(),
			Connect: connFactory.Connect,
			// TODO(#NNN): Wire WriteRunStep here for audit trail completeness.
			// Requires constructing an AuditWriter outside the agent package,
			// which is deferred to a follow-up issue.
		})

		// approvalAdapter and feedbackAdapter are constructed here where
		// pluginDispatcher is in scope.  The outer-scoped variables are assigned so
		// RunLauncherConfig below can reference them without the dispatcher escaping
		// the if block.
		approvalAdapter = runpkg.NewApprovalChannelAdapter(pluginDispatcher)
		feedbackAdapter = runpkg.NewFeedbackChannelAdapter(pluginDispatcher)

		hostSvc = hostsvc.NewServer(
			store.Queries(),
			store.DB(),
			encryptionKey,
			pluginPool,
			hostsvc.NewContextBinder(),
			broadcaster,
			pluginDispatcher,
		)

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
			return fmt.Errorf("start plugin manager: %w", err)
		}
		// Publish the manager to the factory so Connect calls can now resolve
		// running instances. This runs after StartManager so the manager is
		// fully initialised before any call can reach it.
		connFactory.setManager(loader.Manager())
	}

	// Registry construction is placed after encryption key parsing so
	// WithEncryptionKey can be passed at construction time.
	registry := mcp.NewRegistry(store.Queries(),
		mcp.WithMCPTimeout(cfg.MCPTimeout),
		mcp.WithEncryptionKey(encryptionKey),
		mcp.WithToolNamespaceArbiter(arbiter),
	)

	// configureProvider creates an LLM client and registers it in the provider
	// registry. Called both at bootstrap (from DB) and when an admin saves a key.
	configureProvider := func(ctx context.Context, provider string, apiKey string) error {
		client, err := llmfactory.NewClientForProvider(ctx, provider, apiKey)
		if err != nil {
			return err
		}
		providerRegistry.Register(provider, client)
		return nil
	}

	removeProvider := func(provider string) {
		providerRegistry.Unregister(provider)
	}

	adminQuerier := admin.NewQuerierAdapter(store.Queries())
	systemSettings := settings.NewService(store.Queries())
	adminHandler := admin.NewHandler(adminQuerier, systemSettings, encryptionKey, knownProviders, configureProvider, removeProvider, providerRegistry)

	// Bootstrap providers from DB-stored encrypted API keys.
	for _, provName := range knownProviders {
		row, err := store.Queries().GetSystemSetting(ctx, provName+"_api_key")
		if err != nil {
			continue
		}
		apiKey, err := admin.Decrypt(encryptionKey, row.Value)
		if err != nil {
			slog.Error("failed to decrypt stored API key", "provider", provName, "err", err)
			continue
		}
		if err := configureProvider(ctx, provName, apiKey); err != nil {
			slog.Error("failed to bootstrap provider from DB", "provider", provName, "err", err)
		} else {
			slog.Info("bootstrapped provider from stored API key", "provider", provName)
		}
	}

	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		slog.Warn("ANTHROPIC_API_KEY env var is set but no longer used — configure API keys through the admin UI at /admin/models")
	}
	if key := os.Getenv("GOOGLE_API_KEY"); key != "" {
		slog.Warn("GOOGLE_API_KEY env var is set but no longer used — configure API keys through the admin UI at /admin/models")
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		slog.Warn("OPENAI_API_KEY env var is set but no longer used — configure API keys through the admin UI at /admin/models")
	}

	// Wire up the OpenAI-compatible provider handler. The adapter bridges
	// sqlc-generated rows to the handler and loader interfaces. nil tester
	// causes NewOpenAICompatHandler to substitute DefaultConnectionTester.
	openaiAdapter := &openaiCompatAdapter{q: store.Queries()}
	openaiCompatHandler := admin.NewOpenAICompatHandler(openaiAdapter, encryptionKey, providerRegistry, nil)

	// Load any previously-saved OpenAI-compat providers from the DB into the
	// registry at startup. Failure is non-fatal (mirrors bootstrap-providers
	// loop above) — a log entry is sufficient.
	if err := openaicompatllm.LoadAndRegister(ctx, openaiAdapter, encryptionKey, providerRegistry, admin.Decrypt); err != nil {
		slog.Error("failed to load openai-compat providers at startup", "err", err)
	}

	// Ensure the configured default model has an enabled=1 row so that existing
	// deployments are not locked out after the semantic flip (new/unseen models
	// now default to disabled). If the row already exists with enabled=1, the
	// upsert is a no-op.
	if err := ensureDefaultModelEnabled(ctx, store.Queries(), systemSettings); err != nil {
		slog.Warn("could not ensure default model is enabled", "err", err)
	}

	// Build the plugin tool resolver and classifier only when plugins are enabled.
	// When nil, every tool grant goes to the MCP path, preserving pre-plugin behaviour.
	var pluginResolver runpkg.PluginToolResolver
	var toolClassifier runpkg.ToolSourceClassifier
	if cfg.PluginsEnabled {
		pluginResolver = &pluginToolResolverAdapter{
			snap:      pluginManifestSnap,
			registrar: pluginToolRegistrar,
			q:         store.Queries(),
		}
		toolClassifier = &arbiterClassifier{arbiter: arbiter}
	}

	launcher := runpkg.NewRunLauncher(runpkg.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                runManager,
		AgentFactory:           runpkg.NewAgentFactory(providerRegistry),
		Publisher:              broadcaster,
		DefaultFeedbackTimeout: cfg.DefaultFeedbackTimeout,
		ModelResolver:          systemSettings,
		PluginResolver:         pluginResolver,
		ToolClassifier:         toolClassifier,
		PluginRegistrar:        pluginToolRegistrar,
		PluginDispatcher:       pluginDispatchAdapter,
		ApprovalDispatcher:     approvalAdapter,
		FeedbackDispatcher:     feedbackAdapter,
	})

	// Wire the trigger dispatch pipeline now that RunLauncher is available.
	// hostSvc.SetTriggerSink is the late-bind pattern (analogous to
	// connFactory.setManager at line ~237): hostSvc was constructed before
	// RunLauncher existed, so the trigger sink is attached here.
	// Until SetTriggerSink fires, EmitEvent falls back to publisher-only —
	// correct because no plugin subprocess has begun emitting events yet
	// (StartManager runs at line ~224 but Supervisor.StartAll is called below,
	// after this block).
	var triggerSupervisor *plugintrigger.Supervisor
	if cfg.PluginsEnabled && hostSvc != nil {
		triggerDispatcher := plugintrigger.NewDispatcher(plugintrigger.DispatcherConfig{
			Launcher:      launcher,
			Querier:       store.Queries(),
			Dedup:         dedup.Noop{}, // #215 will swap in a rolling-window store
			Publisher:     broadcaster,
			ModelResolver: systemSettings,
			Logger:        slog.Default(),
		})
		hostSvc.SetTriggerSink(plugintrigger.NewSinkAdapter(triggerDispatcher))

		triggerSupervisor = plugintrigger.NewSupervisor(plugintrigger.Config{
			Manager:      loader.Manager(),
			Querier:      store.Queries(),
			Dispatcher:   triggerDispatcher,
			HealthSetter: loader.Manager().HealthSetter(),
			Logger:       slog.Default(),
			// long-lived server ctx; parents stream goroutines so per-request
			// callers of Restart cannot cancel them (#401).
			RootCtx: ctx,
		})
		go func() {
			if err := triggerSupervisor.StartAll(ctx); err != nil {
				slog.Error("trigger supervisor StartAll failed", "err", err)
			}
		}()
	}

	webhookSecretLoader := trigger.NewSecretLoader(store.Queries(), encryptionKey)
	webhookHandler := trigger.NewWebhookHandler(store, launcher, webhookSecretLoader, systemSettings)

	// Build encrypter for policy webhook secret management.
	var webhookEncrypter *webhookSecretEncrypterAdapter
	if encryptionKey != nil {
		webhookEncrypter = &webhookSecretEncrypterAdapter{key: encryptionKey}
	}

	// Warn if the encryption key is absent but encrypted secrets are in the DB.
	if encryptionKey == nil {
		if n, err := countEncryptedWebhookSecrets(ctx, store); err == nil && n > 0 {
			slog.Error("encryption key unset but DB contains encrypted webhook secrets; webhook verification and rotate/reveal will return 500/503",
				"count", n)
		}
	}

	// Wire the policy webhook handler for rotate/reveal endpoints.
	policyService := policy.NewService(store, nil, providerRegistry, providerRegistry, systemSettings)
	if webhookEncrypter != nil {
		policyService.WithWebhookSecretEncrypter(webhookEncrypter)
	}
	if cfg.PluginsEnabled {
		resolver := &pluginInstanceResolver{q: store.Queries()}
		policyService.WithSubscribedBindingValidator(
			policy.NewSubscribedBindingValidator(resolver, pluginManifestSnap),
		)
	}
	policyWebhookHandler := api.NewPolicyWebhookHandler(policyService)

	scheduler := trigger.NewScheduler(store, launcher, systemSettings)
	if err := scheduler.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}

	poller := trigger.NewPoller(store, launcher, registry, systemSettings)
	if err := poller.Start(ctx); err != nil {
		return fmt.Errorf("start poller: %w", err)
	}

	cronRunner := trigger.NewCronRunner(store, launcher, systemSettings)
	if err := cronRunner.Start(ctx); err != nil {
		return fmt.Errorf("start cron runner: %w", err)
	}

	services := api.BackgroundServices{
		Store:            store,
		Broadcaster:      broadcaster,
		Registry:         registry,
		RunManager:       runManager,
		Launcher:         launcher,
		ModelLister:      providerRegistry,
		ProviderRegistry: providerRegistry,
		ModelFilter:      &modelFilterAdapter{q: store.Queries()},
		Poller:           poller,
		Scheduler:        scheduler,
		Cron:             cronRunner,
		EncryptionKey:    encryptionKey,
		Arbiter:          arbiter,
		Settings:         systemSettings,
	}

	// Phase 2: HTTP handlers.
	authHandler := auth.NewHandler(store.Queries(), store.DB())
	settingsHandler := auth.NewSettingsHandler(store.Queries())

	// Reuse pluginManifestSnap (constructed before the launcher) so the audience
	// handler and binding test handler benefit from the same cache as the resolver.
	snap := pluginManifestSnap
	audienceH := api.NewAudienceHandler(store, snap, time.Now)
	bindingTestH := api.NewBindingTestHandler(snap)

	// OAuth2 token management for plugin instances. Constructed here (after
	// systemSettings) so getPublicURL can be bound at call time. The scanner
	// starts only when plugins are enabled and an encryption key is set.
	var pluginOAuthHandler *admin.PluginOAuthHandler
	var pluginCredHandler *admin.PluginCredentialsHandler
	if cfg.PluginsEnabled && encryptionKey != nil {
		enc := func(p string) (string, error) { return admin.Encrypt(encryptionKey, p) }
		dec := func(c string) (string, error) { return admin.Decrypt(encryptionKey, c) }
		oauthStore := pluginoauth.NewDBStore(store.Queries(), enc, dec, store.Queries(), time.Now)
		oauthNonces := pluginoauth.NewDBNonceStore(store.Queries(), time.Now)
		go oauthNonces.StartJanitor(ctx, time.Minute)
		oauthHMACKey := pluginoauth.DeriveHMACKey(encryptionKey)
		// getPublicURL is a zero-arg closure used by the manager and scanner so
		// they do not need a context parameter. Context is elided because public_url
		// is a static admin setting; a background context is adequate here.
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
		pluginOAuthHandler = admin.NewPluginOAuthHandler(store.Queries(), oauthMgr, getPublicURL)
		pluginCredHandler = admin.NewPluginCredentialsHandler(store.Queries(), oauthStore)

		// Wire the callback-URL rescan so it fires whenever an admin changes
		// public_url (#230). Constructed after adminHandler so we can set the hook
		// directly without adminHandler importing internal/plugin/oauth.
		callbackRescanner := pluginoauth.NewCallbackRescanner(
			store.Queries(), store.Queries(), getPublicURL, time.Now,
		)
		adminHandler.OnPublicURLChanged = func(hookCtx context.Context, _, _ string) {
			if _, err := callbackRescanner.Scan(hookCtx); err != nil {
				slog.ErrorContext(hookCtx, "callback rescan failed after public_url change", "err", err)
			}
		}
	}

	handlers := api.HandlerBundle{
		AuthHandler:              authHandler,
		SettingsHandler:          settingsHandler,
		AdminHandler:             adminHandler,
		OpenAICompatHandler:      openaiCompatHandler,
		PluginAdminHandler:       admin.NewPluginHandler(store.Queries(), broadcaster, nil),
		PluginOAuthHandler:       pluginOAuthHandler,
		PluginCredentialsHandler: pluginCredHandler,
		AudienceHandler:          audienceH,
		BindingTestHandler:       bindingTestH,
		WebhookHandler:           webhookHandler,
		SSEHandler:               sseHandler,
		PolicyWebhookHandler:     policyWebhookHandler,
	}

	// Wire the trigger supervisor into the plugin admin handler so that
	// PutSubscriptionScope can restart the stream after a scope change.
	if triggerSupervisor != nil && handlers.PluginAdminHandler != nil {
		handlers.PluginAdminHandler.SetTriggerRestarter(triggerSupervisor)
	}

	// Wire the *db.Store unconditionally so DeleteInstance and Uninstall can
	// open transactions via store.DB().BeginTx. This is always needed — the
	// transactional delete path works even when the plugin subsystem is
	// disabled (DB-only cleanup for the kitchen-sink recovery use case).
	if handlers.PluginAdminHandler != nil {
		handlers.PluginAdminHandler.SetStore(store)
	}

	// Wire the shared Installer into the plugin admin handler so both the
	// fsnotify watcher and the Install endpoint use the same pipeline instance.
	// loader.Installer() returns nil when GLEIPNIR_PLUGINS_ENABLED=false, which
	// disables the install endpoint cleanly (returns 503).
	if loader.Installer() != nil && handlers.PluginAdminHandler != nil {
		handlers.PluginAdminHandler.SetInstaller(loader.Installer())
		// Wire the process manager and plugins dir for subprocess stop + FS
		// cleanup during Uninstall. Only available when plugins are enabled and
		// the manager was successfully started.
		if mgr := loader.Manager(); mgr != nil {
			handlers.PluginAdminHandler.SetProcessManager(mgr)
			handlers.PluginAdminHandler.SetPluginsDir(cfg.PluginsDir)
			handlers.PluginAdminHandler.SetInflightCounter(pluginPool)
		}
	}

	// Wire the RSS sampler inside the plugins-enabled block. When plugins are
	// disabled, rssAggregator is never set and GetPluginRSS returns 503.
	if mgr := loader.Manager(); mgr != nil && handlers.PluginAdminHandler != nil {
		rssSampler := process.NewRSSSampler(mgr.Snapshot)
		rssSampler.Start(ctx, 30*time.Second)
		handlers.PluginAdminHandler.SetRSSAggregator(rssAggregatorAdapter{sampler: rssSampler})
	}

	// Register the post-install spawn hook so that a fresh install (via the
	// admin endpoint or the fsnotify watcher) immediately spawns the plugin
	// subprocess — no server restart required (#386). The same Installer instance
	// is used by both paths so this registration covers both.
	if mgr := loader.Manager(); mgr != nil && loader.Installer() != nil {
		loader.Installer().OnInstalled(func(ctx context.Context, pluginID string) {
			if err := mgr.StartByPluginID(ctx, pluginID); err != nil {
				slog.Warn("post-install spawn failed", "plugin_id", pluginID, "err", err)
			}
		})
	}

	// Phase 3: build the router.
	r := api.BuildRouter(api.RouterConfig{
		Handlers: handlers,
		Services: services,
		Metadata: api.Metadata{
			Version:                       version.Version,
			StartTime:                     startTime,
			DBPath:                        cfg.DBPath,
			SignatureVerificationDisabled: cfg.PluginsEnabled && cfg.AllowUnsignedPlugins,
		},
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// httpWG tracks the ListenAndServe goroutine so main can confirm it has
	// exited after Shutdown returns. Without this, a late panic from the listener
	// could race the process exit.
	var httpWG sync.WaitGroup
	httpWG.Add(1)
	go func() {
		defer httpWG.Done()
		slog.Info("server listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "err", err)
			quit <- syscall.SIGTERM
		}
	}()

	<-quit
	slog.Info("shutting down")

	// Cancel the root context to stop the scheduler, poller, and any background timers.
	// Note: run contexts derive from context.Background() (see launcher.go), so
	// this does NOT cancel in-flight agent runs — CancelAll handles that below.
	cancel()

	// Signal all in-flight agent runs to stop.
	runManager.CancelAll()

	// Wait for poll loops, cron loops, and agent runs to drain, with a timeout.
	// Poll and cron loops should exit quickly (they are just sleeping timers).
	// Agent runs may take longer, so all are waited concurrently.
	runsDrained := make(chan struct{})
	go func() {
		poller.Wait()
		cronRunner.Wait()
		runManager.Wait()
		close(runsDrained)
	}()

	select {
	case <-runsDrained:
		slog.Info("all agent runs drained")
	case <-time.After(cfg.DrainTimeout):
		slog.Warn("agent run drain timed out, proceeding with server shutdown")
	}

	// Stop trigger stream goroutines before tearing down plugin subprocesses.
	// The supervisor's goroutines already observe ctx cancellation; StopAll
	// blocks until all goroutines have exited cleanly.
	if triggerSupervisor != nil {
		triggerSupervisor.StopAll()
	}

	// Stop all plugin subprocesses before closing the dispatch pool. This order
	// matters: any in-flight cancel RPCs from the pool (#292/#198) must still
	// have live transport while subprocesses are stopping.
	if mgr := loader.Manager(); mgr != nil {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 15*time.Second)
		if err := mgr.StopAll(stopCtx); err != nil {
			slog.Warn("plugin StopAll error", "err", err)
		}
		cancelStop()
	}

	// Close the plugin dispatch pool after all runs have drained so no new
	// Cancel RPCs are issued after the connections are torn down.
	// Note: StopAll above already tore down each subprocess's gRPC transport
	// via go-plugin's Kill(). pluginPool.Close() may therefore hit already-closed
	// connections; grpc.ErrClientConnClosing is swallowed by Pool.Close's firstErr
	// capture and is not surfaced here. This re-close is benign post-StopAll.
	if err := pluginPool.Close(); err != nil {
		slog.Warn("plugin dispatch pool close error", "err", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	// Wait() guarantees the listener goroutine has observed ErrServerClosed (or a
	// panic recovery) before main returns, so a late crash cannot race shutdown.
	httpWG.Wait()
	return nil
}

// ensureDefaultModelEnabled upserts an enabled=1 row for the configured
// default model. This prevents existing deployments from being locked out
// after the semantic flip where new/unseen models default to disabled.
// If no default_model setting exists, the function is a no-op.
func ensureDefaultModelEnabled(ctx context.Context, q *db.Queries, s *settings.Service) error {
	provider, model, err := s.GetSystemDefault(ctx)
	if err != nil {
		// Best-effort: a DB read failure here shouldn't block startup.
		// (The no-default-configured case returns ("", "", nil), not an error.)
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := q.UpsertModelSetting(ctx, db.UpsertModelSettingParams{
		Provider:  provider,
		ModelName: model,
		Enabled:   1,
		UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("upsert default model enabled row: %w", err)
	}
	return nil
}

// modelFilterAdapter bridges db.Queries to the api.ModelFilter interface.
type modelFilterAdapter struct {
	q *db.Queries
}

// openaiCompatAdapter bridges *db.Queries to both admin.OpenAICompatQuerier
// and openai.LoaderQuerier. It translates between the sqlc-generated
// db.OpenaiCompatProvider struct (snake_case fields like BaseUrl,
// ApiKeyEncrypted) and the handler/loader interfaces (CamelCase: BaseURL,
// APIKeyEncrypted).
type openaiCompatAdapter struct {
	q *db.Queries
}

func (a *openaiCompatAdapter) ListOpenAICompatProviders(ctx context.Context) ([]admin.OpenAICompatRow, error) {
	rows, err := a.q.ListOpenAICompatProviders(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]admin.OpenAICompatRow, len(rows))
	for i, r := range rows {
		result[i] = sqlcRowToAdminRow(r)
	}
	return result, nil
}

func (a *openaiCompatAdapter) GetOpenAICompatProviderByID(ctx context.Context, id int64) (admin.OpenAICompatRow, error) {
	r, err := a.q.GetOpenAICompatProviderByID(ctx, id)
	if err != nil {
		return admin.OpenAICompatRow{}, err
	}
	return sqlcRowToAdminRow(r), nil
}

func (a *openaiCompatAdapter) GetOpenAICompatProviderByName(ctx context.Context, name string) (admin.OpenAICompatRow, error) {
	r, err := a.q.GetOpenAICompatProviderByName(ctx, name)
	if err != nil {
		return admin.OpenAICompatRow{}, err
	}
	return sqlcRowToAdminRow(r), nil
}

func (a *openaiCompatAdapter) CreateOpenAICompatProvider(ctx context.Context, row admin.OpenAICompatRow) (admin.OpenAICompatRow, error) {
	r, err := a.q.CreateOpenAICompatProvider(ctx, db.CreateOpenAICompatProviderParams{
		Name:            row.Name,
		BaseUrl:         row.BaseURL,
		ApiKeyEncrypted: row.APIKeyEncrypted,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	})
	if err != nil {
		return admin.OpenAICompatRow{}, err
	}
	return sqlcRowToAdminRow(r), nil
}

func (a *openaiCompatAdapter) UpdateOpenAICompatProvider(ctx context.Context, row admin.OpenAICompatRow) (admin.OpenAICompatRow, error) {
	r, err := a.q.UpdateOpenAICompatProvider(ctx, db.UpdateOpenAICompatProviderParams{
		ID:              row.ID,
		Name:            row.Name,
		BaseUrl:         row.BaseURL,
		ApiKeyEncrypted: row.APIKeyEncrypted,
		UpdatedAt:       row.UpdatedAt,
	})
	if err != nil {
		return admin.OpenAICompatRow{}, err
	}
	return sqlcRowToAdminRow(r), nil
}

func (a *openaiCompatAdapter) DeleteOpenAICompatProvider(ctx context.Context, id int64) error {
	return a.q.DeleteOpenAICompatProvider(ctx, id)
}

// ListOpenAICompatProvidersForLoader satisfies openai.LoaderQuerier. It
// returns only the fields the loader needs (name, base URL, encrypted key).
func (a *openaiCompatAdapter) ListOpenAICompatProvidersForLoader(ctx context.Context) ([]openaicompatllm.LoaderRow, error) {
	rows, err := a.q.ListOpenAICompatProviders(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]openaicompatllm.LoaderRow, len(rows))
	for i, r := range rows {
		result[i] = openaicompatllm.LoaderRow{
			Name:            r.Name,
			BaseURL:         r.BaseUrl,
			APIKeyEncrypted: r.ApiKeyEncrypted,
		}
	}
	return result, nil
}

// sqlcRowToAdminRow converts a sqlc-generated db.OpenaiCompatProvider to the
// admin.OpenAICompatRow shape used by the handler and adapter interfaces.
// The field name mapping (BaseUrl→BaseURL, ApiKeyEncrypted→APIKeyEncrypted)
// is the only translation needed.
func sqlcRowToAdminRow(r db.OpenaiCompatProvider) admin.OpenAICompatRow {
	return admin.OpenAICompatRow{
		ID:              r.ID,
		Name:            r.Name,
		BaseURL:         r.BaseUrl,
		APIKeyEncrypted: r.ApiKeyEncrypted,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

func (a *modelFilterAdapter) ListEnabledModels(ctx context.Context) ([]api.EnabledModel, error) {
	rows, err := a.q.ListEnabledModels(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]api.EnabledModel, len(rows))
	for i, r := range rows {
		result[i] = api.EnabledModel{Provider: r.Provider, ModelName: r.ModelName}
	}
	return result, nil
}

// webhookSecretEncrypterAdapter wraps admin.Encrypt and admin.Decrypt so the
// policy package can encrypt/decrypt webhook secrets without importing admin.
// It satisfies both the policy.secretEncrypter interface and the decrypter
// extension interface checked via type assertion in service.go.
type webhookSecretEncrypterAdapter struct {
	key []byte
}

func (a *webhookSecretEncrypterAdapter) EncryptWebhookSecret(plaintext string) (string, error) {
	return admin.Encrypt(a.key, plaintext)
}

func (a *webhookSecretEncrypterAdapter) DecryptWebhookSecret(ciphertext string) (string, error) {
	return admin.Decrypt(a.key, ciphertext)
}

// countEncryptedWebhookSecrets returns the number of policies with a non-NULL
// webhook_secret_encrypted column. Used at startup to warn when the encryption
// key is absent but encrypted secrets exist.
func countEncryptedWebhookSecrets(ctx context.Context, store *db.Store) (int, error) {
	var n int
	err := store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM policies WHERE webhook_secret_encrypted IS NOT NULL`,
	).Scan(&n)
	return n, err
}

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

// arbiterClassifier uses the shared tool namespace arbiter to determine whether
// a dot-name tool grant belongs to a plugin instance. This is the production
// implementation of runpkg.ToolSourceClassifier.
type arbiterClassifier struct {
	arbiter *toolregistry.Registry
}

func (c *arbiterClassifier) IsPluginTool(dotName string) bool {
	src, ok := c.arbiter.Lookup(dotName)
	return ok && src.Kind == toolregistry.KindPlugin
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
// main.go (not in a separate package) because it wires together multiple internal
// packages that must not import each other — the same pattern as pluginDispatchAdapter.
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

		inst, err := r.q.GetPluginInstanceByGlobalName(ctx, instanceName)
		if err != nil {
			return nil, fmt.Errorf("plugin tool %q: instance %q not found: %w", g.Tool, instanceName, err)
		}

		manifest, err := r.snap.ForPluginID(ctx, inst.PluginID)
		if err != nil {
			return nil, fmt.Errorf("plugin tool %q: manifest lookup: %w", g.Tool, err)
		}

		var toolDecl *sdkmanifest.ToolDecl
		for i := range manifest.Tools {
			if manifest.Tools[i].Name == toolName {
				toolDecl = &manifest.Tools[i]
				break
			}
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
// between the two types at wiring time (main.go), keeping the package boundary
// clean. The pattern mirrors managerConnFactory and PluginProcessManager.
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

// pluginInstanceResolver adapts *db.Queries to satisfy policy.InstanceManifestResolver.
// It looks up a plugin instance by its human-readable name across all plugins.
type pluginInstanceResolver struct {
	q *db.Queries
}

func (r *pluginInstanceResolver) ResolveInstanceByName(ctx context.Context, name string) (string, error) {
	inst, err := r.q.GetPluginInstanceByGlobalName(ctx, name)
	if err != nil {
		return "", fmt.Errorf("resolve instance %q: %w", name, err)
	}
	return inst.ID, nil
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
