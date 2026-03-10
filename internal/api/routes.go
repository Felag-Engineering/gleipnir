package api

import "github.com/go-chi/chi/v5"

// NewRouter returns a chi.Router with sub-routers for /policies and /mcp.
// Mount this under /api/v1/ in main.go. Routes within each sub-router are
// populated by subsequent handler PRs.
// Note: existing /api/v1/webhooks/ and /api/v1/runs/ routes remain on the root
// chi router for now. Those handlers will be migrated to the envelope format separately.
func NewRouter() chi.Router {
	r := chi.NewRouter()

	r.Route("/policies", func(r chi.Router) {
		// Policy CRUD handlers added in subsequent PRs.
	})

	r.Route("/mcp", func(r chi.Router) {
		// MCP server and tool handlers added in subsequent PRs.
	})

	return r
}
