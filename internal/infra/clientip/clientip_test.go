package clientip

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func prefixes(t *testing.T, ss ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestResolve(t *testing.T) {
	trusted := []string{"10.0.0.0/8", "192.168.1.5/32", "fd00::/8"}

	cases := []struct {
		name       string
		trusted    []string
		remoteAddr string
		xff        []string
		realIP     string
		trueClient string
		want       string
	}{
		{"no trusted proxies ignores spoofed XFF", nil, "203.0.113.9:4000", []string{"1.2.3.4"}, "5.6.7.8", "", "203.0.113.9"},
		{"untrusted peer XFF ignored", trusted, "203.0.113.9:4000", []string{"1.2.3.4"}, "5.6.7.8", "", "203.0.113.9"},
		{"trusted peer, no headers", trusted, "10.1.1.1:80", nil, "", "", "10.1.1.1"},
		{"trusted peer, single client", trusted, "10.1.1.1:80", []string{"198.51.100.7"}, "", "", "198.51.100.7"},
		{"rightmost untrusted wins over spoofed leftmost", trusted, "10.1.1.1:80", []string{"6.6.6.6, 198.51.100.7, 10.2.2.2"}, "", "", "198.51.100.7"},
		{"all trusted chain gives leftmost", trusted, "10.1.1.1:80", []string{"10.5.5.5, 192.168.1.5, 10.2.2.2"}, "", "", "10.5.5.5"},
		{"malformed rightmost falls back to peer", trusted, "10.1.1.1:80", []string{"198.51.100.7, garbage"}, "", "", "10.1.1.1"},
		{"malformed after trusted hop falls back to that hop", trusted, "10.1.1.1:80", []string{"198.51.100.7, nope, 10.2.2.2"}, "", "", "10.2.2.2"},
		{"empty entry stops the walk", trusted, "10.1.1.1:80", []string{"198.51.100.7,, 10.2.2.2"}, "", "", "10.2.2.2"},
		{"multiple header lines concatenated in order", trusted, "10.1.1.1:80", []string{"6.6.6.6", "198.51.100.7, 10.2.2.2"}, "", "", "198.51.100.7"},
		{"entry with port", trusted, "10.1.1.1:80", []string{"198.51.100.7:5555"}, "", "", "198.51.100.7"},
		{"ipv6 client behind trusted ipv6 proxy", trusted, "[fd00::1]:443", []string{"2001:db8::7, fd00::2"}, "", "", "2001:db8::7"},
		{"bracketed ipv6 entry with port", trusted, "10.1.1.1:80", []string{"[2001:db8::7]:99"}, "", "", "2001:db8::7"},
		{"ipv4-mapped peer is unmapped before matching", trusted, "[::ffff:10.1.1.1]:80", []string{"198.51.100.7"}, "", "", "198.51.100.7"},
		{"RemoteAddr without port", trusted, "203.0.113.9", nil, "", "", "203.0.113.9"},
		{"unparseable RemoteAddr returned verbatim", trusted, "@", []string{"1.2.3.4"}, "", "", "@"},
		{"X-Real-IP honored from trusted peer without XFF", trusted, "10.1.1.1:80", nil, "198.51.100.7", "", "198.51.100.7"},
		{"X-Real-IP ignored from untrusted peer", trusted, "203.0.113.9:80", nil, "198.51.100.7", "", "203.0.113.9"},
		{"X-Real-IP not consulted when XFF present", trusted, "10.1.1.1:80", []string{"198.51.100.7"}, "6.6.6.6", "", "198.51.100.7"},
		{"malformed X-Real-IP falls back to peer", trusted, "10.1.1.1:80", nil, "nope", "", "10.1.1.1"},
		{"True-Client-IP never honored", trusted, "10.1.1.1:80", nil, "", "6.6.6.6", "10.1.1.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := New(prefixes(t, tc.trusted...))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			for _, v := range tc.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if tc.realIP != "" {
				req.Header.Set("X-Real-IP", tc.realIP)
			}
			if tc.trueClient != "" {
				req.Header.Set("True-Client-IP", tc.trueClient)
			}
			if got := rs.Resolve(req); got != tc.want {
				t.Errorf("Resolve() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMiddleware_StoresIPAndLeavesRemoteAddr(t *testing.T) {
	rs := New(prefixes(t, "10.0.0.0/8"))
	var gotIP, gotRemote string
	h := rs.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIP = FromRequest(r)
		gotRemote = r.RemoteAddr
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.1.1:80"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotIP != "198.51.100.7" {
		t.Errorf("FromRequest = %q, want 198.51.100.7", gotIP)
	}
	if gotRemote != "10.1.1.1:80" {
		t.Errorf("RemoteAddr mutated to %q", gotRemote)
	}
}

func TestFromRequest_WithoutMiddlewareUsesPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:1"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := FromRequest(req); got != "203.0.113.9" {
		t.Errorf("FromRequest = %q, want peer", got)
	}
}
