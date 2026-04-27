package arcade_test

import (
	"testing"

	"github.com/felag-engineering/gleipnir/internal/arcade"
)

func TestIsArcadeGateway(t *testing.T) {
	allHeaders := []string{"Authorization", "Arcade-User-ID"}

	tests := []struct {
		name    string
		url     string
		headers []string
		want    bool
	}{
		{
			name:    "all conditions met",
			url:     "https://api.arcade.dev/mcp/my-gateway",
			headers: allHeaders,
			want:    true,
		},
		{
			name:    "missing Arcade-User-ID",
			url:     "https://api.arcade.dev/mcp/my-gateway",
			headers: []string{"Authorization"},
			want:    false,
		},
		{
			name:    "missing Authorization",
			url:     "https://api.arcade.dev/mcp/my-gateway",
			headers: []string{"Arcade-User-ID"},
			want:    false,
		},
		{
			name:    "wrong host",
			url:     "https://api.notarcade.dev/mcp/my-gateway",
			headers: allHeaders,
			want:    false,
		},
		{
			name:    "wrong path prefix",
			url:     "https://api.arcade.dev/v1/my-gateway",
			headers: allHeaders,
			want:    false,
		},
		{
			name:    "mixed-case header names",
			url:     "https://api.arcade.dev/mcp/my-gateway",
			headers: []string{"authorization", "arcade-user-id"},
			want:    true,
		},
		{
			name:    "mixed-case host in URL",
			url:     "https://API.ARCADE.DEV/mcp/my-gateway",
			headers: allHeaders,
			want:    true,
		},
		{
			name:    "trailing path components",
			url:     "https://api.arcade.dev/mcp/my-gateway/extra/path",
			headers: allHeaders,
			want:    true,
		},
		{
			name:    "URL parse failure",
			url:     "://bad-url",
			headers: allHeaders,
			want:    false,
		},
		{
			name:    "empty headers",
			url:     "https://api.arcade.dev/mcp/my-gateway",
			headers: []string{},
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := arcade.IsArcadeGateway(tc.url, tc.headers)
			if got != tc.want {
				t.Errorf("IsArcadeGateway(%q, %v) = %v, want %v", tc.url, tc.headers, got, tc.want)
			}
		})
	}
}
