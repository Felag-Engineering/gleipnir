package admin

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
)

// PluginQuerier is the narrow DB interface required by PluginHandler.
// Accepting an interface (not *db.Queries) keeps the handler testable with
// a fake querier and mirrors the AdminQuerier pattern in handler.go.
type PluginQuerier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
}

// PluginHandler handles plugin-related admin endpoints.
type PluginHandler struct {
	q PluginQuerier
}

// NewPluginHandler returns a PluginHandler backed by the given querier.
func NewPluginHandler(q PluginQuerier) *PluginHandler {
	return &PluginHandler{q: q}
}

// instanceResponse is the JSON shape returned by GetInstance.
// Credentials and other write-only fields are intentionally absent — mirrors
// the ADR-039 read-restraint pattern for encrypted auth headers.
type instanceResponse struct {
	ID           string  `json:"id"`
	PluginID     string  `json:"plugin_id"`
	InstanceName string  `json:"instance_name"`
	State        string  `json:"state"`
	Detail       *string `json:"detail"`
	Version      int64   `json:"version"`
	UpdatedAt    string  `json:"updated_at"`
}

// GetInstance handles GET /api/v1/admin/plugins/{id}/instances/{iid}.
// Returns the health state and detail for a single plugin instance. 404 is
// returned when the instance does not exist or belongs to a different plugin.
func (h *PluginHandler) GetInstance(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	row, err := h.q.GetPluginInstanceByID(r.Context(), instanceID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get instance", "")
		return
	}

	// Validate that the instance belongs to the requested plugin. Return 404
	// rather than 403 to avoid leaking instance existence across plugins.
	if row.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, instanceResponse{
		ID:           row.ID,
		PluginID:     row.PluginID,
		InstanceName: row.InstanceName,
		State:        row.HealthState,
		Detail:       row.HealthDetail,
		Version:      row.Version,
		UpdatedAt:    row.UpdatedAt,
	})
}
