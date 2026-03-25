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

	"github.com/go-chi/chi/v5"

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

	// Fields for user management tests
	users               []db.ListUsersRow
	listUsersErr        error
	allRoles            []db.ListAllUserRolesRow
	listRolesErr        error
	getUser             db.User
	getUserByIDErr      error
	rolesByUser         []string
	listByUserErr       error
	usersByRole         []db.ListUsersByRoleRow
	listByRoleErr       error
	activeUsersByRole   []db.ListActiveUsersByRoleRow
	listActiveByRoleErr error
	deactivateErr       error
	removeRolesErr      error
	createUserUser      db.User
	createUserErr       error
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
	if m.createUserErr != nil {
		return db.User{}, m.createUserErr
	}
	if m.createUserUser.ID != "" {
		return m.createUserUser, nil
	}
	return db.User{ID: "new-user-1", Username: "newuser", CreatedAt: time.Now().UTC().Format(time.RFC3339)}, nil
}

func (m *mockAuthQuerier) CreateFirstUser(_ context.Context, _ db.CreateFirstUserParams) (db.User, error) {
	return db.User{}, nil
}

func (m *mockAuthQuerier) AssignRole(_ context.Context, _ db.AssignRoleParams) error {
	return nil
}

func (m *mockAuthQuerier) ListUsers(_ context.Context) ([]db.ListUsersRow, error) {
	return m.users, m.listUsersErr
}

func (m *mockAuthQuerier) ListAllUserRoles(_ context.Context) ([]db.ListAllUserRolesRow, error) {
	return m.allRoles, m.listRolesErr
}

func (m *mockAuthQuerier) GetUser(_ context.Context, _ string) (db.User, error) {
	return m.getUser, m.getUserByIDErr
}

func (m *mockAuthQuerier) DeactivateUser(_ context.Context, _ db.DeactivateUserParams) error {
	return m.deactivateErr
}

func (m *mockAuthQuerier) RemoveAllRolesForUser(_ context.Context, _ string) error {
	return m.removeRolesErr
}

func (m *mockAuthQuerier) ListRolesByUser(_ context.Context, _ string) ([]string, error) {
	return m.rolesByUser, m.listByUserErr
}

func (m *mockAuthQuerier) ListUsersByRole(_ context.Context, _ string) ([]db.ListUsersByRoleRow, error) {
	return m.usersByRole, m.listByRoleErr
}

func (m *mockAuthQuerier) ListActiveUsersByRole(_ context.Context, _ string) ([]db.ListActiveUsersByRoleRow, error) {
	return m.activeUsersByRole, m.listActiveByRoleErr
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

// withCallerContext injects a UserContext into the request — simulates RequireAuth.
func withCallerContext(r *http.Request, id, username string, roles []string) *http.Request {
	ctx := context.WithValue(r.Context(), contextKey{}, &UserContext{
		ID:       id,
		Username: username,
		Roles:    roles,
	})
	return r.WithContext(ctx)
}

func TestMe(t *testing.T) {
	q := &mockAuthQuerier{}
	h := NewHandler(q, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req = withCallerContext(req, "user-1", "alice", []string{"admin"})
	rec := httptest.NewRecorder()

	h.Me(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp struct {
		Data struct {
			ID       string   `json:"id"`
			Username string   `json:"username"`
			Roles    []string `json:"roles"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.ID != "user-1" {
		t.Errorf("id = %q, want user-1", resp.Data.ID)
	}
	if resp.Data.Username != "alice" {
		t.Errorf("username = %q, want alice", resp.Data.Username)
	}
	if len(resp.Data.Roles) != 1 || resp.Data.Roles[0] != "admin" {
		t.Errorf("roles = %v, want [admin]", resp.Data.Roles)
	}
}

func TestMe_Unauthenticated(t *testing.T) {
	h := NewHandler(&mockAuthQuerier{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()
	h.Me(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestListUsersHandler(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)

	cases := []struct {
		name       string
		users      []db.ListUsersRow
		allRoles   []db.ListAllUserRolesRow
		listErr    error
		rolesErr   error
		wantStatus int
		wantCount  int
	}{
		{
			name: "returns users with roles",
			users: []db.ListUsersRow{
				{ID: "u1", Username: "alice", CreatedAt: now},
				{ID: "u2", Username: "bob", CreatedAt: now},
			},
			allRoles: []db.ListAllUserRolesRow{
				{UserID: "u1", Role: "admin"},
				{UserID: "u2", Role: "operator"},
			},
			wantStatus: http.StatusOK,
			wantCount:  2,
		},
		{
			name:       "empty list returns empty array",
			users:      []db.ListUsersRow{},
			allRoles:   []db.ListAllUserRolesRow{},
			wantStatus: http.StatusOK,
			wantCount:  0,
		},
		{
			name:       "DB error on list users returns 500",
			listErr:    sql.ErrConnDone,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "DB error on list roles returns 500",
			users: []db.ListUsersRow{
				{ID: "u1", Username: "alice", CreatedAt: now},
			},
			rolesErr:   sql.ErrConnDone,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &mockAuthQuerier{
				users:        tc.users,
				listUsersErr: tc.listErr,
				allRoles:     tc.allRoles,
				listRolesErr: tc.rolesErr,
			}
			h := NewHandler(q, nil)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
			req = withCallerContext(req, "caller-1", "admin", []string{"admin"})
			rec := httptest.NewRecorder()

			h.ListUsersHandler(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			if tc.wantStatus == http.StatusOK {
				var resp struct {
					Data []userResponse `json:"data"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if len(resp.Data) != tc.wantCount {
					t.Errorf("user count = %d, want %d", len(resp.Data), tc.wantCount)
				}
			}
		})
	}
}

func TestCreateUserHandler(t *testing.T) {
	// Input validation tests do not reach the DB — use a mock querier.
	validationCases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "missing username returns 400",
			body:       `{"password":"securepass","roles":[]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "short password returns 400",
			body:       `{"username":"newuser","password":"short","roles":[]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid role returns 400",
			body:       `{"username":"newuser","password":"securepass","roles":["superadmin"]}`,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range validationCases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(&mockAuthQuerier{}, nil)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req = withCallerContext(req, "caller-1", "admin", []string{"admin"})
			rec := httptest.NewRecorder()
			h.CreateUserHandler(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}

	// DB-path tests use a real database.
	t.Run("valid input creates user and returns 201", func(t *testing.T) {
		store, sqlDB := newTestDB(t)
		h := NewHandler(store.Queries(), sqlDB)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{"username":"newuser","password":"securepass","roles":["operator"]}`))
		req.Header.Set("Content-Type", "application/json")
		req = withCallerContext(req, "caller-1", "admin", []string{"admin"})
		rec := httptest.NewRecorder()
		h.CreateUserHandler(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data userResponse `json:"data"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Data.Username != "newuser" {
			t.Errorf("username = %q, want newuser", resp.Data.Username)
		}
		if len(resp.Data.Roles) != 1 || resp.Data.Roles[0] != "operator" {
			t.Errorf("roles = %v, want [operator]", resp.Data.Roles)
		}
	})

	t.Run("duplicate username returns 409", func(t *testing.T) {
		store, sqlDB := newTestDB(t)
		// Seed an existing user.
		if _, err := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           model.NewULID(),
			Username:     "existing",
			PasswordHash: "hash",
			CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("seed user: %v", err)
		}

		h := NewHandler(store.Queries(), sqlDB)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{"username":"existing","password":"securepass","roles":[]}`))
		req.Header.Set("Content-Type", "application/json")
		req = withCallerContext(req, "caller-1", "admin", []string{"admin"})
		rec := httptest.NewRecorder()
		h.CreateUserHandler(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
		}
	})
}

// patchWithID builds an http.Request for PATCH /api/v1/users/:id with the chi
// URL param injected.
func patchWithID(targetID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+targetID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", targetID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestUpdateUserHandler(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)

	// Mock-querier tests cover pre-DB error paths.
	t.Run("user not found returns 404", func(t *testing.T) {
		q := &mockAuthQuerier{getUserByIDErr: sql.ErrNoRows}
		h := NewHandler(q, nil)
		req := withCallerContext(patchWithID("missing", `{"deactivated":true}`), "caller-1", "admin", []string{"admin"})
		rec := httptest.NewRecorder()
		h.UpdateUserHandler(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("self-deactivation returns 400", func(t *testing.T) {
		q := &mockAuthQuerier{
			getUser: db.User{ID: "caller-1", Username: "admin", CreatedAt: now},
		}
		h := NewHandler(q, nil)
		req := withCallerContext(patchWithID("caller-1", `{"deactivated":true}`), "caller-1", "admin", []string{"admin"})
		rec := httptest.NewRecorder()
		h.UpdateUserHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	// Real-DB tests cover the transaction path.
	t.Run("deactivate user returns 200", func(t *testing.T) {
		store, sqlDB := newTestDB(t)
		// Create admin (caller) and a target user.
		callerUser, _ := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           "caller-1",
			Username:     "admin",
			PasswordHash: "hash",
			CreatedAt:    now,
		})
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: callerUser.ID, Role: "admin", CreatedAt: now})

		targetUser, _ := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           "target-1",
			Username:     "bob",
			PasswordHash: "hash",
			CreatedAt:    now,
		})
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: targetUser.ID, Role: "operator", CreatedAt: now})

		h := NewHandler(store.Queries(), sqlDB)
		req := withCallerContext(patchWithID("target-1", `{"deactivated":true}`), "caller-1", "admin", []string{"admin"})
		rec := httptest.NewRecorder()
		h.UpdateUserHandler(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data userResponse `json:"data"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Data.DeactivatedAt == nil {
			t.Error("expected deactivated_at to be set")
		}
	})

	t.Run("update roles returns 200", func(t *testing.T) {
		store, sqlDB := newTestDB(t)

		callerUser, _ := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           "caller-2",
			Username:     "admin2",
			PasswordHash: "hash",
			CreatedAt:    now,
		})
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: callerUser.ID, Role: "admin", CreatedAt: now})

		targetUser, _ := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           "target-2",
			Username:     "charlie",
			PasswordHash: "hash",
			CreatedAt:    now,
		})
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: targetUser.ID, Role: "operator", CreatedAt: now})

		h := NewHandler(store.Queries(), sqlDB)
		req := withCallerContext(patchWithID("target-2", `{"roles":["operator","approver"]}`), "caller-2", "admin2", []string{"admin"})
		rec := httptest.NewRecorder()
		h.UpdateUserHandler(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data userResponse `json:"data"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.Data.Roles) != 2 {
			t.Errorf("roles = %v, want [approver, operator]", resp.Data.Roles)
		}
	})

	t.Run("last admin protection returns 400", func(t *testing.T) {
		store, sqlDB := newTestDB(t)

		// Single admin user — deactivating them should be blocked.
		adminUser, _ := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           "admin-only",
			Username:     "adminonly",
			PasswordHash: "hash",
			CreatedAt:    now,
		})
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: adminUser.ID, Role: "admin", CreatedAt: now})

		// A second non-admin caller.
		callerUser, _ := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           "caller-3",
			Username:     "caller",
			PasswordHash: "hash",
			CreatedAt:    now,
		})
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: callerUser.ID, Role: "admin", CreatedAt: now})

		h := NewHandler(store.Queries(), sqlDB)
		// Try to deactivate the ONLY OTHER admin from a 2-admin system — but make
		// one of them the last. Use admin-only as target and caller-3 as caller,
		// but remove caller-3's admin role first so admin-only is the last admin.
		if err := store.Queries().RemoveAllRolesForUser(context.Background(), "caller-3"); err != nil {
			t.Fatalf("remove role: %v", err)
		}
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: "caller-3", Role: "operator", CreatedAt: now})

		req := withCallerContext(patchWithID("admin-only", `{"deactivated":true}`), "caller-3", "caller", []string{"admin"})
		rec := httptest.NewRecorder()
		h.UpdateUserHandler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
		}
	})

	// Regression test: a deactivated admin must not be counted as an active admin
	// when enforcing last-admin protection. Without the fix, deactivating the
	// last active admin would succeed if another (deactivated) admin still held
	// the role in the DB.
	t.Run("deactivated admin is not counted for last-admin protection", func(t *testing.T) {
		store, sqlDB := newTestDB(t)

		// "active-admin" is the only active admin.
		activeAdmin, _ := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           "active-admin",
			Username:     "activeadmin",
			PasswordHash: "hash",
			CreatedAt:    now,
		})
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: activeAdmin.ID, Role: "admin", CreatedAt: now})

		// "ghost-admin" still holds the admin role but is deactivated.
		deactivatedAt := now
		ghostAdmin, _ := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           "ghost-admin",
			Username:     "ghostadmin",
			PasswordHash: "hash",
			CreatedAt:    now,
		})
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: ghostAdmin.ID, Role: "admin", CreatedAt: now})
		_ = store.Queries().DeactivateUser(context.Background(), db.DeactivateUserParams{
			ID:            ghostAdmin.ID,
			DeactivatedAt: &deactivatedAt,
		})

		// A non-admin caller who performs the update request.
		caller, _ := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
			ID:           "caller-4",
			Username:     "caller4",
			PasswordHash: "hash",
			CreatedAt:    now,
		})
		_ = store.Queries().AssignRole(context.Background(), db.AssignRoleParams{UserID: caller.ID, Role: "operator", CreatedAt: now})

		h := NewHandler(store.Queries(), sqlDB)
		// Attempting to deactivate active-admin should be blocked — the deactivated
		// ghost-admin must not count as an "other active admin".
		req := withCallerContext(patchWithID("active-admin", `{"deactivated":true}`), "caller-4", "caller4", []string{"operator"})
		rec := httptest.NewRecorder()
		h.UpdateUserHandler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (last-admin protection); body: %s", rec.Code, rec.Body.String())
		}
	})
}
