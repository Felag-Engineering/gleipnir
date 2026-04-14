package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
)

// mockQuerier implements SessionQuerier for testing.
type mockQuerier struct {
	session      db.Session
	err          error
	user         db.User
	userErr      error
	roles        []string
	listRolesErr error

	// capturedToken is set by GetSessionByToken so tests can verify the
	// middleware hashes the cookie value before calling into the querier.
	capturedToken string
}

func (m *mockQuerier) GetSessionByToken(_ context.Context, token string) (db.Session, error) {
	m.capturedToken = token
	return m.session, m.err
}

func (m *mockQuerier) GetUser(_ context.Context, _ string) (db.User, error) {
	return m.user, m.userErr
}

func (m *mockQuerier) ListRolesByUser(_ context.Context, _ string) ([]string, error) {
	return m.roles, m.listRolesErr
}

// sentinel handler that records whether it was called and injects the context
// user into the response body for inspection.
func echoUserHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok {
			http.Error(w, "no user in context", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(u)
	})
}

func futureExpiry() string {
	return time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
}

func pastExpiry() string {
	return time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
}

func deactivatedAt() *string {
	s := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	return &s
}

func TestRequireAuth(t *testing.T) {
	cases := []struct {
		name          string
		cookie        *http.Cookie // nil means no cookie
		querier       SessionQuerier
		wantStatus    int
		wantUserInCtx bool
		wantUsername  string
		wantRoles     []string
	}{
		{
			name:   "valid session passes through and populates username",
			cookie: &http.Cookie{Name: sessionCookieName, Value: "good-token"},
			querier: &mockQuerier{
				session: db.Session{
					ID:        "sess1",
					UserID:    "user1",
					Token:     HashSessionToken("good-token"),
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
					ExpiresAt: futureExpiry(),
				},
				user: db.User{
					ID:       "user1",
					Username: "alice",
				},
				roles: []string{"admin", "operator"},
			},
			wantStatus:    http.StatusOK,
			wantUserInCtx: true,
			wantUsername:  "alice",
			wantRoles:     []string{"admin", "operator"},
		},
		{
			name:   "valid session with no roles populates empty roles slice",
			cookie: &http.Cookie{Name: sessionCookieName, Value: "good-token"},
			querier: &mockQuerier{
				session: db.Session{
					ID:        "sess1",
					UserID:    "user1",
					Token:     HashSessionToken("good-token"),
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
					ExpiresAt: futureExpiry(),
				},
				user: db.User{
					ID:       "user1",
					Username: "alice",
				},
				roles: nil,
			},
			wantStatus:    http.StatusOK,
			wantUserInCtx: true,
			wantUsername:  "alice",
		},
		{
			name:          "missing cookie returns 401",
			cookie:        nil,
			querier:       &mockQuerier{},
			wantStatus:    http.StatusUnauthorized,
			wantUserInCtx: false,
		},
		{
			name:          "empty cookie value returns 401",
			cookie:        &http.Cookie{Name: sessionCookieName, Value: ""},
			querier:       &mockQuerier{},
			wantStatus:    http.StatusUnauthorized,
			wantUserInCtx: false,
		},
		{
			name:          "session not found returns 401",
			cookie:        &http.Cookie{Name: sessionCookieName, Value: "unknown-token"},
			querier:       &mockQuerier{err: sql.ErrNoRows},
			wantStatus:    http.StatusUnauthorized,
			wantUserInCtx: false,
		},
		{
			name:   "expired session returns 401",
			cookie: &http.Cookie{Name: sessionCookieName, Value: "expired-token"},
			querier: &mockQuerier{
				session: db.Session{
					ID:        "sess2",
					UserID:    "user2",
					Token:     HashSessionToken("expired-token"),
					CreatedAt: time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
					ExpiresAt: pastExpiry(),
				},
			},
			wantStatus:    http.StatusUnauthorized,
			wantUserInCtx: false,
		},
		{
			name:          "session db error returns 401",
			cookie:        &http.Cookie{Name: sessionCookieName, Value: "any-token"},
			querier:       &mockQuerier{err: sql.ErrConnDone},
			wantStatus:    http.StatusUnauthorized,
			wantUserInCtx: false,
		},
		{
			name:   "user not found returns 401",
			cookie: &http.Cookie{Name: sessionCookieName, Value: "good-token"},
			querier: &mockQuerier{
				session: db.Session{
					ID:        "sess3",
					UserID:    "user3",
					Token:     HashSessionToken("good-token"),
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
					ExpiresAt: futureExpiry(),
				},
				userErr: sql.ErrNoRows,
			},
			wantStatus:    http.StatusUnauthorized,
			wantUserInCtx: false,
		},
		{
			name:   "user db error returns 401",
			cookie: &http.Cookie{Name: sessionCookieName, Value: "good-token"},
			querier: &mockQuerier{
				session: db.Session{
					ID:        "sess4",
					UserID:    "user4",
					Token:     HashSessionToken("good-token"),
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
					ExpiresAt: futureExpiry(),
				},
				userErr: sql.ErrConnDone,
			},
			wantStatus:    http.StatusUnauthorized,
			wantUserInCtx: false,
		},
		{
			name:   "deactivated user returns 401",
			cookie: &http.Cookie{Name: sessionCookieName, Value: "good-token"},
			querier: &mockQuerier{
				session: db.Session{
					ID:        "sess5",
					UserID:    "user5",
					Token:     HashSessionToken("good-token"),
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
					ExpiresAt: futureExpiry(),
				},
				user: db.User{
					ID:            "user5",
					Username:      "bob",
					DeactivatedAt: deactivatedAt(),
				},
			},
			wantStatus:    http.StatusUnauthorized,
			wantUserInCtx: false,
		},
		{
			name:   "role lookup error returns 401",
			cookie: &http.Cookie{Name: sessionCookieName, Value: "good-token"},
			querier: &mockQuerier{
				session: db.Session{
					ID:        "sess6",
					UserID:    "user6",
					Token:     HashSessionToken("good-token"),
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
					ExpiresAt: futureExpiry(),
				},
				user: db.User{
					ID:       "user6",
					Username: "charlie",
				},
				listRolesErr: sql.ErrConnDone,
			},
			wantStatus:    http.StatusUnauthorized,
			wantUserInCtx: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := RequireAuth(tc.querier)(echoUserHandler(t))

			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			// Verify that the middleware hashed the raw cookie value before
			// passing it to GetSessionByToken, for any case where a cookie was sent.
			if tc.cookie != nil && tc.cookie.Value != "" {
				mq, ok := tc.querier.(*mockQuerier)
				if ok && mq.capturedToken != "" {
					wantHash := HashSessionToken(tc.cookie.Value)
					if mq.capturedToken != wantHash {
						t.Errorf("GetSessionByToken received %q, want hashed value %q", mq.capturedToken, wantHash)
					}
				}
			}

			if tc.wantUserInCtx && rec.Code == http.StatusOK {
				var u UserContext
				if err := json.NewDecoder(rec.Body).Decode(&u); err != nil {
					t.Fatalf("decode user context: %v", err)
				}
				if u.ID == "" {
					t.Error("UserContext.ID is empty")
				}
				if tc.wantUsername != "" && u.Username != tc.wantUsername {
					t.Errorf("UserContext.Username = %q, want %q", u.Username, tc.wantUsername)
				}
				if tc.wantRoles != nil {
					if len(u.Roles) != len(tc.wantRoles) {
						t.Errorf("UserContext.Roles = %v, want %v", u.Roles, tc.wantRoles)
					} else {
						for i, r := range tc.wantRoles {
							if u.Roles[i] != r {
								t.Errorf("UserContext.Roles[%d] = %q, want %q", i, u.Roles[i], r)
							}
						}
					}
				}
			}
		})
	}
}

func TestUserFromContext(t *testing.T) {
	t.Run("returns false when no user in context", func(t *testing.T) {
		_, ok := UserFromContext(context.Background())
		if ok {
			t.Error("expected false, got true")
		}
	})

	t.Run("returns user when present", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), contextKey{}, &UserContext{
			ID:       "test-id",
			Username: "test-user",
			Roles:    []string{"admin"},
		})
		u, ok := UserFromContext(ctx)
		if !ok {
			t.Fatal("expected true, got false")
		}
		if u.ID != "test-id" {
			t.Errorf("ID = %q, want %q", u.ID, "test-id")
		}
		if len(u.Roles) != 1 || u.Roles[0] != "admin" {
			t.Errorf("Roles = %v, want [admin]", u.Roles)
		}
	})
}
