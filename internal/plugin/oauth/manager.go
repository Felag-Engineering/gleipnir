package oauth

import (
	"context"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

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
// Returns an error when public_url is not configured (operators must set it
// before starting any OAuth flow) or when the instance has no OAuth credentials.
func (m *Manager) BeginAuthcode(ctx context.Context, instanceID, returnURL string) (string, error) {
	publicURL := m.getPublicURL()
	if publicURL == "" {
		return "", fmt.Errorf("oauth begin: public_url is not configured; set it in admin settings before starting an OAuth flow")
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
		return "", fmt.Errorf("oauth begin: %w", err)
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
		return "", fmt.Errorf("oauth callback: exchange code: %w", err)
	}

	if saveErr := m.store.SaveToken(ctx, env.InstanceID, tok); saveErr != nil {
		return "", fmt.Errorf("oauth callback: save token: %w", saveErr)
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

	m.store.EmitIssued(ctx, instanceID)
	return nil
}
