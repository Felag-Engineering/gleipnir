package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

const maxUsernameLength = 64

var validUsername = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// SessionDuration is how long a newly created session remains valid.
const SessionDuration = 24 * time.Hour

// AuthQuerier is the subset of db.Queries used by Handler.
// Defined as an interface to keep auth self-contained and enable test fakes.
type AuthQuerier interface {
	GetUserByUsername(ctx context.Context, username string) (db.User, error)
	CreateSession(ctx context.Context, arg db.CreateSessionParams) (db.Session, error)
	DeleteSessionByToken(ctx context.Context, token string) error
	CountUsers(ctx context.Context) (int64, error)
	CreateUser(ctx context.Context, arg db.CreateUserParams) (db.User, error)
	CreateFirstUser(ctx context.Context, arg db.CreateFirstUserParams) (db.User, error)
	AssignRole(ctx context.Context, arg db.AssignRoleParams) error
}

// Handler handles the login and logout HTTP endpoints.
type Handler struct {
	q  AuthQuerier
	db *sql.DB
}

// NewHandler returns a Handler backed by the given querier.
// db is required for the Setup endpoint, which needs a transaction to
// atomically create the first user and assign their role.
func NewHandler(q AuthQuerier, database *sql.DB) *Handler {
	return &Handler{q: q, db: database}
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

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Status reports whether initial setup is required (i.e., no users exist yet).
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	count, err := h.q.CountUsers(r.Context())
	if err != nil {
		slog.Error("status: count users failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	type statusData struct {
		SetupRequired bool `json:"setup_required"`
	}
	if err := json.NewEncoder(w).Encode(struct {
		Data statusData `json:"data"`
	}{Data: statusData{SetupRequired: count == 0}}); err != nil {
		slog.Error("status: failed to write response", "err", err)
	}
}

// Setup creates the initial admin account. Returns 403 if any user already exists.
// Uses an atomic INSERT…WHERE to prevent TOCTOU races between concurrent requests.
func (h *Handler) Setup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Username == "" {
		writeJSONError(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(req.Username) > maxUsernameLength {
		writeJSONError(w, http.StatusBadRequest, "username must be at most 64 characters")
		return
	}
	if !validUsername.MatchString(req.Username) {
		writeJSONError(w, http.StatusBadRequest, "username may only contain letters, digits, hyphens, and underscores")
		return
	}
	if req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "password is required")
		return
	}
	if len(req.Password) < 8 {
		writeJSONError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		slog.Error("setup: hash password failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	user, err := h.createFirstUserWithRole(r.Context(), req.Username, hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "setup already completed")
			return
		}
		slog.Error("setup: create first user failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	type setupData struct {
		Username string `json:"username"`
	}
	if err := json.NewEncoder(w).Encode(struct {
		Data setupData `json:"data"`
	}{Data: setupData{Username: user.Username}}); err != nil {
		slog.Error("setup: failed to write response", "err", err)
	}
}

// createFirstUserWithRole atomically creates the first user and assigns the
// admin role inside a single transaction. If CreateFirstUser returns
// sql.ErrNoRows (meaning a user already exists), the transaction is rolled back
// and the error is returned unwrapped so the caller can detect it.
func (h *Handler) createFirstUserWithRole(ctx context.Context, username, passwordHash string) (db.User, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return db.User{}, fmt.Errorf("begin setup tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("setup: transaction rollback failed", "err", rbErr)
		}
	}()

	q := db.New(tx)
	now := time.Now().UTC().Format(time.RFC3339)

	user, err := q.CreateFirstUser(ctx, db.CreateFirstUserParams{
		ID:           model.NewULID(),
		Username:     username,
		PasswordHash: passwordHash,
		CreatedAt:    now,
	})
	if err != nil {
		// sql.ErrNoRows means the atomic WHERE guard rejected the insert because
		// a user already exists — propagate unwrapped so Setup can return 403.
		return db.User{}, err
	}

	if err := q.AssignRole(ctx, db.AssignRoleParams{
		UserID:    user.ID,
		Role:      string(model.RoleAdmin),
		CreatedAt: now,
	}); err != nil {
		return db.User{}, fmt.Errorf("assign admin role: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return db.User{}, fmt.Errorf("commit setup tx: %w", err)
	}
	return user, nil
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorResponse{Error: msg}); err != nil {
		slog.Error("failed to write error response", "err", err)
	}
}
