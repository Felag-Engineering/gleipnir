package mcp

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"testing"
)

func TestValidateServerURL(t *testing.T) {
	// Stub DNS so hostname cases are fast, deterministic, and can exercise
	// the resolves-only-to-link-local branch, which real DNS can't produce
	// on demand. Package-level var swap — this test must not use t.Parallel().
	prevLookup := lookupHost
	lookupHost = func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "mcp.example.com":
			return []string{"203.0.113.10"}, nil
		case "linklocal.example.com":
			return []string{"169.254.169.254"}, nil
		case "nxdomain.example.com":
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		default:
			return nil, fmt.Errorf("unexpected lookup for %q in test stub", host)
		}
	}
	t.Cleanup(func() { lookupHost = prevLookup })

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
		{
			// Resolution failure is not a security failure: allow the URL and
			// let the actual connection attempt surface the error.
			name:    "hostname that fails to resolve",
			rawURL:  "http://nxdomain.example.com:9000",
			wantErr: false,
		},

		// Blocked — link-local addresses.
		{
			name:    "hostname resolving only to link-local",
			rawURL:  "http://linklocal.example.com/latest/meta-data",
			wantErr: true,
		},
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
