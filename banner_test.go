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
		wantHint   bool
	}{
		{
			name:       "public_url set is authoritative and suppresses the hint",
			listenAddr: ":8080",
			publicURL:  "https://gleipnir.example.com",
			wantURL:    "https://gleipnir.example.com",
			wantHint:   false,
		},
		{
			name:       "bare port maps to localhost with hint",
			listenAddr: ":8080",
			publicURL:  "",
			wantURL:    "http://localhost:8080",
			wantHint:   true,
		},
		{
			name:       "wildcard host 0.0.0.0 is rewritten to localhost",
			listenAddr: "0.0.0.0:9000",
			publicURL:  "",
			wantURL:    "http://localhost:9000",
			wantHint:   true,
		},
		{
			name:       "explicit loopback host is preserved",
			listenAddr: "127.0.0.1:8080",
			publicURL:  "",
			wantURL:    "http://127.0.0.1:8080",
			wantHint:   true,
		},
		{
			name:       "unparseable addr falls back to raw with hint",
			listenAddr: "8080",
			publicURL:  "",
			wantURL:    "http://localhost8080",
			wantHint:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, hint := resolveDisplayURL(tt.listenAddr, tt.publicURL)
			if url != tt.wantURL {
				t.Errorf("url = %q, want %q", url, tt.wantURL)
			}
			if hint != tt.wantHint {
				t.Errorf("hint = %v, want %v", hint, tt.wantHint)
			}
		})
	}
}

func TestPrintReadyBanner(t *testing.T) {
	t.Run("compose fallback includes URL, version, and host-port hint", func(t *testing.T) {
		var buf bytes.Buffer
		printReadyBanner(&buf, "1.1.0", ":8080", "")
		out := buf.String()
		for _, want := range []string{"1.1.0", "http://localhost:8080", "localhost:3000", "ready"} {
			if !strings.Contains(out, want) {
				t.Errorf("banner missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("public_url set omits the compose hint", func(t *testing.T) {
		var buf bytes.Buffer
		printReadyBanner(&buf, "1.1.0", ":8080", "https://gleipnir.example.com")
		out := buf.String()
		if !strings.Contains(out, "https://gleipnir.example.com") {
			t.Errorf("banner missing public_url; got:\n%s", out)
		}
		if strings.Contains(out, "localhost:3000") {
			t.Errorf("banner should not show the compose hint when public_url is set; got:\n%s", out)
		}
	})
}
