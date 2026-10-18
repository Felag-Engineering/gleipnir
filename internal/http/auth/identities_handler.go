package auth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
)

// maxExternalUserIDLength bounds an actor id. Channel ids are short opaque
// strings; the bound exists so a pasted blob is refused rather than stored.
const maxExternalUserIDLength = 256

// identityResponse is one plugin user identity: the actor id a channel
// instance uses for this user.
type identityResponse struct {
	InstanceID     string  `json:"instance_id"`
	InstanceName   string  `json:"instance_name"`
	ExternalUserID string  `json:"external_user_id"`
	LinkMethod     string  `json:"link_method"`
	VerifiedAt     *string `json:"verified_at"`
	CreatedAt      string  `json:"created_at"`
}

type setIdentityRequest struct {
	ExternalUserID string `json:"external_user_id"`
}

// ListIdentitiesHandler returns a user's plugin identities, one per instance.
// Admin only.
//
//	GET /api/v1/users/{id}/identities
func (h *Handler) ListIdentitiesHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := chi.URLParam(r, "id")
	if _, ok := h.lookupUser(w, r, userID); !ok {
		return
	}
	rows, err := h.q.ListIdentitiesByUser(ctx, userID)
	if err != nil {
		slog.Error("list identities: DB error", "user_id", userID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	out := make([]identityResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, identityResponse(row))
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

// SetIdentityHandler maps a user to the actor id a plugin instance knows them
// by (link_method admin_set, ADR-058), replacing any earlier mapping for that
// user on that instance. Admin only.
//
//	PUT /api/v1/users/{id}/identities/{instanceID}   body {"external_user_id": "..."}
//
// 409 when the actor id is already mapped to a different user on the same
// instance: one actor id names one person per instance.
func (h *Handler) SetIdentityHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "instanceID")

	var req setIdentityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}
	externalID := strings.TrimSpace(req.ExternalUserID)
	if externalID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "external_user_id is required", "")
		return
	}
	if len(externalID) > maxExternalUserIDLength {
		httputil.WriteError(w, http.StatusBadRequest, "external_user_id must be at most 256 characters", "")
		return
	}

	if _, ok := h.lookupUser(w, r, userID); !ok {
		return
	}
	instance, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		httputil.WriteError(w, http.StatusNotFound, "plugin instance not found", "")
		return
	}
	if err != nil {
		slog.Error("set identity: get instance failed", "instance_id", instanceID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.q.SetAdminIdentity(ctx, db.SetAdminIdentityParams{
		InstanceID:     instanceID,
		UserID:         userID,
		ExternalUserID: externalID,
		CreatedAt:      createdAt,
	}); err != nil {
		if isUniqueConstraintError(err) {
			httputil.WriteError(w, http.StatusConflict, "external_user_id is already mapped to another user on this instance", "")
			return
		}
		slog.Error("set identity: DB error", "user_id", userID, "instance_id", instanceID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// Read back rather than echo the request, so the response carries the
	// row's real created_at when this replaced an existing mapping.
	rows, err := h.q.ListIdentitiesByUser(ctx, userID)
	if err != nil {
		slog.Error("set identity: read back failed", "user_id", userID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	for _, row := range rows {
		if row.InstanceID == instanceID {
			httputil.WriteJSON(w, http.StatusOK, identityResponse(row))
			return
		}
	}
	slog.Error("set identity: written row not found on read back", "user_id", userID, "instance_id", instance.ID)
	httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
}

// DeleteIdentityHandler removes a user's identity on one instance. Admin only.
//
//	DELETE /api/v1/users/{id}/identities/{instanceID}
func (h *Handler) DeleteIdentityHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "instanceID")
	n, err := h.q.DeleteIdentity(ctx, db.DeleteIdentityParams{InstanceID: instanceID, UserID: userID})
	if err != nil {
		slog.Error("delete identity: DB error", "user_id", userID, "instance_id", instanceID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if n == 0 {
		httputil.WriteError(w, http.StatusNotFound, "no identity for this user on this instance", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lookupUser writes 404 or 500 and returns false when userID cannot be read.
func (h *Handler) lookupUser(w http.ResponseWriter, r *http.Request, userID string) (db.User, bool) {
	user, err := h.q.GetUser(r.Context(), userID)
	if errors.Is(err, sql.ErrNoRows) {
		httputil.WriteError(w, http.StatusNotFound, "user not found", "")
		return db.User{}, false
	}
	if err != nil {
		slog.Error("identities: get user failed", "user_id", userID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return db.User{}, false
	}
	return user, true
}
