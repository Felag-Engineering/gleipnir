package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/model"
)

type userResponse struct {
	ID            string   `json:"id"`
	Username      string   `json:"username"`
	Roles         []string `json:"roles"`
	CreatedAt     string   `json:"created_at"`
	DeactivatedAt *string  `json:"deactivated_at,omitempty"`
}

type createUserRequest struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"`
}

type updateUserRequest struct {
	Deactivated *bool     `json:"deactivated"`
	Roles       *[]string `json:"roles"`
}

// errLastAdmin is returned when an operation would remove the last active admin.
var errLastAdmin = fmt.Errorf("cannot remove the last admin")

// errAdminCheckFailed is returned when the DB query for admin protection fails.
var errAdminCheckFailed = fmt.Errorf("internal error checking admin count")

// ListUsersHandler returns all users with their roles. Admin only.
func (h *Handler) ListUsersHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	users, err := h.q.ListUsers(ctx)
	if err != nil {
		slog.Error("list users: DB error", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	allRoles, err := h.q.ListAllUserRoles(ctx)
	if err != nil {
		slog.Error("list users: roles DB error", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
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

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// CreateUserHandler creates a new user with the given roles. Admin only.
func (h *Handler) CreateUserHandler(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
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
	if len(req.Password) < 8 {
		httputil.WriteError(w, http.StatusBadRequest, "password must be at least 8 characters", "")
		return
	}
	for _, role := range req.Roles {
		if !model.Role(role).Valid() {
			httputil.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid role: %s", role), "")
			return
		}
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		slog.Error("create user: hash password failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("create user: begin tx failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
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
			httputil.WriteError(w, http.StatusConflict, "username already exists", "")
			return
		}
		slog.Error("create user: insert failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	for _, role := range req.Roles {
		if err := q.AssignRole(ctx, db.AssignRoleParams{
			UserID:    user.ID,
			Role:      role,
			CreatedAt: now,
		}); err != nil {
			slog.Error("create user: assign role failed", "role", role, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("create user: commit failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	roles := req.Roles
	if roles == nil {
		roles = []string{}
	}
	httputil.WriteJSON(w, http.StatusCreated, userResponse{
		ID:        user.ID,
		Username:  user.Username,
		Roles:     roles,
		CreatedAt: user.CreatedAt,
	})
}

// UpdateUserHandler updates a user's deactivated status and/or roles. Admin only.
func (h *Handler) UpdateUserHandler(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "id")

	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	ctx := r.Context()

	// Fetch the target user so we can return a full response and do safety checks.
	target, err := h.q.GetUser(ctx, targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "user not found", "")
			return
		}
		slog.Error("update user: get user failed", "id", targetID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	caller, ok := UserFromContext(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}

	// Prevent an admin from deactivating themselves.
	if req.Deactivated != nil && *req.Deactivated && caller.ID == targetID {
		httputil.WriteError(w, http.StatusBadRequest, "cannot deactivate your own account", "")
		return
	}

	if req.Roles != nil {
		for _, role := range *req.Roles {
			if !model.Role(role).Valid() {
				httputil.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid role: %s", role), "")
				return
			}
		}
	}

	// Last-admin protection: block operations that would leave zero active admins.
	if err := h.checkLastAdminProtection(ctx, caller.ID, targetID, req); err != nil {
		if errors.Is(err, errAdminCheckFailed) {
			slog.Error("update user: admin protection check failed", "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		} else {
			httputil.WriteError(w, http.StatusBadRequest, err.Error(), "")
		}
		return
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("update user: begin tx failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
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
			httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
			return
		}
		target.DeactivatedAt = deactivatedAt
	}

	if req.Roles != nil {
		if err := q.RemoveAllRolesForUser(ctx, targetID); err != nil {
			slog.Error("update user: remove roles failed", "id", targetID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
			return
		}
		for _, role := range *req.Roles {
			if err := q.AssignRole(ctx, db.AssignRoleParams{
				UserID:    targetID,
				Role:      role,
				CreatedAt: now,
			}); err != nil {
				slog.Error("update user: assign role failed", "role", role, "err", err)
				httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
				return
			}
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("update user: commit failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// Read final roles for the response.
	finalRoles, err := h.q.ListRolesByUser(ctx, targetID)
	if err != nil {
		slog.Error("update user: list roles failed", "id", targetID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if finalRoles == nil {
		finalRoles = []string{}
	}

	httputil.WriteJSON(w, http.StatusOK, userResponse{
		ID:            target.ID,
		Username:      target.Username,
		Roles:         finalRoles,
		CreatedAt:     target.CreatedAt,
		DeactivatedAt: target.DeactivatedAt,
	})
}

// checkLastAdminProtection returns an error if the requested operation would
// leave the system with no active admins.
//
// Returns errLastAdmin (caller should 400) or errAdminCheckFailed (caller should 500).
func (h *Handler) checkLastAdminProtection(ctx context.Context, callerID, targetID string, req updateUserRequest) error {
	// We only need to check when deactivating an admin or removing the admin role.
	targetIsBeingDeactivated := req.Deactivated != nil && *req.Deactivated
	targetIsLosingAdminRole := req.Roles != nil && !makeRoleSet(*req.Roles)[string(model.RoleAdmin)]

	if !targetIsBeingDeactivated && !targetIsLosingAdminRole {
		return nil
	}

	// Check whether the target currently holds the admin role.
	currentRoles, err := h.q.ListRolesByUser(ctx, targetID)
	if err != nil {
		return errAdminCheckFailed
	}
	if !makeRoleSet(currentRoles)[string(model.RoleAdmin)] {
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

// isUniqueConstraintError reports whether err is a SQLite UNIQUE constraint violation.
func isUniqueConstraintError(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
