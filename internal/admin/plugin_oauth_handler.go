package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/plugin/oauth"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// OAuthPluginQuerier is the narrow DB interface required by PluginOAuthHandler
// to look up instance details needed for strategy validation.
type OAuthPluginQuerier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	GetPluginByID(ctx context.Context, id string) (db.Plugin, error)
}

// PluginOAuthHandler handles the admin OAuth2 endpoints for plugin instances.
// It owns two routes:
//
//	POST /api/v1/admin/plugins/{id}/instances/{iid}/oauth/begin
//	GET  /api/v1/admin/plugins/oauth/callback  (unprotected)
type PluginOAuthHandler struct {
	mgr *oauth.Manager
	q   OAuthPluginQuerier
}

// NewPluginOAuthHandler constructs a PluginOAuthHandler.
func NewPluginOAuthHandler(q OAuthPluginQuerier, mgr *oauth.Manager) *PluginOAuthHandler {
	return &PluginOAuthHandler{mgr: mgr, q: q}
}

// beginRequest is the JSON body for POST .../oauth/begin.
type beginRequest struct {
	ReturnURL string `json:"return_url"`
}

// beginAuthcodeResponse is returned for authcode strategies.
type beginAuthcodeResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

// beginClientcredResponse is returned for clientcred strategies.
type beginClientcredResponse struct {
	Status string `json:"status"`
}

// Begin handles POST /api/v1/admin/plugins/{id}/instances/{iid}/oauth/begin.
// For authcode: returns {"authorize_url":"..."} — the admin UI opens that URL.
// For clientcred: performs the token exchange synchronously; returns {"status":"ok"}.
// Returns 400 for non-OAuth strategies or missing required fields.
func (h *PluginOAuthHandler) Begin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	var req beginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	// Validate the instance belongs to the plugin (mirrors plugin_handler.go pattern).
	inst, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get instance", "")
		return
	}
	if inst.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}

	// Load the manifest to determine the auth strategy.
	plugin, err := h.q.GetPluginByID(ctx, pluginID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		return
	}

	var m sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &m); parseErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return
	}

	switch m.Auth.Strategy {
	case sdkmanifest.AuthStrategyOAuth2Authcode:
		if req.ReturnURL == "" {
			httputil.WriteError(w, http.StatusBadRequest, "return_url is required for oauth2_authcode", "")
			return
		}
		authorizeURL, err := h.mgr.BeginAuthcode(ctx, instanceID, req.ReturnURL)
		if err != nil {
			slog.ErrorContext(ctx, "oauth begin authcode failed", "instance_id", instanceID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "failed to begin OAuth flow", err.Error())
			return
		}
		httputil.WriteJSON(w, http.StatusOK, beginAuthcodeResponse{AuthorizeURL: authorizeURL})

	case sdkmanifest.AuthStrategyOAuth2Clientcred:
		if err := h.mgr.BeginClientcred(ctx, instanceID); err != nil {
			slog.ErrorContext(ctx, "oauth begin clientcred failed", "instance_id", instanceID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "client credentials exchange failed", err.Error())
			return
		}
		httputil.WriteJSON(w, http.StatusOK, beginClientcredResponse{Status: "ok"})

	default:
		httputil.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("instance auth strategy %q is not an OAuth2 strategy", m.Auth.Strategy), "")
	}
}

// Callback handles GET /api/v1/admin/plugins/oauth/callback.
// This endpoint is intentionally unprotected at the session layer — the browser
// arrives here from the OAuth provider carrying no Gleipnir session cookie. The
// HMAC-signed state envelope provides CSRF protection and integrity (spec §9.2;
// mirrors ADR-034 webhook pattern).
//
// On success it redirects to state.ReturnURL with ?oauth_ok=1.
// On failure it redirects to state.ReturnURL (if decodeable) with ?oauth_error=...
// or returns 400 when the state is undecodable/tampered.
func (h *PluginOAuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	rawState := q.Get("state")
	code := q.Get("code")
	oauthErr := q.Get("error")

	if rawState == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing state parameter", "")
		return
	}

	if oauthErr != "" {
		// The provider denied the request. Decode state for the ReturnURL only.
		env, decodeErr := h.mgr.DecodeStateForRedirect(rawState)
		if decodeErr != nil {
			slog.WarnContext(ctx, "oauth callback: tampered state on provider error", "err", decodeErr)
			httputil.WriteError(w, http.StatusBadRequest, "invalid state", decodeErr.Error())
			return
		}
		redirectWithError(w, r, env.ReturnURL, oauthErr)
		return
	}

	if code == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing code parameter", "")
		return
	}

	returnURL, err := h.mgr.HandleCallback(ctx, rawState, code)
	if err != nil {
		slog.ErrorContext(ctx, "oauth callback: handle failed", "err", err)
		// Try to extract ReturnURL from state for best-effort redirect.
		env, decodeErr := h.mgr.DecodeStateForRedirect(rawState)
		if decodeErr != nil || env.ReturnURL == "" {
			httputil.WriteError(w, http.StatusBadRequest, "oauth callback failed", err.Error())
			return
		}
		redirectWithError(w, r, env.ReturnURL, err.Error())
		return
	}

	target := returnURL
	if target == "" {
		target = "/"
	}
	u, parseErr := url.Parse(target)
	if parseErr != nil {
		http.Redirect(w, r, "/?oauth_ok=1", http.StatusFound)
		return
	}
	params := u.Query()
	params.Set("oauth_ok", "1")
	u.RawQuery = params.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// redirectWithError redirects the browser to returnURL with an oauth_error
// query parameter. If returnURL is empty or unparseable, it falls back to a
// plain 400 error response.
func redirectWithError(w http.ResponseWriter, r *http.Request, returnURL, errMsg string) {
	if returnURL == "" {
		httputil.WriteError(w, http.StatusBadRequest, "oauth error", errMsg)
		return
	}
	u, err := url.Parse(returnURL)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "oauth error", errMsg)
		return
	}
	params := u.Query()
	params.Set("oauth_error", errMsg)
	u.RawQuery = params.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}
