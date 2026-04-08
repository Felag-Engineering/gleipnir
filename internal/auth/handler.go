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
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
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
var dummyHash []byte

func init() {
	h, err := bcrypt.GenerateFromPassword([]byte("gleipnir-dummy-password"), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt.GenerateFromPassword only fails if the cost is out of range.
		// bcrypt.DefaultCost is always valid, so this is unreachable in practice.
		panic("failed to generate dummy bcrypt hash: " + err.Error())
	}
	dummyHash = h
}

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
			// Run bcrypt against a dummy hash so this path takes the same wall-clock
			// time as the wrong-password path. The error is intentionally discarded —
			// the only purpose is the constant-time delay to prevent user enumeration.
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
			writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		slog.Error("login: user lookup failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if user.DeactivatedAt != nil {
		// Still run a real bcrypt comparison to avoid leaking via timing that the
		// account exists at all. The error is intentionally discarded — constant-time
		// enumeration defense only.
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

	// Capture the client's IP (RemoteAddr may be "host:port") and User-Agent
	// for session management UI display.
	ipAddress := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ipAddress = host
	}

	_, err = h.q.CreateSession(r.Context(), db.CreateSessionParams{
		ID:        model.NewULID(),
		UserID:    user.ID,
		Token:     token,
		CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: expiresAt.Format(time.RFC3339),
		UserAgent: r.Header.Get("User-Agent"),
		IpAddress: ipAddress,
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

type userResponse struct {
	ID            string   `json:"id"`
	Username      string   `json:"username"`
	Roles         []string `json:"roles"`
	CreatedAt     string   `json:"created_at"`
	DeactivatedAt *string  `json:"deactivated_at,omitempty"`
}

func writeJSONSuccess(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(struct {
		Data any `json:"data"`
	}{Data: data}); err != nil {
		slog.Error("failed to write JSON response", "err", err)
	}
}

// Me returns the current authenticated user's identity.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeJSONSuccess(w, http.StatusOK, userResponse{
		ID:       user.ID,
		Username: user.Username,
		Roles:    user.Roles,
	})
}

// ListUsersHandler returns all users with their roles. Admin only.
func (h *Handler) ListUsersHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	users, err := h.q.ListUsers(ctx)
	if err != nil {
		slog.Error("list users: DB error", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	allRoles, err := h.q.ListAllUserRoles(ctx)
	if err != nil {
		slog.Error("list users: roles DB error", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	rolesByUser := make(map[string][]string)
	for _, r := range allRoles {
		rolesByUser[r.UserID] = append(rolesByUser[r.UserID], r.Role)
	}

	resp := make([]userResponse, 0, len(users))
	for _, u := range users {
		roles := rolesByUser[u.ID]
		if roles == nil {
			roles = []string{}
		}
		resp = append(resp, userResponse{
			ID:            u.ID,
			Username:      u.Username,
			Roles:         roles,
			CreatedAt:     u.CreatedAt,
			DeactivatedAt: u.DeactivatedAt,
		})
	}

	writeJSONSuccess(w, http.StatusOK, resp)
}

type createUserRequest struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"`
}

// CreateUserHandler creates a new user with the given roles. Admin only.
func (h *Handler) CreateUserHandler(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
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
	if len(req.Password) < 8 {
		writeJSONError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	for _, role := range req.Roles {
		if !model.Role(role).Valid() {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid role: %s", role))
			return
		}
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		slog.Error("create user: hash password failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("create user: begin tx failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("create user: rollback failed", "err", rbErr)
		}
	}()

	q := db.New(tx)
	now := time.Now().UTC().Format(time.RFC3339)

	user, err := q.CreateUser(ctx, db.CreateUserParams{
		ID:           model.NewULID(),
		Username:     req.Username,
		PasswordHash: hash,
		CreatedAt:    now,
	})
	if err != nil {
		if isUniqueConstraintError(err) {
			writeJSONError(w, http.StatusConflict, "username already exists")
			return
		}
		slog.Error("create user: insert failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	for _, role := range req.Roles {
		if err := q.AssignRole(ctx, db.AssignRoleParams{
			UserID:    user.ID,
			Role:      role,
			CreatedAt: now,
		}); err != nil {
			slog.Error("create user: assign role failed", "role", role, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("create user: commit failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	roles := req.Roles
	if roles == nil {
		roles = []string{}
	}
	writeJSONSuccess(w, http.StatusCreated, userResponse{
		ID:        user.ID,
		Username:  user.Username,
		Roles:     roles,
		CreatedAt: user.CreatedAt,
	})
}

type updateUserRequest struct {
	Deactivated *bool     `json:"deactivated"`
	Roles       *[]string `json:"roles"`
}

// UpdateUserHandler updates a user's deactivated status and/or roles. Admin only.
func (h *Handler) UpdateUserHandler(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "id")

	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := r.Context()

	// Fetch the target user so we can return a full response and do safety checks.
	target, err := h.q.GetUser(ctx, targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "user not found")
			return
		}
		slog.Error("update user: get user failed", "id", targetID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	caller, ok := UserFromContext(ctx)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Prevent an admin from deactivating themselves.
	if req.Deactivated != nil && *req.Deactivated && caller.ID == targetID {
		writeJSONError(w, http.StatusBadRequest, "cannot deactivate your own account")
		return
	}

	if req.Roles != nil {
		for _, role := range *req.Roles {
			if !model.Role(role).Valid() {
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid role: %s", role))
				return
			}
		}
	}

	// Last-admin protection: block operations that would leave zero active admins.
	if err := h.checkLastAdminProtection(ctx, caller.ID, targetID, req); err != nil {
		if errors.Is(err, errAdminCheckFailed) {
			slog.Error("update user: admin protection check failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
		} else {
			writeJSONError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("update user: begin tx failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("update user: rollback failed", "err", rbErr)
		}
	}()

	q := db.New(tx)
	now := time.Now().UTC().Format(time.RFC3339)

	if req.Deactivated != nil {
		var deactivatedAt *string
		if *req.Deactivated {
			deactivatedAt = &now
		}
		if err := q.DeactivateUser(ctx, db.DeactivateUserParams{
			DeactivatedAt: deactivatedAt,
			ID:            targetID,
		}); err != nil {
			slog.Error("update user: deactivate failed", "id", targetID, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		target.DeactivatedAt = deactivatedAt
	}

	if req.Roles != nil {
		if err := q.RemoveAllRolesForUser(ctx, targetID); err != nil {
			slog.Error("update user: remove roles failed", "id", targetID, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, role := range *req.Roles {
			if err := q.AssignRole(ctx, db.AssignRoleParams{
				UserID:    targetID,
				Role:      role,
				CreatedAt: now,
			}); err != nil {
				slog.Error("update user: assign role failed", "role", role, "err", err)
				writeJSONError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("update user: commit failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Read final roles for the response.
	finalRoles, err := h.q.ListRolesByUser(ctx, targetID)
	if err != nil {
		slog.Error("update user: list roles failed", "id", targetID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if finalRoles == nil {
		finalRoles = []string{}
	}

	writeJSONSuccess(w, http.StatusOK, userResponse{
		ID:            target.ID,
		Username:      target.Username,
		Roles:         finalRoles,
		CreatedAt:     target.CreatedAt,
		DeactivatedAt: target.DeactivatedAt,
	})
}

// errLastAdmin is returned when an operation would remove the last active admin.
var errLastAdmin = fmt.Errorf("cannot remove the last admin")

// errAdminCheckFailed is returned when the DB query for admin protection fails.
var errAdminCheckFailed = fmt.Errorf("internal error checking admin count")

// checkLastAdminProtection returns an error if the requested operation would
// leave the system with no active admins.
//
// Returns errLastAdmin (caller should 400) or errAdminCheckFailed (caller should 500).
func (h *Handler) checkLastAdminProtection(ctx context.Context, callerID, targetID string, req updateUserRequest) error {
	// We only need to check when deactivating an admin or removing the admin role.
	targetIsBeingDeactivated := req.Deactivated != nil && *req.Deactivated
	targetIsLosingAdminRole := req.Roles != nil && !containsRole(*req.Roles, string(model.RoleAdmin))

	if !targetIsBeingDeactivated && !targetIsLosingAdminRole {
		return nil
	}

	// Check whether the target currently holds the admin role.
	currentRoles, err := h.q.ListRolesByUser(ctx, targetID)
	if err != nil {
		return errAdminCheckFailed
	}
	if !containsRole(currentRoles, string(model.RoleAdmin)) {
		// Target is not an admin — no protection needed.
		return nil
	}

	// Count other active admins (admins that are not the target).
	// Uses ListActiveUsersByRole so deactivated admins are not counted.
	admins, err := h.q.ListActiveUsersByRole(ctx, string(model.RoleAdmin))
	if err != nil {
		return errAdminCheckFailed
	}

	activeAdminCount := 0
	for _, a := range admins {
		if a.UserID != targetID {
			activeAdminCount++
		}
	}

	if activeAdminCount == 0 {
		return errLastAdmin
	}
	return nil
}

func containsRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// isUniqueConstraintError reports whether err is a SQLite UNIQUE constraint violation.
func isUniqueConstraintError(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorResponse{Error: msg}); err != nil {
		slog.Error("failed to write error response", "err", err)
	}
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePasswordHandler changes the current user's password.
// POST /api/v1/auth/password
func (h *Handler) ChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.CurrentPassword == "" {
		writeJSONError(w, http.StatusBadRequest, "current_password is required")
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSONError(w, http.StatusBadRequest, "new_password must be at least 8 characters")
		return
	}

	dbUser, err := h.q.GetUser(r.Context(), user.ID)
	if err != nil {
		slog.Error("change password: get user failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := CheckPassword(dbUser.PasswordHash, req.CurrentPassword); err != nil {
		writeJSONError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	newHash, err := HashPassword(req.NewPassword)
	if err != nil {
		slog.Error("change password: hash failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := h.q.UpdateUserPassword(r.Context(), db.UpdateUserPasswordParams{
		PasswordHash: newHash,
		ID:           user.ID,
	}); err != nil {
		slog.Error("change password: update failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type sessionResponse struct {
	ID        string `json:"id"`
	UserAgent string `json:"user_agent"`
	IPAddress string `json:"ip_address"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	IsCurrent bool   `json:"is_current"`
}

// ListSessionsHandler returns all active sessions for the current user.
// GET /api/v1/auth/sessions
func (h *Handler) ListSessionsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	sessions, err := h.q.ListSessionsByUser(r.Context(), db.ListSessionsByUserParams{
		UserID: user.ID,
		Now:    now,
	})
	if err != nil {
		slog.Error("list sessions: DB error", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Identify the current session token from the cookie so we can mark it.
	currentToken := ""
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		currentToken = cookie.Value
	}

	resp := make([]sessionResponse, 0, len(sessions))
	for _, s := range sessions {
		resp = append(resp, sessionResponse{
			ID:        s.ID,
			UserAgent: s.UserAgent,
			IPAddress: s.IpAddress,
			CreatedAt: s.CreatedAt,
			ExpiresAt: s.ExpiresAt,
			IsCurrent: s.Token == currentToken,
		})
	}

	writeJSONSuccess(w, http.StatusOK, resp)
}

// RevokeSessionHandler deletes a specific session for the current user.
// DELETE /api/v1/auth/sessions/{sessionID}
func (h *Handler) RevokeSessionHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "sessionID is required")
		return
	}

	if err := h.q.DeleteSessionByID(r.Context(), db.DeleteSessionByIDParams{
		ID:     sessionID,
		UserID: user.ID,
	}); err != nil {
		slog.Error("revoke session: DB error", "id", sessionID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
