package trigger_test

import (
	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/run"
)

// newRunsRouter builds a chi router with the standard runs endpoints.
// Duplicated from internal/run's test helpers — kept local so trigger_test
// can wire integration tests without importing run's test package.
func newRunsRouter(h *run.RunsHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/v1/runs", h.List)
	r.Get("/api/v1/runs/{runID}", h.Get)
	r.Get("/api/v1/runs/{runID}/steps", h.ListSteps)
	r.Post("/api/v1/runs/{runID}/cancel", h.Cancel)
	r.Post("/api/v1/runs/{runID}/approval", h.SubmitApproval)
	r.Post("/api/v1/runs/{runID}/feedback", h.SubmitFeedback)
	return r
}
