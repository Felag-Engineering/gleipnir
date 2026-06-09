package admin

import (
	"testing"

	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

func TestComputeInstanceReadinessDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		strategy   string
		configJSON string
		credsSet   bool
		want       string
	}{
		{
			name:       "empty_config_returns_config_missing",
			strategy:   sdkmanifest.AuthStrategyStaticAPIKey,
			configJSON: "{}",
			credsSet:   true,
			want:       "config_missing",
		},
		{
			name:       "empty_config_string_returns_config_missing",
			strategy:   sdkmanifest.AuthStrategyNone,
			configJSON: "",
			credsSet:   false,
			want:       "config_missing",
		},
		{
			name:       "config_set_with_strategy_none_returns_empty",
			strategy:   sdkmanifest.AuthStrategyNone,
			configJSON: `{"key":"v"}`,
			credsSet:   false,
			want:       "",
		},
		{
			name:       "config_set_unset_strategy_treated_as_none",
			strategy:   "",
			configJSON: `{"key":"v"}`,
			credsSet:   false,
			want:       "",
		},
		{
			name:       "config_set_creds_missing_for_auth_strategy_returns_credentials_missing",
			strategy:   sdkmanifest.AuthStrategyStaticAPIKey,
			configJSON: `{"key":"v"}`,
			credsSet:   false,
			want:       "credentials_missing",
		},
		{
			name:       "config_set_creds_set_for_oauth_returns_empty",
			strategy:   sdkmanifest.AuthStrategyOAuth2Authcode,
			configJSON: `{"app_level_token":"xapp-1"}`,
			credsSet:   true,
			want:       "",
		},
		{
			name:       "config_set_creds_missing_for_oauth_returns_credentials_missing",
			strategy:   sdkmanifest.AuthStrategyOAuth2Authcode,
			configJSON: `{"app_level_token":"xapp-1"}`,
			credsSet:   false,
			want:       "credentials_missing",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &sdkmanifest.Manifest{Auth: sdkmanifest.AuthDecl{Strategy: tc.strategy}}
			got := computeInstanceReadinessDetail(m, tc.configJSON, tc.credsSet)
			if got != tc.want {
				t.Errorf("computeInstanceReadinessDetail = %q, want %q", got, tc.want)
			}
		})
	}
}
