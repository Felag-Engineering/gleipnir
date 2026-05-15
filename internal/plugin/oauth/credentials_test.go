package oauth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// TestStoredCredentials_RoundTrip verifies that every strategy's credentials
// survive a Marshal → UnmarshalCredentials round-trip intact.
func TestStoredCredentials_RoundTrip(t *testing.T) {
	expiry := time.Unix(2000000, 0).UTC()

	tests := []struct {
		name  string
		input StoredCredentials
	}{
		{
			name:  "none",
			input: StoredCredentials{Strategy: sdkmanifest.AuthStrategyNone},
		},
		{
			name: "static_api_key",
			input: StoredCredentials{
				Strategy: sdkmanifest.AuthStrategyStaticAPIKey,
				StaticAPIKey: &StaticAPIKeyCreds{
					HeaderName: "X-API-Key",
					Scheme:     "Bearer",
					APIKey:     "secret-key",
				},
			},
		},
		{
			name: "static_api_key no scheme",
			input: StoredCredentials{
				Strategy: sdkmanifest.AuthStrategyStaticAPIKey,
				StaticAPIKey: &StaticAPIKeyCreds{
					HeaderName: "Authorization",
					APIKey:     "raw-key",
				},
			},
		},
		{
			name: "header_set",
			input: StoredCredentials{
				Strategy: sdkmanifest.AuthStrategyHeaderSet,
				HeaderSet: &HeaderSetCreds{
					Headers: []NamedHeader{
						{Name: "X-Org-ID", Value: "org-123"},
						{Name: "X-Token", Value: "tok-abc"},
					},
				},
			},
		},
		{
			name: "basic_auth",
			input: StoredCredentials{
				Strategy:  sdkmanifest.AuthStrategyBasicAuth,
				BasicAuth: &BasicAuthCreds{Username: "alice", Password: "s3cr3t"},
			},
		},
		{
			name: "oauth2_authcode with token",
			input: StoredCredentials{
				Strategy:         sdkmanifest.AuthStrategyOAuth2Authcode,
				ClientID:         "cid",
				ClientSecret:     "csec",
				AuthorizationURL: "https://provider/auth",
				TokenURL:         "https://provider/token",
				Scopes:           []string{"read", "write"},
				Token: &oauth2.Token{
					AccessToken: "access-tok",
					Expiry:      expiry,
				},
			},
		},
		{
			name: "oauth2_clientcred",
			input: StoredCredentials{
				Strategy:     sdkmanifest.AuthStrategyOAuth2Clientcred,
				ClientID:     "cid2",
				ClientSecret: "csec2",
				TokenURL:     "https://provider/token",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := tt.input.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			got, err := UnmarshalCredentials(s)
			if err != nil {
				t.Fatalf("UnmarshalCredentials: %v", err)
			}
			if got.Strategy != tt.input.Strategy {
				t.Errorf("Strategy: got %q, want %q", got.Strategy, tt.input.Strategy)
			}
			// Re-marshal both and compare JSON — the simplest deep-equality check
			// that avoids pointer-address issues in oauth2.Token.
			wantJSON, _ := json.Marshal(tt.input)
			gotJSON, _ := json.Marshal(got)
			if string(wantJSON) != string(gotJSON) {
				t.Errorf("round-trip mismatch:\n got:  %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}
}

// TestRedact_NoSecretFields verifies that Redact never includes secret values.
func TestRedact_NoSecretFields(t *testing.T) {
	secretValues := []string{"secret-key", "s3cr3t", "csec", "access-tok", "raw-password"}

	expiry := time.Unix(2000000, 0).UTC()

	inputs := []StoredCredentials{
		{
			Strategy: sdkmanifest.AuthStrategyStaticAPIKey,
			StaticAPIKey: &StaticAPIKeyCreds{
				HeaderName: "X-API-Key",
				Scheme:     "Bearer",
				APIKey:     "secret-key",
			},
		},
		{
			Strategy: sdkmanifest.AuthStrategyHeaderSet,
			HeaderSet: &HeaderSetCreds{
				Headers: []NamedHeader{
					{Name: "X-Org-ID", Value: "raw-password"},
				},
			},
		},
		{
			Strategy:  sdkmanifest.AuthStrategyBasicAuth,
			BasicAuth: &BasicAuthCreds{Username: "alice", Password: "s3cr3t"},
		},
		{
			Strategy:         sdkmanifest.AuthStrategyOAuth2Authcode,
			ClientID:         "cid",
			ClientSecret:     "csec",
			AuthorizationURL: "https://provider/auth",
			TokenURL:         "https://provider/token",
			Token: &oauth2.Token{
				AccessToken: "access-tok",
				Expiry:      expiry,
			},
		},
	}

	for _, creds := range inputs {
		redacted := creds.Redact()
		b, _ := json.Marshal(redacted)
		s := string(b)
		for _, secret := range secretValues {
			if strings.Contains(s, secret) {
				t.Errorf("Redact(%q) leaked secret %q in JSON: %s", creds.Strategy, secret, s)
			}
		}
	}
}

// TestRedact_PresenceFlags verifies that HasAPIKey, HasPassword, HasClientSecret,
// and HasToken are set correctly based on whether the secret is populated.
func TestRedact_PresenceFlags(t *testing.T) {
	expiry := time.Unix(2000000, 0).UTC()

	t.Run("static_api_key with key", func(t *testing.T) {
		c := StoredCredentials{
			Strategy:     sdkmanifest.AuthStrategyStaticAPIKey,
			StaticAPIKey: &StaticAPIKeyCreds{HeaderName: "X-Key", APIKey: "val"},
		}
		r := c.Redact()
		if !r.HasAPIKey {
			t.Error("HasAPIKey should be true when APIKey is set")
		}
		if r.HeaderName != "X-Key" {
			t.Errorf("HeaderName: got %q, want %q", r.HeaderName, "X-Key")
		}
	})

	t.Run("static_api_key without key", func(t *testing.T) {
		c := StoredCredentials{
			Strategy:     sdkmanifest.AuthStrategyStaticAPIKey,
			StaticAPIKey: &StaticAPIKeyCreds{HeaderName: "X-Key", APIKey: ""},
		}
		r := c.Redact()
		if r.HasAPIKey {
			t.Error("HasAPIKey should be false when APIKey is empty")
		}
	})

	t.Run("basic_auth with password", func(t *testing.T) {
		c := StoredCredentials{
			Strategy:  sdkmanifest.AuthStrategyBasicAuth,
			BasicAuth: &BasicAuthCreds{Username: "alice", Password: "pw"},
		}
		r := c.Redact()
		if !r.HasPassword {
			t.Error("HasPassword should be true when Password is set")
		}
		if r.Username != "alice" {
			t.Errorf("Username: got %q, want %q", r.Username, "alice")
		}
	})

	t.Run("oauth2 with token and expiry", func(t *testing.T) {
		c := StoredCredentials{
			Strategy:     sdkmanifest.AuthStrategyOAuth2Authcode,
			ClientID:     "cid",
			ClientSecret: "csec",
			Token: &oauth2.Token{
				AccessToken: "tok",
				Expiry:      expiry,
			},
		}
		r := c.Redact()
		if !r.HasToken {
			t.Error("HasToken should be true")
		}
		if !r.HasClientSecret {
			t.Error("HasClientSecret should be true")
		}
		if r.TokenExpiresAt == "" {
			t.Error("TokenExpiresAt should be non-empty when Expiry is set")
		}
		if r.ClientID != "cid" {
			t.Errorf("ClientID: got %q, want %q", r.ClientID, "cid")
		}
	})

	t.Run("oauth2 with token zero expiry", func(t *testing.T) {
		c := StoredCredentials{
			Strategy: sdkmanifest.AuthStrategyOAuth2Authcode,
			Token:    &oauth2.Token{AccessToken: "tok"},
		}
		r := c.Redact()
		if !r.HasToken {
			t.Error("HasToken should be true")
		}
		if r.TokenExpiresAt != "" {
			t.Errorf("TokenExpiresAt should be empty for zero Expiry, got %q", r.TokenExpiresAt)
		}
	})

	t.Run("oauth2 without token", func(t *testing.T) {
		c := StoredCredentials{
			Strategy: sdkmanifest.AuthStrategyOAuth2Authcode,
		}
		r := c.Redact()
		if r.HasToken {
			t.Error("HasToken should be false when Token is nil")
		}
		if r.TokenExpiresAt != "" {
			t.Errorf("TokenExpiresAt should be empty when Token is nil, got %q", r.TokenExpiresAt)
		}
	})

	t.Run("header_set names exposed, values not", func(t *testing.T) {
		c := StoredCredentials{
			Strategy: sdkmanifest.AuthStrategyHeaderSet,
			HeaderSet: &HeaderSetCreds{
				Headers: []NamedHeader{
					{Name: "X-A", Value: "secret-val-a"},
					{Name: "X-B", Value: "secret-val-b"},
				},
			},
		}
		r := c.Redact()
		if len(r.HeaderNames) != 2 {
			t.Fatalf("HeaderNames len: got %d, want 2", len(r.HeaderNames))
		}
		if r.HeaderNames[0] != "X-A" || r.HeaderNames[1] != "X-B" {
			t.Errorf("HeaderNames: got %v, want [X-A X-B]", r.HeaderNames)
		}
	})
}

// TestBuildSeedCredentials verifies that every strategy returns (creds, true)
// and an unknown strategy returns (zero, false).
func TestBuildSeedCredentials(t *testing.T) {
	strategies := []string{
		sdkmanifest.AuthStrategyNone,
		sdkmanifest.AuthStrategyStaticAPIKey,
		sdkmanifest.AuthStrategyHeaderSet,
		sdkmanifest.AuthStrategyBasicAuth,
		sdkmanifest.AuthStrategyOAuth2Authcode,
		sdkmanifest.AuthStrategyOAuth2Clientcred,
	}

	for _, s := range strategies {
		t.Run(s, func(t *testing.T) {
			authDecl := sdkmanifest.AuthDecl{Strategy: s}
			creds, ok := BuildSeedCredentials(authDecl, nil)
			if !ok {
				t.Fatalf("BuildSeedCredentials(%q): got ok=false, want true", s)
			}
			if creds.Strategy != s {
				t.Errorf("Strategy: got %q, want %q", creds.Strategy, s)
			}
		})
	}

	t.Run("unknown strategy returns false", func(t *testing.T) {
		authDecl := sdkmanifest.AuthDecl{Strategy: "unknown_strategy"}
		_, ok := BuildSeedCredentials(authDecl, nil)
		if ok {
			t.Error("expected ok=false for unknown strategy")
		}
	})

	t.Run("oauth2_authcode with defaults", func(t *testing.T) {
		authDecl := sdkmanifest.AuthDecl{Strategy: sdkmanifest.AuthStrategyOAuth2Authcode}
		defaults := &sdkmanifest.OAuthDefaultsDecl{
			AuthorizationURL: "https://provider/auth",
			TokenURL:         "https://provider/token",
			Scopes:           []string{"openid"},
		}
		creds, ok := BuildSeedCredentials(authDecl, defaults)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if creds.AuthorizationURL != defaults.AuthorizationURL {
			t.Errorf("AuthorizationURL: got %q, want %q", creds.AuthorizationURL, defaults.AuthorizationURL)
		}
		if creds.TokenURL != defaults.TokenURL {
			t.Errorf("TokenURL: got %q, want %q", creds.TokenURL, defaults.TokenURL)
		}
	})
}
