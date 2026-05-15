// Package credentials provides typed access to the JSON blob delivered by the
// GetCredentials host RPC. Plugin authors should use Unmarshal to decode the
// blob and Apply to inject the credentials into outbound HTTP requests.
//
// Strategy constants are defined in plugin-sdk/manifest (e.g.
// manifest.AuthStrategyStaticAPIKey). This package is stdlib-only so it can be
// embedded in any plugin binary without dragging in host-side dependencies.
//
// # No plugin-side caching (spec §9.4)
//
// Plugins MUST call GetCredentials on every outbound substrate request and MUST
// NOT cache the returned Credentials across calls. The host rotates OAuth2
// tokens transparently; a cached token will be stale after the first rotation.
// The host itself performs no in-process caching beyond the request scope, so
// the round-trip cost is bounded by a single DB read per call.
package credentials

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Strategy constants mirror the AuthStrategy* constants in plugin-sdk/manifest
// so this package does not import manifest (keeping it stdlib-only).
const (
	StrategyNone             = "none"
	StrategyStaticAPIKey     = "static_api_key"
	StrategyHeaderSet        = "header_set"
	StrategyBasicAuth        = "basic_auth"
	StrategyOAuth2Authcode   = "oauth2_authcode"
	StrategyOAuth2Clientcred = "oauth2_clientcred"
)

// Credentials is the top-level decoded form of the JSON blob returned by
// GetCredentials. Strategy is the discriminator; only the sub-field for the
// declared strategy is populated.
type Credentials struct {
	Strategy string `json:"strategy"`

	// StaticAPIKey is populated when Strategy == StrategyStaticAPIKey.
	StaticAPIKey *StaticAPIKey `json:"static_api_key,omitempty"`

	// HeaderSet is populated when Strategy == StrategyHeaderSet.
	HeaderSet *HeaderSet `json:"header_set,omitempty"`

	// BasicAuth is populated when Strategy == StrategyBasicAuth.
	BasicAuth *BasicAuth `json:"basic_auth,omitempty"`

	// For OAuth2 strategies the access token lives in the oauth2.Token blob.
	// Plugins that need the raw token for custom signing should decode the
	// blob themselves; most should use the token source provided by the SDK.
}

// StaticAPIKey holds the credentials for the static_api_key strategy.
type StaticAPIKey struct {
	// HeaderName is the HTTP header to set (e.g. "X-API-Key").
	HeaderName string `json:"header_name"`

	// Scheme is an optional prefix prepended to the key value with a space
	// separator (e.g. "Bearer" → "Bearer <key>"). Empty means no prefix.
	Scheme string `json:"scheme,omitempty"`

	// APIKey is the secret value.
	APIKey string `json:"api_key"`
}

// HeaderSet holds the credentials for the header_set strategy.
type HeaderSet struct {
	// Headers is the list of headers to inject verbatim.
	Headers []NamedHeader `json:"headers"`
}

// NamedHeader is a single HTTP header name/value pair.
type NamedHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// BasicAuth holds the credentials for the basic_auth strategy.
// basic_auth is a stepping stone for legacy enterprise services; new plugins
// should prefer static_api_key or oauth2_authcode.
type BasicAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Unmarshal decodes the JSON blob returned by the GetCredentials RPC into a
// Credentials value. Returns an error if the input is not valid JSON.
//
// Do not cache the returned Credentials; call host.GetCredentials on every
// outbound substrate request so OAuth2 token rotations are picked up (spec §9.4).
func Unmarshal(data []byte) (Credentials, error) {
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return Credentials{}, fmt.Errorf("credentials: unmarshal: %w", err)
	}
	return c, nil
}

// Apply injects the credentials into req according to Strategy:
//
//   - none: no-op
//   - static_api_key: sets HeaderName to APIKey (or "Scheme APIKey" when Scheme is non-empty)
//   - header_set: calls req.Header.Set for each entry in Headers
//   - basic_auth: calls req.SetBasicAuth(Username, Password)
//   - oauth2_authcode / oauth2_clientcred: no-op — token injection is the
//     plugin's responsibility via the oauth2.Token in the credential blob
//
// Apply is a pure convenience helper; callers are free to ignore it and inject
// headers manually.
func (c Credentials) Apply(req *http.Request) {
	switch c.Strategy {
	case StrategyStaticAPIKey:
		if c.StaticAPIKey == nil {
			return
		}
		value := c.StaticAPIKey.APIKey
		if c.StaticAPIKey.Scheme != "" {
			value = c.StaticAPIKey.Scheme + " " + c.StaticAPIKey.APIKey
		}
		req.Header.Set(c.StaticAPIKey.HeaderName, value)

	case StrategyHeaderSet:
		if c.HeaderSet == nil {
			return
		}
		for _, h := range c.HeaderSet.Headers {
			req.Header.Set(h.Name, h.Value)
		}

	case StrategyBasicAuth:
		if c.BasicAuth == nil {
			return
		}
		req.SetBasicAuth(c.BasicAuth.Username, c.BasicAuth.Password)

		// none and oauth2_* are intentional no-ops.
	}
}
