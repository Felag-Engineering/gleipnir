package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
)

// NewRouter returns a chi.Router with sub-routers for /policies and /mcp.
// Mount this under /api/v1/ in main.go.
// Note: existing /api/v1/webhooks/ and /api/v1/runs/ routes remain on the root
// chi router for now. Those handlers will be migrated to the envelope format separately.
func NewRouter(store *db.Store) chi.Router {
	r := chi.NewRouter()

	policies := NewPolicyHandler(store)
	r.Route("/policies", func(r chi.Router) {
		r.Get("/", policies.List)
		r.Get("/{id}", policies.Get)
	})

	r.Route("/mcp", func(r chi.Router) {
		// MCP server and tool handlers added in subsequent PRs.
	})

	return r
}
