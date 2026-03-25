package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rapp992/gleipnir/internal/model"
)

// okHandler is a sentinel that always writes 200 OK.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func contextWithUser(user *UserContext) context.Context {
	return context.WithValue(context.Background(), contextKey{}, user)
}

func TestRequireRole(t *testing.T) {
	cases := []struct {
		name          string
		user          *UserContext // nil means no user in context
		requiredRoles []model.Role
		wantStatus    int
	}{
		{
			name:          "admin bypasses all guards",
			user:          &UserContext{ID: "1", Roles: []string{"admin"}},
			requiredRoles: []model.Role{model.RoleOperator},
			wantStatus:    http.StatusOK,
		},
		{
			name:          "admin bypasses even with empty required roles",
			user:          &UserContext{ID: "1", Roles: []string{"admin"}},
			requiredRoles: []model.Role{},
			wantStatus:    http.StatusOK,
		},
		{
			name:          "single matching role passes",
			user:          &UserContext{ID: "1", Roles: []string{"operator"}},
			requiredRoles: []model.Role{model.RoleOperator},
			wantStatus:    http.StatusOK,
		},
		{
			name:          "first of multiple required roles matches",
			user:          &UserContext{ID: "1", Roles: []string{"auditor"}},
			requiredRoles: []model.Role{model.RoleOperator, model.RoleAuditor},
			wantStatus:    http.StatusOK,
		},
		{
			name:          "second of multiple required roles matches",
			user:          &UserContext{ID: "1", Roles: []string{"operator"}},
			requiredRoles: []model.Role{model.RoleAuditor, model.RoleOperator},
			wantStatus:    http.StatusOK,
		},
		{
			name:          "no matching role returns 403",
			user:          &UserContext{ID: "1", Roles: []string{"auditor"}},
			requiredRoles: []model.Role{model.RoleOperator},
			wantStatus:    http.StatusForbidden,
		},
		{
			name:          "user with no roles returns 403",
			user:          &UserContext{ID: "1", Roles: nil},
			requiredRoles: []model.Role{model.RoleOperator},
			wantStatus:    http.StatusForbidden,
		},
		{
			name:          "empty required roles with non-admin returns 403",
			user:          &UserContext{ID: "1", Roles: []string{"operator"}},
			requiredRoles: []model.Role{},
			wantStatus:    http.StatusForbidden,
		},
		{
			name:          "no user in context returns 401",
			user:          nil,
			requiredRoles: []model.Role{model.RoleOperator},
			wantStatus:    http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := RequireRole(tc.requiredRoles...)(okHandler)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.user != nil {
				req = req.WithContext(contextWithUser(tc.user))
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestUserContext_HasRole(t *testing.T) {
	cases := []struct {
		name     string
		roles    []string
		checkFor model.Role
		want     bool
	}{
		{
			name:     "role present returns true",
			roles:    []string{"admin", "operator"},
			checkFor: model.RoleAdmin,
			want:     true,
		},
		{
			name:     "role absent returns false",
			roles:    []string{"operator"},
			checkFor: model.RoleAdmin,
			want:     false,
		},
		{
			name:     "empty roles returns false",
			roles:    nil,
			checkFor: model.RoleAdmin,
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := &UserContext{Roles: tc.roles}
			got := u.HasRole(tc.checkFor)
			if got != tc.want {
				t.Errorf("HasRole(%q) = %v, want %v", tc.checkFor, got, tc.want)
			}
		})
	}
}
