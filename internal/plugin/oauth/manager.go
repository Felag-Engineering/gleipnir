package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/oauth2"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// ErrConfigInvalid is returned by BeginAuthcode when operator configuration is
// incomplete (e.g. public_url not set, client_id or token_url missing). The
// admin handler maps this sentinel to HTTP 400 so the UI can surface the
// message directly without the operator needing to tail server logs.
var ErrConfigInvalid = errors.New("oauth: operator configuration invalid")

// ProviderExchangeError is returned by HandleCallback when the OAuth2 provider
// rejects the token exchange with a structured error response. It carries the
// provider's machine-readable error code (e.g. "invalid_code",
// "redirect_uri_mismatch", "invalid_client") separately from the human-readable
// description so callers can surface actionable messages without parsing blobs.
//
// The Raw field contains the full error string from golang.org/x/oauth2 for
// logging; it is not shown in UI redirects to avoid leaking internal details.
type ProviderExchangeError struct {
	// Code is the OAuth2 error code from the provider's JSON response
	// (e.g. "invalid_code", "redirect_uri_mismatch", "invalid_client").
	Code string
	// Description is the human-readable error_description from the provider.
	// May be empty when the provider omits the field.
	Description string
	// Raw is the full error string from golang.org/x/oauth2.RetrieveError.
	// Logged at error level; never included in redirect URLs.
	Raw string
}

func (e *ProviderExchangeError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("oauth provider error: %s: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("oauth provider error: %s", e.Code)
}

// Manager orchestrates the host-side OAuth2 flows. It handles:
//   - BeginAuthcode: builds the authorization URL and encodes a signed state
//   - HandleCallback: exchanges the authorization code for tokens and persists them
//   - BeginClientcred: performs the client credentials exchange synchronously
type Manager struct {
	store        *DBStore
	nonces       NonceStore
	clock        func() time.Time
	hmacKey      []byte
	getPublicURL func() string
}

// NewManager constructs a Manager. getPublicURL is called at begin-time (not
// construction time) so it can reflect late-bound system settings.
func NewManager(store *DBStore, nonces NonceStore, clock func() time.Time, hmacKey []byte, getPublicURL func() string) *Manager {
	if clock == nil {
		clock = time.Now
	}
	return &Manager{
		store:        store,
		nonces:       nonces,
		clock:        clock,
		hmacKey:      hmacKey,
		getPublicURL: getPublicURL,
	}
}

// callbackPath is the fixed path component of the OAuth callback URL.
const callbackPath = "/api/v1/admin/plugins/oauth/callback"

// BeginAuthcode starts the OAuth2 authorization code flow for instanceID.
// It builds the authorization URL, encodes a signed state envelope, records the
// nonce, and writes the last_oauth_callback_url to the instance row.
//
// returnURL is where the callback handler redirects the browser after completing
// the dance (typically the admin UI instance page). It is embedded in the
// signed state envelope so the callback handler can redirect without trusting
// the URL from the browser.
//
// Returns ErrConfigInvalid when public_url is not configured (operators must
// set it in admin settings before starting any OAuth flow) or when required
// OAuth fields (client_id, token_url) are missing from the stored credentials.
func (m *Manager) BeginAuthcode(ctx context.Context, instanceID, returnURL string) (string, error) {
	publicURL := m.getPublicURL()
	if publicURL == "" {
		return "", fmt.Errorf("%w: oauth begin: public_url is not configured; set it in admin settings before starting an OAuth flow", ErrConfigInvalid)
	}

	creds, ver, err := m.store.LoadCredentials(ctx, instanceID)
	if err != nil {
		return "", fmt.Errorf("oauth begin: load credentials: %w", err)
	}
	if creds.Strategy != sdkmanifest.AuthStrategyOAuth2Authcode {
		return "", fmt.Errorf("oauth begin: instance strategy is %q, want %q", creds.Strategy, sdkmanifest.AuthStrategyOAuth2Authcode)
	}

	callbackURL := publicURL + callbackPath

	cfg, err := OAuthConfig(creds, callbackURL)
	if err != nil {
		// OAuthConfig returns an error when client_id or token_url is missing —
		// both are operator config mistakes, not server faults.
		return "", fmt.Errorf("%w: oauth begin: %w", ErrConfigInvalid, err)
	}

	env, nonce, err := NewStateEnvelope(instanceID, returnURL, m.clock)
	if err != nil {
		return "", fmt.Errorf("oauth begin: %w", err)
	}

	encodedState, err := EncodeState(env, m.hmacKey)
	if err != nil {
		return "", fmt.Errorf("oauth begin: encode state: %w", err)
	}

	if err := m.nonces.Record(ctx, nonce, instanceID); err != nil {
		return "", fmt.Errorf("oauth begin: record nonce: %w", err)
	}

	// Record the callback URL on the instance row so operators can detect when
	// public_url changes mid-dance (provider would reject the mismatched redirect).
	nowStr := m.clock().UTC().Format(time.RFC3339Nano)
	_, _ = m.store.q.UpdatePluginInstanceOAuthCallback(ctx, db.UpdatePluginInstanceOAuthCallbackParams{
		LastOauthCallbackUrl: &callbackURL,
		UpdatedAt:            nowStr,
		ID:                   instanceID,
		ExpectedVersion:      ver,
	})
	// Non-fatal: the callback URL is informational; the flow continues regardless.

	authorizeURL := cfg.AuthCodeURL(encodedState)
	return authorizeURL, nil
}

// HandleCallback processes the OAuth2 authorization code callback. It decodes
// and verifies the state envelope, consumes the nonce (rejecting replays),
// exchanges the code for tokens, persists them, and emits a plugin_oauth_issued
// audit event.
//
// Returns (returnURL, nil) on success so the HTTP handler can redirect the browser.
func (m *Manager) HandleCallback(ctx context.Context, rawState, code string) (string, error) {
	env, err := DecodeState(rawState, m.hmacKey, m.clock)
	if err != nil {
		return "", fmt.Errorf("oauth callback: %w", err)
	}

	ok, err := m.nonces.Consume(ctx, env.Nonce)
	if err != nil {
		return "", fmt.Errorf("oauth callback: consume nonce: %w", err)
	}
	if !ok {
		return "", ErrNonceUsed
	}

	publicURL := m.getPublicURL()
	if publicURL == "" {
		return "", fmt.Errorf("oauth callback: public_url not configured")
	}
	callbackURL := publicURL + callbackPath

	creds, _, err := m.store.LoadCredentials(ctx, env.InstanceID)
	if err != nil {
		return "", fmt.Errorf("oauth callback: load credentials: %w", err)
	}

	cfg, err := OAuthConfig(creds, callbackURL)
	if err != nil {
		return "", fmt.Errorf("oauth callback: %w", err)
	}

	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		// Type-assert *oauth2.RetrieveError so we can surface the structured
		// error_code and error_description fields from the provider's JSON response
		// separately from the opaque blob produced by err.Error(). This lets the
		// HTTP handler redirect operators with a readable "invalid_code" rather than
		// a long "oauth2: cannot fetch token: 200 OK / Response: {...}" blob.
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return "", &ProviderExchangeError{
				Code:        re.ErrorCode,
				Description: re.ErrorDescription,
				Raw:         err.Error(),
			}
		}
		return "", fmt.Errorf("oauth callback: exchange code: %w", err)
	}

	if saveErr := m.store.SaveToken(ctx, env.InstanceID, tok); saveErr != nil {
		return "", fmt.Errorf("oauth callback: save token: %w", saveErr)
	}

	// Record last_oauth_callback_url on successful token exchange so future
	// public_url change rescans (#230) know which URL this instance last used.
	// We re-read the instance to get the current version for the CAS guard;
	// a conflict or missing row is non-fatal — the token has already been saved.
	if freshRow, getErr := m.store.q.GetPluginInstanceByID(ctx, env.InstanceID); getErr == nil {
		nowStr := m.clock().UTC().Format(time.RFC3339Nano)
		if n, writeErr := m.store.q.UpdatePluginInstanceOAuthCallback(ctx, db.UpdatePluginInstanceOAuthCallbackParams{
			LastOauthCallbackUrl: &callbackURL,
			UpdatedAt:            nowStr,
			ID:                   env.InstanceID,
			ExpectedVersion:      freshRow.Version,
		}); writeErr != nil || n == 0 {
			slog.WarnContext(ctx, "oauth: record callback url", "instance_id", env.InstanceID, "written", n, "err", writeErr)
		}
	}

	m.store.EmitIssued(ctx, env.InstanceID)

	// Transition the instance to healthy if it was waiting for authorization.
	// ErrIllegalTransition is silently ignored — the instance may already be healthy.
	_ = pluginstate.SetHealthState(
		ctx, m.store.health, nil, env.InstanceID,
		pluginstate.OriginHost,
		model.PluginHealthStateHealthy,
		"oauth authorization completed",
	)

	return env.ReturnURL, nil
}

// DecodeStateForRedirect decodes the state envelope without consuming the nonce
// or verifying timing. It is used by the callback handler to extract ReturnURL
// for error redirects when the full HandleCallback path is not taken. Only the
// HMAC signature is verified — expired or replayed states may still decode
// successfully here, which is intentional (we need ReturnURL to redirect errors).
func (m *Manager) DecodeStateForRedirect(rawState string) (StateEnvelope, error) {
	// We decode ignoring expiry by using a far-future clock.
	env, err := DecodeState(rawState, m.hmacKey, func() time.Time {
		// Always-valid clock: returns year 1 so ExpiresAt is always in the future.
		return time.Time{}
	})
	return env, err
}

// BeginClientcred performs the OAuth2 client credentials grant synchronously.
// It fetches a token, persists it, and emits a plugin_oauth_issued audit event.
// This is called from the admin Begin endpoint when the instance strategy is
// oauth2_clientcred — no browser interaction is needed.
func (m *Manager) BeginClientcred(ctx context.Context, instanceID string) error {
	creds, _, err := m.store.LoadCredentials(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("oauth clientcred: load credentials: %w", err)
	}
	if creds.Strategy != sdkmanifest.AuthStrategyOAuth2Clientcred {
		return fmt.Errorf("oauth clientcred: instance strategy is %q, want %q", creds.Strategy, sdkmanifest.AuthStrategyOAuth2Clientcred)
	}

	cfg := ClientCredConfig(creds)
	tok, err := cfg.Token(ctx)
	if err != nil {
		return fmt.Errorf("oauth clientcred: token exchange: %w", err)
	}

	if saveErr := m.store.SaveToken(ctx, instanceID, tok); saveErr != nil {
		return fmt.Errorf("oauth clientcred: save token: %w", saveErr)
	}

	// Record last_oauth_callback_url so public_url change rescans (#230) can
	// track this instance. Client credentials do not use a browser redirect, but
	// we store the value consistently so the rescan logic works uniformly across
	// both OAuth strategies. Skip silently when public_url is not yet configured.
	if publicURL := m.getPublicURL(); publicURL != "" {
		callbackURL := publicURL + callbackPath
		if freshRow, getErr := m.store.q.GetPluginInstanceByID(ctx, instanceID); getErr == nil {
			nowStr := m.clock().UTC().Format(time.RFC3339Nano)
			if n, writeErr := m.store.q.UpdatePluginInstanceOAuthCallback(ctx, db.UpdatePluginInstanceOAuthCallbackParams{
				LastOauthCallbackUrl: &callbackURL,
				UpdatedAt:            nowStr,
				ID:                   instanceID,
				ExpectedVersion:      freshRow.Version,
			}); writeErr != nil || n == 0 {
				slog.WarnContext(ctx, "oauth: record callback url", "instance_id", instanceID, "written", n, "err", writeErr)
			}
		}
	}

	m.store.EmitIssued(ctx, instanceID)

	// Transition the instance to healthy if it was waiting for re-authorization.
	// Mirrors HandleCallback: both ErrIllegalTransition (already healthy) and
	// ErrTransitionConflict (concurrent writer) are benign outcomes here.
	_ = pluginstate.SetHealthState(
		ctx, m.store.health, nil, instanceID,
		pluginstate.OriginHost,
		model.PluginHealthStateHealthy,
		"oauth client credentials issued",
	)

	return nil
}
