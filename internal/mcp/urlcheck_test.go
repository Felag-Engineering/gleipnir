package mcp

import (
	"context"
	"net/url"
	"testing"
)

func TestValidateServerURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		// Valid URLs — should pass.
		{
			name:    "http localhost",
			rawURL:  "http://localhost:9000",
			wantErr: false,
		},
		{
			name:    "http loopback IP",
			rawURL:  "http://127.0.0.1:9000",
			wantErr: false,
		},
		{
			name:    "http private RFC1918 192.168.x.x",
			rawURL:  "http://192.168.1.100:9000",
			wantErr: false,
		},
		{
			name:    "http private RFC1918 10.x.x.x",
			rawURL:  "http://10.0.0.1:9000",
			wantErr: false,
		},
		{
			name:    "http private RFC1918 172.16.x.x",
			rawURL:  "http://172.16.0.1:9000",
			wantErr: false,
		},
		{
			name:    "https public host",
			rawURL:  "https://mcp.example.com/tools",
			wantErr: false,
		},
		{
			name:    "http public host",
			rawURL:  "http://mcp.example.com/tools",
			wantErr: false,
		},
		{
			name:    "http with path",
			rawURL:  "http://192.168.50.10:8080/mcp",
			wantErr: false,
		},

		// Blocked — link-local addresses.
		{
			name:    "link-local cloud metadata IP",
			rawURL:  "http://169.254.169.254/latest/meta-data",
			wantErr: true,
		},
		{
			name:    "link-local APIPA address",
			rawURL:  "http://169.254.1.1:9000",
			wantErr: true,
		},
		{
			name:    "IPv6 link-local",
			rawURL:  "http://[fe80::1]:9000",
			wantErr: true,
		},

		// Blocked — bad scheme.
		{
			name:    "ftp scheme",
			rawURL:  "ftp://192.168.1.1/files",
			wantErr: true,
		},
		{
			name:    "file scheme",
			rawURL:  "file:///etc/passwd",
			wantErr: true,
		},
		{
			name:    "no scheme",
			rawURL:  "192.168.1.1:9000",
			wantErr: true,
		},

		// Blocked — structural problems.
		{
			name:    "empty url",
			rawURL:  "",
			wantErr: true,
		},
		{
			name:    "scheme only no host",
			rawURL:  "http://",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateServerURL(context.Background(), tc.rawURL)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateServerURL(%q) = nil, want error", tc.rawURL)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateServerURL(%q) = %v, want nil", tc.rawURL, err)
			}
		})
	}
}

func TestCheckRedirectTarget(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		// Allowed redirects.
		{
			name:    "http redirect to private IP",
			rawURL:  "http://192.168.1.100:9000/mcp",
			wantErr: false,
		},
		{
			name:    "http redirect to loopback",
			rawURL:  "http://127.0.0.1:9000",
			wantErr: false,
		},
		{
			name:    "https redirect",
			rawURL:  "https://example.com/mcp",
			wantErr: false,
		},

		// Blocked redirects.
		{
			name:    "redirect to file scheme",
			rawURL:  "file:///etc/passwd",
			wantErr: true,
		},
		{
			name:    "redirect to ftp scheme",
			rawURL:  "ftp://example.com/data",
			wantErr: true,
		},
		{
			name:    "redirect to link-local",
			rawURL:  "http://169.254.169.254/latest/meta-data",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.rawURL, err)
			}
			err = checkRedirectTarget(u)
			if tc.wantErr && err == nil {
				t.Errorf("checkRedirectTarget(%q) = nil, want error", tc.rawURL)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("checkRedirectTarget(%q) = %v, want nil", tc.rawURL, err)
			}
		})
	}
}
