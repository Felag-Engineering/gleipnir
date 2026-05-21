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

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/settings"
)

// mockQuerier is an in-memory AdminQuerier for tests.
type mockQuerier struct {
	settings map[string]db.SystemSetting
	models   map[string]db.ModelSetting // key: "provider:model"
}

func newMockQuerier() *mockQuerier {
	return &mockQuerier{
		settings: make(map[string]db.SystemSetting),
		models:   make(map[string]db.ModelSetting),
	}
}

func (m *mockQuerier) GetSystemSetting(_ context.Context, key string) (db.SystemSetting, error) {
	row, ok := m.settings[key]
	if !ok {
		return db.SystemSetting{}, ErrNotFound
	}
	return row, nil
}

func (m *mockQuerier) UpsertSystemSetting(_ context.Context, key, value, updatedAt string) error {
	m.settings[key] = db.SystemSetting{Key: key, Value: value, UpdatedAt: updatedAt}
	return nil
}

func (m *mockQuerier) DeleteSystemSetting(_ context.Context, key string) error {
	delete(m.settings, key)
	return nil
}

func (m *mockQuerier) ListSystemSettings(_ context.Context) ([]db.SystemSetting, error) {
	rows := make([]db.SystemSetting, 0, len(m.settings))
	for _, row := range m.settings {
		rows = append(rows, row)
	}
	return rows, nil
}

func (m *mockQuerier) ListEnabledModels(_ context.Context) ([]db.ListEnabledModelsRow, error) {
	var rows []db.ListEnabledModelsRow
	for _, row := range m.models {
		if row.Enabled != 0 {
			rows = append(rows, db.ListEnabledModelsRow{Provider: row.Provider, ModelName: row.ModelName})
		}
	}
	return rows, nil
}

func (m *mockQuerier) UpsertModelSetting(_ context.Context, provider, modelName string, enabled int64, updatedAt string) error {
	key := provider + ":" + modelName
	m.models[key] = db.ModelSetting{Provider: provider, ModelName: modelName, Enabled: enabled, UpdatedAt: updatedAt}
	return nil
}

func (m *mockQuerier) ListModelSettings(_ context.Context) ([]db.ModelSetting, error) {
	rows := make([]db.ModelSetting, 0, len(m.models))
	for _, row := range m.models {
		rows = append(rows, row)
	}
	return rows, nil
}

// testEncryptionKey is a fixed 32-byte key for tests.
var testEncryptionKey = []byte("01234567890123456789012345678901")

func newTestHandler(q *mockQuerier) *Handler {
	return NewHandler(q, settings.NewService(q), testEncryptionKey, []string{"anthropic", "openai"}, nil, nil, nil)
}

// newTestHandlerWithLister constructs a Handler with a nil configureProvider,
// a no-op removeProvider, and the supplied lister.
func newTestHandlerWithLister(q *mockQuerier, lister llm.ModelLister) *Handler {
	return NewHandler(q, settings.NewService(q), testEncryptionKey, []string{"anthropic", "openai"}, nil, nil, lister)
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
	h := NewHandler(q, settings.NewService(q), testEncryptionKey, []string{"anthropic"}, failConfigure, nil, nil)

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
	q.settings["default_model"] = db.SystemSetting{
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

	q.settings["default_model"] = db.SystemSetting{
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

	q.settings["anthropic_api_key"] = db.SystemSetting{Key: "anthropic_api_key", Value: "encrypted-val"}
	q.settings["default_model"] = db.SystemSetting{Key: "default_model", Value: "anthropic:claude-sonnet-4-20250514"}
	q.settings["max_tokens"] = db.SystemSetting{Key: "max_tokens", Value: "4096"}

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

func TestDeleteProviderKey(t *testing.T) {
	q := newMockQuerier()
	var removedProvider string
	h := NewHandler(q, settings.NewService(q), testEncryptionKey, []string{"anthropic"}, nil, func(provider string) {
		removedProvider = provider
	}, nil)

	q.settings["anthropic_api_key"] = db.SystemSetting{Key: "anthropic_api_key", Value: "encrypted"}

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

// stubLister satisfies llm.ModelLister for ListAllModels tests.
type stubLister struct {
	models  map[string][]llm.ModelInfo
	listErr error
}

func (s *stubLister) ListModels(_ context.Context, provider string) ([]llm.ModelInfo, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.models[provider], nil
}

func (s *stubLister) ListAllModels(_ context.Context) (map[string][]llm.ModelInfo, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.models, nil
}

func (s *stubLister) InvalidateModelCache(_ string) error { return nil }
func (s *stubLister) InvalidateAllModelCaches()           {}

func TestListAllModels(t *testing.T) {
	t.Run("returns all models with correct enabled state", func(t *testing.T) {
		q := newMockQuerier()
		h := newTestHandler(q)

		lister := &stubLister{
			models: map[string][]llm.ModelInfo{
				"anthropic": {
					{Name: "claude-sonnet-4", DisplayName: "Claude Sonnet 4"},
					{Name: "claude-haiku-4", DisplayName: "Claude Haiku 4"},
				},
				"google": {
					{Name: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash"},
				},
			},
		}

		// Enable one anthropic model.
		_ = q.UpsertModelSetting(context.Background(), "anthropic", "claude-sonnet-4", 1, "2024-01-01T00:00:00Z")

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/admin/models/all", nil)
		h.ListAllModels(lister)(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}

		data := parseDataResponse(t, rec)
		var models []struct {
			Provider    string `json:"provider"`
			ModelName   string `json:"model_name"`
			DisplayName string `json:"display_name"`
			Enabled     bool   `json:"enabled"`
		}
		if err := json.Unmarshal(data, &models); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if len(models) != 3 {
			t.Fatalf("expected 3 models, got %d", len(models))
		}

		byKey := make(map[string]bool)
		for _, m := range models {
			byKey[m.Provider+":"+m.ModelName] = m.Enabled
		}

		if !byKey["anthropic:claude-sonnet-4"] {
			t.Error("claude-sonnet-4 should be enabled")
		}
		if byKey["anthropic:claude-haiku-4"] {
			t.Error("claude-haiku-4 should be disabled (no enabled row)")
		}
		if byKey["google:gemini-2.5-flash"] {
			t.Error("gemini-2.5-flash should be disabled (no enabled row)")
		}
	})

	t.Run("lister error returns 500", func(t *testing.T) {
		q := newMockQuerier()
		h := newTestHandler(q)

		lister := &stubLister{listErr: fmt.Errorf("provider unreachable")}

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/admin/models/all", nil)
		h.ListAllModels(lister)(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
	})
}

// setProviderKeyRequest is a helper that issues a PUT .../key request and
// returns the recorded response.
func setProviderKeyRequest(h *Handler, provider, key string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"key": "%s"}`, key)
	req := httptest.NewRequest(http.MethodPut, "/providers/"+provider+"/key", strings.NewReader(body))
	req = withChiParam(req, "name", provider)
	rec := httptest.NewRecorder()
	h.SetProviderKey(rec, req)
	return rec
}

func TestSetProviderKey_AutoEnablesModels(t *testing.T) {
	q := newMockQuerier()
	lister := &stubLister{
		models: map[string][]llm.ModelInfo{
			"anthropic": {
				{Name: "claude-sonnet-4", DisplayName: "Claude Sonnet 4"},
				{Name: "claude-haiku-4", DisplayName: "Claude Haiku 4"},
			},
		},
	}
	h := newTestHandlerWithLister(q, lister)

	rec := setProviderKeyRequest(h, "anthropic", "sk-ant-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	for _, modelName := range []string{"claude-sonnet-4", "claude-haiku-4"} {
		row, ok := q.models["anthropic:"+modelName]
		if !ok {
			t.Errorf("model %q not found in model settings", modelName)
			continue
		}
		if row.Enabled == 0 {
			t.Errorf("model %q should be enabled after SetProviderKey", modelName)
		}
	}
}

func TestSetProviderKey_ListerErrorDoesNotFailRequest(t *testing.T) {
	q := newMockQuerier()
	lister := &stubLister{listErr: fmt.Errorf("provider registry unavailable")}
	h := newTestHandlerWithLister(q, lister)

	rec := setProviderKeyRequest(h, "anthropic", "sk-ant-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even with lister error, got %d; body: %s", rec.Code, rec.Body.String())
	}

	// The encrypted key must still be stored.
	if _, err := q.GetSystemSetting(context.Background(), "anthropic_api_key"); err != nil {
		t.Errorf("api key should have been stored despite lister error: %v", err)
	}
}

func deleteProviderKeyRequest(h *Handler, provider string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/providers/"+provider+"/key", nil)
	req = withChiParam(req, "name", provider)
	rec := httptest.NewRecorder()
	h.DeleteProviderKey(rec, req)
	return rec
}

func TestDeleteProviderKey_DisablesProviderModels(t *testing.T) {
	q := newMockQuerier()
	// Pre-seed two enabled anthropic models and one enabled openai model.
	q.models["anthropic:claude-sonnet-4"] = db.ModelSetting{Provider: "anthropic", ModelName: "claude-sonnet-4", Enabled: 1}
	q.models["anthropic:claude-haiku-4"] = db.ModelSetting{Provider: "anthropic", ModelName: "claude-haiku-4", Enabled: 1}
	q.models["openai:gpt-4o"] = db.ModelSetting{Provider: "openai", ModelName: "gpt-4o", Enabled: 1}
	q.settings["anthropic_api_key"] = db.SystemSetting{Key: "anthropic_api_key", Value: "encrypted"}
	h := newTestHandler(q)

	rec := deleteProviderKeyRequest(h, "anthropic")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	if q.models["anthropic:claude-sonnet-4"].Enabled != 0 {
		t.Error("claude-sonnet-4 should be disabled after key deletion")
	}
	if q.models["anthropic:claude-haiku-4"].Enabled != 0 {
		t.Error("claude-haiku-4 should be disabled after key deletion")
	}
	// OpenAI model must remain untouched.
	if q.models["openai:gpt-4o"].Enabled != 1 {
		t.Error("openai gpt-4o should remain enabled")
	}
}

// --- public_url validation tests ---

func TestUpdateSettings_ValidatesPublicURL_Relative(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	body := `{"public_url": "/relative/path"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for relative URL, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if _, err := q.GetSystemSetting(context.Background(), "public_url"); err == nil {
		t.Error("public_url should not have been stored for a relative path")
	}
}

func TestUpdateSettings_ValidatesPublicURL_NotAURL(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	body := `{"public_url": "not-a-url"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-URL string, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateSettings_PublicURL_StripsTrailingSlash(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	body := `{"public_url": "https://gleipnir.example.com/"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	row, err := q.GetSystemSetting(context.Background(), "public_url")
	if err != nil {
		t.Fatalf("public_url not stored: %v", err)
	}
	want := "https://gleipnir.example.com"
	if row.Value != want {
		t.Errorf("trailing slash not stripped: got %q, want %q", row.Value, want)
	}
}

func TestUpdateSettings_PublicURL_AcceptsHTTP(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	body := `{"public_url": "http://localhost:8080"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for http URL, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateSettings_PublicURL_EmptyClears(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	// Pre-seed a value so we can verify it's deleted rather than replaced.
	q.settings["public_url"] = db.SystemSetting{Key: "public_url", Value: "https://gleipnir.example.com"}

	body := `{"public_url": ""}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when clearing public_url, got %d; body: %s", rec.Code, rec.Body.String())
	}

	// The row should be gone, not set to "".
	if _, err := q.GetSystemSetting(context.Background(), "public_url"); err == nil {
		t.Error("public_url should have been deleted, not stored as empty string")
	}
}

// publicConfigTestResponse mirrors publicConfigResponse for test assertions.
// Using a typed struct catches shape mismatches that a map[string]string would silently drop.
type publicConfigTestResponse struct {
	PublicURL    string `json:"public_url"`
	DefaultModel *struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
	} `json:"default_model"`
}

func TestGetPublicConfig_WithValue(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	q.settings["public_url"] = db.SystemSetting{Key: "public_url", Value: "https://gleipnir.example.com"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	h.GetPublicConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var cfg publicConfigTestResponse
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.PublicURL != "https://gleipnir.example.com" {
		t.Errorf("public_url = %q, want %q", cfg.PublicURL, "https://gleipnir.example.com")
	}
	if cfg.DefaultModel != nil {
		t.Errorf("expected default_model to be nil when not configured, got %+v", cfg.DefaultModel)
	}
}

func TestGetPublicConfig_Unset(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	// No public_url in store.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	h.GetPublicConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when public_url is not set, got %d; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var cfg publicConfigTestResponse
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.PublicURL != "" {
		t.Errorf("expected empty public_url, got %q", cfg.PublicURL)
	}
	if cfg.DefaultModel != nil {
		t.Errorf("expected default_model to be nil when not configured, got %+v", cfg.DefaultModel)
	}
}

func TestGetPublicConfig_WithDefaultModel(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	// Seed the default_model setting and mark the model as enabled.
	q.settings["default_model"] = db.SystemSetting{Key: "default_model", Value: "anthropic:claude-sonnet-4-6"}
	q.models["anthropic:claude-sonnet-4-6"] = db.ModelSetting{
		Provider: "anthropic", ModelName: "claude-sonnet-4-6", Enabled: 1,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	h.GetPublicConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var cfg publicConfigTestResponse
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.DefaultModel == nil {
		t.Fatal("expected default_model to be non-nil")
	}
	if cfg.DefaultModel.Provider != "anthropic" {
		t.Errorf("default_model.provider = %q, want %q", cfg.DefaultModel.Provider, "anthropic")
	}
	if cfg.DefaultModel.Name != "claude-sonnet-4-6" {
		t.Errorf("default_model.name = %q, want %q", cfg.DefaultModel.Name, "claude-sonnet-4-6")
	}
}

func TestGetPublicConfig_DefaultModelDisabled(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	// Seed the default_model setting but do NOT add it to enabled models.
	// This simulates a stale default whose model was disabled via direct DB edit.
	q.settings["default_model"] = db.SystemSetting{Key: "default_model", Value: "anthropic:claude-sonnet-4-6"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	h.GetPublicConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var cfg publicConfigTestResponse
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.DefaultModel != nil {
		t.Errorf("expected default_model to be nil when default is disabled, got %+v", cfg.DefaultModel)
	}
}

// --- OnPublicURLChanged hook tests ---

func TestUpdateSettings_OnPublicURLChanged_InvokedWhenChanged(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)
	q.settings["public_url"] = db.SystemSetting{Key: "public_url", Value: "https://old.example.com"}

	var gotOld, gotNew string
	h.OnPublicURLChanged = func(_ context.Context, oldURL, newURL string) {
		gotOld = oldURL
		gotNew = newURL
	}

	body := `{"public_url": "https://new.example.com"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if gotOld != "https://old.example.com" {
		t.Errorf("oldURL = %q, want https://old.example.com", gotOld)
	}
	if gotNew != "https://new.example.com" {
		t.Errorf("newURL = %q, want https://new.example.com", gotNew)
	}
}

func TestUpdateSettings_OnPublicURLChanged_NotInvokedWhenUnchanged(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)
	q.settings["public_url"] = db.SystemSetting{Key: "public_url", Value: "https://gleipnir.example.com"}

	invoked := false
	h.OnPublicURLChanged = func(_ context.Context, _, _ string) {
		invoked = true
	}

	// Same value — hook must not fire.
	body := `{"public_url": "https://gleipnir.example.com"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if invoked {
		t.Error("OnPublicURLChanged must not be invoked when the URL has not changed")
	}
}

func TestUpdateSettings_OnPublicURLChanged_NotInvokedWhenAbsent(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	invoked := false
	h.OnPublicURLChanged = func(_ context.Context, _, _ string) {
		invoked = true
	}

	// Request does not include public_url — hook must not fire.
	body := `{"some_other_setting": "value"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req)

	if invoked {
		t.Error("OnPublicURLChanged must not be invoked when public_url is not in the request")
	}
}

func TestUpdateSettings_OnPublicURLChanged_NilSafe(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q) // hook is nil by default

	// Must not panic when hook is nil.
	body := `{"public_url": "https://gleipnir.example.com"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateSettings(rec, req) // must not panic

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// --- SetDefaultModel tests ---

func TestSetDefaultModel(t *testing.T) {
	tests := []struct {
		name       string
		setupQ     func(q *mockQuerier)
		body       string
		wantStatus int
		wantErr    string
		checkQ     func(t *testing.T, q *mockQuerier)
	}{
		{
			name: "happy_path_known_provider",
			setupQ: func(q *mockQuerier) {
				// Anthropic is a known provider — needs a key row.
				q.settings["anthropic_api_key"] = db.SystemSetting{Key: "anthropic_api_key", Value: "encrypted-key"}
				// And the model must be enabled.
				q.models["anthropic:claude-sonnet-4-6"] = db.ModelSetting{
					Provider: "anthropic", ModelName: "claude-sonnet-4-6", Enabled: 1,
				}
			},
			body:       `{"provider":"anthropic","name":"claude-sonnet-4-6"}`,
			wantStatus: http.StatusOK,
			checkQ: func(t *testing.T, q *mockQuerier) {
				t.Helper()
				row, ok := q.settings["default_model"]
				if !ok {
					t.Fatal("default_model not written to settings")
				}
				if row.Value != "anthropic:claude-sonnet-4-6" {
					t.Errorf("default_model value = %q, want anthropic:claude-sonnet-4-6", row.Value)
				}
			},
		},
		{
			name: "happy_path_openai_compat",
			setupQ: func(q *mockQuerier) {
				// "my-compat" is NOT a known provider — no API key check.
				// Only need an enabled model entry.
				q.models["my-compat:some-model"] = db.ModelSetting{
					Provider: "my-compat", ModelName: "some-model", Enabled: 1,
				}
			},
			body:       `{"provider":"my-compat","name":"some-model"}`,
			wantStatus: http.StatusOK,
			checkQ: func(t *testing.T, q *mockQuerier) {
				t.Helper()
				row, ok := q.settings["default_model"]
				if !ok {
					t.Fatal("default_model not written to settings")
				}
				if row.Value != "my-compat:some-model" {
					t.Errorf("default_model value = %q, want my-compat:some-model", row.Value)
				}
			},
		},
		{
			name: "missing_provider_key_known_provider",
			setupQ: func(q *mockQuerier) {
				// Anthropic is known, but no API key row seeded.
				q.models["anthropic:claude-sonnet-4-6"] = db.ModelSetting{
					Provider: "anthropic", ModelName: "claude-sonnet-4-6", Enabled: 1,
				}
			},
			body:       `{"provider":"anthropic","name":"claude-sonnet-4-6"}`,
			wantStatus: http.StatusBadRequest,
			wantErr:    "provider has no API key configured",
		},
		{
			name: "unknown_model_known_provider",
			setupQ: func(q *mockQuerier) {
				q.settings["anthropic_api_key"] = db.SystemSetting{Key: "anthropic_api_key", Value: "encrypted"}
				// No enabled model entry for claude-sonnet-4-6.
			},
			body:       `{"provider":"anthropic","name":"claude-sonnet-4-6"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantErr:    "model is not enabled for this provider",
		},
		{
			name: "unknown_model_openai_compat",
			setupQ: func(q *mockQuerier) {
				// Unknown provider AND no enabled model row.
			},
			body:       `{"provider":"my-compat","name":"nonexistent-model"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantErr:    "model is not enabled for this provider",
		},
		{
			name:       "empty_provider",
			setupQ:     func(_ *mockQuerier) {},
			body:       `{"provider":"","name":"claude-sonnet-4-6"}`,
			wantStatus: http.StatusBadRequest,
			wantErr:    "provider is required",
		},
		{
			name:       "empty_name",
			setupQ:     func(_ *mockQuerier) {},
			body:       `{"provider":"anthropic","name":""}`,
			wantStatus: http.StatusBadRequest,
			wantErr:    "name is required",
		},
		{
			name:       "invalid_json",
			setupQ:     func(_ *mockQuerier) {},
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
			wantErr:    "invalid JSON body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newMockQuerier()
			tt.setupQ(q)
			h := newTestHandler(q)

			req := httptest.NewRequest(http.MethodPut, "/admin/settings/default-model", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.SetDefaultModel(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if tt.wantErr != "" {
				errMsg := parseErrorResponse(t, rec)
				if !strings.Contains(errMsg, tt.wantErr) {
					t.Errorf("error = %q, want to contain %q", errMsg, tt.wantErr)
				}
			}

			if tt.checkQ != nil {
				tt.checkQ(t, q)
			}
		})
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
