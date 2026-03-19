package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// SessionDuration is how long a newly created session remains valid.
const SessionDuration = 24 * time.Hour

// AuthQuerier is the subset of db.Queries used by Handler.
// Defined as an interface to keep auth self-contained and enable test fakes.
type AuthQuerier interface {
	GetUserByUsername(ctx context.Context, username string) (db.User, error)
	CreateSession(ctx context.Context, arg db.CreateSessionParams) (db.Session, error)
	DeleteSessionByToken(ctx context.Context, token string) error
}

// Handler handles the login and logout HTTP endpoints.
type Handler struct {
	q AuthQuerier
}

// NewHandler returns a Handler backed by the given querier.
func NewHandler(q AuthQuerier) *Handler {
	return &Handler{q: q}
}

// dummyHash is used for constant-time password checking when a user is not
// found, to prevent timing-based user enumeration attacks.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("gleipnir-dummy-password"), bcrypt.DefaultCost)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Data struct {
		Username string `json:"username"`
	} `json:"data"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// Login accepts a JSON body with username and password, validates the
// credentials, creates a session, and sets the session cookie.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Username == "" {
		writeJSONError(w, http.StatusBadRequest, "username is required")
		return
	}
	if req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "password is required")
		return
	}

	user, err := h.q.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Run bcrypt against a dummy hash so this path takes the same time
			// as a wrong-password path (prevents user enumeration via timing).
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
			writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		slog.Error("login: user lookup failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if user.DeactivatedAt != nil {
		// Still check the password to avoid leaking that the account exists.
		_ = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := CheckPassword(user.PasswordHash, req.Password); err != nil {
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		slog.Error("login: failed to generate session token", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	token := hex.EncodeToString(tokenBytes)

	now := time.Now().UTC()
	expiresAt := now.Add(SessionDuration)

	_, err = h.q.CreateSession(r.Context(), db.CreateSessionParams{
		ID:        model.NewULID(),
		UserID:    user.ID,
		Token:     token,
		CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})
	if err != nil {
		slog.Error("login: failed to create session", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	})

	var resp loginResponse
	resp.Data.Username = user.Username
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("login: failed to write response", "err", err)
	}
}

// Logout invalidates the current session and clears the session cookie.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		// No cookie present; nothing to invalidate.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.q.DeleteSessionByToken(r.Context(), cookie.Value); err != nil {
		slog.Error("logout: failed to delete session", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	w.WriteHeader(http.StatusNoContent)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorResponse{Error: msg}); err != nil {
		slog.Error("failed to write error response", "err", err)
	}
}
