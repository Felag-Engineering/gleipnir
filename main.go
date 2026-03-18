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
	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
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
	r.Get("/api/v1/events", sseHandler.ServeHTTP)

	registry := mcp.NewRegistry(store.Queries(), mcp.WithMCPTimeout(cfg.MCPTimeout))
	runManager := trigger.NewRunManager()
	claudeClient := anthropic.NewClient()
	launcher := trigger.NewRunLauncher(store, registry, runManager, trigger.NewAgentFactory(&claudeClient), broadcaster)

	webhookHandler := trigger.NewWebhookHandler(store, launcher)
	r.With(middleware.Throttle(10), api.BodySizeLimit(api.MaxRequestBodySize)).Post("/api/v1/webhooks/{policyID}", webhookHandler.Handle)

	manualTriggerHandler := trigger.NewManualTriggerHandler(store, launcher)
	r.With(api.BodySizeLimit(api.MaxRequestBodySize)).Post("/api/v1/policies/{policyID}/trigger", manualTriggerHandler.Handle)

	scheduler := trigger.NewScheduler(store, launcher)
	if err := scheduler.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}

	runsHandler := trigger.NewRunsHandler(store, runManager)
	r.Get("/api/v1/runs", runsHandler.List)
	r.Get("/api/v1/runs/{runID}", runsHandler.Get)
	r.Get("/api/v1/runs/{runID}/steps", runsHandler.ListSteps)
	r.Post("/api/v1/runs/{runID}/cancel", runsHandler.Cancel)

	policySvc := policy.NewService(store, nil, policy.NewAnthropicModelValidator(&claudeClient))

	// Mount /api/v1/policies and /api/v1/mcp route groups.
	// Existing /api/v1/webhooks/ and /api/v1/runs/ routes remain on this root router.
	r.Mount("/api/v1", api.NewRouter(store, policySvc, registry))

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
