package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
)

const sessionCookieName = "gleipnir_session"

// SessionQuerier is the subset of db.Queries used by RequireAuth.
// Defined here to keep the interface narrow and enable test fakes.
type SessionQuerier interface {
	GetSessionByToken(ctx context.Context, token string) (db.Session, error)
	GetUser(ctx context.Context, id string) (db.User, error)
}

// UserContext holds the authenticated user's identity injected into the
// request context by RequireAuth. It intentionally omits password_hash to
// prevent accidental leakage into handler code.
type UserContext struct {
	ID       string
	Username string
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
				writeUnauthorized(w, "authentication required")
				return
			}

			session, err := querier.GetSessionByToken(r.Context(), cookie.Value)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					writeUnauthorized(w, "invalid or expired session")
					return
				}
				slog.Error("session lookup failed", "err", err)
				writeUnauthorized(w, "authentication error")
				return
			}

			expires, err := time.Parse(time.RFC3339, session.ExpiresAt)
			if err != nil {
				slog.Error("session expires_at parse failed", "expires_at", session.ExpiresAt, "err", err)
				writeUnauthorized(w, "authentication error")
				return
			}
			if time.Now().UTC().After(expires) {
				writeUnauthorized(w, "invalid or expired session")
				return
			}

			user, err := querier.GetUser(r.Context(), session.UserID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					writeUnauthorized(w, "invalid or expired session")
					return
				}
				slog.Error("user lookup failed", "user_id", session.UserID, "err", err)
				writeUnauthorized(w, "authentication error")
				return
			}

			if user.DeactivatedAt != nil {
				writeUnauthorized(w, "account deactivated")
				return
			}

			ctx := context.WithValue(r.Context(), contextKey{}, &UserContext{
				ID:       user.ID,
				Username: user.Username,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type unauthorizedEnvelope struct {
	Error string `json:"error"`
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	if err := json.NewEncoder(w).Encode(unauthorizedEnvelope{Error: msg}); err != nil {
		slog.Error("failed to encode 401 response", "err", err)
	}
}
