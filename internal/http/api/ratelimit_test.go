package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/clientip"
)

func TestRateLimitKeyByClientIP(t *testing.T) {
	trusted := clientip.New([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{"direct ipv4 peer", "203.0.113.9:1000", "", "203.0.113.9"},
		{"spoofed XFF from untrusted peer ignored", "203.0.113.9:1000", "1.2.3.4", "203.0.113.9"},
		{"proxied client keyed by real IP", "10.1.1.1:80", "198.51.100.7", "198.51.100.7"},
		{"ipv6 bucketed by /64", "[2001:db8:1:2:3:4:5:6]:80", "", "2001:db8:1:2::/64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			h := trusted.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				got, err = rateLimitKeyByClientIP(r)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != tc.want {
				t.Errorf("key = %q, want %q", got, tc.want)
			}
		})
	}
}
