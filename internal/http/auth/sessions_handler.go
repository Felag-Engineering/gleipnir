package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
)

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type sessionResponse struct {
	ID        string `json:"id"`
	UserAgent string `json:"user_agent"`
	IPAddress string `json:"ip_address"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	IsCurrent bool   `json:"is_current"`
}

// ChangePasswordHandler changes the current user's password.
// POST /api/v1/auth/password
func (h *Handler) ChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	if req.CurrentPassword == "" {
		httputil.WriteError(w, http.StatusBadRequest, "current_password is required", "")
		return
	}
	if len(req.NewPassword) < 8 {
		httputil.WriteError(w, http.StatusBadRequest, "new_password must be at least 8 characters", "")
		return
	}

	dbUser, err := h.q.GetUser(r.Context(), user.ID)
	if err != nil {
		slog.Error("change password: get user failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	if err := CheckPassword(dbUser.PasswordHash, req.CurrentPassword); err != nil {
		httputil.WriteError(w, http.StatusUnauthorized, "current password is incorrect", "")
		return
	}

	newHash, err := HashPassword(req.NewPassword)
	if err != nil {
		slog.Error("change password: hash failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	if err := h.q.UpdateUserPassword(r.Context(), db.UpdateUserPasswordParams{
		PasswordHash: newHash,
		ID:           user.ID,
	}); err != nil {
		slog.Error("change password: update failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListSessionsHandler returns all active sessions for the current user.
// GET /api/v1/auth/sessions
func (h *Handler) ListSessionsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	sessions, err := h.q.ListSessionsByUser(r.Context(), db.ListSessionsByUserParams{
		UserID: user.ID,
		Now:    now,
	})
	if err != nil {
		slog.Error("list sessions: DB error", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// Identify the current session by hashing the cookie value and comparing
	// against the stored hash. The DB stores hashes, not raw tokens.
	currentTokenHash := ""
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		currentTokenHash = HashSessionToken(cookie.Value)
	}

	resp := make([]sessionResponse, 0, len(sessions))
	for _, s := range sessions {
		resp = append(resp, sessionResponse{
			ID:        s.ID,
			UserAgent: s.UserAgent,
			IPAddress: s.IpAddress,
			CreatedAt: s.CreatedAt,
			ExpiresAt: s.ExpiresAt,
			IsCurrent: s.Token == currentTokenHash,
		})
	}

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// RevokeSessionHandler deletes a specific session for the current user.
// DELETE /api/v1/auth/sessions/{sessionID}
func (h *Handler) RevokeSessionHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}

	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "sessionID is required", "")
		return
	}

	if err := h.q.DeleteSessionByID(r.Context(), db.DeleteSessionByIDParams{
		ID:     sessionID,
		UserID: user.ID,
	}); err != nil {
		slog.Error("revoke session: DB error", "id", sessionID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
