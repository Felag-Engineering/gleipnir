package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/auth"
	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
	anthropicllm "github.com/rapp992/gleipnir/internal/llm/anthropic"
	googlellm "github.com/rapp992/gleipnir/internal/llm/google"
	openaillm "github.com/rapp992/gleipnir/internal/llm/openai"
	openaicompatllm "github.com/rapp992/gleipnir/internal/llm/openaicompat"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/policy"
	runpkg "github.com/rapp992/gleipnir/internal/run"
	"github.com/rapp992/gleipnir/internal/sse"
	"github.com/rapp992/gleipnir/internal/timeout"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// version is set via ldflags at build time.
var version = "dev"

// knownProviders is the list of LLM providers the system supports.
var knownProviders = []string{"anthropic", "google", "openai"}

const (
	// drainTimeout is how long we wait for in-flight agent runs to finish
	// before proceeding with HTTP server shutdown.
	drainTimeout = 25 * time.Second

	// shutdownTimeout is the time budget for the HTTP server's graceful
	// shutdown after agent runs have drained (or timed out).
	shutdownTimeout = 5 * time.Second
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if err := run(cfg); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfg config.Config) error {
	startTime := time.Now()

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

	registry := mcp.NewRegistry(store.Queries(), mcp.WithMCPTimeout(cfg.MCPTimeout))
	runManager := runpkg.NewRunManager()
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

	// configureProvider creates an LLM client and registers it in the provider
	// registry. Called both at bootstrap (from DB) and when an admin saves a key.
	configureProvider := func(ctx context.Context, provider string, apiKey string) error {
		var client llm.LLMClient
		var err error
		switch provider {
		case "anthropic":
			client = anthropicllm.NewClient(apiKey)
		case "google":
			client, err = googlellm.NewClient(ctx, apiKey)
			if err != nil {
				return fmt.Errorf("create google client: %w", err)
			}
		case "openai":
			client = openaillm.NewClient(apiKey)
		default:
			return fmt.Errorf("unknown provider %q", provider)
		}
		providerRegistry.Register(provider, client)
		return nil
	}

	removeProvider := func(provider string) {
		providerRegistry.Unregister(provider)
	}

	adminQuerier := admin.NewQuerierAdapter(store.Queries())
	adminHandler := admin.NewHandler(adminQuerier, encryptionKey, knownProviders, configureProvider, removeProvider, providerRegistry)

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
	if err := ensureDefaultModelEnabled(ctx, store.Queries(), adminHandler); err != nil {
		slog.Warn("could not ensure default model is enabled", "err", err)
	}

	launcher := runpkg.NewRunLauncher(store, registry, runManager, runpkg.NewAgentFactory(providerRegistry), broadcaster, cfg.DefaultFeedbackTimeout)

	webhookSecretLoader := trigger.NewSecretLoader(store.Queries(), encryptionKey)
	webhookHandler := trigger.NewWebhookHandler(store, launcher, webhookSecretLoader)

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
	policyService := policy.NewService(store, nil, providerRegistry, providerRegistry, adminHandler)
	if webhookEncrypter != nil {
		policyService.WithWebhookSecretEncrypter(webhookEncrypter)
	}
	policyWebhookHandler := api.NewPolicyWebhookHandler(policyService)

	scheduler := trigger.NewScheduler(store, launcher)
	if err := scheduler.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}

	poller := trigger.NewPoller(store, launcher, registry)
	if err := poller.Start(ctx); err != nil {
		return fmt.Errorf("start poller: %w", err)
	}

	authHandler := auth.NewHandler(store.Queries(), store.DB())
	settingsHandler := auth.NewSettingsHandler(store.Queries())

	r := api.BuildRouter(api.RouterConfig{
		Store:                store,
		Broadcaster:          broadcaster,
		Registry:             registry,
		RunManager:           runManager,
		Launcher:             launcher,
		ModelLister:          providerRegistry,
		ProviderRegistry:     providerRegistry,
		ModelFilter:          &modelFilterAdapter{q: store.Queries()},
		AuthHandler:          authHandler,
		SettingsHandler:      settingsHandler,
		AdminHandler:         adminHandler,
		OpenAICompatHandler:  openaiCompatHandler,
		WebhookHandler:       webhookHandler,
		SSEHandler:           sseHandler,
		PolicyWebhookHandler: policyWebhookHandler,
		Poller:               poller,
		Scheduler:            scheduler,
		Version:              version,
		StartTime:            startTime,
		DBPath:               cfg.DBPath,
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

	// Wait for poll loops and agent runs to drain, with a timeout. Poll loops
	// should exit quickly (they are just sleeping timers). Agent runs may take
	// longer, so both are waited concurrently.
	runsDrained := make(chan struct{})
	go func() {
		poller.Wait()
		runManager.Wait()
		close(runsDrained)
	}()

	select {
	case <-runsDrained:
		slog.Info("all agent runs drained")
	case <-time.After(drainTimeout):
		slog.Warn("agent run drain timed out, proceeding with server shutdown")
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
func ensureDefaultModelEnabled(ctx context.Context, q *db.Queries, h *admin.Handler) error {
	provider, model, err := h.GetSystemDefault(ctx)
	if err != nil {
		// No default model configured — nothing to do.
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
