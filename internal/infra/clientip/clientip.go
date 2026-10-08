// Package clientip resolves the originating client address of an HTTP request
// behind zero or more trusted reverse proxies. It is a leaf package: standard
// library only, no internal imports.
//
// Forwarding headers are attacker-controlled unless the connection came from a
// proxy the operator named in GLEIPNIR_TRUSTED_PROXIES. The resolver therefore
// consults them only when the direct peer is trusted, and even then reads
// X-Forwarded-For from the right (the end each trusted proxy appended to)
// rather than the left (the end the client wrote). Anything it cannot parse
// stops the walk and falls back to the last hop it was entitled to believe.
package clientip

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver maps a request to its client address. The zero value trusts no
// proxy, so the client is always the direct peer.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a Resolver that honours forwarding headers only from peers
// inside trusted.
func New(trusted []netip.Prefix) *Resolver {
	return &Resolver{trusted: trusted}
}

func (rs *Resolver) isTrusted(a netip.Addr) bool {
	for _, p := range rs.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolve returns the client address for r as a bare IP string (no port,
// IPv4-mapped IPv6 unmapped). If r.RemoteAddr is not an IP at all it is
// returned verbatim, so callers always get a stable non-empty key.
func (rs *Resolver) Resolve(r *http.Request) string {
	peer, ok := parseAddr(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	if rs == nil || !rs.isTrusted(peer) {
		return peer.String()
	}

	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		return rs.walkForwardedFor(peer, values).String()
	}
	// Single-proxy setups (e.g. nginx with only proxy_set_header X-Real-IP)
	// send no XFF. Believed only because the peer is trusted.
	if real, ok := parseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ok {
		return real.String()
	}
	return peer.String()
}

// walkForwardedFor scans the XFF chain right to left. current starts as the
// trusted direct peer and advances over each trusted hop, so every early exit
// returns the last address the walk had reason to believe.
func (rs *Resolver) walkForwardedFor(peer netip.Addr, headerLines []string) netip.Addr {
	// Multiple header lines are one logical list, in order.
	entries := strings.Split(strings.Join(headerLines, ","), ",")

	current := peer
	for i := len(entries) - 1; i >= 0; i-- {
		addr, ok := parseAddr(strings.TrimSpace(entries[i]))
		if !ok {
			return current
		}
		if !rs.isTrusted(addr) {
			return addr
		}
		current = addr
	}
	return current
}

// parseAddr accepts "ip", "ip:port", "[ip]:port" and "[ip]".
func parseAddr(s string) (netip.Addr, bool) {
	if s == "" {
		return netip.Addr{}, false
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return normalize(ap.Addr()), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return normalize(a), true
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if a, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return normalize(a), true
		}
	}
	// A host:port whose port ParseAddrPort rejects still names a host.
	if host, _, err := net.SplitHostPort(s); err == nil {
		if a, err := netip.ParseAddr(host); err == nil {
			return normalize(a), true
		}
	}
	return netip.Addr{}, false
}

func normalize(a netip.Addr) netip.Addr {
	return a.Unmap().WithZone("")
}

type contextKey struct{}

// Middleware resolves the client address once and stores it in the request
// context. r.RemoteAddr is deliberately left untouched.
func (rs *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), contextKey{}, rs.Resolve(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// FromRequest returns the client address resolved by Middleware. Without the
// middleware it degrades to the direct peer, never to a header value.
func FromRequest(r *http.Request) string {
	if ip, ok := r.Context().Value(contextKey{}).(string); ok {
		return ip
	}
	return (*Resolver)(nil).Resolve(r)
}
