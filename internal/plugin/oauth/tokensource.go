package oauth

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// OAuthConfig builds an *oauth2.Config from StoredCredentials and the
// callback URL registered with the OAuth provider. callbackURL is the full URL
// (e.g. https://my-gleipnir.example.com/api/v1/admin/plugins/oauth/callback).
// Returns an error when the stored credentials are missing required fields.
func OAuthConfig(stored StoredCredentials, callbackURL string) (*oauth2.Config, error) {
	if stored.ClientID == "" {
		return nil, fmt.Errorf("oauth2_authcode config: client_id is empty")
	}
	if stored.TokenURL == "" {
		return nil, fmt.Errorf("oauth2_authcode config: token_url is empty")
	}
	return &oauth2.Config{
		ClientID:     stored.ClientID,
		ClientSecret: stored.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  stored.AuthorizationURL,
			TokenURL: stored.TokenURL,
		},
		RedirectURL: callbackURL,
		Scopes:      stored.Scopes,
	}, nil
}

// ClientCredConfig builds a *clientcredentials.Config from StoredCredentials.
func ClientCredConfig(stored StoredCredentials) *clientcredentials.Config {
	return &clientcredentials.Config{
		ClientID:     stored.ClientID,
		ClientSecret: stored.ClientSecret,
		TokenURL:     stored.TokenURL,
		Scopes:       stored.Scopes,
	}
}

// NewTokenSource returns an oauth2.TokenSource that wraps the upstream
// x/oauth2 token source with a persisting decorator: whenever the upstream
// source produces a token that differs from the last seen token, SaveToken is
// called to persist it. Refresh failures call MarkRefreshFailed and return the
// error to the caller.
//
// callbackURL is only needed for the authcode strategy (it must match the URL
// registered with the provider). For clientcred it is ignored.
func NewTokenSource(ctx context.Context, store *DBStore, instanceID, callbackURL string, clock func() time.Time) oauth2.TokenSource {
	return &persistingTokenSource{
		ctx:         ctx,
		store:       store,
		instanceID:  instanceID,
		callbackURL: callbackURL,
		clock:       clock,
	}
}

// persistingTokenSource wraps an inner TokenSource (built lazily on first
// .Token() call) and persists every new token it observes.
type persistingTokenSource struct {
	ctx         context.Context
	store       *DBStore
	instanceID  string
	callbackURL string
	clock       func() time.Time

	// lastSeen is the token returned by the previous .Token() call. We use it to
	// detect rotation: when the inner source returns a different AccessToken or
	// Expiry, that's a refresh and we emit plugin_oauth_refreshed.
	lastSeen *oauth2.Token

	// inner is initialised lazily on the first Token() call so we can load the
	// latest stored credentials (including any token already in the DB) without
	// needing them at construction time.
	inner oauth2.TokenSource
}

// Token implements oauth2.TokenSource. On the first call it loads credentials
// from the store and builds the inner source. Subsequent calls delegate to the
// (possibly cached) inner source. If the inner source returns a fresher token
// it is persisted via SaveToken; errors are forwarded to MarkRefreshFailed.
func (ts *persistingTokenSource) Token() (*oauth2.Token, error) {
	if ts.inner == nil {
		if err := ts.init(); err != nil {
			return nil, err
		}
	}

	tok, err := ts.inner.Token()
	if err != nil {
		if markErr := ts.store.MarkRefreshFailed(ts.ctx, ts.instanceID, err); markErr != nil {
			// Log but return the original error so the caller sees it.
			_ = markErr
		}
		return nil, fmt.Errorf("token source: %w", err)
	}

	// Persist + audit only when the inner source returned a token that differs
	// from the last one we saw. The upstream ReuseTokenSource hands back the
	// cached token unchanged until refresh is needed, so this check separates
	// "cache hit" from "actual refresh" — the audit log should reflect the latter.
	if tok != nil && tokenChanged(ts.lastSeen, tok) {
		if saveErr := ts.store.SaveToken(ts.ctx, ts.instanceID, tok); saveErr != nil {
			// Non-fatal: the caller can still use the token; persistence will be
			// retried by the background scanner.
			_ = saveErr
		} else {
			ts.store.EmitRefreshed(ts.ctx, ts.instanceID)
		}
		ts.lastSeen = tok
	}
	return tok, nil
}

// tokenChanged returns true when `next` is materially different from `prev`
// — different AccessToken or different Expiry. A nil prev counts as "changed"
// when next is non-nil, since the very first call has nothing to compare against.
func tokenChanged(prev, next *oauth2.Token) bool {
	if prev == nil {
		return next != nil
	}
	if next == nil {
		return false
	}
	return prev.AccessToken != next.AccessToken || !prev.Expiry.Equal(next.Expiry)
}

// init loads the stored credentials and constructs the appropriate upstream
// token source. It acquires the per-instance lock so concurrent .Token() calls
// on a cold source serialise here rather than each building their own inner source.
func (ts *persistingTokenSource) init() error {
	mu := ts.store.locks.Get(ts.instanceID)
	mu.Lock()
	defer mu.Unlock()

	// Double-check: another goroutine may have won the lock and set ts.inner.
	if ts.inner != nil {
		return nil
	}

	creds, _, err := ts.store.LoadCredentials(ts.ctx, ts.instanceID)
	if err != nil {
		return fmt.Errorf("token source init: %w", err)
	}

	switch creds.Strategy {
	case sdkmanifest.AuthStrategyOAuth2Authcode:
		cfg, err := OAuthConfig(creds, ts.callbackURL)
		if err != nil {
			return fmt.Errorf("token source init: %w", err)
		}
		// ReuseTokenSource caches the token and only calls the underlying source
		// when Valid() returns false — i.e. it does not refresh more than needed.
		ts.inner = cfg.TokenSource(ts.ctx, creds.Token)

	case sdkmanifest.AuthStrategyOAuth2Clientcred:
		cfg := ClientCredConfig(creds)
		ts.inner = cfg.TokenSource(ts.ctx)

	default:
		return fmt.Errorf("token source init: unsupported strategy %q", creds.Strategy)
	}

	// Seed lastSeen with the token already on disk so the first .Token() call
	// only emits a refresh event when the upstream source actually rotated.
	ts.lastSeen = creds.Token
	return nil
}
