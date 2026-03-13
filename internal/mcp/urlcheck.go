package mcp

import (
	"context"
	"fmt"
	"net"
	"net/url"
)

// linkLocalNets contains address ranges that are never legitimate MCP server
// destinations: link-local unicast (169.254.0.0/16, fe80::/10) and
// link-local multicast. RFC 1918 private and loopback addresses are
// intentionally absent — homelab MCP servers routinely live there.
var linkLocalNets = func() []*net.IPNet {
	cidrs := []string{
		"169.254.0.0/16", // IPv4 link-local (cloud metadata, APIPA)
		"fe80::/10",      // IPv6 link-local unicast
		"ff02::/16",      // IPv6 link-local multicast
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, n, _ := net.ParseCIDR(cidr)
		nets = append(nets, n)
	}
	return nets
}()

// isUnsafeIP returns true for addresses that should never be MCP server
// targets: link-local unicast, link-local multicast, and the unspecified
// address (0.0.0.0 / ::).
func isUnsafeIP(ip net.IP) bool {
	if ip.IsUnspecified() {
		return true
	}
	for _, n := range linkLocalNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateServerURL checks that rawURL is a structurally valid MCP server URL.
// It allows both http:// and https://, permits private IPs and loopback
// (homelab use), and blocks only link-local ranges (169.254.x.x, fe80::)
// since those indicate cloud metadata endpoints or misconfigured addresses.
func ValidateServerURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("url must not be empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}

	if u.Scheme == "" {
		return fmt.Errorf("url must have a scheme (http or https)")
	}
	if u.Host == "" {
		return fmt.Errorf("url must have a host")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme %q is not allowed; only http and https are permitted", u.Scheme)
	}

	// If the host is an IP literal we can check it directly without a DNS lookup.
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isUnsafeIP(ip) {
			return fmt.Errorf("url targets a link-local address (%s), which is not a valid MCP server destination", ip)
		}
		return nil
	}

	// Hostname — resolve and block only if every address is link-local.
	addrs, err := net.DefaultResolver.LookupHost(context.Background(), host)
	if err != nil {
		// Resolution failure is not a security issue; allow the URL and let
		// the actual connection attempt surface the error.
		return nil
	}

	allUnsafe := len(addrs) > 0
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || !isUnsafeIP(ip) {
			allUnsafe = false
			break
		}
	}
	if allUnsafe {
		return fmt.Errorf("url %q resolves only to link-local addresses, which are not valid MCP server destinations", rawURL)
	}

	return nil
}

// checkRedirectTarget returns an error if the redirect URL uses a non-HTTP(S)
// scheme or targets a link-local IP address. It is intended for use as an
// http.Client.CheckRedirect hook.
func checkRedirectTarget(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("redirect to non-HTTP scheme %q is not allowed", u.Scheme)
	}

	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isUnsafeIP(ip) {
			return fmt.Errorf("redirect to link-local address %s is not allowed", ip)
		}
	}

	return nil
}
