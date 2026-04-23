package api

import (
	"net/http"

	"github.com/rapp992/gleipnir/internal/http/httputil"
)

// StatsHandler serves GET /api/v1/stats.
type StatsHandler struct {
	svc *StatsService
}

// NewStatsHandler creates a StatsHandler backed by the given service.
func NewStatsHandler(svc *StatsService) *StatsHandler {
	return &StatsHandler{svc: svc}
}

// Get handles GET /api/v1/stats.
func (h *StatsHandler) Get(w http.ResponseWriter, r *http.Request) {
	stats, err := h.svc.Compute(r.Context())
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to compute stats", err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, stats)
}
