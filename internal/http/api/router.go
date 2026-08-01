package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"github.com/felag-engineering/gleipnir/frontend"
	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/http/sse"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
	"github.com/felag-engineering/gleipnir/internal/trigger"
)

// PolicyNotifier is implemented by background components (Poller, Scheduler)
// that need to react immediately when a policy is created, updated, or deleted.
// Both fields in RouterConfig are optional (nil-safe): existing tests that do
// not construct a real Poller or Scheduler can leave them unset.
type PolicyNotifier interface {
	Notify(ctx context.Context, policyID string)
}

// AdminHandler is the subset of *admin.Handler's HTTP-handler surface that
// BuildRouter wires into routes. Defining it here (rather than importing the
// concrete type) lets BuildRouter be exercised with a stub in tests, and is
// consistent with how every other handler dependency in RouterConfig is
// expressed as an interface rather than a concrete type.
type AdminHandler interface {
	GetPublicConfig(w http.ResponseWriter, r *http.Request)
	ListProviders(w http.ResponseWriter, r *http.Request)
	SetProviderKey(w http.ResponseWriter, r *http.Request)
	DeleteProviderKey(w http.ResponseWriter, r *http.Request)
	GetSettings(w http.ResponseWriter, r *http.Request)
	UpdateSettings(w http.ResponseWriter, r *http.Request)
	SetDefaultModel(w http.ResponseWriter, r *http.Request)
	ListModelsAdmin(w http.ResponseWriter, r *http.Request)
	ListAllModels(lister llm.ModelLister) http.HandlerFunc
	SetModelEnabled(w http.ResponseWriter, r *http.Request)
}

// HandlerBundle groups all pre-constructed HTTP handlers. Each field is a
// handler ready to wire into routes — either a concrete handler pointer or a
// consumer-defined interface (AdminHandler). BuildRouter never constructs
// handlers itself.
type HandlerBundle struct {
	AuthHandler              *auth.Handler
	SettingsHandler          *auth.SettingsHandler
	AdminHandler             AdminHandler
	OpenAICompatHandler      *admin.OpenAICompatHandler
	PluginAdminHandler       *admin.PluginHandler
	PluginOAuthHandler       *admin.PluginOAuthHandler
	PluginCredentialsHandler *admin.PluginCredentialsHandler
	PluginOptionsHandler     *admin.PluginOptionsHandler
	AudienceHandler          *AudienceHandler
	BindingTestHandler       *BindingTestHandler
	WebhookHandler           *trigger.WebhookHandler
	SSEHandler               *sse.Handler
	PolicyWebhookHandler     *PolicyWebhookHandler
}

// BackgroundServices groups shared infrastructure and long-lived dependencies.
// These are constructed before any handlers and are referenced by both the
// HTTP layer and shutdown logic (e.g. RunManager.CancelAll, poller.Wait).
type BackgroundServices struct {
	Store            *db.Store
	Broadcaster      *sse.Broadcaster
	Registry         *mcp.Registry
	RunManager       *run.RunManager
	Launcher         *run.RunLauncher
	ModelLister      llm.ModelLister       // interface for listing available models
	ProviderRegistry *llm.ProviderRegistry // concrete registry for policy validation
	ModelFilter      ModelFilter
	Poller           PolicyNotifier         // notified on poll-trigger policy mutations
	Scheduler        PolicyNotifier         // notified on scheduled-trigger policy mutations
	Cron             PolicyNotifier         // notified on cron-trigger policy mutations
	EncryptionKey    []byte                 // AES-256 key for MCP auth header encryption; nil when unset
	Arbiter          *toolregistry.Registry // cross-source tool namespace arbiter; nil disables enforcement
	Settings         *settings.Service      // system-wide runtime settings; required by manual-trigger and policy services
}

// Metadata holds descriptive, read-only values about the running instance.
type Metadata struct {
	Version   string
	StartTime time.Time
	DBPath    string
	// SignatureVerificationDisabled is true when the host is running with
	// GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true (ADR-045 §6). It is surfaced via
	// the public /api/v1/health endpoint so health-checking infrastructure
	// and the admin UI can detect the permissive mode externally. Signed
	// plugins are still fully verified — the flag only governs unsigned
	// bundles.
	SignatureVerificationDisabled bool
}

// RouterConfig bundles all dependencies needed to build the complete route tree.
// Fields are grouped by concern so the caller's wiring code reads as three
// sequential phases: services → handlers → router.
type RouterConfig struct {
	Handlers HandlerBundle
	Services BackgroundServices
	Metadata Metadata
}

// BuildRouter constructs the complete chi.Router for the application.
// Route registration order matters: more-specific paths are registered before
// catch-alls, and the SPA handler is always last.
func BuildRouter(cfg RouterConfig) chi.Router {
	r := chi.NewRouter()
	r.Use(httputil.SecurityHeaders)
	r.Use(middleware.RequestID)
	// RemoteAddr feeds the access log / logctx only — never an authorization
	// input — so the XFF-spoofing hazard is limited to log pollution here.
	//lint:ignore SA1019 deprecated upstream for XFF spoofing; replacement with a trusted-proxy-aware resolver is tracked in #758
	r.Use(middleware.RealIP)
	r.Use(slogContext) // enriches context with request_id + remote_addr logger
	r.Use(httpMetrics) // records Prometheus duration histogram and request counter
	r.Use(slogAccess)  // emits structured JSON access log after each response
	r.Use(middleware.Recoverer)
	// Compress API JSON responses and embedded frontend assets. SSE is excluded
	// automatically because text/event-stream is not in the compressible type
	// list — the middleware forwards it unmodified.
	r.Use(middleware.Compress(5))

	// Webhook endpoint is unprotected at the session layer: the WebhookHandler
	// dispatches authentication based on the trigger.auth mode stored in the
	// policy YAML (hmac | bearer | none). The shared secret itself lives in the
	// webhook_secret_encrypted DB column — not in YAML — per ADR-034.
	r.With(middleware.Throttle(10), httputil.BodySizeLimit(httputil.MaxRequestBodySize)).
		Post("/api/v1/webhooks/{policyID}", cfg.Handlers.WebhookHandler.Handle)

	// OAuth callback is unprotected at the session layer: the HMAC-signed state
	// envelope (spec §9.2) provides CSRF + integrity. The browser arrives from
	// the OAuth provider with no Gleipnir session cookie — requiring auth here
	// would 401 every callback. Mirrors the ADR-034 webhook pattern.
	if cfg.Handlers.PluginOAuthHandler != nil {
		r.Get("/api/v1/admin/plugins/oauth/callback", cfg.Handlers.PluginOAuthHandler.Callback)
	}

	// Health check is intentionally public (no auth required).
	// DO NOT move this route inside the authenticated sub-router — doing so
	// would break Docker HEALTHCHECK directives, load balancer probes, and
	// uptime monitors that cannot send session cookies.
	r.Get("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{"status": "ok"}
		if cfg.Metadata.SignatureVerificationDisabled {
			// Per ADR-045 §6: the value is reported as a string so the field
			// type stays stable when v2 introduces additional verification
			// states (e.g. "degraded" if a TOFU re-pin is mid-flight).
			body["signature_verification"] = "disabled"
		}
		httputil.WriteJSON(w, http.StatusOK, body)
	})

	// Auth routes that do not require an existing session.
	//
	// /setup and /login are brute-force / credential-stuffing targets, so they
	// carry a per-IP rate limit (httprate, keyed by middleware.RealIP) returning
	// 429 once the window is exhausted (#491). This is the real rate limiter;
	// middleware.Throttle only caps concurrent in-flight requests and is kept
	// alongside it purely to bound concurrent bcrypt work (CPU), not as a
	// brute-force control. Setup is effectively one-time, so its window is tighter.
	r.Route("/api/v1/auth", func(r chi.Router) {
		r.Get("/status", cfg.Handlers.AuthHandler.Status)
		r.With(httprate.LimitByIP(5, time.Minute), middleware.Throttle(5), httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/setup", cfg.Handlers.AuthHandler.Setup)
		r.With(httprate.LimitByIP(10, time.Minute), middleware.Throttle(10), httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/login", cfg.Handlers.AuthHandler.Login)
		r.Post("/logout", cfg.Handlers.AuthHandler.Logout)
	})

	requireAuth := auth.RequireAuth(cfg.Services.Store.Queries())

	// All UI-facing API endpoints require a valid session cookie.
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)

		// SSE event stream requires a session: it discloses live run/approval/
		// feedback IDs, step types, and activity, which must not reach
		// unauthenticated clients (#486). The pre-auth UI (login/setup) does not
		// mount the stream — it polls /api/v1/auth/status instead.
		r.Get("/api/v1/events", cfg.Handlers.SSEHandler.ServeHTTP)

		// Auth: session management and password operations.
		r.Get("/api/v1/auth/me", cfg.Handlers.AuthHandler.Me)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/api/v1/auth/password", cfg.Handlers.AuthHandler.ChangePasswordHandler)
		r.Get("/api/v1/auth/sessions", cfg.Handlers.AuthHandler.ListSessionsHandler)
		r.Delete("/api/v1/auth/sessions/{sessionID}", cfg.Handlers.AuthHandler.RevokeSessionHandler)

		// Settings: per-user preferences.
		r.Get("/api/v1/settings/preferences", cfg.Handlers.SettingsHandler.GetPreferences)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Put("/api/v1/settings/preferences", cfg.Handlers.SettingsHandler.UpdatePreferences)

		// Users: admin-only user management.
		r.Route("/api/v1/users", func(r chi.Router) {
			r.Use(auth.RequireRole(model.RoleAdmin))
			r.Get("/", cfg.Handlers.AuthHandler.ListUsersHandler)
			r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Post("/", cfg.Handlers.AuthHandler.CreateUserHandler)
			r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize)).Patch("/{id}", cfg.Handlers.AuthHandler.UpdateUserHandler)
		})

		// Manual trigger: operators fire a run from the UI or API.
		manualTriggerHandler := trigger.NewManualTriggerHandler(cfg.Services.Store, cfg.Services.Launcher, cfg.Services.Settings)
		r.With(httputil.BodySizeLimit(httputil.MaxRequestBodySize), auth.RequireRole(model.RoleOperator)).
			Post("/api/v1/policies/{policyID}/trigger", manualTriggerHandler.Handle)

		// Runs: list, inspect, cancel, and submit approval/feedback decisions.
		runsHandler := run.NewRunsHandler(cfg.Services.Store, cfg.Services.RunManager, cfg.Services.Broadcaster)
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
		r.Get("/api/v1/config", cfg.Handlers.AdminHandler.GetPublicConfig)

		// Policies, MCP, stats, models, and attention — mounted under /api/v1.
		policySvc := policy.NewService(cfg.Services.Store, nil, cfg.Services.ProviderRegistry, cfg.Services.ProviderRegistry, cfg.Services.Settings)
		r.Mount("/api/v1", newAPISubRouter(cfg.Services.Store, policySvc, cfg.Services.Registry, cfg.Services.ModelLister, cfg.Services.ModelFilter, cfg.Handlers.PolicyWebhookHandler, cfg.Services.Poller, cfg.Services.Scheduler, cfg.Services.Cron, cfg.Services.EncryptionKey, cfg.Services.Arbiter))

		// Plugin install and create-instance endpoints are registered outside the
		// /api/v1/admin route group so each can carry its own body-size limit.
		// The /api/v1/admin group applies a 1 MiB cap globally; the install endpoint
		// needs 100 MiB, and nesting it inside the group would silently cap uploads.
		if cfg.Handlers.PluginAdminHandler != nil {
			r.With(auth.RequireRole(model.RoleAdmin),
				httputil.BodySizeLimit(100<<20)).
				Post("/api/v1/admin/plugins", cfg.Handlers.PluginAdminHandler.Install)
			r.With(auth.RequireRole(model.RoleAdmin),
				httputil.BodySizeLimit(httputil.MaxRequestBodySize)).
				Post("/api/v1/admin/plugins/{id}/instances", cfg.Handlers.PluginAdminHandler.CreateInstance)
		}

		// Admin: provider key management, settings, and model configuration.
		r.Route("/api/v1/admin", func(r chi.Router) {
			r.Use(auth.RequireRole(model.RoleAdmin))
			r.Use(httputil.BodySizeLimit(httputil.MaxRequestBodySize))
			r.Get("/providers", cfg.Handlers.AdminHandler.ListProviders)
			r.Put("/providers/{name}/key", cfg.Handlers.AdminHandler.SetProviderKey)
			r.Delete("/providers/{name}/key", cfg.Handlers.AdminHandler.DeleteProviderKey)
			r.Get("/settings", cfg.Handlers.AdminHandler.GetSettings)
			r.Put("/settings", cfg.Handlers.AdminHandler.UpdateSettings)
			r.Put("/settings/default-model", cfg.Handlers.AdminHandler.SetDefaultModel)
			r.Get("/models", cfg.Handlers.AdminHandler.ListModelsAdmin)
			r.Get("/models/all", cfg.Handlers.AdminHandler.ListAllModels(cfg.Services.ModelLister))
			r.Put("/models/enabled", cfg.Handlers.AdminHandler.SetModelEnabled)
			r.Get("/system-info", admin.GetSystemInfo(admin.SystemInfoDeps{
				Version:   cfg.Metadata.Version,
				StartTime: cfg.Metadata.StartTime,
				DBPath:    cfg.Metadata.DBPath,
				CountMCPServers: func(ctx context.Context) (int, error) {
					n, err := cfg.Services.Store.Queries().CountMCPServers(ctx)
					return int(n), err
				},
				CountPolicies: func(ctx context.Context) (int, error) {
					n, err := cfg.Services.Store.Queries().CountPolicies(ctx)
					return int(n), err
				},
				CountUsers: func(ctx context.Context) (int, error) {
					n, err := cfg.Services.Store.Queries().CountUsers(ctx)
					return int(n), err
				},
			}))

			if cfg.Handlers.PluginAdminHandler != nil {
				// GET /plugins/rss must be registered BEFORE /plugins/{id} so chi
				// matches the literal path before the parameterized catch-all. chi
				// routes are matched in registration order when a path segment could
				// be either a literal or a parameter.
				r.Get("/plugins/rss", cfg.Handlers.PluginAdminHandler.GetPluginRSS)
				r.Get("/plugins/{id}/instances/{iid}", cfg.Handlers.PluginAdminHandler.GetInstance)
				r.Put("/plugins/{id}/instances/{iid}/subscription-scope", cfg.Handlers.PluginAdminHandler.PutSubscriptionScope)
				r.Put("/plugins/{id}/instances/{iid}/config", cfg.Handlers.PluginAdminHandler.PutInstanceConfig)
				r.Put("/plugins/{id}/instances/{iid}/config/{property}", cfg.Handlers.PluginAdminHandler.PutInstanceConfigProperty)
				r.Put("/plugins/{id}/instances/{iid}/event-rate-limit", cfg.Handlers.PluginAdminHandler.PutEventRateLimit)
				r.Delete("/plugins/{id}/instances/{iid}", cfg.Handlers.PluginAdminHandler.DeleteInstance)
				r.Post("/plugins/{id}/instances/{iid}/deactivate", cfg.Handlers.PluginAdminHandler.DeactivateInstance)
				r.Post("/plugins/{id}/instances/{iid}/activate", cfg.Handlers.PluginAdminHandler.ActivateInstance)
				r.Post("/plugins/{id}/accept-new-key", cfg.Handlers.PluginAdminHandler.AcceptNewKey)
				r.Post("/plugins/{id}/accept-manifest", cfg.Handlers.PluginAdminHandler.AcceptManifest)
				r.Get("/plugins/{id}/sbom", cfg.Handlers.PluginAdminHandler.GetPluginSBOM)
				r.Delete("/plugins/{id}", cfg.Handlers.PluginAdminHandler.Uninstall)
				// Approve/reject routes must come after more-specific /{id}/* paths
				// to avoid chi capturing "approve" or "reject" as the {id} parameter.
				r.Post("/plugins/{id}/approve", cfg.Handlers.PluginAdminHandler.ApprovePlugin)
				r.Post("/plugins/{id}/reject", cfg.Handlers.PluginAdminHandler.RejectPlugin)
				// GET /{id} must come after all /{id}/{sub} routes so chi does not
				// shadow the sub-paths with the catch-all id parameter.
				r.Get("/plugins/{id}", cfg.Handlers.PluginAdminHandler.GetPluginDetail)
				r.Get("/plugins", cfg.Handlers.PluginAdminHandler.ListPlugins)
			}
			if cfg.Handlers.PluginOAuthHandler != nil {
				r.Post("/plugins/{id}/instances/{iid}/oauth/begin", cfg.Handlers.PluginOAuthHandler.Begin)
			}
			if h := cfg.Handlers.PluginCredentialsHandler; h != nil {
				r.Get("/plugins/{id}/instances/{iid}/credentials", h.Get)
				r.Delete("/plugins/{id}/instances/{iid}/credentials", h.Delete)
				r.Put("/plugins/{id}/instances/{iid}/credentials/static-api-key", h.SetStaticAPIKey)
				r.Put("/plugins/{id}/instances/{iid}/credentials/headers/{name}", h.SetHeader)
				r.Delete("/plugins/{id}/instances/{iid}/credentials/headers/{name}", h.DeleteHeader)
				r.Put("/plugins/{id}/instances/{iid}/credentials/basic-auth", h.SetBasicAuth)
				r.Put("/plugins/{id}/instances/{iid}/credentials/oauth-client", h.SetOAuthClient)
				r.Put("/plugins/{id}/instances/{iid}/credentials/oauth-token", h.SetOAuthToken)
			}
			if h := cfg.Handlers.PluginOptionsHandler; h != nil {
				r.Get("/plugins/{id}/instances/{iid}/options/{source}", h.GetInstanceOptions)
			}

			r.Route("/openai-providers", func(r chi.Router) {
				r.Get("/", cfg.Handlers.OpenAICompatHandler.ListProviders)
				r.Post("/", cfg.Handlers.OpenAICompatHandler.CreateProvider)
				r.Get("/{id}", cfg.Handlers.OpenAICompatHandler.GetProvider)
				r.Put("/{id}", cfg.Handlers.OpenAICompatHandler.UpdateProvider)
				r.Delete("/{id}", cfg.Handlers.OpenAICompatHandler.DeleteProvider)
				r.Post("/{id}/test", cfg.Handlers.OpenAICompatHandler.TestProvider)
			})
		})

		// Plugin instances: trigger picker + audience editor both need this list.
		// Gated by admin|operator|auditor (read-only; no mutations).
		if cfg.Handlers.AudienceHandler != nil {
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor)).
				Get("/api/v1/admin/plugin-instances", cfg.Handlers.AudienceHandler.ListPluginInstances)
		}

		// Binding test endpoint: read-only, gated by admin|operator|auditor.
		// Registered alongside the plugin-instances list so partial bundles in
		// tests that omit BindingTestHandler still compile without a nil dereference.
		if cfg.Handlers.BindingTestHandler != nil {
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor),
				httputil.BodySizeLimit(httputil.MaxRequestBodySize)).
				Post("/api/v1/admin/plugin-instances/{iid}/event-kinds/{kind}/test-binding",
					cfg.Handlers.BindingTestHandler.Test)
		}

		// Audience management: admin/operator for mutations, auditor for reads.
		// Registered outside the admin-only sub-router (which uses RequireRole(Admin)
		// globally) so auditors can access GETs per spec §11.7.
		// Per-route RequireRole mirrors the /api/v1/policies pattern.
		if cfg.Handlers.AudienceHandler != nil {
			r.Route("/api/v1/admin/audiences", func(r chi.Router) {
				r.Use(httputil.BodySizeLimit(httputil.MaxRequestBodySize))
				r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor)).
					Get("/", cfg.Handlers.AudienceHandler.List)
				r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).
					Post("/", cfg.Handlers.AudienceHandler.Create)
				r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor)).
					Get("/{id}", cfg.Handlers.AudienceHandler.Get)
				r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).
					Put("/{id}", cfg.Handlers.AudienceHandler.Update)
				r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).
					Delete("/{id}", cfg.Handlers.AudienceHandler.Delete)
				r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor)).
					Get("/{id}/references", cfg.Handlers.AudienceHandler.References)
			})
		}
	})

	// SPA catch-all: serve the embedded React frontend for all non-API routes.
	// Must be registered last so API routes take precedence.
	r.Handle("/*", frontend.NewSPAHandler())

	return r
}

// newAPISubRouter builds the sub-router that was previously returned by NewRouter.
// It is mounted at /api/v1 inside the authenticated group in BuildRouter.
func newAPISubRouter(store *db.Store, svc *policy.Service, registry *mcp.Registry, modelLister llm.ModelLister, modelFilter ModelFilter, policyWebhook *PolicyWebhookHandler, poller, scheduler, cron PolicyNotifier, encKey []byte, arbiter *toolregistry.Registry) chi.Router {
	r := chi.NewRouter()
	r.Use(httputil.BodySizeLimit(httputil.MaxRequestBodySize))

	statsHandler := NewStatsHandler(NewStatsService(store))
	r.Get("/stats", statsHandler.Get)

	timeseriesHandler := NewTimeSeriesHandler(store)
	r.Get("/stats/timeseries", timeseriesHandler.Get)

	attentionHandler := NewAttentionHandler(store)
	r.Get("/attention", attentionHandler.Get)

	policies := NewPolicyHandler(store, svc, poller, scheduler, cron)
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
		mcpH := NewMCPHandler(store, registry, encKey, WithToolNamespaceArbiter(arbiter))
		r.Route("/servers", func(r chi.Router) {
			r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/", mcpH.List)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/", mcpH.Create)
			// /test must be registered before /{id} so chi does not capture "test" as an id parameter.
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/test", mcpH.TestConnection)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Delete("/{id}", mcpH.Delete)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Put("/{id}", mcpH.Update)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Put("/{id}/headers/{name}", mcpH.SetAuthHeader)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Delete("/{id}/headers/{name}", mcpH.DeleteAuthHeader)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/{id}/discover", mcpH.Discover)
			r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/{id}/tools", mcpH.ListTools)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).
				Put("/{id}/tools/{toolID}/enabled", mcpH.SetToolEnabled)

			// Arcade pre-authorization routes (ADR-040). Constructed inside the
			// /servers lambda so it shares the same closure-captured store and encKey.
			arcadeH := NewArcadeHandler(store, encKey)
			r.Route("/{id}/arcade", func(r chi.Router) {
				r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).
					Post("/authorize", arcadeH.Authorize)
				r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).
					Post("/authorize/wait", arcadeH.AuthorizeWait)
			})
		})
	})

	return r
}
