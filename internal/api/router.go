package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rapp992/gleipnir/frontend"
	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/auth"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/httputil"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/run"
	"github.com/rapp992/gleipnir/internal/sse"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// RouterConfig bundles all dependencies needed to build the complete route tree.
// Every handler is pre-constructed so BuildRouter only wires routes — it does
// not make construction decisions.
type RouterConfig struct {
	Store               *db.Store
	Broadcaster         *sse.Broadcaster
	Registry            *mcp.Registry
	RunManager          *run.RunManager
	Launcher            *run.RunLauncher
	ModelLister         llm.ModelLister       // interface for listing available models
	ProviderRegistry    *llm.ProviderRegistry // concrete registry for policy validation
	ModelFilter         ModelFilter
	AuthHandler         *auth.Handler
	SettingsHandler     *auth.SettingsHandler
	AdminHandler        *admin.Handler
	OpenAICompatHandler *admin.OpenAICompatHandler
	WebhookHandler      *trigger.WebhookHandler
	SSEHandler          *sse.Handler
	Version             string
	StartTime           time.Time
	DBPath              string
}

// BuildRouter constructs the complete chi.Router for the application.
// Route registration order matters: more-specific paths are registered before
// catch-alls, and the SPA handler is always last.
func BuildRouter(cfg RouterConfig) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// Compress API JSON responses and embedded frontend assets. SSE is excluded
	// automatically because text/event-stream is not in the compressible type
	// list — the middleware forwards it unmodified.
	r.Use(middleware.Compress(5))

	// SSE endpoint is unprotected: the UI needs events before and during auth.
	r.Get("/api/v1/events", cfg.SSEHandler.ServeHTTP)

	// Webhook endpoint is unprotected: external systems authenticate via the
	// per-policy secret defined in the policy YAML (trigger.secret).
	r.With(middleware.Throttle(10), httputil.BodySizeLimit(httputil.MaxRequestBodySize)).
		Post("/api/v1/webhooks/{policyID}", cfg.WebhookHandler.Handle)

	// Auth routes that do not require an existing session.
	r.Route("/api/v1/auth", func(r chi.Router) {
		r.Get("/status", cfg.AuthHandler.Status)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/setup", cfg.AuthHandler.Setup)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/login", cfg.AuthHandler.Login)
		r.Post("/logout", cfg.AuthHandler.Logout)
	})

	requireAuth := auth.RequireAuth(cfg.Store.Queries())

	// All UI-facing API endpoints require a valid session cookie.
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)

		// Auth: session management and password operations.
		r.Get("/api/v1/auth/me", cfg.AuthHandler.Me)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/api/v1/auth/password", cfg.AuthHandler.ChangePasswordHandler)
		r.Get("/api/v1/auth/sessions", cfg.AuthHandler.ListSessionsHandler)
		r.Delete("/api/v1/auth/sessions/{sessionID}", cfg.AuthHandler.RevokeSessionHandler)

		// Settings: per-user preferences.
		r.Get("/api/v1/settings/preferences", cfg.SettingsHandler.GetPreferences)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Put("/api/v1/settings/preferences", cfg.SettingsHandler.UpdatePreferences)

		// Users: admin-only user management.
		r.Route("/api/v1/users", func(r chi.Router) {
			r.Use(auth.RequireRole(model.RoleAdmin))
			r.Get("/", cfg.AuthHandler.ListUsersHandler)
			r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/", cfg.AuthHandler.CreateUserHandler)
			r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Patch("/{id}", cfg.AuthHandler.UpdateUserHandler)
		})

		// Manual trigger: operators fire a run from the UI or API.
		manualTriggerHandler := trigger.NewManualTriggerHandler(cfg.Store, cfg.Launcher)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize), auth.RequireRole(model.RoleOperator)).
			Post("/api/v1/policies/{policyID}/trigger", manualTriggerHandler.Handle)

		// Runs: list, inspect, cancel, and submit approval/feedback decisions.
		runsHandler := run.NewRunsHandler(cfg.Store, cfg.RunManager, cfg.Broadcaster)
		r.With(auth.RequireRole(model.RoleOperator, model.RoleApprover, model.RoleAuditor)).Get("/api/v1/runs", runsHandler.List)
		r.With(auth.RequireRole(model.RoleOperator, model.RoleApprover, model.RoleAuditor)).Get("/api/v1/runs/{runID}", runsHandler.Get)
		r.With(auth.RequireRole(model.RoleOperator, model.RoleApprover, model.RoleAuditor)).Get("/api/v1/runs/{runID}/steps", runsHandler.ListSteps)
		r.With(auth.RequireRole(model.RoleOperator)).Post("/api/v1/runs/{runID}/cancel", runsHandler.Cancel)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize), auth.RequireRole(model.RoleApprover)).
			Post("/api/v1/runs/{runID}/approval", runsHandler.SubmitApproval)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize), auth.RequireRole(model.RoleApprover, model.RoleOperator)).
			Post("/api/v1/runs/{runID}/feedback", runsHandler.SubmitFeedback)

		// Policies, MCP, stats, models, health, and attention — mounted under /api/v1.
		policySvc := policy.NewService(cfg.Store, nil, cfg.ProviderRegistry, cfg.ProviderRegistry, cfg.AdminHandler)
		r.Mount("/api/v1", newAPISubRouter(cfg.Store, policySvc, cfg.Registry, cfg.ModelLister, cfg.ModelFilter))

		// Admin: provider key management, settings, and model configuration.
		r.Route("/api/v1/admin", func(r chi.Router) {
			r.Use(auth.RequireRole(model.RoleAdmin))
			r.Use(httputil.BodySizeLimit(httputil.MaxRequestBodySize))
			r.Get("/providers", cfg.AdminHandler.ListProviders)
			r.Put("/providers/{name}/key", cfg.AdminHandler.SetProviderKey)
			r.Delete("/providers/{name}/key", cfg.AdminHandler.DeleteProviderKey)
			r.Get("/settings", cfg.AdminHandler.GetSettings)
			r.Put("/settings", cfg.AdminHandler.UpdateSettings)
			r.Get("/models", cfg.AdminHandler.ListModelsAdmin)
			r.Get("/models/all", cfg.AdminHandler.ListAllModels(cfg.ModelLister))
			r.Put("/models/{id}/enabled", cfg.AdminHandler.SetModelEnabled)
			r.Get("/system-info", admin.GetSystemInfo(admin.SystemInfoDeps{
				Version:   cfg.Version,
				StartTime: cfg.StartTime,
				DBPath:    cfg.DBPath,
				CountMCPServers: func(ctx context.Context) (int, error) {
					n, err := cfg.Store.Queries().CountMCPServers(ctx)
					return int(n), err
				},
				CountPolicies: func(ctx context.Context) (int, error) {
					n, err := cfg.Store.Queries().CountPolicies(ctx)
					return int(n), err
				},
				CountUsers: func(ctx context.Context) (int, error) {
					n, err := cfg.Store.Queries().CountUsers(ctx)
					return int(n), err
				},
			}))

			r.Route("/openai-providers", func(r chi.Router) {
				r.Get("/", cfg.OpenAICompatHandler.ListProviders)
				r.Post("/", cfg.OpenAICompatHandler.CreateProvider)
				r.Get("/{id}", cfg.OpenAICompatHandler.GetProvider)
				r.Put("/{id}", cfg.OpenAICompatHandler.UpdateProvider)
				r.Delete("/{id}", cfg.OpenAICompatHandler.DeleteProvider)
				r.Post("/{id}/test", cfg.OpenAICompatHandler.TestProvider)
			})
		})
	})

	// SPA catch-all: serve the embedded React frontend for all non-API routes.
	// Must be registered last so API routes take precedence.
	r.Handle("/*", frontend.NewSPAHandler())

	return r
}

// newAPISubRouter builds the sub-router that was previously returned by NewRouter.
// It is mounted at /api/v1 inside the authenticated group in BuildRouter.
func newAPISubRouter(store *db.Store, svc *policy.Service, registry *mcp.Registry, modelLister llm.ModelLister, modelFilter ModelFilter) chi.Router {
	r := chi.NewRouter()
	r.Use(httputil.BodySizeLimit(httputil.MaxRequestBodySize))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	statsHandler := NewStatsHandler(NewStatsService(store))
	r.Get("/stats", statsHandler.Get)

	timeseriesHandler := NewTimeSeriesHandler(store)
	r.Get("/stats/timeseries", timeseriesHandler.Get)

	attentionHandler := NewAttentionHandler(store)
	r.Get("/attention", attentionHandler.Get)

	policies := NewPolicyHandler(store, svc)
	r.Route("/policies", func(r chi.Router) {
		r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/", policies.List)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/", policies.Create)
		r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/{id}", policies.Get)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Put("/{id}", policies.Update)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Delete("/{id}", policies.Delete)
	})

	modelsH := NewModelsHandler(modelLister, modelFilter)
	r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/models", modelsH.List)
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/models/refresh", modelsH.Refresh)

	r.Route("/mcp", func(r chi.Router) {
		r.Use(RequireJSON)
		mcpH := NewMCPHandler(store, registry)
		r.Route("/servers", func(r chi.Router) {
			r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/", mcpH.List)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/", mcpH.Create)
			// /test must be registered before /{id} so chi does not capture "test" as an id parameter.
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/test", mcpH.TestConnection)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Delete("/{id}", mcpH.Delete)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/{id}/discover", mcpH.Discover)
			r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/{id}/tools", mcpH.ListTools)
		})
	})

	return r
}
