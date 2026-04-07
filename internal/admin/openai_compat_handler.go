package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/llm/openai"
)

// OpenAICompatRow is the in-memory representation of one row. Decouples the
// handler from the sqlc-generated struct so the handler is test-friendly.
type OpenAICompatRow struct {
	ID              int64
	Name            string
	BaseURL         string
	APIKeyEncrypted string
	CreatedAt       string
	UpdatedAt       string
}

// OpenAICompatQuerier is the minimal DB interface the handler depends on.
// In production, main.go adapts sqlc-generated methods into this interface.
type OpenAICompatQuerier interface {
	ListOpenAICompatProviders(ctx context.Context) ([]OpenAICompatRow, error)
	GetOpenAICompatProviderByID(ctx context.Context, id int64) (OpenAICompatRow, error)
	GetOpenAICompatProviderByName(ctx context.Context, name string) (OpenAICompatRow, error)
	CreateOpenAICompatProvider(ctx context.Context, row OpenAICompatRow) (OpenAICompatRow, error)
	UpdateOpenAICompatProvider(ctx context.Context, row OpenAICompatRow) (OpenAICompatRow, error)
	DeleteOpenAICompatProvider(ctx context.Context, id int64) error
}

// ConnectionTester runs the save-time connection test. Injected for testing.
// Returns (modelsEndpointAvailable, error).
type ConnectionTester func(ctx context.Context, baseURL, apiKey string) (bool, error)

// DefaultConnectionTester calls GET {baseURL}/models with a 5s timeout.
// Non-error with a non-empty model list → (true, nil).
// Non-error with an empty list → (false, nil), meaning the backend returned
// 404 on /models (the escape-hatch from ADR-032) — caller records this as
// "models endpoint unavailable" but accepts the save.
// Any network/HTTP error → (false, err).
func DefaultConnectionTester(ctx context.Context, baseURL, apiKey string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := openai.NewClient(baseURL, apiKey, openai.WithTimeout(5*time.Second))
	models, err := client.ListModels(ctx)
	if err != nil {
		return false, err
	}
	// loadModelsFromServer returns an empty slice (no error) when the backend
	// responds 404 on /models. That's the "models endpoint unavailable" path.
	return len(models) > 0, nil
}

var nameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var reservedNames = map[string]bool{"anthropic": true, "google": true}

// OpenAICompatHandler handles /api/v1/admin/openai-providers/*.
type OpenAICompatHandler struct {
	q        OpenAICompatQuerier
	encKey   []byte
	registry *llm.ProviderRegistry
	tester   ConnectionTester

	// In-memory models-endpoint-available state, keyed by row name.
	// Rebuilt on startup (from each save) and updated on every save/test.
	modelsAvail map[string]bool
}

// NewOpenAICompatHandler constructs the handler. If tester is nil,
// DefaultConnectionTester is used.
func NewOpenAICompatHandler(q OpenAICompatQuerier, encKey []byte, registry *llm.ProviderRegistry, tester ConnectionTester) *OpenAICompatHandler {
	if tester == nil {
		tester = DefaultConnectionTester
	}
	return &OpenAICompatHandler{
		q:           q,
		encKey:      encKey,
		registry:    registry,
		tester:      tester,
		modelsAvail: map[string]bool{},
	}
}

// providerResponse is the JSON-encoded shape returned by GET/POST/PUT.
type providerResponse struct {
	ID                      int64  `json:"id"`
	Name                    string `json:"name"`
	BaseURL                 string `json:"base_url"`
	MaskedKey               string `json:"masked_key"`
	ModelsEndpointAvailable bool   `json:"models_endpoint_available"`
	CreatedAt               string `json:"created_at"`
	UpdatedAt               string `json:"updated_at"`
}

func (h *OpenAICompatHandler) rowToResponse(row OpenAICompatRow) providerResponse {
	// Decrypt error is intentionally swallowed: if the key is unreadable we
	// still want to return a response — MaskKey("") produces an empty string,
	// which is a safe degraded value the UI can handle without crashing.
	plain, _ := Decrypt(h.encKey, row.APIKeyEncrypted)
	return providerResponse{
		ID:                      row.ID,
		Name:                    row.Name,
		BaseURL:                 row.BaseURL,
		MaskedKey:               MaskKey(plain),
		ModelsEndpointAvailable: h.modelsAvail[row.Name],
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
	}
}

// ListProviders GET /api/v1/admin/openai-providers
func (h *OpenAICompatHandler) ListProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q.ListOpenAICompatProviders(r.Context())
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to list providers", err.Error())
		return
	}
	out := make([]providerResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, h.rowToResponse(row))
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// GetProvider GET /api/v1/admin/openai-providers/{id}
func (h *OpenAICompatHandler) GetProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	row, err := h.q.GetOpenAICompatProviderByID(r.Context(), id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "provider not found", "")
		return
	}
	api.WriteJSON(w, http.StatusOK, h.rowToResponse(row))
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid id", raw)
		return 0, false
	}
	return id, true
}

// validateNameFormat returns an error describing why name is invalid, or nil.
func validateNameFormat(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if reservedNames[name] {
		return fmt.Errorf("name %q is reserved", name)
	}
	if !nameRegexp.MatchString(name) {
		return fmt.Errorf("name must match ^[a-z0-9][a-z0-9_-]{0,63}$")
	}
	return nil
}

// validateBaseURL parses and normalizes (strips trailing slashes) a base URL.
func validateBaseURL(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("base_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("base_url is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base_url must use http or https, got %q", u.Scheme)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("base_url must not contain a query string or fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

// isMaskedKey returns true when the supplied API key is actually a masked
// value (like "sk-...wxyz") rather than a real key. The handler treats this
// as "don't change the stored key."
func isMaskedKey(key string) bool {
	return strings.Contains(key, "...")
}

// createRequest is the JSON body accepted by POST and PUT.
type createRequest struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// CreateProvider POST /api/v1/admin/openai-providers
func (h *OpenAICompatHandler) CreateProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body createRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid JSON body", err.Error())
		return
	}
	if err := validateNameFormat(body.Name); err != nil {
		api.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	base, err := validateBaseURL(body.BaseURL)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	if body.APIKey == "" {
		api.WriteError(w, http.StatusBadRequest, "api_key is required", "")
		return
	}
	// Any error from GetOpenAICompatProviderByName is treated as "name available" for v1.
	if _, err := h.q.GetOpenAICompatProviderByName(ctx, body.Name); err == nil {
		api.WriteError(w, http.StatusConflict, "name already in use", "")
		return
	}
	modelsAvail, err := h.tester(ctx, base, body.APIKey)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("connection test failed: %v", err), "")
		return
	}
	enc, err := Encrypt(h.encKey, body.APIKey)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "encryption failed", "")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	row, err := h.q.CreateOpenAICompatProvider(ctx, OpenAICompatRow{
		Name:            body.Name,
		BaseURL:         base,
		APIKeyEncrypted: enc,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to create provider", err.Error())
		return
	}
	// Only mutate the registry after the DB write succeeds.
	client := openai.NewClient(base, body.APIKey)
	h.registry.Register(body.Name, client)
	h.modelsAvail[body.Name] = modelsAvail

	api.WriteJSON(w, http.StatusCreated, h.rowToResponse(row))
}

// UpdateProvider PUT /api/v1/admin/openai-providers/{id}
func (h *OpenAICompatHandler) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var body createRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid JSON body", err.Error())
		return
	}
	if err := validateNameFormat(body.Name); err != nil {
		api.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	base, err := validateBaseURL(body.BaseURL)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	existing, err := h.q.GetOpenAICompatProviderByID(r.Context(), id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "provider not found", "")
		return
	}

	// Name collision with a *different* row. Skip when name is unchanged so a
	// row updating only its base_url or key does not collide with itself.
	if body.Name != existing.Name {
		if _, err := h.q.GetOpenAICompatProviderByName(r.Context(), body.Name); err == nil {
			api.WriteError(w, http.StatusConflict, "name already in use", "")
			return
		}
	}

	// Resolve the effective plaintext key and the ciphertext to persist.
	// If the client echoes back the masked value (e.g. "sk-...wxyz") or sends
	// an empty string, we treat it as "keep the existing key" — the stored
	// ciphertext is preserved unchanged. Otherwise, we treat the value as a
	// new plaintext key and re-encrypt it. This is the sentinel behavior that
	// lets the UI round-trip without ever exposing the real plaintext.
	var effectiveKey string
	var newCiphertext string
	if isMaskedKey(body.APIKey) || body.APIKey == "" {
		plain, err := Decrypt(h.encKey, existing.APIKeyEncrypted)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "could not decrypt existing key", "")
			return
		}
		effectiveKey = plain
		newCiphertext = existing.APIKeyEncrypted
	} else {
		effectiveKey = body.APIKey
		enc, err := Encrypt(h.encKey, body.APIKey)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "encryption failed", "")
			return
		}
		newCiphertext = enc
	}

	modelsAvail, err := h.tester(r.Context(), base, effectiveKey)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("connection test failed: %v", err), "")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	updated, err := h.q.UpdateOpenAICompatProvider(r.Context(), OpenAICompatRow{
		ID:              id,
		Name:            body.Name,
		BaseURL:         base,
		APIKeyEncrypted: newCiphertext,
		CreatedAt:       existing.CreatedAt,
		UpdatedAt:       now,
	})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to update provider", err.Error())
		return
	}

	// Mutate the registry only after the DB write succeeds (matches create-path
	// invariant). Unregister the old name first if it changed, then register
	// under the new name.
	if body.Name != existing.Name {
		h.registry.Unregister(existing.Name)
		delete(h.modelsAvail, existing.Name)
	}
	client := openai.NewClient(base, effectiveKey)
	h.registry.Register(body.Name, client)
	h.modelsAvail[body.Name] = modelsAvail

	api.WriteJSON(w, http.StatusOK, h.rowToResponse(updated))
}
func (h *OpenAICompatHandler) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotImplemented, "not implemented", "")
}
func (h *OpenAICompatHandler) TestProvider(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotImplemented, "not implemented", "")
}
