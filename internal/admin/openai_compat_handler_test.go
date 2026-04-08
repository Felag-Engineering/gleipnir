package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/llm"
)

// fakeOpenAICompatQuerier is an in-memory implementation for tests.
type fakeOpenAICompatQuerier struct {
	rows    map[int64]OpenAICompatRow
	nextID  int64
	listErr error
}

func newFakeQuerier() *fakeOpenAICompatQuerier {
	return &fakeOpenAICompatQuerier{rows: map[int64]OpenAICompatRow{}, nextID: 1}
}

func (f *fakeOpenAICompatQuerier) ListOpenAICompatProviders(ctx context.Context) ([]OpenAICompatRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]OpenAICompatRow, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeOpenAICompatQuerier) GetOpenAICompatProviderByID(ctx context.Context, id int64) (OpenAICompatRow, error) {
	r, ok := f.rows[id]
	if !ok {
		return OpenAICompatRow{}, sql.ErrNoRows
	}
	return r, nil
}

func (f *fakeOpenAICompatQuerier) GetOpenAICompatProviderByName(ctx context.Context, name string) (OpenAICompatRow, error) {
	for _, r := range f.rows {
		if r.Name == name {
			return r, nil
		}
	}
	return OpenAICompatRow{}, sql.ErrNoRows
}

func (f *fakeOpenAICompatQuerier) CreateOpenAICompatProvider(ctx context.Context, row OpenAICompatRow) (OpenAICompatRow, error) {
	row.ID = f.nextID
	f.nextID++
	f.rows[row.ID] = row
	return row, nil
}

func (f *fakeOpenAICompatQuerier) UpdateOpenAICompatProvider(ctx context.Context, row OpenAICompatRow) (OpenAICompatRow, error) {
	if _, ok := f.rows[row.ID]; !ok {
		return OpenAICompatRow{}, sql.ErrNoRows
	}
	f.rows[row.ID] = row
	return row, nil
}

func (f *fakeOpenAICompatQuerier) DeleteOpenAICompatProvider(ctx context.Context, id int64) error {
	if _, ok := f.rows[id]; !ok {
		return sql.ErrNoRows
	}
	delete(f.rows, id)
	return nil
}

// testKey is a 32-byte AES-GCM key.
var testKey = []byte("01234567890123456789012345678901")

// okTester is a ConnectionTester that always succeeds.
func okTester(ctx context.Context, baseURL, apiKey string) (bool, error) {
	return true, nil
}

// failTester returns the supplied error.
func failTester(err error) ConnectionTester {
	return func(ctx context.Context, baseURL, apiKey string) (bool, error) {
		return false, err
	}
}

// notFoundTester simulates a backend without /v1/models.
func notFoundTester(ctx context.Context, baseURL, apiKey string) (bool, error) {
	return false, nil
}

// newOpenAICompatTestHandler wires up a fake querier, registry, and tester.
func newOpenAICompatTestHandler(tester ConnectionTester) (*OpenAICompatHandler, *fakeOpenAICompatQuerier, *llm.ProviderRegistry) {
	q := newFakeQuerier()
	reg := llm.NewProviderRegistry()
	h := NewOpenAICompatHandler(q, testKey, reg, tester)
	return h, q, reg
}

func doRequest(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// mountRouter registers every openai-compat route on a chi mux for testing.
func mountRouter(h *OpenAICompatHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/admin/openai-providers", h.ListProviders)
	r.Post("/api/v1/admin/openai-providers", h.CreateProvider)
	r.Get("/api/v1/admin/openai-providers/{id}", h.GetProvider)
	r.Put("/api/v1/admin/openai-providers/{id}", h.UpdateProvider)
	r.Delete("/api/v1/admin/openai-providers/{id}", h.DeleteProvider)
	r.Post("/api/v1/admin/openai-providers/{id}/test", h.TestProvider)
	return r
}

func TestCreate_HappyPath(t *testing.T) {
	h, q, reg := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)

	body := map[string]string{"name": "ollama", "base_url": "http://ollama:11434/v1", "api_key": "sk-abc"}
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}
	if len(q.rows) != 1 {
		t.Errorf("row not persisted")
	}
	if _, err := reg.Get("ollama"); err != nil {
		t.Errorf("not registered: %v", err)
	}
}

func TestCreate_ReservedName(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	for _, name := range []string{"anthropic", "google", "openai"} {
		body := map[string]string{"name": name, "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"}
		w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, w.Code)
		}
	}
}

func TestCreate_InvalidNameFormat(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	for _, bad := range []string{"", "With Spaces", "UPPER", "-leading-dash", "way-too-long-name-that-exceeds-the-sixty-four-character-limit-by-quite-a-bit"} {
		body := map[string]string{"name": bad, "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"}
		w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("name %q: status %d, want 400", bad, w.Code)
		}
	}
}

func TestCreate_InvalidBaseURL(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	for _, bad := range []string{"", "not-a-url", "ftp://example.com", "https://example.com/?x=1"} {
		body := map[string]string{"name": "ok", "base_url": bad, "api_key": "sk-abc"}
		w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("base_url %q: status %d, want 400", bad, w.Code)
		}
	}
}

func TestCreate_EmptyAPIKey(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	body := map[string]string{"name": "ok", "base_url": "https://api.openai.com/v1", "api_key": ""}
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: %d, want 400", w.Code)
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	body := map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"}
	_ = doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusConflict {
		t.Errorf("status: %d, want 409", w.Code)
	}
}

func TestCreate_ConnectionTestFails(t *testing.T) {
	h, q, reg := newOpenAICompatTestHandler(failTester(errors.New("connection refused")))
	router := mountRouter(h)
	body := map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"}
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: %d, want 400", w.Code)
	}
	if len(q.rows) != 0 {
		t.Errorf("row should not have been created")
	}
	if len(reg.Providers()) != 0 {
		t.Errorf("registry should not have been mutated")
	}
	if !strings.Contains(w.Body.String(), "connection refused") {
		t.Errorf("error should mention connection refused: %s", w.Body.String())
	}
}

func TestCreate_ConnectionTest404AcceptsWithFlag(t *testing.T) {
	h, q, reg := newOpenAICompatTestHandler(notFoundTester)
	router := mountRouter(h)
	body := map[string]string{"name": "ollama", "base_url": "http://ollama:11434/v1", "api_key": "placeholder"}
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d, want 201. body: %s", w.Code, w.Body.String())
	}
	if len(q.rows) != 1 {
		t.Errorf("row not persisted")
	}
	if _, err := reg.Get("ollama"); err != nil {
		t.Errorf("not registered: %v", err)
	}
	var resp providerResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ModelsEndpointAvailable {
		t.Errorf("models_endpoint_available should be false")
	}
}

func TestUpdate_MaskedKeyKeepsCiphertext(t *testing.T) {
	h, q, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)

	// Create a provider.
	create := map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-original"}
	wc := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", create)
	if wc.Code != http.StatusCreated {
		t.Fatalf("create: %d", wc.Code)
	}
	// Response is wrapped in {"data": ...}; unwrap to read the masked key.
	var envelope struct {
		Data providerResponse `json:"data"`
	}
	_ = json.Unmarshal(wc.Body.Bytes(), &envelope)
	created := envelope.Data

	originalCiphertext := q.rows[created.ID].APIKeyEncrypted
	originalMasked := created.MaskedKey

	// PUT with the masked value — should keep ciphertext unchanged.
	upd := map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v2", "api_key": originalMasked}
	wu := doRequest(t, router, "PUT", "/api/v1/admin/openai-providers/1", upd)
	if wu.Code != http.StatusOK {
		t.Fatalf("update: %d, body: %s", wu.Code, wu.Body.String())
	}
	if got := q.rows[created.ID].APIKeyEncrypted; got != originalCiphertext {
		t.Errorf("ciphertext should be unchanged, got different bytes")
	}
	if got := q.rows[created.ID].BaseURL; got != "https://api.openai.com/v2" {
		t.Errorf("base_url not updated: %q", got)
	}
}

func TestUpdate_NewKeyReencrypts(t *testing.T) {
	h, q, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)

	create := map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-original"}
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers", create)
	originalCiphertext := q.rows[1].APIKeyEncrypted

	upd := map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-new-key-xyz"}
	wu := doRequest(t, router, "PUT", "/api/v1/admin/openai-providers/1", upd)
	if wu.Code != http.StatusOK {
		t.Fatalf("update: %d", wu.Code)
	}
	if q.rows[1].APIKeyEncrypted == originalCiphertext {
		t.Errorf("ciphertext should have changed")
	}
	plain, _ := Decrypt(testKey, q.rows[1].APIKeyEncrypted)
	if plain != "sk-new-key-xyz" {
		t.Errorf("plaintext after decrypt: %q", plain)
	}
}

func TestUpdate_NameChangeReregisters(t *testing.T) {
	h, _, reg := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "old-name", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"})

	upd := map[string]string{"name": "new-name", "base_url": "https://api.openai.com/v1", "api_key": "sk-...abc"}
	_ = doRequest(t, router, "PUT", "/api/v1/admin/openai-providers/1", upd)

	if _, err := reg.Get("new-name"); err != nil {
		t.Errorf("new-name not registered: %v", err)
	}
	if _, err := reg.Get("old-name"); err == nil {
		t.Errorf("old-name should have been unregistered")
	}
}

func TestUpdate_NameCollision(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "a", "base_url": "https://api.openai.com/v1", "api_key": "sk-1"})
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "b", "base_url": "https://api.openai.com/v1", "api_key": "sk-2"})

	// Try to rename row 2 to "a".
	upd := map[string]string{"name": "a", "base_url": "https://api.openai.com/v1", "api_key": "sk-...sk-2"}
	w := doRequest(t, router, "PUT", "/api/v1/admin/openai-providers/2", upd)
	if w.Code != http.StatusConflict {
		t.Errorf("status: %d, want 409", w.Code)
	}
}

func TestDelete_HappyPath(t *testing.T) {
	h, q, reg := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"})

	w := doRequest(t, router, "DELETE", "/api/v1/admin/openai-providers/1", nil)
	if w.Code != http.StatusNoContent {
		t.Errorf("status: %d, want 204", w.Code)
	}
	if len(q.rows) != 0 {
		t.Errorf("row should be deleted")
	}
	if _, err := reg.Get("my-openai"); err == nil {
		t.Errorf("should be unregistered")
	}
}

func TestDelete_NotFound(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	w := doRequest(t, router, "DELETE", "/api/v1/admin/openai-providers/999", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: %d, want 404", w.Code)
	}
}

func TestList_MasksKeys(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-very-secret-value"})

	w := doRequest(t, router, "GET", "/api/v1/admin/openai-providers", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "sk-very-secret-value") {
		t.Error("plaintext key leaked in list response")
	}
}

func TestGet_MasksKey(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-secret-xyz"})

	w := doRequest(t, router, "GET", "/api/v1/admin/openai-providers/1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "sk-secret-xyz") {
		t.Error("plaintext key leaked in get response")
	}
}

func TestTestProvider_HappyPath(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"})

	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers/1/test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}
	// WriteJSON wraps in {"data": ...}.
	var envelope struct {
		Data struct {
			OK                      bool   `json:"ok"`
			ModelsEndpointAvailable bool   `json:"models_endpoint_available"`
			Error                   string `json:"error"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if !envelope.Data.OK {
		t.Errorf("want ok=true, got %+v", envelope.Data)
	}
}

func TestTestProvider_Unreachable(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "my-openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"})

	// Swap the tester to a failing one for the re-test.
	h.tester = failTester(errors.New("connection refused"))

	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers/1/test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Data.OK {
		t.Errorf("want ok=false, got %+v", envelope.Data)
	}
	if !strings.Contains(envelope.Data.Error, "connection refused") {
		t.Errorf("error should mention connection refused: %q", envelope.Data.Error)
	}
}

func TestTestProvider_NotFound(t *testing.T) {
	h, _, _ := newOpenAICompatTestHandler(okTester)
	router := mountRouter(h)
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers/999/test", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: %d, want 404", w.Code)
	}
}
