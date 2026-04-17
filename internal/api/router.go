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

// PolicyNotifier is implemented by background components (Poller, Scheduler)
// that need to react immediately when a policy is created, updated, or deleted.
// Both fields in RouterConfig are optional (nil-safe): existing tests that do
// not construct a real Poller or Scheduler can leave them unset.
type PolicyNotifier interface {
	Notify(ctx context.Context, policyID string)
}

// RouterConfig bundles all dependencies needed to build the complete route tree.
// Every handler is pre-constructed so BuildRouter only wires routes — it does
// not make construction decisions.
type RouterConfig struct {
	Store                *db.Store
	Broadcaster          *sse.Broadcaster
	Registry             *mcp.Registry
	RunManager           *run.RunManager
	Launcher             *run.RunLauncher
	ModelLister          llm.ModelLister       // interface for listing available models
	ProviderRegistry     *llm.ProviderRegistry // concrete registry for policy validation
	ModelFilter          ModelFilter
	AuthHandler          *auth.Handler
	SettingsHandler      *auth.SettingsHandler
	AdminHandler         *admin.Handler
	OpenAICompatHandler  *admin.OpenAICompatHandler
	WebhookHandler       *trigger.WebhookHandler
	SSEHandler           *sse.Handler
	PolicyWebhookHandler *PolicyWebhookHandler
	Poller               PolicyNotifier // notified on poll-trigger policy mutations
	Scheduler            PolicyNotifier // notified on scheduled-trigger policy mutations
	Version              string
	StartTime            time.Time
	DBPath               string
}

// BuildRouter constructs the complete chi.Router for the application.
// Route registration order matters: more-specific paths are registered before
// catch-alls, and the SPA handler is always last.
func BuildRouter(cfg RouterConfig) chi.Router {
	r := chi.NewRouter()
	r.Use(httputil.SecurityHeaders)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(slogContext)   // enriches context with request_id + remote_addr logger
	r.Use(httpMetrics)   // records Prometheus duration histogram and request counter
	r.Use(slogAccess)    // emits structured JSON access log after each response
	r.Use(middleware.Recoverer)
	// Compress API JSON responses and embedded frontend assets. SSE is excluded
	// automatically because text/event-stream is not in the compressible type
	// list — the middleware forwards it unmodified.
	r.Use(middleware.Compress(5))

	// SSE endpoint is unprotected: the UI needs events before and during auth.
	r.Get("/api/v1/events", cfg.SSEHandler.ServeHTTP)

	// Webhook endpoint is unprotected at the session layer: the WebhookHandler
	// dispatches authentication based on the trigger.auth mode stored in the
	// policy YAML (hmac | bearer | none). The shared secret itself lives in the
	// webhook_secret_encrypted DB column — not in YAML — per ADR-034.
	r.With(middleware.Throttle(10), httputil.BodySizeLimit(httputil.MaxRequestBodySize)).
		Post("/api/v1/webhooks/{policyID}", cfg.WebhookHandler.Handle)

	// Health check is intentionally public (no auth required).
	// DO NOT move this route inside the authenticated sub-router — doing so
	// would break Docker HEALTHCHECK directives, load balancer probes, and
	// uptime monitors that cannot send session cookies.
	r.Get("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Auth routes that do not require an existing session.
	r.Route("/api/v1/auth", func(r chi.Router) {
		r.Get("/status", cfg.AuthHandler.Status)
		r.With(middleware.Throttle(5), httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/setup", cfg.AuthHandler.Setup)
		r.With(middleware.Throttle(10), httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/login", cfg.AuthHandler.Login)
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
		manualTriggerHandler := trigger.NewManualTriggerHandler(cfg.Store, cfg.Launcher, cfg.AdminHandler)
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

		// Public config — accessible to all authenticated users.
		// Operators and auditors need public_url to construct full webhook URLs.
		// This route must be registered before the r.Mount("/api/v1", ...) below;
		// in chi, literal routes must precede mount prefix catch-alls to avoid shadowing.
		r.Get("/api/v1/config", cfg.AdminHandler.GetPublicConfig)

		// Policies, MCP, stats, models, and attention — mounted under /api/v1.
		policySvc := policy.NewService(cfg.Store, nil, cfg.ProviderRegistry, cfg.ProviderRegistry, cfg.AdminHandler)
		r.Mount("/api/v1", newAPISubRouter(cfg.Store, policySvc, cfg.Registry, cfg.ModelLister, cfg.ModelFilter, cfg.PolicyWebhookHandler, cfg.Poller, cfg.Scheduler))

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
func newAPISubRouter(store *db.Store, svc *policy.Service, registry *mcp.Registry, modelLister llm.ModelLister, modelFilter ModelFilter, policyWebhook *PolicyWebhookHandler, poller, scheduler PolicyNotifier) chi.Router {
	r := chi.NewRouter()
	r.Use(httputil.BodySizeLimit(httputil.MaxRequestBodySize))

	statsHandler := NewStatsHandler(NewStatsService(store))
	r.Get("/stats", statsHandler.Get)

	timeseriesHandler := NewTimeSeriesHandler(store)
	r.Get("/stats/timeseries", timeseriesHandler.Get)

	attentionHandler := NewAttentionHandler(store)
	r.Get("/attention", attentionHandler.Get)

	policies := NewPolicyHandler(store, svc, poller, scheduler)
	r.Route("/policies", func(r chi.Router) {
		r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/", policies.List)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/", policies.Create)
		r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/{id}", policies.Get)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Put("/{id}", policies.Update)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/{id}/pause", policies.Pause)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/{id}/resume", policies.Resume)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Delete("/{id}", policies.Delete)
		// Webhook secret management: rotate and reveal are admin|operator only.
		// Auditors can see trigger.auth mode via GET /policies/{id} (it's in YAML)
		// but cannot access the plaintext secret.
		if policyWebhook != nil {
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).
				Post("/{id}/webhook/rotate", policyWebhook.Rotate)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).
				Get("/{id}/webhook/secret", policyWebhook.Get)
		}
	})

	modelsH := NewModelsHandler(modelLister, modelFilter)
	r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/models", modelsH.List)
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/models/refresh", modelsH.Refresh)

	r.Route("/mcp", func(r chi.Router) {
		r.Use(httputil.RequireJSON)
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
