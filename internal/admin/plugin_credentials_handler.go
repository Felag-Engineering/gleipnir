package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/infra/headervalidate"
	"github.com/felag-engineering/gleipnir/internal/plugin/oauth"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// PluginCredentialsHandler manages write-only credential endpoints for the
// non-OAuth plugin auth strategies: static_api_key, header_set, basic_auth,
// and none. All routes are admin-only; that constraint is enforced by the
// enclosing router group in router.go.
//
// GET returns a redacted view (key names and presence flags only).
// PUT overwrites (rotation).
// DELETE wipes the credential sub-blob while preserving Strategy.
//
// The pattern mirrors ADR-039 (MCP auth headers) and ADR-034 (webhook
// secrets): secrets are write-only over the API.
type PluginCredentialsHandler struct {
	// q is used to look up instance details (belongs-to-plugin check and
	// manifest snapshot for strategy validation). OAuthPluginQuerier is
	// declared in plugin_oauth_handler.go — same package, no redeclaration.
	q         OAuthPluginQuerier
	store     *oauth.DBStore
	publisher event.Publisher
}

// NewPluginCredentialsHandler constructs a PluginCredentialsHandler.
// pub is used to broadcast plugin.health_changed events after a credential write
// advances the instance readiness detail (config_missing → credentials_missing → "").
// Pass nil when health events are not needed (e.g. tests that don't assert on them).
func NewPluginCredentialsHandler(q OAuthPluginQuerier, store *oauth.DBStore, pub event.Publisher) *PluginCredentialsHandler {
	return &PluginCredentialsHandler{q: q, store: store, publisher: pub}
}

// Get handles GET /api/v1/admin/plugins/{id}/instances/{iid}/credentials.
// Returns a redacted view of the stored credentials (key names and presence
// flags; never secret values).
func (h *PluginCredentialsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, instanceID, ok := h.resolveInstance(w, r)
	if !ok {
		return
	}

	creds, _, err := h.store.LoadCredentials(ctx, instanceID)
	if err != nil {
		slog.ErrorContext(ctx, "credentials: load failed", "instance_id", instanceID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to load credentials", "")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, creds.Redact())
}

// Delete handles DELETE /api/v1/admin/plugins/{id}/instances/{iid}/credentials.
// Wipes the secret sub-blob for the instance while preserving the Strategy.
// Returns 204 on success.
func (h *PluginCredentialsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, instanceID, ok := h.resolveInstance(w, r)
	if !ok {
		return
	}

	if err := h.store.ClearCredentials(ctx, instanceID); err != nil {
		slog.ErrorContext(ctx, "credentials: clear failed", "instance_id", instanceID, "err", err)
		if errors.Is(err, oauth.ErrCASConflict) {
			httputil.WriteError(w, http.StatusConflict, "concurrent write conflict, please retry", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to clear credentials", "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// setStaticAPIKeyRequest is the JSON body for PUT .../credentials/static-api-key.
type setStaticAPIKeyRequest struct {
	HeaderName string `json:"header_name"`
	Scheme     string `json:"scheme"`
	APIKey     string `json:"api_key"`
}

// SetStaticAPIKey handles PUT .../credentials/static-api-key.
// Validates the header name, then overwrites the static_api_key sub-blob.
// Returns 400 when the header name is invalid or the instance uses a different strategy.
func (h *PluginCredentialsHandler) SetStaticAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID, instanceID, ok := h.resolveInstance(w, r)
	if !ok {
		return
	}

	var req setStaticAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	if err := headervalidate.ValidateName(req.HeaderName); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	if req.APIKey == "" {
		httputil.WriteError(w, http.StatusBadRequest, "api_key must not be empty", "")
		return
	}

	if _, ok := h.requireOneOfStrategies(w, r, pluginID, sdkmanifest.AuthStrategyStaticAPIKey); !ok {
		return
	}

	if err := h.store.SetStaticAPIKey(ctx, instanceID, req.HeaderName, req.Scheme, req.APIKey); err != nil {
		h.handleStoreError(w, r, err, instanceID, "set static api key")
		return
	}

	h.advanceReadiness(ctx, pluginID, instanceID)
	w.WriteHeader(http.StatusNoContent)
}

// setHeaderRequest is the JSON body for PUT .../credentials/headers/{name}.
type setHeaderRequest struct {
	Value string `json:"value"`
}

// SetHeader handles PUT .../credentials/headers/{name}.
// Validates the header name and adds or replaces the named header in the
// header_set sub-blob. Mirrors MCPHandler.SetAuthHeader.
func (h *PluginCredentialsHandler) SetHeader(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID, instanceID, ok := h.resolveInstance(w, r)
	if !ok {
		return
	}

	name := chi.URLParam(r, "name")
	if err := headervalidate.ValidateName(name); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	var req setHeaderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	if _, ok := h.requireOneOfStrategies(w, r, pluginID, sdkmanifest.AuthStrategyHeaderSet); !ok {
		return
	}

	if err := h.store.SetHeaderSetEntry(ctx, instanceID, oauth.NamedHeader{Name: name, Value: req.Value}); err != nil {
		h.handleStoreError(w, r, err, instanceID, "set header")
		return
	}

	h.advanceReadiness(ctx, pluginID, instanceID)
	w.WriteHeader(http.StatusNoContent)
}

// DeleteHeader handles DELETE .../credentials/headers/{name}.
// Removes the named header from the header_set sub-blob. Idempotent: no 404
// when the header is absent, mirroring MCPHandler.DeleteAuthHeader.
func (h *PluginCredentialsHandler) DeleteHeader(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID, instanceID, ok := h.resolveInstance(w, r)
	if !ok {
		return
	}

	name := chi.URLParam(r, "name")

	if _, ok := h.requireOneOfStrategies(w, r, pluginID, sdkmanifest.AuthStrategyHeaderSet); !ok {
		return
	}

	if err := h.store.DeleteHeaderSetEntry(ctx, instanceID, name); err != nil {
		h.handleStoreError(w, r, err, instanceID, "delete header")
		return
	}

	h.advanceReadiness(ctx, pluginID, instanceID)
	w.WriteHeader(http.StatusNoContent)
}

// setBasicAuthRequest is the JSON body for PUT .../credentials/basic-auth.
type setBasicAuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// SetBasicAuth handles PUT .../credentials/basic-auth.
// basic_auth is a stepping stone for legacy enterprise services; new plugins
// should prefer static_api_key or oauth2_authcode (see spec §9.1).
func (h *PluginCredentialsHandler) SetBasicAuth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID, instanceID, ok := h.resolveInstance(w, r)
	if !ok {
		return
	}

	var req setBasicAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	if req.Username == "" {
		httputil.WriteError(w, http.StatusBadRequest, "username must not be empty", "")
		return
	}
	if req.Password == "" {
		httputil.WriteError(w, http.StatusBadRequest, "password must not be empty", "")
		return
	}

	if _, ok := h.requireOneOfStrategies(w, r, pluginID, sdkmanifest.AuthStrategyBasicAuth); !ok {
		return
	}

	if err := h.store.SetBasicAuth(ctx, instanceID, req.Username, req.Password); err != nil {
		h.handleStoreError(w, r, err, instanceID, "set basic auth")
		return
	}

	h.advanceReadiness(ctx, pluginID, instanceID)
	w.WriteHeader(http.StatusNoContent)
}

// resolveInstance extracts pluginID and instanceID from the URL, loads the
// instance row, and verifies that the instance belongs to the given plugin.
// Returns (pluginID, instanceID, true) on success, writes an error response
// and returns ("", "", false) on failure.
func (h *PluginCredentialsHandler) resolveInstance(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	inst, err := h.q.GetPluginInstanceByID(r.Context(), instanceID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return "", "", false
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get instance", "")
		return "", "", false
	}
	if inst.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return "", "", false
	}
	return pluginID, instanceID, true
}

// requireOneOfStrategies loads the plugin manifest and checks whether the
// manifest's auth strategy is one of the allowed values. It returns (matched,
// true) on success, writes a 400 and returns ("", false) when the strategy
// does not match or the manifest cannot be parsed.
func (h *PluginCredentialsHandler) requireOneOfStrategies(w http.ResponseWriter, r *http.Request, pluginID string, strategies ...string) (string, bool) {
	plugin, err := h.q.GetPluginByID(r.Context(), pluginID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		return "", false
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		return "", false
	}

	var m sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &m); parseErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return "", false
	}

	for _, s := range strategies {
		if m.Auth.Strategy == s {
			return s, true
		}
	}
	httputil.WriteError(w, http.StatusBadRequest,
		fmt.Sprintf("instance auth strategy is %q, this endpoint requires one of: %v", m.Auth.Strategy, strategies), "")
	return "", false
}

// setOAuthClientRequest is the JSON body for PUT .../credentials/oauth-client.
type setOAuthClientRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// SetOAuthClient handles PUT .../credentials/oauth-client.
// Stores the OAuth2 client_id and client_secret an operator has copied from
// the provider's app config (e.g. Slack app Basic Information). This is the
// prerequisite for clicking "Authorize" — BeginAuthcode requires both values
// to be present in StoredCredentials.
func (h *PluginCredentialsHandler) SetOAuthClient(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID, instanceID, ok := h.resolveInstance(w, r)
	if !ok {
		return
	}

	var req setOAuthClientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}
	if req.ClientID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "client_id must not be empty", "")
		return
	}
	if req.ClientSecret == "" {
		httputil.WriteError(w, http.StatusBadRequest, "client_secret must not be empty", "")
		return
	}

	strategy, ok := h.requireOneOfStrategies(w, r, pluginID,
		sdkmanifest.AuthStrategyOAuth2Authcode,
		sdkmanifest.AuthStrategyOAuth2Clientcred,
	)
	if !ok {
		return
	}

	if err := h.store.SetOAuthClient(ctx, instanceID, strategy, req.ClientID, req.ClientSecret); err != nil {
		h.handleStoreError(w, r, err, instanceID, "set oauth client")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// setOAuthTokenRequest is the JSON body for PUT .../credentials/oauth-token.
type setOAuthTokenRequest struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"` // RFC3339
}

// SetOAuthToken handles PUT .../credentials/oauth-token.
// Directly seeds an OAuth access/refresh token for instances whose manifest
// declares oauth2_authcode or oauth2_clientcred strategy. This is the admin
// escape hatch for E2E tests and manual recovery; the canonical happy path
// remains the authcode UI flow.
func (h *PluginCredentialsHandler) SetOAuthToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID, instanceID, ok := h.resolveInstance(w, r)
	if !ok {
		return
	}

	var req setOAuthTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}
	if req.AccessToken == "" {
		httputil.WriteError(w, http.StatusBadRequest, "access_token must not be empty", "")
		return
	}

	var expiresAt time.Time
	if req.ExpiresAt != "" {
		var parseErr error
		expiresAt, parseErr = time.Parse(time.RFC3339, req.ExpiresAt)
		if parseErr != nil {
			httputil.WriteError(w, http.StatusBadRequest, "expires_at must be RFC3339", "")
			return
		}
	}

	strategy, ok := h.requireOneOfStrategies(w, r, pluginID,
		sdkmanifest.AuthStrategyOAuth2Authcode,
		sdkmanifest.AuthStrategyOAuth2Clientcred,
	)
	if !ok {
		return
	}

	tok := &oauth2.Token{
		AccessToken:  req.AccessToken,
		RefreshToken: req.RefreshToken,
	}
	if !expiresAt.IsZero() {
		tok.Expiry = expiresAt
	}

	if err := h.store.SeedOAuthToken(ctx, instanceID, strategy, tok); err != nil {
		h.handleStoreError(w, r, err, instanceID, "set oauth token")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleStoreError maps store errors to appropriate HTTP responses.
func (h *PluginCredentialsHandler) handleStoreError(w http.ResponseWriter, r *http.Request, err error, instanceID, op string) {
	slog.ErrorContext(r.Context(), "credentials: "+op+" failed", "instance_id", instanceID, "err", err)
	switch {
	case errors.Is(err, oauth.ErrWrongStrategy):
		httputil.WriteError(w, http.StatusConflict, "instance strategy does not match operation", "")
	case errors.Is(err, oauth.ErrCASConflict):
		httputil.WriteError(w, http.StatusConflict, "concurrent write conflict, please retry", "")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, "failed to "+op, "")
	}
}

// advanceReadiness re-fetches the instance row (which now reflects the
// just-written credentials_encrypted) and calls AdvanceInstanceReadiness to
// progress the health detail through credentials_missing → "". Best-effort:
// any error is logged and the caller still returns 204.
func (h *PluginCredentialsHandler) advanceReadiness(ctx context.Context, pluginID, instanceID string) {
	inst, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		slog.WarnContext(ctx, "credentials: advanceReadiness: failed to re-fetch instance",
			"instance_id", instanceID, "err", err)
		return
	}

	plugin, err := h.q.GetPluginByID(ctx, pluginID)
	if err != nil {
		slog.WarnContext(ctx, "credentials: advanceReadiness: failed to get plugin",
			"plugin_id", pluginID, "err", err)
		return
	}

	var manifest sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &manifest); parseErr != nil {
		slog.WarnContext(ctx, "credentials: advanceReadiness: corrupt manifest snapshot",
			"plugin_id", pluginID, "err", parseErr)
		return
	}

	AdvanceInstanceReadiness(ctx, h.q, h.publisher, inst, &manifest)
}
