package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/infra/version"
	"github.com/felag-engineering/gleipnir/internal/llm"
	llmfactory "github.com/felag-engineering/gleipnir/internal/llm/factory"
	openaicompatllm "github.com/felag-engineering/gleipnir/internal/llm/openaicompat"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
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
	toolInputScanner := timeout.NewToolInputScanner(
		store,
		cfg.FeedbackScanInterval,
		timeout.WithPublisher(broadcaster),
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

	// systemSettings is constructed early so startPluginRuntime can use it for
	// the OAuth getPublicURL closure. It is also used by the provider bootstrap
	// loop and the launcher below.
	systemSettings := settings.NewService(store.Queries())

	// Bring up the plugin subsystem. On success rt is always non-nil; some of its
	// fields are nil when no encryption key is configured (OAuth/credentials).
	rt, err := startPluginRuntime(ctx, cfg, store, broadcaster, encryptionKey, arbiter, systemSettings)
	if err != nil {
		return fmt.Errorf("start plugin runtime: %w", err)
	}
	runManager.WithPluginCanceller(rt.Pool)

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
		Resolver:               runpkg.NewDefaultToolResolver(registry, rt.ToolClassifier, rt.ToolResolver),
		Manager:                runManager,
		AgentFactory:           runpkg.NewAgentFactory(providerRegistry),
		Publisher:              broadcaster,
		DefaultFeedbackTimeout: cfg.DefaultFeedbackTimeout,
		ModelResolver:          systemSettings,
		PluginRegistrar:        rt.ToolRegistrar,
		PluginDispatcher:       rt.DispatchAdapter,
		ApprovalDispatcher:     rt.ApprovalAdapter,
		FeedbackDispatcher:     rt.FeedbackAdapter,
	})

	// Wire the trigger supervisor now that the launcher is available. The trigger
	// dispatcher needs the launcher (to fire runs); wireTriggerSupervisor is
	// therefore a two-phase completion of the plugin runtime.
	rt.wireTriggerSupervisor(ctx, launcher, store, broadcaster, systemSettings)

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
	subscribedResolver := &pluginInstanceResolver{q: store.Queries()}
	policyService := policy.NewService(store, toolLookup, providerRegistry, providerRegistry, systemSettings)
	policyService.WithSubscribedBindingValidator(
		policy.NewSubscribedBindingValidator(subscribedResolver, rt.ManifestSnap),
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
	// and the plugin tool resolver (constructed in startPluginRuntime).
	snap := rt.ManifestSnap
	audienceH := api.NewAudienceHandler(store, snap, time.Now)
	bindingTestH := api.NewBindingTestHandler(snap)

	// Wire OAuth handlers and the public-URL rescan hook. All three are populated
	// by startPluginRuntime only when an encryption key is set; nil otherwise.
	pluginOAuthHandler := rt.OAuthHandler
	pluginCredHandler := rt.CredentialsHandler
	pluginOptionsHandler := rt.OptionsHandler
	if rt.OnPublicURLChanged != nil {
		adminHandler.OnPublicURLChanged = rt.OnPublicURLChanged
	}

	// Build InstanceLifecycle and InstanceConfig modules before PluginHandler so
	// all deps are constructor-injected (no late-bind setters, per issue #504).
	//
	// These deps come from the plugin runtime and are non-nil in normal
	// operation. The nil-guards are defensive: the admin helpers are designed to
	// tolerate absent deps (DB-only cleanup still works), which keeps them
	// test-injectable and safe against a partially-initialized runtime.
	//
	// processManager and pluginsDir are shared by reference/value into BOTH the
	// InstanceLifecycleDeps AND the PluginHandlerDeps — both holders use the
	// one shared instance (plan §DEPS THAT STAY).
	var (
		pluginProcMgr    admin.PluginProcessManager
		pluginTrigger    admin.TriggerRestarter
		pluginInflight   admin.InflightCounter
		pluginEvictor    admin.ToolConnEvictor
		pluginPluginsDir string
		pluginInstaller  admin.PluginInstaller
		pluginRSSAgg     admin.RSSAggregator
	)
	if rt.TriggerSupervisor != nil {
		pluginTrigger = rt.TriggerSupervisor
	}
	if rt.Loader().Installer() != nil {
		pluginInstaller = rt.Loader().Installer()
		if mgr := rt.Manager(); mgr != nil {
			pluginProcMgr = mgr
			pluginPluginsDir = cfg.PluginsDir
			pluginInflight = rt.Pool
			pluginEvictor = rt.Pool
		}
	}
	if mgr := rt.Manager(); mgr != nil {
		rssSampler := process.NewRSSSampler(mgr.Snapshot)
		rssSampler.Start(ctx, 30*time.Second)
		pluginRSSAgg = rssAggregatorAdapter{sampler: rssSampler}
	}

	pluginLifecycle := admin.NewInstanceLifecycle(admin.InstanceLifecycleDeps{
		Q:          store.Queries(),
		Store:      store,
		Publisher:  broadcaster,
		ProcMgr:    pluginProcMgr,
		Trigger:    pluginTrigger,
		Inflight:   pluginInflight,
		Evictor:    pluginEvictor,
		PluginsDir: pluginPluginsDir,
		Unreg:      rt.ToolRegistrar,
	})
	pluginConfig := admin.NewInstanceConfig(admin.InstanceConfigDeps{
		Q:         store.Queries(),
		Publisher: broadcaster,
		Trigger:   pluginTrigger,
	})
	// Seed the credential blob on instance create (#572). rt.CredStore is nil
	// when no encryption key is configured; we must avoid stuffing a typed-nil
	// pointer into the CredentialSeeder interface (that would defeat the handler's
	// nil-guard), so only set the field when the store is genuinely present.
	var pluginCredSeeder admin.CredentialSeeder
	if rt.CredStore != nil {
		pluginCredSeeder = rt.CredStore
	}
	pluginAdmin := admin.NewPluginHandler(admin.PluginHandlerDeps{
		Q:                store.Queries(),
		Publisher:        broadcaster,
		Installer:        pluginInstaller,
		RSSAggregator:    pluginRSSAgg,
		ProcessManager:   pluginProcMgr,
		PluginsDir:       pluginPluginsDir,
		Lifecycle:        pluginLifecycle,
		Config:           pluginConfig,
		CredentialSeeder: pluginCredSeeder,
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
	if mgr := rt.Manager(); mgr != nil && rt.Loader().Installer() != nil {
		rt.Loader().Installer().OnInstalled(func(ctx context.Context, pluginID string) {
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
	// leaves the run unawaited and racing dispatch-pool teardown. quiesceTriggers
	// calls TriggerSupervisor.StopAll(), which synchronously cancels and joins every
	// stream goroutine, so no new Launch can land once it returns. Doing this before
	// CancelAll means any run that does land during the quiesce window is still
	// cancelled by CancelAll and awaited by the drain below (#500).
	rt.quiesceTriggers()

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

	// Stop the plugin runtime (trigger supervisor → subprocesses → dispatch pool).
	rt.shutdown()

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
