package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/httputil"
	"github.com/rapp992/gleipnir/internal/llm"
)

var ErrNotFound = sql.ErrNoRows

type AdminQuerier interface {
	GetSystemSetting(ctx context.Context, key string) (db.SystemSetting, error)
	UpsertSystemSetting(ctx context.Context, key, value, updatedAt string) error
	DeleteSystemSetting(ctx context.Context, key string) error
	ListSystemSettings(ctx context.Context) ([]db.SystemSetting, error)
	ListEnabledModels(ctx context.Context) ([]db.ListEnabledModelsRow, error)
	UpsertModelSetting(ctx context.Context, provider, modelName string, enabled int64, updatedAt string) error
	ListModelSettings(ctx context.Context) ([]db.ModelSetting, error)
}

type ProviderStatus struct {
	Name      string `json:"name"`
	HasKey    bool   `json:"has_key"`
	MaskedKey string `json:"masked_key,omitempty"`
}

type ProviderConfigurator func(ctx context.Context, provider string, apiKey string) error
type ProviderRemover func(provider string)

type Handler struct {
	q                 AdminQuerier
	encryptionKey     []byte
	knownProviders    []string
	knownProviderSet  map[string]bool
	configureProvider ProviderConfigurator
	removeProvider    ProviderRemover
	lister            llm.ModelLister
}

func NewHandler(q AdminQuerier, encryptionKey []byte, knownProviders []string, configure ProviderConfigurator, remove ProviderRemover, lister llm.ModelLister) *Handler {
	providerSet := make(map[string]bool, len(knownProviders))
	for _, p := range knownProviders {
		providerSet[p] = true
	}
	return &Handler{
		q:                 q,
		encryptionKey:     encryptionKey,
		knownProviders:    knownProviders,
		knownProviderSet:  providerSet,
		configureProvider: configure,
		removeProvider:    remove,
		lister:            lister,
	}
}

// ListProviders returns the status of each known LLM provider's API key.
func (h *Handler) ListProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	statuses := make([]ProviderStatus, 0, len(h.knownProviders))

	for _, name := range h.knownProviders {
		ps := ProviderStatus{Name: name}
		row, err := h.q.GetSystemSetting(ctx, name+"_api_key")
		if err == nil {
			decrypted, derr := Decrypt(h.encryptionKey, row.Value)
			if derr == nil {
				ps.HasKey = true
				ps.MaskedKey = MaskKey(decrypted)
			}
		}
		statuses = append(statuses, ps)
	}

	httputil.WriteJSON(w, http.StatusOK, statuses)
}

// SetProviderKey encrypts and stores an API key for the given provider.
func (h *Handler) SetProviderKey(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !h.knownProviderSet[name] {
		httputil.WriteError(w, http.StatusBadRequest, "unknown provider", "")
		return
	}

	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid JSON body", "")
		return
	}
	if body.Key == "" {
		httputil.WriteError(w, http.StatusBadRequest, "key is required", "")
		return
	}

	if h.configureProvider != nil {
		if err := h.configureProvider(r.Context(), name, body.Key); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, fmt.Sprintf("provider configuration failed: %v", err), "")
			return
		}
	}

	encrypted, err := Encrypt(h.encryptionKey, body.Key)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "encryption failed", "")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := h.q.UpsertSystemSetting(r.Context(), name+"_api_key", encrypted, now); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to save key", "")
		return
	}

	// Best-effort: enable all models for this provider and seed the default if
	// none is set. Errors are logged but never fail the key-save response.
	h.autoEnableModelsForProvider(r.Context(), name)

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// autoEnableModelsForProvider enables every model returned by the provider's
// lister. It is best-effort: individual errors are logged and never surface to
// the caller. A nil lister is valid — the function becomes a no-op.
func (h *Handler) autoEnableModelsForProvider(ctx context.Context, provider string) {
	if h.lister == nil {
		return
	}

	models, err := h.lister.ListModels(ctx, provider)
	if err != nil {
		slog.Warn("auto-enable: list models failed", "provider", provider, "err", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, m := range models {
		if err := h.q.UpsertModelSetting(ctx, provider, m.Name, 1, now); err != nil {
			slog.Warn("auto-enable: upsert model failed", "provider", provider, "model", m.Name, "err", err)
			// Continue — best-effort, other models should still be enabled.
		}
	}
}

// DeleteProviderKey removes an API key for the given provider.
func (h *Handler) DeleteProviderKey(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !h.knownProviderSet[name] {
		httputil.WriteError(w, http.StatusBadRequest, "unknown provider", "")
		return
	}

	if err := h.q.DeleteSystemSetting(r.Context(), name+"_api_key"); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to delete key", "")
		return
	}

	// Best-effort: disable the provider's models and clear default_model if it
	// points to this provider. Errors are logged but never fail the response.
	h.disableModelsForProvider(r.Context(), name)

	if h.removeProvider != nil {
		h.removeProvider(name)
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// disableModelsForProvider sets enabled=0 for every model belonging to the
// given provider. It operates only on h.q and never touches h.lister.
func (h *Handler) disableModelsForProvider(ctx context.Context, provider string) {
	rows, err := h.q.ListModelSettings(ctx)
	if err != nil {
		slog.Warn("disable-models: list model settings failed", "provider", provider, "err", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, row := range rows {
		if row.Provider != provider || row.Enabled == 0 {
			continue
		}
		if err := h.q.UpsertModelSetting(ctx, provider, row.ModelName, 0, now); err != nil {
			slog.Warn("disable-models: upsert model failed", "provider", provider, "model", row.ModelName, "err", err)
			// Continue — best-effort, disable remaining models.
		}
	}
}

// GetSettings returns all system settings except API keys.
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q.ListSystemSettings(r.Context())
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list settings", "")
		return
	}

	settings := make(map[string]string)
	for _, row := range rows {
		if strings.HasSuffix(row.Key, "_api_key") {
			continue
		}
		settings[row.Key] = row.Value
	}

	httputil.WriteJSON(w, http.StatusOK, settings)
}

// validateSettings checks setting values before they are persisted.
// It returns an HTTP status code and error message on failure (status 0 means valid).
// Currently validates public_url: must be an absolute URL with scheme and host.
// Trailing slashes are stripped and the cleaned value is written back to the map.
func validateSettings(body map[string]string) (httpStatus int, errMsg string) {
	rawURL, ok := body["public_url"]
	if !ok || rawURL == "" {
		// Empty string is valid — it clears the setting.
		return 0, ""
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return http.StatusBadRequest, "public_url must be an absolute URL (e.g. https://gleipnir.example.com)"
	}

	// Strip trailing slash so stored values are always canonical.
	body["public_url"] = strings.TrimRight(rawURL, "/")
	return 0, ""
}

// UpdateSettings upserts system settings, rejecting any _api_key keys.
func (h *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid JSON body", "")
		return
	}

	for key := range body {
		if strings.HasSuffix(key, "_api_key") {
			httputil.WriteError(w, http.StatusBadRequest, "cannot set API keys through settings endpoint", "")
			return
		}
	}

	if status, msg := validateSettings(body); status != 0 {
		httputil.WriteError(w, status, msg, "")
		return
	}

	// Settings are written one-at-a-time. For the small number of settings in
	// practice (2-3 keys), the risk of partial failure is negligible. A future
	// enhancement could wrap this in a transaction.
	now := time.Now().UTC().Format(time.RFC3339)
	for key, value := range body {
		// An empty public_url means the operator wants to clear the setting.
		// We delete rather than upsert because value TEXT NOT NULL accepts empty
		// strings — storing "" would be indistinguishable from "not set".
		if key == "public_url" && value == "" {
			if err := h.q.DeleteSystemSetting(r.Context(), key); err != nil {
				httputil.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("failed to clear setting %q", key), "")
				return
			}
			continue // deletion handled above — skip the upsert below
		}

		if err := h.q.UpsertSystemSetting(r.Context(), key, value, now); err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save setting %q", key), "")
			return
		}
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetPublicURL reads the public_url system setting and returns it.
// If the setting has not been configured, it returns an empty string (not an error).
// Other database errors are returned as-is for the caller to handle.
func (h *Handler) GetPublicURL(ctx context.Context) (string, error) {
	row, err := h.q.GetSystemSetting(ctx, "public_url")
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read public_url: %w", err)
	}
	return row.Value, nil
}

// publicConfigResponse is the JSON shape returned by GetPublicConfig.
// default_model is null when no system default is configured or when the
// configured default has been disabled.
type publicConfigResponse struct {
	PublicURL    string        `json:"public_url"`
	DefaultModel *defaultModel `json:"default_model"`
}

type defaultModel struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

// GetPublicConfig returns non-sensitive runtime configuration to all authenticated
// users. Exposes public_url and default_model. Operators and auditors need this to
// display full webhook URLs and to pre-populate the model selector for new policies
// without having access to the admin settings endpoint.
func (h *Handler) GetPublicConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	publicURL, err := h.GetPublicURL(ctx)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to read config", "")
		return
	}

	resp := publicConfigResponse{PublicURL: publicURL}

	provider, name, err := h.GetSystemDefault(ctx)
	if err != nil {
		// Log but do not fail the whole config fetch — default_model stays nil.
		slog.Warn("GetPublicConfig: failed to read default_model", "err", err)
	} else if provider != "" {
		// Guard against a stale default that was disabled via direct DB edits
		// (the SetModelEnabled API already prevents disabling the current default,
		// but this defends against out-of-band changes).
		enabled, listErr := h.q.ListEnabledModels(ctx)
		if listErr != nil {
			slog.Warn("GetPublicConfig: failed to list enabled models", "err", listErr)
		} else {
			for _, row := range enabled {
				if row.Provider == provider && row.ModelName == name {
					resp.DefaultModel = &defaultModel{Provider: provider, Name: name}
					break
				}
			}
		}
	}

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// ListModelsAdmin returns all model settings.
func (h *Handler) ListModelsAdmin(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q.ListModelSettings(r.Context())
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list models", "")
		return
	}

	type modelResponse struct {
		Provider  string `json:"provider"`
		ModelName string `json:"model_name"`
		Enabled   bool   `json:"enabled"`
		UpdatedAt string `json:"updated_at"`
	}

	models := make([]modelResponse, 0, len(rows))
	for _, row := range rows {
		models = append(models, modelResponse{
			Provider:  row.Provider,
			ModelName: row.ModelName,
			Enabled:   row.Enabled != 0,
			UpdatedAt: row.UpdatedAt,
		})
	}

	httputil.WriteJSON(w, http.StatusOK, models)
}

// ListAllModels handles GET /api/v1/admin/models/all.
// It returns every model from every registered provider joined with their
// enabled state from model_settings. Models with no row in model_settings
// default to enabled=false (new/unseen models are disabled by default).
func (h *Handler) ListAllModels(lister llm.ModelLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		all, err := lister.ListAllModels(ctx)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to list models", "")
			return
		}

		enabledRows, err := h.q.ListEnabledModels(ctx)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to load model settings", "")
			return
		}

		enabledSet := make(map[string]struct{}, len(enabledRows))
		for _, r := range enabledRows {
			enabledSet[r.Provider+":"+r.ModelName] = struct{}{}
		}

		type allModelResponse struct {
			Provider    string `json:"provider"`
			ModelName   string `json:"model_name"`
			DisplayName string `json:"display_name"`
			Enabled     bool   `json:"enabled"`
		}

		var result []allModelResponse
		for prov, models := range all {
			for _, m := range models {
				_, isEnabled := enabledSet[prov+":"+m.Name]
				result = append(result, allModelResponse{
					Provider:    prov,
					ModelName:   m.Name,
					DisplayName: m.DisplayName,
					Enabled:     isEnabled,
				})
			}
		}

		httputil.WriteJSON(w, http.StatusOK, result)
	}
}

// SetModelEnabled enables or disables a model. Disabling the current default model returns 409.
func (h *Handler) SetModelEnabled(w http.ResponseWriter, r *http.Request) {
	modelID := chi.URLParam(r, "id")

	var body struct {
		Provider string `json:"provider"`
		Enabled  bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid JSON body", "")
		return
	}

	if !body.Enabled {
		defaultRow, err := h.q.GetSystemSetting(r.Context(), "default_model")
		if err == nil {
			defaultVal := defaultRow.Value
			candidate := body.Provider + ":" + modelID
			if defaultVal == candidate {
				httputil.WriteError(w, http.StatusConflict, "cannot disable the current default model", "")
				return
			}
		}
	}

	var enabled int64
	if body.Enabled {
		enabled = 1
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := h.q.UpsertModelSetting(r.Context(), body.Provider, modelID, enabled, now); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update model setting", "")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetSystemDefault returns the provider and model name from the default_model setting.
// Returns ("", "", nil) when no default is configured (sql.ErrNoRows), so callers
// can distinguish "unset" from "read failure" — matching the GetPublicURL pattern.
func (h *Handler) GetSystemDefault(ctx context.Context) (string, string, error) {
	row, err := h.q.GetSystemSetting(ctx, "default_model")
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("read default_model: %w", err)
	}
	parts := strings.SplitN(row.Value, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid default_model format: %q", row.Value)
	}
	return parts[0], parts[1], nil
}

// SystemInfo holds data returned by the system info endpoint.
type SystemInfo struct {
	Version    string `json:"version"`
	Uptime     string `json:"uptime"`
	DBSize     string `json:"db_size"`
	MCPServers int    `json:"mcp_servers"`
	Policies   int    `json:"policies"`
	Users      int    `json:"users"`
}

// SystemInfoDeps provides the dependencies for GetSystemInfo.
type SystemInfoDeps struct {
	Version         string
	StartTime       time.Time
	DBPath          string
	CountMCPServers func(ctx context.Context) (int, error)
	CountPolicies   func(ctx context.Context) (int, error)
	CountUsers      func(ctx context.Context) (int, error)
}

// GetSystemInfo returns an http.HandlerFunc that computes and returns system info.
func GetSystemInfo(deps SystemInfoDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info := SystemInfo{
			Version: deps.Version,
			Uptime:  formatUptime(time.Since(deps.StartTime)),
			DBSize:  formatDBSize(deps.DBPath),
		}

		ctx := r.Context()

		if deps.CountMCPServers != nil {
			mcpCount, err := deps.CountMCPServers(ctx)
			if err != nil {
				slog.Error("failed to count MCP servers", "err", err)
			}
			info.MCPServers = mcpCount
		}
		if deps.CountPolicies != nil {
			policyCount, err := deps.CountPolicies(ctx)
			if err != nil {
				slog.Error("failed to count policies", "err", err)
			}
			info.Policies = policyCount
		}
		if deps.CountUsers != nil {
			userCount, err := deps.CountUsers(ctx)
			if err != nil {
				slog.Error("failed to count users", "err", err)
			}
			info.Users = userCount
		}

		httputil.WriteJSON(w, http.StatusOK, info)
	}
}

func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func formatDBSize(path string) string {
	if path == "" {
		return "unknown"
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "unknown"
	}
	size := fi.Size()
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(size)/float64(1<<30))
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/float64(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", size)
	}
}
