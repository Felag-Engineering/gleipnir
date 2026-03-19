package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
)

// mockAuthQuerier implements AuthQuerier for testing.
type mockAuthQuerier struct {
	user            db.User
	getUserErr      error
	createdSession  db.Session
	createSessionErr error
	deleteSessionErr error
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
			h := NewHandler(tc.querier)
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
			h := NewHandler(tc.querier)
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
