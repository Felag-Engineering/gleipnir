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

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rapp992/gleipnir/frontend"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/approval"
	"github.com/rapp992/gleipnir/internal/auth"
	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
	anthropicllm "github.com/rapp992/gleipnir/internal/llm/anthropic"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/sse"
	"github.com/rapp992/gleipnir/internal/trigger"
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
	// SSE events are unprotected so the UI can receive events before auth UI is
	// implemented (follow-up issues will add login/logout).
	r.Get("/api/v1/events", sseHandler.ServeHTTP)

	registry := mcp.NewRegistry(store.Queries(), mcp.WithMCPTimeout(cfg.MCPTimeout))
	runManager := trigger.NewRunManager()
	claudeClient := anthropic.NewClient()
	llmClient := anthropicllm.NewClientFromEnv()
	providerRegistry := llm.NewProviderRegistry()
	providerRegistry.Register("anthropic", llmClient)
	launcher := trigger.NewRunLauncher(store, registry, runManager, trigger.NewAgentFactory(providerRegistry), broadcaster)

	// Webhooks are unprotected — they are called by external systems with their
	// own secret-based authentication (policy.trigger.secret in the policy YAML).
	webhookHandler := trigger.NewWebhookHandler(store, launcher)
	r.With(middleware.Throttle(10), api.BodySizeLimit(api.MaxRequestBodySize)).Post("/api/v1/webhooks/{policyID}", webhookHandler.Handle)

	scheduler := trigger.NewScheduler(store, launcher)
	if err := scheduler.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}

	authHandler := auth.NewHandler(store.Queries(), store.DB())
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

		policySvc := policy.NewService(store, nil, policy.NewAnthropicModelValidator(&claudeClient), providerRegistry)

		// Mount /api/v1/policies, /api/v1/mcp, /api/v1/stats, and /api/v1/health route groups.
		r.Mount("/api/v1", api.NewRouter(store, policySvc, registry))
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

	// Cancel the root context to stop the scheduler and any background timers.
	// Note: run contexts derive from context.Background() (see launcher.go), so
	// this does NOT cancel in-flight agent runs — CancelAll handles that below.
	cancel()

	// Signal all in-flight agent runs to stop.
	runManager.CancelAll()

	// Wait for agent runs to drain, with a timeout. We give runs 25 s so the
	// remaining 5 s budget can be used for the HTTP server shutdown.
	runsDrained := make(chan struct{})
	go func() {
		runManager.Wait()
		close(runsDrained)
	}()

	drainTimeout := 25 * time.Second
	select {
	case <-runsDrained:
		slog.Info("all agent runs drained")
	case <-time.After(drainTimeout):
		slog.Warn("agent run drain timed out, proceeding with server shutdown")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	return srv.Shutdown(shutdownCtx)
}
