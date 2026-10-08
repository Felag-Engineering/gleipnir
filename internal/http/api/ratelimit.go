package api

import (
	"net/http"
	"net/netip"

	"github.com/felag-engineering/gleipnir/internal/infra/clientip"
)

// rateLimitKeyByClientIP keys httprate on the trusted-proxy-aware client IP
// rather than r.RemoteAddr, so a deployment behind a reverse proxy limits per
// real client instead of throttling everyone as the proxy. IPv6 addresses are
// bucketed by /64, matching httprate.KeyByIP: a single host commonly owns a
// whole /64 and could otherwise rotate addresses to dodge the limit.
func rateLimitKeyByClientIP(r *http.Request) (string, error) {
	ip := clientip.FromRequest(r)
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is6() {
		return ip, nil
	}
	return netip.PrefixFrom(addr, 64).Masked().String(), nil
}
