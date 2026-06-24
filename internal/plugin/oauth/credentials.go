// Package oauth owns host-side credential storage for ALL plugin auth
// strategies declared in spec §9.1. OAuth2 is the most complex strategy and
// gives the package its name, but static_api_key, header_set, basic_auth, and
// none are also stored, delivered, and audited here.
//
// Resolve / InstanceConfigOverride remain OAuth-only — non-OAuth strategies
// have no manifest defaults.
package oauth

import (
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/oauth2"

	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// StoredCredentials is the JSON blob written to
// plugin_instances.credentials_encrypted. Strategy is the discriminator field;
// only the sub-blob corresponding to Strategy is populated.
//
// OAuth2 fields (ClientID … Token) are populated when Strategy is
// AuthStrategyOAuth2Authcode or AuthStrategyOAuth2Clientcred.
// StaticAPIKey is populated for AuthStrategyStaticAPIKey.
// HeaderSet is populated for AuthStrategyHeaderSet.
// BasicAuth is populated for AuthStrategyBasicAuth.
// No sub-blob exists for AuthStrategyNone; Strategy alone is sufficient.
type StoredCredentials struct {
	// Strategy is the auth strategy declared in the manifest (one of the
	// AuthStrategy* constants in plugin-sdk/manifest).
	Strategy string `json:"strategy"`

	// --- OAuth2 fields (oauth2_authcode / oauth2_clientcred only) ---

	// ClientID is the OAuth2 client identifier.
	ClientID string `json:"client_id,omitempty"`

	// ClientSecret is the OAuth2 client secret. Write-only; never exposed
	// through any read API.
	ClientSecret string `json:"client_secret,omitempty"`

	// AuthorizationURL is the provider's authorization endpoint (authcode only).
	AuthorizationURL string `json:"authorization_url,omitempty"`

	// TokenURL is the provider's token endpoint.
	TokenURL string `json:"token_url,omitempty"`

	// Scopes is the set of OAuth2 scopes requested.
	Scopes []string `json:"scopes,omitempty"`

	// Token is the live oauth2.Token. Nil before the first successful exchange.
	Token *oauth2.Token `json:"token,omitempty"`

	// --- Non-OAuth strategy sub-blobs ---

	StaticAPIKey *StaticAPIKeyCreds `json:"static_api_key,omitempty"`
	HeaderSet    *HeaderSetCreds    `json:"header_set,omitempty"`
	BasicAuth    *BasicAuthCreds    `json:"basic_auth,omitempty"`
}

// StaticAPIKeyCreds holds credentials for the static_api_key strategy.
// The plugin receives one header whose value is Scheme + " " + APIKey (or
// just APIKey when Scheme is empty).
type StaticAPIKeyCreds struct {
	HeaderName string `json:"header_name"`      // e.g. "X-API-Key"
	Scheme     string `json:"scheme,omitempty"` // optional prefix, e.g. "Bearer"
	APIKey     string `json:"api_key"`
}

// HeaderSetCreds holds credentials for the header_set strategy.
// Any number of named headers may be stored; each is injected verbatim.
type HeaderSetCreds struct {
	Headers []NamedHeader `json:"headers"`
}

// NamedHeader is a single HTTP header name/value pair stored inside HeaderSetCreds.
type NamedHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// BasicAuthCreds holds credentials for the basic_auth strategy.
// basic_auth is a stepping stone for legacy enterprise services; new plugins
// should prefer static_api_key or oauth2_authcode.
type BasicAuthCreds struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// RedactedCredentials is the read-safe view of StoredCredentials returned by
// GET /credentials endpoints. Secret fields (api_key, header values, password,
// client_secret, token) are NEVER included — only presence flags and
// non-secret metadata.
type RedactedCredentials struct {
	Strategy string `json:"strategy"`

	// static_api_key fields
	HeaderName string `json:"header_name,omitempty"`
	Scheme     string `json:"scheme,omitempty"`
	HasAPIKey  bool   `json:"has_api_key,omitempty"`

	// header_set fields
	HeaderNames []string `json:"header_names,omitempty"`

	// basic_auth fields
	Username    string `json:"username,omitempty"`
	HasPassword bool   `json:"has_password,omitempty"`

	// oauth2_* fields
	ClientID         string   `json:"client_id,omitempty"`
	HasClientSecret  bool     `json:"has_client_secret,omitempty"`
	AuthorizationURL string   `json:"authorization_url,omitempty"`
	TokenURL         string   `json:"token_url,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	HasToken         bool     `json:"has_token,omitempty"`
	// TokenExpiresAt is the token expiry in RFC3339Nano UTC. Empty when the
	// token is absent or has a zero expiry.
	TokenExpiresAt string `json:"token_expires_at,omitempty"`
}

// Redact returns a read-safe view of c, populated only for the fields
// relevant to c.Strategy. Secret values are replaced by boolean presence flags.
func (c StoredCredentials) Redact() RedactedCredentials {
	r := RedactedCredentials{Strategy: c.Strategy}

	switch c.Strategy {
	case sdkmanifest.AuthStrategyStaticAPIKey:
		if c.StaticAPIKey != nil {
			r.HeaderName = c.StaticAPIKey.HeaderName
			r.Scheme = c.StaticAPIKey.Scheme
			r.HasAPIKey = c.StaticAPIKey.APIKey != ""
		}

	case sdkmanifest.AuthStrategyHeaderSet:
		if c.HeaderSet != nil {
			for _, h := range c.HeaderSet.Headers {
				r.HeaderNames = append(r.HeaderNames, h.Name)
			}
		}

	case sdkmanifest.AuthStrategyBasicAuth:
		if c.BasicAuth != nil {
			r.Username = c.BasicAuth.Username
			r.HasPassword = c.BasicAuth.Password != ""
		}

	case sdkmanifest.AuthStrategyOAuth2Authcode, sdkmanifest.AuthStrategyOAuth2Clientcred:
		r.ClientID = c.ClientID
		r.HasClientSecret = c.ClientSecret != ""
		r.AuthorizationURL = c.AuthorizationURL
		r.TokenURL = c.TokenURL
		r.Scopes = append([]string(nil), c.Scopes...)
		if c.Token != nil {
			r.HasToken = true
			if !c.Token.Expiry.IsZero() {
				r.TokenExpiresAt = c.Token.Expiry.UTC().Format(time.RFC3339Nano)
			}
		}
	}

	return r
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
// installed plugin instance. For OAuth2 strategies it populates endpoint
// defaults from the manifest; for all other supported strategies it returns a
// zero credentials blob pre-seeded with the Strategy so instance provisioning
// can record the empty row before any secrets are set.
//
// Returns (StoredCredentials{Strategy: s}, true) for every supported strategy.
// Returns (StoredCredentials{}, false) only for an unrecognised strategy string.
//
// Wired into the instance-create path via admin.PluginHandler.CreateInstance →
// seedInstanceCredentials (#572): the credential blob is seeded with the
// manifest's declared strategy + endpoints at creation, before any token
// exchange.
func BuildSeedCredentials(authDecl sdkmanifest.AuthDecl, defaults *sdkmanifest.OAuthDefaultsDecl) (StoredCredentials, bool) {
	switch authDecl.Strategy {
	case sdkmanifest.AuthStrategyOAuth2Authcode, sdkmanifest.AuthStrategyOAuth2Clientcred:
		sc := StoredCredentials{Strategy: authDecl.Strategy}
		if defaults != nil {
			sc.AuthorizationURL = defaults.AuthorizationURL
			sc.TokenURL = defaults.TokenURL
			sc.Scopes = append([]string(nil), defaults.Scopes...)
		}
		return sc, true

	case sdkmanifest.AuthStrategyNone,
		sdkmanifest.AuthStrategyStaticAPIKey,
		sdkmanifest.AuthStrategyHeaderSet,
		sdkmanifest.AuthStrategyBasicAuth:
		return StoredCredentials{Strategy: authDecl.Strategy}, true

	default:
		return StoredCredentials{}, false
	}
}
