// Package oauth implements host-side OAuth2 orchestration for plugin instances.
// Gleipnir owns the OAuth dance: it stores tokens encrypted in
// plugin_instances.credentials_encrypted, manages refresh via
// golang.org/x/oauth2, and wires refresh failures to the plugin health state
// machine (spec §9.1/§9.2).
package oauth

import (
	"encoding/json"
	"fmt"

	"golang.org/x/oauth2"

	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// StoredCredentials is the JSON blob written to
// plugin_instances.credentials_encrypted. It combines the OAuth2 flow
// parameters with the live token so callers can build an oauth2.Config and
// resume refreshes without any additional DB reads.
type StoredCredentials struct {
	// Strategy is the auth strategy declared in the manifest
	// (AuthStrategyOAuth2Authcode or AuthStrategyOAuth2Clientcred).
	Strategy string `json:"strategy"`

	// ClientID is the OAuth2 client identifier. Sourced from the manifest
	// defaults or overridden by per-instance config.
	ClientID string `json:"client_id"`

	// ClientSecret is the OAuth2 client secret. Write-only; never exposed
	// through any read API.
	ClientSecret string `json:"client_secret"`

	// AuthorizationURL is the provider's authorization endpoint. Only used for
	// the authcode flow.
	AuthorizationURL string `json:"authorization_url,omitempty"`

	// TokenURL is the provider's token endpoint. Required for both flows.
	TokenURL string `json:"token_url"`

	// Scopes is the set of OAuth2 scopes requested.
	Scopes []string `json:"scopes,omitempty"`

	// Token is the live oauth2.Token (access token + optional refresh token +
	// expiry). Nil before the first successful exchange.
	Token *oauth2.Token `json:"token,omitempty"`
}

// Marshal serialises StoredCredentials to a JSON string ready for encryption.
func (sc StoredCredentials) Marshal() (string, error) {
	b, err := json.Marshal(sc)
	if err != nil {
		return "", fmt.Errorf("oauth credentials: marshal: %w", err)
	}
	return string(b), nil
}

// UnmarshalCredentials deserialises the JSON blob produced by Marshal.
func UnmarshalCredentials(s string) (StoredCredentials, error) {
	var sc StoredCredentials
	if err := json.Unmarshal([]byte(s), &sc); err != nil {
		return StoredCredentials{}, fmt.Errorf("oauth credentials: unmarshal: %w", err)
	}
	return sc, nil
}

// InstanceConfigOverride holds per-instance values that can override the
// manifest-level OAuth2 defaults. A zero value means "no override" for each
// field. Typically sourced from the plugin's config_json.
type InstanceConfigOverride struct {
	ClientID         string
	ClientSecret     string
	AuthorizationURL string
	TokenURL         string
	Scopes           []string
}

// Resolve builds a StoredCredentials by overlaying per-instance overrides on
// top of the manifest's oauth_defaults. If a field is non-empty in override it
// wins; otherwise the manifest default is used.
//
// Resolve returns an error when the resulting credentials are missing mandatory
// fields (ClientID, ClientSecret, TokenURL).
func Resolve(authDecl sdkmanifest.AuthDecl, defaults *sdkmanifest.OAuthDefaultsDecl, override InstanceConfigOverride) (StoredCredentials, error) {
	sc := StoredCredentials{Strategy: authDecl.Strategy}

	if defaults != nil {
		sc.AuthorizationURL = defaults.AuthorizationURL
		sc.TokenURL = defaults.TokenURL
		sc.Scopes = append([]string(nil), defaults.Scopes...)
	}

	// Per-instance overrides win over manifest defaults.
	if override.ClientID != "" {
		sc.ClientID = override.ClientID
	}
	if override.ClientSecret != "" {
		sc.ClientSecret = override.ClientSecret
	}
	if override.AuthorizationURL != "" {
		sc.AuthorizationURL = override.AuthorizationURL
	}
	if override.TokenURL != "" {
		sc.TokenURL = override.TokenURL
	}
	if len(override.Scopes) > 0 {
		sc.Scopes = override.Scopes
	}

	if sc.ClientID == "" {
		return StoredCredentials{}, fmt.Errorf("oauth credentials: client_id is required")
	}
	if sc.ClientSecret == "" {
		return StoredCredentials{}, fmt.Errorf("oauth credentials: client_secret is required")
	}
	if sc.TokenURL == "" {
		return StoredCredentials{}, fmt.Errorf("oauth credentials: token_url is required")
	}

	return sc, nil
}

// BuildSeedCredentials constructs the initial StoredCredentials for a newly
// installed plugin instance. It populates the strategy and endpoint defaults
// from the manifest; client_id and client_secret are left empty because
// manifest.OAuthDefaultsDecl only carries HasClientID/HasClientSecret booleans
// (actual secrets are supplied via per-instance config or the admin UI).
//
// Returns (StoredCredentials, true) when the manifest declares an OAuth2
// strategy, (StoredCredentials{}, false) otherwise.
//
// TODO(plugin-instance-provision): Call this from the instance creation path
// when CreatePluginInstance lands. For #224 the helper exists but has no live
// call site; production wiring is deferred to the instance auto-provision
// follow-up.
func BuildSeedCredentials(authDecl sdkmanifest.AuthDecl, defaults *sdkmanifest.OAuthDefaultsDecl) (StoredCredentials, bool) {
	if authDecl.Strategy != sdkmanifest.AuthStrategyOAuth2Authcode &&
		authDecl.Strategy != sdkmanifest.AuthStrategyOAuth2Clientcred {
		return StoredCredentials{}, false
	}
	sc := StoredCredentials{Strategy: authDecl.Strategy}
	if defaults != nil {
		sc.AuthorizationURL = defaults.AuthorizationURL
		sc.TokenURL = defaults.TokenURL
		sc.Scopes = append([]string(nil), defaults.Scopes...)
	}
	return sc, true
}
