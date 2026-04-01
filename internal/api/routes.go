package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/auth"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// NewRouter returns a chi.Router with sub-routers for /policies and /mcp.
// Mount this under /api/v1/ in main.go.
func NewRouter(store *db.Store, svc *policy.Service, registry *mcp.Registry, modelLister ModelLister) chi.Router {
	r := chi.NewRouter()
	r.Use(BodySizeLimit(MaxRequestBodySize))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	statsHandler := NewStatsHandler(NewStatsService(store))
	r.Get("/stats", statsHandler.Get)

	policies := NewPolicyHandler(store, svc)
	r.Route("/policies", func(r chi.Router) {
		r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/", policies.List)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/", policies.Create)
		r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/{id}", policies.Get)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Put("/{id}", policies.Update)
		r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Delete("/{id}", policies.Delete)
	})

	modelsH := NewModelsHandler(modelLister)
	r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/models", modelsH.List)
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/models/refresh", modelsH.Refresh)

	r.Route("/mcp", func(r chi.Router) {
		r.Use(RequireJSON)
		mcpH := NewMCPHandler(store, registry)
		r.Route("/servers", func(r chi.Router) {
			r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/", mcpH.List)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/", mcpH.Create)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Delete("/{id}", mcpH.Delete)
			r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/{id}/discover", mcpH.Discover)
			r.With(auth.RequireRole(model.RoleOperator, model.RoleAuditor)).Get("/{id}/tools", mcpH.ListTools)
		})
	})

	return r
}
