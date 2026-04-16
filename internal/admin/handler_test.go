package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
)

// mockQuerier is an in-memory AdminQuerier for tests.
type mockQuerier struct {
	settings       map[string]db.SystemSetting
	models         map[string]db.ModelSetting // key: "provider:model"
	getSettingErrs map[string]error           // inject per-key errors for GetSystemSetting
}

func newMockQuerier() *mockQuerier {
	return &mockQuerier{
		settings:       make(map[string]db.SystemSetting),
		models:         make(map[string]db.ModelSetting),
		getSettingErrs: make(map[string]error),
	}
}

func (m *mockQuerier) GetSystemSetting(_ context.Context, key string) (db.SystemSetting, error) {
	if err, ok := m.getSettingErrs[key]; ok {
		return db.SystemSetting{}, err
	}
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
	return NewHandler(q, testEncryptionKey, []string{"anthropic", "openai"}, nil, nil, nil)
}

// newTestHandlerWithLister constructs a Handler with a nil configureProvider,
// a no-op removeProvider, and the supplied lister.
func newTestHandlerWithLister(q *mockQuerier, lister llm.ModelLister) *Handler {
	return NewHandler(q, testEncryptionKey, []string{"anthropic", "openai"}, nil, nil, lister)
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
	h := NewHandler(q, testEncryptionKey, []string{"anthropic"}, failConfigure, nil, nil)

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

func TestGetSystemDefault(t *testing.T) {
	q := newMockQuerier()
	h := newTestHandler(q)

	q.settings["default_model"] = db.SystemSetting{
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

func TestSetProviderKey_SetsDefaultWhenNoneExists(t *testing.T) {
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

	row, err := q.GetSystemSetting(context.Background(), "default_model")
	if err != nil {
		t.Fatalf("default_model not set: %v", err)
	}
	want := "anthropic:claude-sonnet-4"
	if row.Value != want {
		t.Errorf("default_model = %q, want %q", row.Value, want)
	}
}

func TestSetProviderKey_DoesNotOverwriteExistingDefault(t *testing.T) {
	q := newMockQuerier()
	q.settings["default_model"] = db.SystemSetting{Key: "default_model", Value: "openai:gpt-4o"}
	lister := &stubLister{
		models: map[string][]llm.ModelInfo{
			"anthropic": {
				{Name: "claude-sonnet-4", DisplayName: "Claude Sonnet 4"},
			},
		},
	}
	h := newTestHandlerWithLister(q, lister)

	rec := setProviderKeyRequest(h, "anthropic", "sk-ant-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	row, err := q.GetSystemSetting(context.Background(), "default_model")
	if err != nil {
		t.Fatalf("default_model missing: %v", err)
	}
	if row.Value != "openai:gpt-4o" {
		t.Errorf("default_model overwritten: got %q, want %q", row.Value, "openai:gpt-4o")
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

func TestSetProviderKey_TransientDefaultReadErrorDoesNotSeedBadDefault(t *testing.T) {
	q := newMockQuerier()
	// Inject a non-ErrNoRows error for "default_model" so that autoEnableModelsForProvider
	// cannot distinguish "missing" from "transient failure". The default must NOT be written.
	transientErr := errors.New("disk I/O error")
	q.getSettingErrs["default_model"] = transientErr

	lister := &stubLister{
		models: map[string][]llm.ModelInfo{
			"anthropic": {
				{Name: "claude-sonnet-4", DisplayName: "Claude Sonnet 4"},
			},
		},
	}
	h := newTestHandlerWithLister(q, lister)

	rec := setProviderKeyRequest(h, "anthropic", "sk-ant-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	// The error injection is still in place, so GetSystemSetting returns transientErr.
	// We verify nothing was written by checking the settings map directly.
	if _, ok := q.settings["default_model"]; ok {
		t.Error("default_model should NOT have been written when GetSystemSetting returned a non-ErrNoRows error")
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

func TestDeleteProviderKey_ClearsDefaultWhenItPointsToThisProvider(t *testing.T) {
	q := newMockQuerier()
	q.settings["anthropic_api_key"] = db.SystemSetting{Key: "anthropic_api_key", Value: "encrypted"}
	q.settings["default_model"] = db.SystemSetting{Key: "default_model", Value: "anthropic:claude-sonnet-4"}
	h := newTestHandler(q)

	rec := deleteProviderKeyRequest(h, "anthropic")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	_, err := q.GetSystemSetting(context.Background(), "default_model")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("default_model should have been cleared, got err=%v", err)
	}
}

func TestDeleteProviderKey_LeavesDefaultWhenItPointsElsewhere(t *testing.T) {
	q := newMockQuerier()
	q.settings["anthropic_api_key"] = db.SystemSetting{Key: "anthropic_api_key", Value: "encrypted"}
	q.settings["default_model"] = db.SystemSetting{Key: "default_model", Value: "openai:gpt-4o"}
	h := newTestHandler(q)

	rec := deleteProviderKeyRequest(h, "anthropic")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	row, err := q.GetSystemSetting(context.Background(), "default_model")
	if err != nil {
		t.Fatalf("default_model should still exist: %v", err)
	}
	if row.Value != "openai:gpt-4o" {
		t.Errorf("default_model should be unchanged, got %q", row.Value)
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
	var cfg map[string]string
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg["public_url"] != "https://gleipnir.example.com" {
		t.Errorf("public_url = %q, want %q", cfg["public_url"], "https://gleipnir.example.com")
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
	var cfg map[string]string
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, ok := cfg["public_url"]; !ok || v != "" {
		t.Errorf("expected public_url=\"\", got %q (ok=%v)", v, ok)
	}
}

func TestGetPublicURL_Helper(t *testing.T) {
	t.Run("returns stored value", func(t *testing.T) {
		q := newMockQuerier()
		h := newTestHandler(q)
		q.settings["public_url"] = db.SystemSetting{Key: "public_url", Value: "https://gleipnir.example.com"}

		got, err := h.GetPublicURL(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://gleipnir.example.com" {
			t.Errorf("got %q, want %q", got, "https://gleipnir.example.com")
		}
	})

	t.Run("returns empty string when not set", func(t *testing.T) {
		q := newMockQuerier()
		h := newTestHandler(q)

		got, err := h.GetPublicURL(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})
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
