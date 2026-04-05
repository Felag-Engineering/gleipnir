package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rapp992/gleipnir/frontend"
	"github.com/rapp992/gleipnir/internal/agent"
	claudecode "github.com/rapp992/gleipnir/internal/agent/claudecode"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/approval"
	"github.com/rapp992/gleipnir/internal/auth"
	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/feedback"
	"github.com/rapp992/gleipnir/internal/llm"
	anthropicllm "github.com/rapp992/gleipnir/internal/llm/anthropic"
	googlellm "github.com/rapp992/gleipnir/internal/llm/google"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/sse"
	"github.com/rapp992/gleipnir/internal/trigger"
)

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

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	broadcaster := sse.NewBroadcaster()
	sseHandler := sse.NewHandler(broadcaster)

	approvalScanner := approval.NewScanner(
		store,
		cfg.ApprovalScanInterval,
		approval.WithPublisher(broadcaster),
	)
	approvalScanner.Start(ctx)

	feedbackScanner := feedback.NewScanner(
		store,
		cfg.FeedbackScanInterval,
		feedback.WithPublisher(broadcaster),
	)
	feedbackScanner.Start(ctx)
	// SSE events are unprotected so the UI can receive events before auth UI is
	// implemented (follow-up issues will add login/logout).
	r.Get("/api/v1/events", sseHandler.ServeHTTP)

	registry := mcp.NewRegistry(store.Queries(), mcp.WithMCPTimeout(cfg.MCPTimeout))
	runManager := trigger.NewRunManager()
	providerRegistry, err := buildProviderRegistry(ctx,
		os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("GOOGLE_API_KEY"),
		newAnthropicProvider, newGoogleProvider)
	if err != nil {
		return err
	}

	// claudeCodeFactory bridges agent.Config to claudecode.Config so the trigger
	// layer does not need to import internal/agent/claudecode directly.
	claudeCodeFactory := func(cfg agent.Config) (agent.Runner, error) {
		return claudecode.New(claudecode.Config{
			Policy:       cfg.Policy,
			Tools:        cfg.Tools,
			Audit:        cfg.Audit,
			StateMachine: cfg.StateMachine,
			ApprovalCh:   cfg.ApprovalCh,
			FeedbackCh:   cfg.FeedbackCh,
		})
	}

	launcher := trigger.NewRunLauncher(store, registry, runManager, trigger.NewAgentFactory(providerRegistry, claudeCodeFactory), broadcaster, cfg.DefaultFeedbackTimeout)

	// Webhooks are unprotected — they are called by external systems with their
	// own secret-based authentication (policy.trigger.secret in the policy YAML).
	webhookHandler := trigger.NewWebhookHandler(store, launcher)
	r.With(middleware.Throttle(10), api.BodySizeLimit(api.MaxRequestBodySize)).Post("/api/v1/webhooks/{policyID}", webhookHandler.Handle)

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
	r.Route("/api/v1/auth", func(r chi.Router) {
		r.Get("/status", authHandler.Status)
		r.With(api.BodySizeLimit(api.MaxRequestBodySize)).Post("/setup", authHandler.Setup)
		r.With(api.BodySizeLimit(api.MaxRequestBodySize)).Post("/login", authHandler.Login)
		r.Post("/logout", authHandler.Logout)
	})

	requireAuth := auth.RequireAuth(store.Queries())

	// Protected routes: all UI-facing API endpoints require a valid session cookie.
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)

		r.Get("/api/v1/auth/me", authHandler.Me)
		r.With(api.BodySizeLimit(api.MaxRequestBodySize)).Post("/api/v1/auth/password", authHandler.ChangePasswordHandler)
		r.Get("/api/v1/auth/sessions", authHandler.ListSessionsHandler)
		r.Delete("/api/v1/auth/sessions/{sessionID}", authHandler.RevokeSessionHandler)

		r.Get("/api/v1/settings/preferences", settingsHandler.GetPreferences)
		r.With(api.BodySizeLimit(api.MaxRequestBodySize)).Put("/api/v1/settings/preferences", settingsHandler.UpdatePreferences)

		r.Route("/api/v1/users", func(r chi.Router) {
			r.Use(auth.RequireRole(model.RoleAdmin))
			r.Get("/", authHandler.ListUsersHandler)
			r.With(api.BodySizeLimit(api.MaxRequestBodySize)).Post("/", authHandler.CreateUserHandler)
			r.With(api.BodySizeLimit(api.MaxRequestBodySize)).Patch("/{id}", authHandler.UpdateUserHandler)
		})

		manualTriggerHandler := trigger.NewManualTriggerHandler(store, launcher)
		r.With(api.BodySizeLimit(api.MaxRequestBodySize), auth.RequireRole(model.RoleOperator)).Post("/api/v1/policies/{policyID}/trigger", manualTriggerHandler.Handle)

		runsHandler := trigger.NewRunsHandler(store, runManager, broadcaster)
		r.With(auth.RequireRole(model.RoleOperator, model.RoleApprover, model.RoleAuditor)).Get("/api/v1/runs", runsHandler.List)
		r.With(auth.RequireRole(model.RoleOperator, model.RoleApprover, model.RoleAuditor)).Get("/api/v1/runs/{runID}", runsHandler.Get)
		r.With(auth.RequireRole(model.RoleOperator, model.RoleApprover, model.RoleAuditor)).Get("/api/v1/runs/{runID}/steps", runsHandler.ListSteps)
		r.With(auth.RequireRole(model.RoleOperator)).Post("/api/v1/runs/{runID}/cancel", runsHandler.Cancel)
		r.With(api.BodySizeLimit(api.MaxRequestBodySize), auth.RequireRole(model.RoleApprover)).Post("/api/v1/runs/{runID}/approval", runsHandler.SubmitApproval)
		r.With(api.BodySizeLimit(api.MaxRequestBodySize), auth.RequireRole(model.RoleApprover, model.RoleOperator)).Post("/api/v1/runs/{runID}/feedback", runsHandler.SubmitFeedback)

		policySvc := policy.NewService(store, nil, providerRegistry, providerRegistry, cfg.DefaultProvider, cfg.DefaultModel)

		// Mount /api/v1/policies, /api/v1/mcp, /api/v1/stats, and /api/v1/health route groups.
		r.Mount("/api/v1", api.NewRouter(store, policySvc, registry, providerRegistry))
	})

	// Serve the embedded React SPA for all non-API routes.
	r.Handle("/*", frontend.NewSPAHandler())

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
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

	return srv.Shutdown(shutdownCtx)
}

// providerFactory creates an LLMClient from an API key.
// The context parameter accommodates providers (like Google) that require it
// during client construction; Anthropic ignores it.
type providerFactory func(ctx context.Context, apiKey string) (llm.LLMClient, error)

// buildProviderRegistry creates and populates the provider registry based on
// which API keys are present. Returns an error if no providers are available.
// Factory functions allow tests to inject stubs without real API calls.
func buildProviderRegistry(ctx context.Context, anthropicKey, googleKey string, newAnthropic, newGoogle providerFactory) (*llm.ProviderRegistry, error) {
	reg := llm.NewProviderRegistry()
	count := 0

	if anthropicKey != "" {
		client, err := newAnthropic(ctx, anthropicKey)
		if err != nil {
			return nil, fmt.Errorf("create anthropic LLM client: %w", err)
		}
		reg.Register("anthropic", client)
		count++
	} else {
		slog.Warn("ANTHROPIC_API_KEY not set, Anthropic provider unavailable")
	}

	if googleKey != "" {
		client, err := newGoogle(ctx, googleKey)
		if err != nil {
			return nil, fmt.Errorf("create google LLM client: %w", err)
		}
		reg.Register("google", client)
		count++
	} else {
		slog.Warn("GOOGLE_API_KEY not set, Google provider unavailable")
	}

	if count == 0 {
		return nil, fmt.Errorf("no LLM providers available: set ANTHROPIC_API_KEY and/or GOOGLE_API_KEY")
	}
	return reg, nil
}

func newAnthropicProvider(_ context.Context, apiKey string) (llm.LLMClient, error) {
	return anthropicllm.NewClient(apiKey), nil
}

func newGoogleProvider(ctx context.Context, apiKey string) (llm.LLMClient, error) {
	return googlellm.NewClient(ctx, apiKey)
}
