package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
)

// allowedPreferenceKeys is the set of user preference keys accepted by the API.
var allowedPreferenceKeys = map[string]bool{
	"timezone":    true,
	"date_format": true,
}

// SettingsQuerier is the subset of db.Queries used by SettingsHandler.
type SettingsQuerier interface {
	ListUserPreferences(ctx context.Context, userID string) ([]db.UserPreference, error)
	UpsertUserPreference(ctx context.Context, arg db.UpsertUserPreferenceParams) (db.UserPreference, error)
}

// SettingsHandler handles user preferences endpoints.
type SettingsHandler struct {
	q SettingsQuerier
}

// NewSettingsHandler returns a SettingsHandler backed by the given querier.
func NewSettingsHandler(q SettingsQuerier) *SettingsHandler {
	return &SettingsHandler{q: q}
}

// GetPreferences returns all stored preferences for the current user as a key-value map.
// GET /api/v1/settings/preferences
func (h *SettingsHandler) GetPreferences(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}

	prefs, err := h.q.ListUserPreferences(r.Context(), user.ID)
	if err != nil {
		slog.Error("get preferences: DB error", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	result := make(map[string]string, len(prefs))
	for _, p := range prefs {
		result[p.PreferenceKey] = p.PreferenceValue
	}

	httputil.WriteJSON(w, http.StatusOK, result)
}

// UpdatePreferences upserts a set of preferences for the current user.
// Only keys in the allowlist are accepted.
// PUT /api/v1/settings/preferences
func (h *SettingsHandler) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}

	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	for key := range body {
		if !allowedPreferenceKeys[key] {
			httputil.WriteError(w, http.StatusBadRequest, "unknown preference key: "+key, "")
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for key, value := range body {
		if _, err := h.q.UpsertUserPreference(r.Context(), db.UpsertUserPreferenceParams{
			UserID:          user.ID,
			PreferenceKey:   key,
			PreferenceValue: value,
			UpdatedAt:       now,
		}); err != nil {
			slog.Error("update preferences: upsert failed", "key", key, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
			return
		}
	}

	// Return the full updated preference map.
	prefs, err := h.q.ListUserPreferences(r.Context(), user.ID)
	if err != nil {
		slog.Error("update preferences: list after upsert failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	result := make(map[string]string, len(prefs))
	for _, p := range prefs {
		result[p.PreferenceKey] = p.PreferenceValue
	}

	httputil.WriteJSON(w, http.StatusOK, result)
}
