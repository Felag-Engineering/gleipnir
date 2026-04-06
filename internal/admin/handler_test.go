package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// mockQuerier is an in-memory AdminQuerier for tests.
type mockQuerier struct {
	settings map[string]SystemSettingRow
	models   map[string]ModelSettingRow // key: "provider:model"
}

func newMockQuerier() *mockQuerier {
	return &mockQuerier{
		settings: make(map[string]SystemSettingRow),
		models:   make(map[string]ModelSettingRow),
	}
}

func (m *mockQuerier) GetSystemSetting(_ context.Context, key string) (SystemSettingRow, error) {
	row, ok := m.settings[key]
	if !ok {
		return SystemSettingRow{}, ErrNotFound
	}
	return row, nil
}

func (m *mockQuerier) UpsertSystemSetting(_ context.Context, key, value, updatedAt string) error {
	m.settings[key] = SystemSettingRow{Key: key, Value: value, UpdatedAt: updatedAt}
	return nil
}

func (m *mockQuerier) DeleteSystemSetting(_ context.Context, key string) error {
	delete(m.settings, key)
	return nil
}

func (m *mockQuerier) ListSystemSettings(_ context.Context) ([]SystemSettingRow, error) {
	rows := make([]SystemSettingRow, 0, len(m.settings))
	for _, row := range m.settings {
		rows = append(rows, row)
	}
	return rows, nil
}

func (m *mockQuerier) ListDisabledModels(_ context.Context) ([]DisabledModelRow, error) {
	var rows []DisabledModelRow
	for _, row := range m.models {
		if row.Enabled == 0 {
			rows = append(rows, DisabledModelRow{Provider: row.Provider, ModelName: row.ModelName})
		}
	}
	return rows, nil
}

func (m *mockQuerier) UpsertModelSetting(_ context.Context, provider, modelName string, enabled int64, updatedAt string) error {
	key := provider + ":" + modelName
	m.models[key] = ModelSettingRow{Provider: provider, ModelName: modelName, Enabled: enabled, UpdatedAt: updatedAt}
	return nil
}

func (m *mockQuerier) ListModelSettings(_ context.Context) ([]ModelSettingRow, error) {
	rows := make([]ModelSettingRow, 0, len(m.models))
	for _, row := range m.models {
		rows = append(rows, row)
	}
	return rows, nil
}

// testEncryptionKey is a fixed 32-byte key for tests.
var testEncryptionKey = []byte("01234567890123456789012345678901")

func newTestHandler(q *mockQuerier) *Handler {
	return NewHandler(q, testEncryptionKey, []string{"anthropic", "openai"}, nil, nil)
}

func withChiParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func parseDataResponse(t *testing.T, rec *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var resp map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"]
	if !ok {
		t.Fatal("response missing 'data' key")
	}
	return data
}

func parseErrorResponse(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return resp["error"]
}

func TestListProviders_NoKeys(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/providers", nil)
	h.ListProviders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	data := parseDataResponse(t, rec)
	var statuses []ProviderStatus
	if err := json.Unmarshal(data, &statuses); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(statuses) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(statuses))
	}
	for _, s := range statuses {
		if s.HasKey {
			t.Errorf("provider %q should not have key", s.Name)
		}
		if s.MaskedKey != "" {
			t.Errorf("provider %q should not have masked key", s.Name)
		}
	}
}

func TestSetProviderKey_ThenListProviders(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	// Set a key for anthropic.
	body := `{"key": "sk-ant-api03-test-key-value-1234"}`
	req := httptest.NewRequest(http.MethodPut, "/providers/anthropic/key", strings.NewReader(body))
	req = withChiParam(req, "name", "anthropic")
	rec := httptest.NewRecorder()
	h.SetProviderKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("SetProviderKey: expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	// Verify key is stored encrypted.
	row, err := q.GetSystemSetting(context.Background(), "anthropic_api_key")
	if err != nil {
		t.Fatalf("key not stored: %v", err)
	}
	if row.Value == "sk-ant-api03-test-key-value-1234" {
		t.Fatal("key stored in plaintext")
	}

	// List providers and check anthropic shows as configured.
	req2 := httptest.NewRequest(http.MethodGet, "/providers", nil)
	rec2 := httptest.NewRecorder()
	h.ListProviders(rec2, req2)

	data := parseDataResponse(t, rec2)
	var statuses []ProviderStatus
	if err := json.Unmarshal(data, &statuses); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var found bool
	for _, s := range statuses {
		if s.Name == "anthropic" {
			found = true
			if !s.HasKey {
				t.Error("anthropic should have key")
			}
			if s.MaskedKey == "" {
				t.Error("anthropic should have masked key")
			}
			if !strings.Contains(s.MaskedKey, "...") {
				t.Errorf("masked key should contain '...', got %q", s.MaskedKey)
			}
		}
	}
	if !found {
		t.Fatal("anthropic not found in providers list")
	}
}

func TestSetProviderKey_ConfigureProviderFails(t *testing.T) {
	q := newMockQuerier()
	failConfigure := func(_ context.Context, _ string, _ string) error {
		return fmt.Errorf("invalid API key")
	}
	h := NewHandler(q, testEncryptionKey, []string{"anthropic"}, failConfigure, nil)

	body := `{"key": "bad-key"}`
	req := httptest.NewRequest(http.MethodPut, "/providers/anthropic/key", strings.NewReader(body))
	req = withChiParam(req, "name", "anthropic")
	rec := httptest.NewRecorder()
	h.SetProviderKey(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	// Key should not be stored.
	if _, err := q.GetSystemSetting(context.Background(), "anthropic_api_key"); err == nil {
		t.Fatal("key should not have been stored after configure failure")
	}
}

func TestSetModelEnabled_DisableDefault_Returns409(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	// Set a default model.
	q.settings["default_model"] = SystemSettingRow{
		Key:   "default_model",
		Value: "anthropic:claude-sonnet-4-20250514",
	}

	body := `{"provider": "anthropic", "enabled": false}`
	req := httptest.NewRequest(http.MethodPut, "/models/claude-sonnet-4-20250514/enabled", strings.NewReader(body))
	req = withChiParam(req, "id", "claude-sonnet-4-20250514")
	rec := httptest.NewRecorder()
	h.SetModelEnabled(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d; body: %s", rec.Code, rec.Body.String())
	}

	errMsg := parseErrorResponse(t, rec)
	if !strings.Contains(errMsg, "default model") {
		t.Errorf("expected error about default model, got %q", errMsg)
	}
}

func TestSetModelEnabled_DisableNonDefault_OK(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	q.settings["default_model"] = SystemSettingRow{
		Key:   "default_model",
		Value: "anthropic:claude-sonnet-4-20250514",
	}

	body := `{"provider": "openai", "enabled": false}`
	req := httptest.NewRequest(http.MethodPut, "/models/gpt-4o/enabled", strings.NewReader(body))
	req = withChiParam(req, "id", "gpt-4o")
	rec := httptest.NewRecorder()
	h.SetModelEnabled(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestGetSettings_ExcludesAPIKeys(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	q.settings["anthropic_api_key"] = SystemSettingRow{Key: "anthropic_api_key", Value: "encrypted-val"}
	q.settings["default_model"] = SystemSettingRow{Key: "default_model", Value: "anthropic:claude-sonnet-4-20250514"}
	q.settings["max_tokens"] = SystemSettingRow{Key: "max_tokens", Value: "4096"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	h.GetSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	data := parseDataResponse(t, rec)
	var settings map[string]string
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := settings["anthropic_api_key"]; ok {
		t.Error("API key should be excluded from settings")
	}
	if v, ok := settings["default_model"]; !ok || v != "anthropic:claude-sonnet-4-20250514" {
		t.Errorf("expected default_model, got %v", settings)
	}
	if v, ok := settings["max_tokens"]; !ok || v != "4096" {
		t.Errorf("expected max_tokens, got %v", settings)
	}
}

func TestUpdateSettings_RejectsAPIKeys(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	body := `{"anthropic_api_key": "should-not-work", "theme": "dark"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	// Verify nothing was saved.
	if _, err := q.GetSystemSetting(context.Background(), "theme"); err == nil {
		t.Error("theme should not have been saved when request contained _api_key")
	}
}

func TestUpdateSettings_OK(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	body := `{"theme": "dark", "default_model": "anthropic:claude-sonnet-4-20250514"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	if row, err := q.GetSystemSetting(context.Background(), "theme"); err != nil || row.Value != "dark" {
		t.Errorf("theme not saved correctly: %v, %v", row, err)
	}
}

func TestGetSystemDefault(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	q.settings["default_model"] = SystemSettingRow{
		Key:   "default_model",
		Value: "anthropic:claude-sonnet-4-20250514",
	}

	provider, model, err := h.GetSystemDefault(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider != "anthropic" || model != "claude-sonnet-4-20250514" {
		t.Errorf("got provider=%q model=%q", provider, model)
	}
}

func TestGetSystemDefault_NotSet(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	_, _, err := h.GetSystemDefault(context.Background())
	if err == nil {
		t.Fatal("expected error when default_model not set")
	}
}

func TestDeleteProviderKey(t *testing.T) {
	q := newMockQuerier()
	var removedProvider string
	h := NewHandler(q, testEncryptionKey, []string{"anthropic"}, nil, func(provider string) {
		removedProvider = provider
	})

	q.settings["anthropic_api_key"] = SystemSettingRow{Key: "anthropic_api_key", Value: "encrypted"}

	req := httptest.NewRequest(http.MethodDelete, "/providers/anthropic/key", nil)
	req = withChiParam(req, "name", "anthropic")
	rec := httptest.NewRecorder()
	h.DeleteProviderKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if _, err := q.GetSystemSetting(context.Background(), "anthropic_api_key"); err == nil {
		t.Error("key should have been deleted")
	}
	if removedProvider != "anthropic" {
		t.Errorf("removeProvider not called with 'anthropic', got %q", removedProvider)
	}
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		name     string
		minutes  int
		expected string
	}{
		{"zero", 0, "0m"},
		{"minutes only", 45, "45m"},
		{"hours and minutes", 125, "2h 5m"},
		{"days", 1500, "1d 1h 0m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUptime(time.Duration(tt.minutes) * time.Minute)
			if got != tt.expected {
				t.Errorf("formatUptime(%dm) = %q, want %q", tt.minutes, got, tt.expected)
			}
		})
	}
}
