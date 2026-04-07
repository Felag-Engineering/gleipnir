package admin

import (
	"context"
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
