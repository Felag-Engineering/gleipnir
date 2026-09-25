package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
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
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/infra/version"
	"github.com/felag-engineering/gleipnir/internal/llm"
	llmfactory "github.com/felag-engineering/gleipnir/internal/llm/factory"
	openaicompatllm "github.com/felag-engineering/gleipnir/internal/llm/openaicompat"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/timeout"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
	"github.com/felag-engineering/gleipnir/internal/trigger"
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

	store, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

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

	// Tool-initiated input requests (ADR-055) share the feedback scan interval:
	// both measure the same thing — how long a human has been asked to wait —
	// so a second knob would be a setting nobody could reason about separately.
	//
	// WithOnTerminated records a decision (ADR-055 §6.6) for every claim THIS
	// scanner wins — the restart backstop: a host that restarted while a run
	// was paused has no in-process Route call left to record anything, so the
	// scanner's own claim is the only settlement event that will ever happen
	// for that row (ADR-061).
	toolInputScanner := timeout.NewToolInputScanner(
		store,
		cfg.FeedbackScanInterval,
		timeout.WithPublisher(broadcaster),
		timeout.WithOnTerminated(agent.NewToolInputTimeoutDecisionHook(store.Queries())),
	)
	toolInputScanner.Start(ctx)

	// Apply the LLM transient-failure retry policy BEFORE any provider client is
	// constructed. The Anthropic/OpenAI SDKs retry internally — they read
	// MaxAttempts (via SDKMaxRetries) at construction. Google + openaicompat have
	// no built-in retry, so our manual loop handles them at call time (429/5xx
	// honoring Retry-After, plus connection errors with backoff + full jitter).
	llm.SetDefaultRetryConfig(llm.RetryConfig{
		MaxAttempts:    cfg.LLMRetryMaxAttempts,
		InitialBackoff: cfg.LLMRetryInitialBackoff,
		MaxBackoff:     cfg.LLMRetryMaxBackoff,
	})

	runManager := runpkg.NewRunManager()
	providerRegistry := llm.NewProviderRegistry()

	// Parse the encryption key for admin API key storage.
	var encryptionKey []byte
	if raw := cfg.EncryptionKey; raw != "" {
		var err error
		encryptionKey, err = crypto.ParseEncryptionKey(raw)
		if err != nil {
			return fmt.Errorf("parse GLEIPNIR_ENCRYPTION_KEY: %w", err)
		}
	}

	if encryptionKey == nil {
		slog.Warn("GLEIPNIR_ENCRYPTION_KEY not set — admin API key management will be unavailable")
	}

	// Cross-source tool namespace arbiter: a single in-memory registry shared
	// by the MCP server creation path and the plugin tool registrar.
	// Constructed once here so both sides see the same state.
	arbiter := toolregistry.New()

	// systemSettings is constructed early so startPluginSubsystem can use it for
	// the OAuth getPublicURL closure. It is also used by the provider bootstrap
	// loop and the launcher below.
	systemSettings := settings.NewService(store.Queries())

	// Bring up the plugin subsystem. Exactly one implementation of
	// pluginSubsystem is compiled into any given binary — plugins_v1.go (the
	// default build) or plugins_v2.go (-tags substratev2) — so run() never
	// reaches into either substrate's concrete fields directly.
	pluginSys, err := startPluginSubsystem(ctx, cfg, store, broadcaster, encryptionKey, arbiter, systemSettings)
	if err != nil {
		return fmt.Errorf("start plugin subsystem: %w", err)
	}
	pluginLauncherDeps := pluginSys.launcherDeps()
	runManager.WithPluginCanceller(pluginLauncherDeps.Canceller)

	// Host-plane invariant (mcp-realignment-spec.md §8, ADR-057): no
	// host-endpoint tool name may sit in the shared tool namespace, because
	// everything there is discoverable and grantable to an agent. The plugin
	// runtime has made its reservations by this point; MCP-side reservations
	// happen lazily later, but those come from the operator-registered server
	// rows this same check re-runs against on the conformance suite
	// (milestone #20). Refusing to start beats serving a policy gate that
	// silently grants host tools — the #871 posture, applied to the
	// capability boundary itself.
	if err := hostendpoint.AssertHostPlane(arbiter); err != nil {
		return err
	}

	// Registry construction is placed after encryption key parsing so
	// WithEncryptionKey can be passed at construction time.
	registry := mcp.NewRegistry(store.Queries(),
		mcp.WithMCPTimeout(cfg.MCPTimeout),
		mcp.WithEncryptionKey(encryptionKey),
		mcp.WithToolNamespaceArbiter(arbiter),
		mcp.WithElicitationControls(
			mcp.ElicitationLimits{
				MaxRequestStateBytes: cfg.ElicitationMaxRequestStateBytes,
				MaxRequests:          cfg.ElicitationMaxRequests,
				MaxRequestsBytes:     cfg.ElicitationMaxRequestsBytes,
			},
			cfg.ElicitationRatePerSec,
			cfg.ElicitationBurst,
		),
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
	adminHandler := admin.NewHandler(adminQuerier, systemSettings, encryptionKey, knownProviders, configureProvider, removeProvider, providerRegistry)

	// Bootstrap providers from DB-stored encrypted API keys.
	for _, provName := range knownProviders {
		row, err := store.Queries().GetSystemSetting(ctx, provName+"_api_key")
		if err != nil {
			continue
		}
		apiKey, err := crypto.Decrypt(encryptionKey, row.Value)
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
	if err := openaicompatllm.LoadAndRegister(ctx, openaiAdapter, encryptionKey, providerRegistry, crypto.Decrypt); err != nil {
		slog.Error("failed to load openai-compat providers at startup", "err", err)
	}

	// Ensure the configured default model has an enabled=1 row so that existing
	// deployments are not locked out after the semantic flip (new/unseen models
	// now default to disabled). If the row already exists with enabled=1, the
	// upsert is a no-op.
	if err := ensureDefaultModelEnabled(ctx, store.Queries(), systemSettings); err != nil {
		slog.Warn("could not ensure default model is enabled", "err", err)
	}

	launcher := runpkg.NewRunLauncher(runpkg.RunLauncherConfig{
		Store:                  store,
		Resolver:               runpkg.NewDefaultToolResolver(registry, pluginLauncherDeps.ToolClassifier, pluginLauncherDeps.ToolResolver),
		Manager:                runManager,
		AgentFactory:           runpkg.NewAgentFactory(providerRegistry),
		Publisher:              broadcaster,
		DefaultFeedbackTimeout: cfg.DefaultFeedbackTimeout,
		ModelResolver:          systemSettings,
		PluginRegistrar:        pluginLauncherDeps.Registrar,
		PluginDispatcher:       pluginLauncherDeps.Dispatcher,
		ApprovalDispatcher:     pluginLauncherDeps.ApprovalDispatcher,
		FeedbackDispatcher:     pluginLauncherDeps.FeedbackDispatcher,
	})

	// Complete the two-phase wiring now that the launcher is available: the
	// trigger dispatcher (v1) or its v2 equivalent needs the launcher to fire
	// runs.
	pluginSys.bindLauncher(ctx, launcher)

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

	// The one policy.Service. It serves the policy routes AND the webhook
	// rotate/reveal handler; there used to be a separate service per consumer,
	// each holding a different subset of the collaborators, which is how ADR-017
	// tool lookup (#788) and then ADR-048 binding validation (#870) each ended
	// up wired to a service that nothing routed through. One construction site
	// is what makes RequireComplete below able to speak for the whole system.
	//
	// *mcp.Registry satisfies policy.ToolLookup. Assign through a nil check: a
	// typed-nil *mcp.Registry stored in the interface would make Service.lookup
	// non-nil and panic on first use.
	var toolLookup policy.ToolLookup
	if registry != nil {
		toolLookup = registry
	}
	// adminDeps is fetched once, here, because ManifestSnap is needed before
	// the plugin admin surface itself is built below.
	pluginAdminDeps := pluginSys.adminDeps()

	subscribedResolver := &pluginInstanceResolver{q: store.Queries()}
	policyService := policy.NewService(store, toolLookup, providerRegistry, providerRegistry, systemSettings)
	policyService.WithSubscribedBindingValidator(
		policy.NewSubscribedBindingValidator(subscribedResolver, pluginAdminDeps.ManifestSnap),
	)
	if webhookEncrypter != nil {
		policyService.WithWebhookSecretEncrypter(webhookEncrypter)
	}
	// Refuse to serve with a collaborator missing. A nil one does not fail —
	// it makes its check quietly do nothing, which is exactly what nobody
	// noticed twice (#871).
	if err := policyService.RequireComplete(); err != nil {
		return fmt.Errorf("wire policy service: %w", err)
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
		PolicyService:    policyService,
	}

	// Phase 2: HTTP handlers.
	authHandler := auth.NewHandler(store.Queries(), store.DB())
	settingsHandler := auth.NewSettingsHandler(store.Queries())

	// ManifestSnap is shared between the audience handler, binding test handler,
	// and the plugin tool resolver (constructed inside the plugin subsystem).
	snap := pluginAdminDeps.ManifestSnap
	audienceH := api.NewAudienceHandler(store, snap, time.Now)
	bindingTestH := api.NewBindingTestHandler(snap)

	// Wire OAuth handlers and the public-URL rescan hook. All three are populated
	// by the plugin subsystem only when an encryption key is set; nil otherwise.
	pluginOAuthHandler := pluginAdminDeps.OAuthHandler
	pluginCredHandler := pluginAdminDeps.CredentialsHandler
	pluginOptionsHandler := pluginAdminDeps.OptionsHandler
	if pluginAdminDeps.OnPublicURLChanged != nil {
		adminHandler.OnPublicURLChanged = pluginAdminDeps.OnPublicURLChanged
	}

	// Build InstanceLifecycle and InstanceConfig modules before PluginHandler so
	// all deps are constructor-injected (no late-bind setters, per issue #504).
	//
	// These deps come from the plugin subsystem and are non-nil in normal
	// operation. The nil-guards inside adminDeps() are defensive: the admin
	// helpers are designed to tolerate absent deps (DB-only cleanup still
	// works), which keeps them test-injectable and safe against a
	// partially-initialized runtime.
	//
	// processManager and pluginsDir are shared by reference/value into BOTH the
	// InstanceLifecycleDeps AND the PluginHandlerDeps — both holders use the
	// one shared instance (plan §DEPS THAT STAY).
	pluginLifecycle := admin.NewInstanceLifecycle(admin.InstanceLifecycleDeps{
		Q:          store.Queries(),
		Store:      store,
		Publisher:  broadcaster,
		ProcMgr:    pluginAdminDeps.ProcMgr,
		Trigger:    pluginAdminDeps.Trigger,
		Inflight:   pluginAdminDeps.Inflight,
		Evictor:    pluginAdminDeps.Evictor,
		PluginsDir: pluginAdminDeps.PluginsDir,
		Unreg:      pluginAdminDeps.Unregistrar,
	})
	pluginConfig := admin.NewInstanceConfig(admin.InstanceConfigDeps{
		Q:         store.Queries(),
		Publisher: broadcaster,
		Trigger:   pluginAdminDeps.Trigger,
	})
	pluginAdmin := admin.NewPluginHandler(admin.PluginHandlerDeps{
		Q:                store.Queries(),
		Publisher:        broadcaster,
		Installer:        pluginAdminDeps.Installer,
		RSSAggregator:    pluginAdminDeps.RSSAggregator,
		ProcessManager:   pluginAdminDeps.ProcMgr,
		PluginsDir:       pluginAdminDeps.PluginsDir,
		Lifecycle:        pluginLifecycle,
		Config:           pluginConfig,
		CredentialSeeder: pluginAdminDeps.CredentialSeeder,
	})

	handlers := api.HandlerBundle{
		AuthHandler:              authHandler,
		SettingsHandler:          settingsHandler,
		AdminHandler:             adminHandler,
		OpenAICompatHandler:      openaiCompatHandler,
		PluginAdminHandler:       pluginAdmin,
		PluginOAuthHandler:       pluginOAuthHandler,
		PluginCredentialsHandler: pluginCredHandler,
		PluginOptionsHandler:     pluginOptionsHandler,
		AudienceHandler:          audienceH,
		BindingTestHandler:       bindingTestH,
		WebhookHandler:           webhookHandler,
		SSEHandler:               sseHandler,
		PolicyWebhookHandler:     policyWebhookHandler,
	}

	// Register the post-install spawn hook so that a fresh install (via the
	// admin endpoint or the fsnotify watcher) immediately spawns the plugin
	// subprocess — no server restart required (#386). The same Installer instance
	// is used by both paths so this registration covers both.
	if pluginAdminDeps.Installer != nil && pluginAdminDeps.ProcMgr != nil {
		procMgr := pluginAdminDeps.ProcMgr
		pluginAdminDeps.Installer.OnInstalled(func(ctx context.Context, pluginID string) {
			if err := procMgr.StartByPluginID(ctx, pluginID); err != nil {
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
			SignatureVerificationDisabled: cfg.AllowUnsignedPlugins,
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

	// Bind the listener explicitly before serving so the "ready" banner is only
	// shown once the socket is actually accepting connections. A failed bind
	// (e.g. the port is already in use) must fail loudly rather than advertise a
	// URL that doesn't work.
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		slog.Error("failed to bind listen address", "addr", cfg.ListenAddr, "err", err)
		os.Exit(1)
	}

	// public_url (ADR-035), when set, is the canonical URL to advertise; on any
	// read error we simply fall back to the localhost form in the banner.
	publicURL, err := systemSettings.GetPublicURL(ctx)
	if err != nil {
		slog.Warn("could not read public_url for startup banner", "err", err)
		publicURL = ""
	}

	// httpWG tracks the Serve goroutine so main can confirm it has exited after
	// Shutdown returns. Without this, a late panic from the listener could race
	// the process exit.
	var httpWG sync.WaitGroup
	httpWG.Add(1)
	go func() {
		defer httpWG.Done()
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "err", err)
			quit <- syscall.SIGTERM
		}
	}()

	slog.Info("server listening", "addr", cfg.ListenAddr)
	// Human-facing affordance: a plain "ready → open this URL" notice so an
	// operator staring at `compose up` output knows it booted and where to go
	// (structured logs are easy to miss / some compose providers swallow them).
	printReadyBanner(os.Stdout, version.Version, cfg.ListenAddr, publicURL)

	<-quit
	slog.Info("shutting down")

	// Cancel the root context to stop the scheduler, poller, and any background timers.
	// Note: run contexts derive from context.Background() (see launcher.go), so
	// this does NOT cancel in-flight agent runs — CancelAll handles that below.
	cancel()

	// Quiesce plugin trigger ingress BEFORE draining runs. cancel() above only
	// signals the supervisor's stream goroutines cooperatively; a goroutine could
	// still pass its ctx check and reach RunLauncher.Launch — which does the
	// RunManager wg.Add — after runManager.Wait() returns below. That add-after-Wait
	// leaves the run unawaited and racing dispatch-pool teardown. quiesce()
	// calls TriggerSupervisor.StopAll() (v1) or its v2 equivalent, which
	// synchronously cancels and joins every stream goroutine, so no new Launch
	// can land once it returns. Doing this before CancelAll means any run that
	// does land during the quiesce window is still cancelled by CancelAll and
	// awaited by the drain below (#500).
	pluginSys.quiesce()

	// Signal all in-flight agent runs to stop.
	runManager.CancelAll()

	// Wait for poll loops, cron loops, scheduled timers, timeout scanners, and
	// agent runs to drain, with a timeout. The trigger loops/timers and scanner
	// loops should exit quickly once their root context is cancelled (cancel()
	// above), but an in-flight scheduled fire() or scanner resolveTimeout() — the
	// latter writes run steps and transitions run state — must be allowed to
	// finish rather than be cut off mid-flight. Agent runs may take longer, so
	// all are waited concurrently and bounded by cfg.DrainTimeout below (#487).
	runsDrained := make(chan struct{})
	go func() {
		poller.Wait()
		cronRunner.Wait()
		scheduler.Wait()
		approvalScanner.Wait()
		feedbackScanner.Wait()
		toolInputScanner.Wait()
		runManager.Wait()
		close(runsDrained)
	}()

	select {
	case <-runsDrained:
		slog.Info("all agent runs drained")
	case <-time.After(cfg.DrainTimeout):
		slog.Warn("agent run drain timed out, proceeding with server shutdown")
	}

	// Stop the plugin subsystem (trigger supervisor → subprocesses → dispatch pool).
	pluginSys.shutdown()

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

// webhookSecretEncrypterAdapter wraps crypto.Encrypt and crypto.Decrypt so the
// policy package can encrypt/decrypt webhook secrets through the SecretCipher
// interface without binding to a concrete crypto helper.
// It satisfies both the policy.secretEncrypter interface and the decrypter
// extension interface checked via type assertion in service.go.
type webhookSecretEncrypterAdapter struct {
	key []byte
}

func (a *webhookSecretEncrypterAdapter) EncryptWebhookSecret(plaintext string) (string, error) {
	return crypto.Encrypt(a.key, plaintext)
}

func (a *webhookSecretEncrypterAdapter) DecryptWebhookSecret(ciphertext string) (string, error) {
	return crypto.Decrypt(a.key, ciphertext)
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
