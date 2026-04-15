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
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/httputil"
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
	ListUsers(ctx context.Context) ([]db.ListUsersRow, error)
	ListAllUserRoles(ctx context.Context) ([]db.ListAllUserRolesRow, error)
	GetUser(ctx context.Context, id string) (db.User, error)
	DeactivateUser(ctx context.Context, arg db.DeactivateUserParams) error
	RemoveAllRolesForUser(ctx context.Context, userID string) error
	ListRolesByUser(ctx context.Context, userID string) ([]string, error)
	ListUsersByRole(ctx context.Context, role string) ([]db.ListUsersByRoleRow, error)
	ListActiveUsersByRole(ctx context.Context, role string) ([]db.ListActiveUsersByRoleRow, error)
	UpdateUserPassword(ctx context.Context, arg db.UpdateUserPasswordParams) error
	ListSessionsByUser(ctx context.Context, arg db.ListSessionsByUserParams) ([]db.ListSessionsByUserRow, error)
	DeleteSessionByID(ctx context.Context, arg db.DeleteSessionByIDParams) error
}

// Handler handles the authentication flow: login, logout, setup check, initial
// admin setup, and the current-user identity endpoint (/me).
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

// getDummyHash returns a bcrypt hash of a fixed dummy password, computed once
// on first call. Used for constant-time password checking when a user is not
// found, to prevent timing-based user enumeration attacks.
var getDummyHash = sync.OnceValue(func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("gleipnir-dummy-password"), bcryptCost)
	if err != nil {
		// bcrypt.GenerateFromPassword only fails if the cost is out of range.
		// bcryptCost is a compile-time constant within bcrypt's valid range,
		// so this is unreachable in practice.
		panic("failed to generate dummy bcrypt hash: " + err.Error())
	}
	return h
})

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse is the payload returned on successful login.
// writeJSON wraps this in {"data": ...} automatically.
type loginResponse struct {
	Username string `json:"username"`
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login accepts a JSON body with username and password, validates the
// credentials, creates a session, and sets the session cookie.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	if req.Username == "" {
		httputil.WriteError(w, http.StatusBadRequest, "username is required", "")
		return
	}
	if req.Password == "" {
		httputil.WriteError(w, http.StatusBadRequest, "password is required", "")
		return
	}

	user, err := h.q.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Run bcrypt against a dummy hash so this path takes the same wall-clock
			// time as the wrong-password path. The error is intentionally discarded —
			// the only purpose is the constant-time delay to prevent user enumeration.
			_ = bcrypt.CompareHashAndPassword(getDummyHash(), []byte(req.Password))
			httputil.WriteError(w, http.StatusUnauthorized, "invalid credentials", "")
			return
		}
		slog.Error("login: user lookup failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	if user.DeactivatedAt != nil {
		// Still run a real bcrypt comparison to avoid leaking via timing that the
		// account exists at all. The error is intentionally discarded — constant-time
		// enumeration defense only.
		_ = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
		httputil.WriteError(w, http.StatusUnauthorized, "invalid credentials", "")
		return
	}

	if err := CheckPassword(user.PasswordHash, req.Password); err != nil {
		httputil.WriteError(w, http.StatusUnauthorized, "invalid credentials", "")
		return
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		slog.Error("login: failed to generate session token", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	token := hex.EncodeToString(tokenBytes)
	tokenHash := HashSessionToken(token)

	now := time.Now().UTC()
	expiresAt := now.Add(SessionDuration)

	// Capture the client's IP (RemoteAddr may be "host:port") and User-Agent
	// for session management UI display.
	ipAddress := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ipAddress = host
	}

	_, err = h.q.CreateSession(r.Context(), db.CreateSessionParams{
		ID:        model.NewULID(),
		UserID:    user.ID,
		Token:     tokenHash,
		CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: expiresAt.Format(time.RFC3339),
		UserAgent: r.Header.Get("User-Agent"),
		IpAddress: ipAddress,
	})
	if err != nil {
		slog.Error("login: failed to create session", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	})

	httputil.WriteJSON(w, http.StatusOK, loginResponse{Username: user.Username})
}

// Logout invalidates the current session and clears the session cookie.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		// No cookie present; nothing to invalidate.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.q.DeleteSessionByToken(r.Context(), HashSessionToken(cookie.Value)); err != nil {
		slog.Error("logout: failed to delete session", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	w.WriteHeader(http.StatusNoContent)
}

// isSecureRequest reports whether the request arrived over HTTPS — either
// because Go terminated TLS directly (r.TLS != nil) or because a reverse
// proxy terminated TLS and forwarded the original scheme via X-Forwarded-Proto.
// Used to decide whether to set the Secure flag on session cookies, so HTTP
// homelab deployments work without any extra configuration.
func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// Status reports whether initial setup is required (i.e., no users exist yet).
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	count, err := h.q.CountUsers(r.Context())
	if err != nil {
		slog.Error("status: count users failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	type statusData struct {
		SetupRequired bool `json:"setup_required"`
	}
	httputil.WriteJSON(w, http.StatusOK, statusData{SetupRequired: count == 0})
}

// Setup creates the initial admin account. Returns 403 if any user already exists.
// Uses an atomic INSERT…WHERE to prevent TOCTOU races between concurrent requests.
func (h *Handler) Setup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	if req.Username == "" {
		httputil.WriteError(w, http.StatusBadRequest, "username is required", "")
		return
	}
	if len(req.Username) > maxUsernameLength {
		httputil.WriteError(w, http.StatusBadRequest, "username must be at most 64 characters", "")
		return
	}
	if !validUsername.MatchString(req.Username) {
		httputil.WriteError(w, http.StatusBadRequest, "username may only contain letters, digits, hyphens, and underscores", "")
		return
	}
	if req.Password == "" {
		httputil.WriteError(w, http.StatusBadRequest, "password is required", "")
		return
	}
	if len(req.Password) < 8 {
		httputil.WriteError(w, http.StatusBadRequest, "password must be at least 8 characters", "")
		return
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		slog.Error("setup: hash password failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	user, err := h.createFirstUserWithRole(r.Context(), req.Username, hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusForbidden, "setup already completed", "")
			return
		}
		slog.Error("setup: create first user failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	type setupData struct {
		Username string `json:"username"`
	}
	httputil.WriteJSON(w, http.StatusCreated, setupData{Username: user.Username})
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

// Me returns the current authenticated user's identity.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, userResponse{
		ID:       user.ID,
		Username: user.Username,
		Roles:    user.Roles,
	})
}
