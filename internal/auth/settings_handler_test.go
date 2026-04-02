package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
)

// mockSettingsQuerier implements SettingsQuerier for testing.
type mockSettingsQuerier struct {
	prefs       []db.UserPreference
	listErr     error
	upsertedKey string
	upsertErr   error
}

func (m *mockSettingsQuerier) ListUserPreferences(_ context.Context, _ string) ([]db.UserPreference, error) {
	return m.prefs, m.listErr
}

func (m *mockSettingsQuerier) UpsertUserPreference(_ context.Context, arg db.UpsertUserPreferenceParams) (db.UserPreference, error) {
	if m.upsertErr != nil {
		return db.UserPreference{}, m.upsertErr
	}
	m.upsertedKey = arg.PreferenceKey
	return db.UserPreference{
		UserID:          arg.UserID,
		PreferenceKey:   arg.PreferenceKey,
		PreferenceValue: arg.PreferenceValue,
		UpdatedAt:       arg.UpdatedAt,
	}, nil
}

func TestGetPreferences(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)

	cases := []struct {
		name       string
		prefs      []db.UserPreference
		listErr    error
		wantStatus int
		wantKeys   []string
	}{
		{
			name: "returns stored preferences as key-value map",
			prefs: []db.UserPreference{
				{UserID: "user-1", PreferenceKey: "timezone", PreferenceValue: "UTC", UpdatedAt: now},
				{UserID: "user-1", PreferenceKey: "default_model", PreferenceValue: "claude-sonnet-4-6", UpdatedAt: now},
			},
			wantStatus: http.StatusOK,
			wantKeys:   []string{"timezone", "default_model"},
		},
		{
			name:       "empty prefs returns empty map",
			prefs:      []db.UserPreference{},
			wantStatus: http.StatusOK,
			wantKeys:   []string{},
		},
		{
			name:       "DB error returns 500",
			listErr:    context.DeadlineExceeded,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &mockSettingsQuerier{prefs: tc.prefs, listErr: tc.listErr}
			h := NewSettingsHandler(q)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/preferences", nil)
			req = withCallerContext(req, "user-1", "alice", []string{"admin"})
			rec := httptest.NewRecorder()

			h.GetPreferences(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantStatus == http.StatusOK {
				var resp struct {
					Data map[string]string `json:"data"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				for _, key := range tc.wantKeys {
					if _, ok := resp.Data[key]; !ok {
						t.Errorf("expected key %q in response", key)
					}
				}
			}
		})
	}
}

func TestUpdatePreferences(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)

	cases := []struct {
		name       string
		body       string
		prefs      []db.UserPreference // returned by ListUserPreferences after upsert
		upsertErr  error
		wantStatus int
	}{
		{
			name: "valid keys are upserted and returned",
			body: `{"timezone":"America/New_York","default_model":"claude-opus-4"}`,
			prefs: []db.UserPreference{
				{UserID: "user-1", PreferenceKey: "timezone", PreferenceValue: "America/New_York", UpdatedAt: now},
				{UserID: "user-1", PreferenceKey: "default_model", PreferenceValue: "claude-opus-4", UpdatedAt: now},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown key returns 400",
			body:       `{"unknown_key":"value"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON returns 400",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "DB upsert error returns 500",
			body:       `{"timezone":"UTC"}`,
			upsertErr:  context.DeadlineExceeded,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &mockSettingsQuerier{prefs: tc.prefs, upsertErr: tc.upsertErr}
			h := NewSettingsHandler(q)

			req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/preferences", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req = withCallerContext(req, "user-1", "alice", []string{"admin"})
			rec := httptest.NewRecorder()

			h.UpdatePreferences(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantStatus == http.StatusOK {
				var resp struct {
					Data map[string]string `json:"data"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if len(resp.Data) != len(tc.prefs) {
					t.Errorf("preference count = %d, want %d", len(resp.Data), len(tc.prefs))
				}
			}
		})
	}
}
