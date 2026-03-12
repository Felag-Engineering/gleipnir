package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/policy"
)

// NewRouter returns a chi.Router with sub-routers for /policies and /mcp.
// Mount this under /api/v1/ in main.go.
// Note: existing /api/v1/webhooks/ and /api/v1/runs/ routes remain on the root
// chi router for now. Those handlers will be migrated to the envelope format separately.
func NewRouter(store *db.Store, svc *policy.Service, registry *mcp.Registry) chi.Router {
	r := chi.NewRouter()

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	statsHandler := NewStatsHandler(NewStatsService(store))
	r.Get("/stats", statsHandler.Get)

	policies := NewPolicyHandler(store, svc)
	r.Route("/policies", func(r chi.Router) {
		r.Get("/", policies.List)
		r.Post("/", policies.Create)
		r.Get("/{id}", policies.Get)
		r.Put("/{id}", policies.Update)
		r.Delete("/{id}", policies.Delete)
	})

	r.Route("/mcp", func(r chi.Router) {
		mcpH := NewMCPHandler(store, registry)
		r.Route("/servers", func(r chi.Router) {
			r.Get("/", mcpH.List)
			r.Post("/", mcpH.Create)
			r.Delete("/{id}", mcpH.Delete)
			r.Post("/{id}/discover", mcpH.Discover)
			r.Get("/{id}/tools", mcpH.ListTools)
		})
		r.Patch("/tools/{id}", mcpH.UpdateToolRole)
	})

	return r
}
