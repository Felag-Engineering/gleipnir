package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// mockAuthQuerier implements AuthQuerier for testing Login, Logout, and Status.
// Setup tests use a real DB (see newTestDB).
type mockAuthQuerier struct {
	user             db.User
	getUserErr       error
	createdSession   db.Session
	createSessionErr error
	deleteSessionErr error
	userCount        int64
	countUsersErr    error
}

func (m *mockAuthQuerier) GetUserByUsername(_ context.Context, _ string) (db.User, error) {
	return m.user, m.getUserErr
}

func (m *mockAuthQuerier) CreateSession(_ context.Context, arg db.CreateSessionParams) (db.Session, error) {
	if m.createSessionErr != nil {
		return db.Session{}, m.createSessionErr
	}
	s := db.Session{
		ID:        arg.ID,
		UserID:    arg.UserID,
		Token:     arg.Token,
		CreatedAt: arg.CreatedAt,
		ExpiresAt: arg.ExpiresAt,
	}
	m.createdSession = s
	return s, nil
}

func (m *mockAuthQuerier) DeleteSessionByToken(_ context.Context, _ string) error {
	return m.deleteSessionErr
}

func (m *mockAuthQuerier) CountUsers(_ context.Context) (int64, error) {
	return m.userCount, m.countUsersErr
}

func (m *mockAuthQuerier) CreateUser(_ context.Context, _ db.CreateUserParams) (db.User, error) {
	return db.User{}, nil
}

func (m *mockAuthQuerier) CreateFirstUser(_ context.Context, _ db.CreateFirstUserParams) (db.User, error) {
	return db.User{}, nil
}

func (m *mockAuthQuerier) AssignRole(_ context.Context, _ db.AssignRoleParams) error {
	return nil
}

func makeUser(username string, deactivated bool) db.User {
	hash, _ := HashPassword("correct-password")
	u := db.User{
		ID:           "user-1",
		Username:     username,
		PasswordHash: hash,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if deactivated {
		ts := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
		u.DeactivatedAt = &ts
	}
	return u
}

func loginBody(username, password string) string {
	return fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
}

func TestHandler_Login(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		querier    *mockAuthQuerier
		wantStatus int
		wantCookie bool
		wantUser   string
	}{
		{
			name:       "valid credentials returns 200 and sets cookie",
			body:       loginBody("alice", "correct-password"),
			querier:    &mockAuthQuerier{user: makeUser("alice", false)},
			wantStatus: http.StatusOK,
			wantCookie: true,
			wantUser:   "alice",
		},
		{
			name:       "missing username returns 400",
			body:       `{"password":"pass"}`,
			querier:    &mockAuthQuerier{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing password returns 400",
			body:       `{"username":"alice"}`,
			querier:    &mockAuthQuerier{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty username returns 400",
			body:       loginBody("", "pass"),
			querier:    &mockAuthQuerier{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty password returns 400",
			body:       loginBody("alice", ""),
			querier:    &mockAuthQuerier{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown user returns 401",
			body:       loginBody("nobody", "any-password"),
			querier:    &mockAuthQuerier{getUserErr: sql.ErrNoRows},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong password returns 401",
			body:       loginBody("alice", "wrong-password"),
			querier:    &mockAuthQuerier{user: makeUser("alice", false)},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "deactivated user returns 401",
			body:       loginBody("alice", "correct-password"),
			querier:    &mockAuthQuerier{user: makeUser("alice", true)},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "DB error on user lookup returns 500",
			body:       loginBody("alice", "correct-password"),
			querier:    &mockAuthQuerier{getUserErr: sql.ErrConnDone},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "DB error on session create returns 500",
			body:       loginBody("alice", "correct-password"),
			querier:    &mockAuthQuerier{user: makeUser("alice", false), createSessionErr: sql.ErrConnDone},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "invalid JSON returns 400",
			body:       "not-json",
			querier:    &mockAuthQuerier{},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(tc.querier, nil)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			h.Login(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			if tc.wantCookie {
				cookies := rec.Result().Cookies()
				var sessionCookie *http.Cookie
				for _, c := range cookies {
					if c.Name == sessionCookieName {
						sessionCookie = c
						break
					}
				}
				if sessionCookie == nil {
					t.Fatal("expected session cookie, got none")
				}
				if sessionCookie.Value == "" {
					t.Error("session cookie value is empty")
				}
				if !sessionCookie.HttpOnly {
					t.Error("session cookie should be HttpOnly")
				}
			}

			if tc.wantUser != "" && rec.Code == http.StatusOK {
				var resp struct {
					Data struct {
						Username string `json:"username"`
					} `json:"data"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.Data.Username != tc.wantUser {
					t.Errorf("username = %q, want %q", resp.Data.Username, tc.wantUser)
				}
			}
		})
	}
}

func TestHandler_Logout(t *testing.T) {
	cases := []struct {
		name       string
		cookie     *http.Cookie
		querier    *mockAuthQuerier
		wantStatus int
		wantClear  bool
	}{
		{
			name:       "valid cookie clears session and returns 204",
			cookie:     &http.Cookie{Name: sessionCookieName, Value: "some-token"},
			querier:    &mockAuthQuerier{},
			wantStatus: http.StatusNoContent,
			wantClear:  true,
		},
		{
			name:       "no cookie returns 204 without error",
			cookie:     nil,
			querier:    &mockAuthQuerier{},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "DB error on delete returns 500",
			cookie:     &http.Cookie{Name: sessionCookieName, Value: "some-token"},
			querier:    &mockAuthQuerier{deleteSessionErr: sql.ErrConnDone},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(tc.querier, nil)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			rec := httptest.NewRecorder()

			h.Logout(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			if tc.wantClear {
				cookies := rec.Result().Cookies()
				var sessionCookie *http.Cookie
				for _, c := range cookies {
					if c.Name == sessionCookieName {
						sessionCookie = c
						break
					}
				}
				if sessionCookie == nil {
					t.Fatal("expected session cookie in response (to clear it), got none")
				}
				if sessionCookie.MaxAge != -1 {
					t.Errorf("cookie MaxAge = %d, want -1 (clear)", sessionCookie.MaxAge)
				}
			}
		})
	}
}

func TestHandler_Status(t *testing.T) {
	cases := []struct {
		name       string
		querier    *mockAuthQuerier
		wantStatus int
		wantSetup  bool
	}{
		{
			name:       "no users returns setup_required true",
			querier:    &mockAuthQuerier{userCount: 0},
			wantStatus: http.StatusOK,
			wantSetup:  true,
		},
		{
			name:       "users exist returns setup_required false",
			querier:    &mockAuthQuerier{userCount: 1},
			wantStatus: http.StatusOK,
			wantSetup:  false,
		},
		{
			name:       "DB error returns 500",
			querier:    &mockAuthQuerier{countUsersErr: sql.ErrConnDone},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(tc.querier, nil)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
			rec := httptest.NewRecorder()

			h.Status(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			if rec.Code == http.StatusOK {
				var resp struct {
					Data struct {
						SetupRequired bool `json:"setup_required"`
					} `json:"data"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.Data.SetupRequired != tc.wantSetup {
					t.Errorf("setup_required = %v, want %v", resp.Data.SetupRequired, tc.wantSetup)
				}
			}
		})
	}
}

func setupBody(username, password string) string {
	return fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
}

// newTestDB opens a temporary SQLite database, applies migrations, and returns
// the underlying *sql.DB. The store is closed automatically via t.Cleanup.
func newTestDB(t *testing.T) (*db.Store, *sql.DB) {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "auth_test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return s, s.DB()
}

func TestHandler_Setup(t *testing.T) {
	// Input-validation cases do not reach the DB, so we pass a real (empty) DB
	// but the handler short-circuits before any transaction begins.
	t.Run("missing username returns 400", func(t *testing.T) {
		_, sqlDB := newTestDB(t)
		h := NewHandler(&mockAuthQuerier{}, sqlDB)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(`{"password":"securepassword"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.Setup(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("missing password returns 400", func(t *testing.T) {
		_, sqlDB := newTestDB(t)
		h := NewHandler(&mockAuthQuerier{}, sqlDB)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(`{"username":"admin"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.Setup(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("password too short returns 400", func(t *testing.T) {
		_, sqlDB := newTestDB(t)
		h := NewHandler(&mockAuthQuerier{}, sqlDB)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(setupBody("admin", "short")))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.Setup(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("username too long returns 400", func(t *testing.T) {
		_, sqlDB := newTestDB(t)
		h := NewHandler(&mockAuthQuerier{}, sqlDB)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup",
			strings.NewReader(setupBody("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "securepassword")))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.Setup(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("username with invalid characters returns 400", func(t *testing.T) {
		_, sqlDB := newTestDB(t)
		h := NewHandler(&mockAuthQuerier{}, sqlDB)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(setupBody("admin@host", "securepassword")))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.Setup(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		_, sqlDB := newTestDB(t)
		h := NewHandler(&mockAuthQuerier{}, sqlDB)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader("not-json"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.Setup(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	// The following cases exercise the transaction path and use a real DB.

	t.Run("success creates user with admin role and returns 201", func(t *testing.T) {
		store, sqlDB := newTestDB(t)
		h := NewHandler(store.Queries(), sqlDB)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(setupBody("admin", "securepassword")))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.Setup(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
		}
		var resp struct {
			Data struct {
				Username string `json:"username"`
			} `json:"data"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Data.Username != "admin" {
			t.Errorf("username = %q, want %q", resp.Data.Username, "admin")
		}

		// Verify user and admin role were persisted atomically.
		user, err := store.Queries().GetUserByUsername(context.Background(), "admin")
		if err != nil {
			t.Fatalf("GetUserByUsername: %v", err)
		}
		roles, err := store.Queries().ListRolesByUser(context.Background(), user.ID)
		if err != nil {
			t.Fatalf("ListRolesByUser: %v", err)
		}
		if len(roles) != 1 || roles[0] != string(model.RoleAdmin) {
			t.Errorf("roles = %v, want [%s]", roles, model.RoleAdmin)
		}
	})

	t.Run("returns 403 when a user already exists", func(t *testing.T) {
		store, sqlDB := newTestDB(t)
		// Seed a pre-existing user so the atomic guard rejects the second creation.
		_, err := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           model.NewULID(),
			Username:     "existing",
			PasswordHash: "hash",
			CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("seed user: %v", err)
		}

		h := NewHandler(store.Queries(), sqlDB)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(setupBody("admin", "securepassword")))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.Setup(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
	})

	// Verify atomicity: if setup is called twice concurrently the second call
	// must not leave a user without roles (the original bug). We simulate this
	// by running setup twice sequentially on the same DB — the second must 403.
	t.Run("second concurrent setup attempt does not create a roleless user", func(t *testing.T) {
		store, sqlDB := newTestDB(t)
		h := NewHandler(store.Queries(), sqlDB)

		makeReq := func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(setupBody("admin", "securepassword")))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.Setup(rec, req)
			return rec
		}

		first := makeReq()
		if first.Code != http.StatusCreated {
			t.Fatalf("first setup: status = %d, want 201", first.Code)
		}
		second := makeReq()
		if second.Code != http.StatusForbidden {
			t.Fatalf("second setup: status = %d, want 403", second.Code)
		}

		// Exactly one user must exist and they must have the admin role.
		var count int64
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
			t.Fatalf("count users: %v", err)
		}
		if count != 1 {
			t.Errorf("user count = %d, want 1", count)
		}

		user, err := store.Queries().GetUserByUsername(context.Background(), "admin")
		if err != nil {
			t.Fatalf("GetUserByUsername: %v", err)
		}
		roles, err := store.Queries().ListRolesByUser(context.Background(), user.ID)
		if err != nil {
			t.Fatalf("ListRolesByUser: %v", err)
		}
		if len(roles) != 1 || roles[0] != string(model.RoleAdmin) {
			t.Errorf("roles = %v, want [%s]", roles, model.RoleAdmin)
		}
	})
}
