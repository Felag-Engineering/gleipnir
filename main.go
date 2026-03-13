package main

import (
	"context"
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
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/sse"
	"github.com/rapp992/gleipnir/internal/trigger"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := envOrDefault("GLEIPNIR_DB_PATH", "/data/gleipnir.db")
	listenAddr := envOrDefault("GLEIPNIR_LISTEN_ADDR", ":8080")

	// Root context cancelled on shutdown so background components (Scheduler) can stop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := db.Open(dbPath)
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

	registry := mcp.NewRegistry(store.DB())
	runManager := trigger.NewRunManager()
	claudeClient := anthropic.NewClient()
	launcher := trigger.NewRunLauncher(store, registry, runManager, trigger.NewAgentFactory(&claudeClient), broadcaster)

	webhookHandler := trigger.NewWebhookHandler(store, launcher)
	r.With(middleware.Throttle(10), api.BodySizeLimit(1<<20)).Post("/api/v1/webhooks/{policyID}", webhookHandler.Handle)

	manualTriggerHandler := trigger.NewManualTriggerHandler(store, launcher)
	r.With(api.BodySizeLimit(1<<20)).Post("/api/v1/policies/{policyID}/trigger", manualTriggerHandler.Handle)

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

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("server listening", "addr", listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			quit <- syscall.SIGTERM
		}
	}()

	<-quit
	slog.Info("shutting down")

	// Cancel the root context to stop the scheduler and any background timers.
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	return srv.Shutdown(shutdownCtx)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
