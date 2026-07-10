package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveDisplayURL(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr string
		publicURL  string
		wantURL    string
	}{
		{
			name:       "public_url is authoritative",
			listenAddr: ":8080",
			publicURL:  "https://gleipnir.example.com",
			wantURL:    "https://gleipnir.example.com",
		},
		{
			name:       "bare port maps to localhost",
			listenAddr: ":8080",
			publicURL:  "",
			wantURL:    "http://localhost:8080",
		},
		{
			name:       "wildcard host 0.0.0.0 is rewritten to localhost",
			listenAddr: "0.0.0.0:9000",
			publicURL:  "",
			wantURL:    "http://localhost:9000",
		},
		{
			name:       "explicit loopback host is preserved",
			listenAddr: "127.0.0.1:8080",
			publicURL:  "",
			wantURL:    "http://127.0.0.1:8080",
		},
		{
			name:       "unparseable addr falls back to raw",
			listenAddr: "8080",
			publicURL:  "",
			wantURL:    "http://localhost8080",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveDisplayURL(tt.listenAddr, tt.publicURL); got != tt.wantURL {
				t.Errorf("resolveDisplayURL(%q, %q) = %q, want %q", tt.listenAddr, tt.publicURL, got, tt.wantURL)
			}
		})
	}
}

func TestPrintReadyBanner(t *testing.T) {
	t.Run("shows version and localhost URL", func(t *testing.T) {
		var buf bytes.Buffer
		printReadyBanner(&buf, "1.1.0", ":8080", "")
		out := buf.String()
		for _, want := range []string{"1.1.0", "http://localhost:8080", "ready"} {
			if !strings.Contains(out, want) {
				t.Errorf("banner missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("prefers public_url when set", func(t *testing.T) {
		var buf bytes.Buffer
		printReadyBanner(&buf, "1.1.0", ":8080", "https://gleipnir.example.com")
		if out := buf.String(); !strings.Contains(out, "https://gleipnir.example.com") {
			t.Errorf("banner missing public_url; got:\n%s", out)
		}
	})
}
