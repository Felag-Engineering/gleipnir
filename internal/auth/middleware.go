package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/httputil"
	"github.com/rapp992/gleipnir/internal/model"
)

const sessionCookieName = "gleipnir_session"

// SessionQuerier is the subset of db.Queries used by RequireAuth.
// Defined here to keep the interface narrow and enable test fakes.
type SessionQuerier interface {
	GetSessionByToken(ctx context.Context, token string) (db.Session, error)
	GetUser(ctx context.Context, id string) (db.User, error)
	ListRolesByUser(ctx context.Context, userID string) ([]string, error)
}

// UserContext holds the authenticated user's identity injected into the
// request context by RequireAuth. It intentionally omits password_hash to
// prevent accidental leakage into handler code.
type UserContext struct {
	ID       string
	Username string
	Roles    []string
}

// HasRole reports whether the user holds the given role.
func (u *UserContext) HasRole(role model.Role) bool {
	for _, r := range u.Roles {
		if r == string(role) {
			return true
		}
	}
	return false
}

type contextKey struct{}

// UserFromContext extracts the authenticated UserContext from ctx.
// Returns (nil, false) when the request was not authenticated.
func UserFromContext(ctx context.Context) (*UserContext, bool) {
	u, ok := ctx.Value(contextKey{}).(*UserContext)
	return u, ok
}

// RequireAuth returns a Chi middleware that validates the gleipnir_session
// cookie. Unauthenticated or expired sessions receive a 401 and the handler
// chain is short-circuited. Valid sessions inject a *UserContext into the
// request context.
func RequireAuth(querier SessionQuerier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || cookie.Value == "" {
				httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
				return
			}

			session, err := querier.GetSessionByToken(r.Context(), cookie.Value)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					httputil.WriteError(w, http.StatusUnauthorized, "invalid or expired session", "")
					return
				}
				slog.Error("session lookup failed", "err", err)
				httputil.WriteError(w, http.StatusUnauthorized, "authentication error", "")
				return
			}

			expires, err := time.Parse(time.RFC3339, session.ExpiresAt)
			if err != nil {
				slog.Error("session expires_at parse failed", "expires_at", session.ExpiresAt, "err", err)
				httputil.WriteError(w, http.StatusUnauthorized, "authentication error", "")
				return
			}
			if time.Now().UTC().After(expires) {
				httputil.WriteError(w, http.StatusUnauthorized, "invalid or expired session", "")
				return
			}

			user, err := querier.GetUser(r.Context(), session.UserID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					httputil.WriteError(w, http.StatusUnauthorized, "invalid or expired session", "")
					return
				}
				slog.Error("user lookup failed", "user_id", session.UserID, "err", err)
				httputil.WriteError(w, http.StatusUnauthorized, "authentication error", "")
				return
			}

			if user.DeactivatedAt != nil {
				httputil.WriteError(w, http.StatusUnauthorized, "account deactivated", "")
				return
			}

			roles, err := querier.ListRolesByUser(r.Context(), user.ID)
			if err != nil {
				slog.Error("role lookup failed", "user_id", user.ID, "err", err)
				httputil.WriteError(w, http.StatusUnauthorized, "authentication error", "")
				return
			}

			ctx := context.WithValue(r.Context(), contextKey{}, &UserContext{
				ID:       user.ID,
				Username: user.Username,
				Roles:    roles,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole returns a Chi middleware that enforces role-based access control.
// The user must hold at least one of the given roles; admins always pass.
// A 401 is returned when no user is in context; a 403 for insufficient roles.
func RequireRole(roles ...model.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok {
				httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
				return
			}

			// Admins bypass all role guards.
			if user.HasRole(model.RoleAdmin) {
				next.ServeHTTP(w, r)
				return
			}

			for _, required := range roles {
				if user.HasRole(required) {
					next.ServeHTTP(w, r)
					return
				}
			}

			httputil.WriteError(w, http.StatusForbidden, "insufficient permissions", "")
		})
	}
}
