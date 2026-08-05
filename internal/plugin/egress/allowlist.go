// Package egress implements the host-side forward proxy that turns a plugin's
// manifest-declared, admin-consented egress grants into runtime enforcement
// (ADR-056, mcp-realignment-spec.md §7; issue #812).
//
// Default deny is already established by topology: every instance sits on its
// own `Internal: true` network, so it reaches nothing. This package is the
// GRANT — the one way out, and the place that decides whether a particular
// destination is one the admin consented to.
//
// Why a proxy rather than firewall rules or DNS filtering is recorded in
// docs/developer/egress-containment.md. The short version: it is the only
// mechanism that needs no new host privilege, tunnels TLS without terminating
// it, matches on the NAME the admin consented to rather than addresses that
// rotate underneath it, and can say out loud what it refused.
package egress

import (
	"fmt"
	"strings"
)

// wildcardPrefix is the only wildcard form a grant may use.
const wildcardPrefix = "*."

// Allowlist is one instance's consented destinations.
//
// The zero value allows nothing, which is both the safe default and the
// meaningful one: a plugin declaring `egress: []` reaches nothing, and that is
// a real configuration rather than a missing one.
type Allowlist struct {
	// exact holds lower-cased hostnames granted verbatim.
	exact map[string]struct{}

	// suffixes holds lower-cased parent domains from `*.parent` grants, stored
	// WITH the leading dot ("​.slack.com") so a suffix match cannot accidentally
	// admit "evil-slack.com".
	suffixes []string
}

// NewAllowlist compiles a set of grant domains.
//
// Invalid grants are an error rather than something to skip: a typo'd domain in
// a manifest an admin already consented to should surface at load, not silently
// narrow what the plugin can reach and produce a support question later.
func NewAllowlist(domains []string) (Allowlist, error) {
	list := Allowlist{exact: make(map[string]struct{}, len(domains))}

	for _, raw := range domains {
		domain := strings.ToLower(strings.TrimSpace(raw))
		if err := validateGrant(domain); err != nil {
			return Allowlist{}, fmt.Errorf("egress grant %q: %w", raw, err)
		}
		if strings.HasPrefix(domain, wildcardPrefix) {
			// Stored with the dot: ".slack.com" matches "api.slack.com" and
			// refuses "evil-slack.com", which a bare suffix would admit.
			list.suffixes = append(list.suffixes, domain[1:])
			continue
		}
		list.exact[domain] = struct{}{}
	}
	return list, nil
}

// validateGrant rejects anything that is not a bare host or a single-label
// wildcard. A grant is a destination an admin consented to; a scheme, a port,
// or a path are properties of a request, and accepting them here would make the
// consent screen say something different from what is enforced.
func validateGrant(domain string) error {
	if domain == "" {
		return fmt.Errorf("is empty")
	}
	if domain == wildcardPrefix {
		return fmt.Errorf("is a bare wildcard, which would grant everything")
	}
	body := strings.TrimPrefix(domain, wildcardPrefix)
	if strings.Contains(body, "*") {
		return fmt.Errorf("wildcards are only allowed as a single leading %q", wildcardPrefix)
	}
	if strings.ContainsAny(body, "/:@?# ") {
		return fmt.Errorf("must be a bare host: no scheme, port, path, or credentials")
	}
	if !strings.Contains(body, ".") {
		// A single-label grant is either a typo or an intranet name, and an
		// intranet name resolves into private space, which the proxy refuses
		// anyway. Rejecting it here says so at consent time.
		return fmt.Errorf("must be a dotted host name")
	}
	if strings.HasPrefix(body, ".") || strings.HasSuffix(body, ".") {
		return fmt.Errorf("has a leading or trailing dot")
	}
	return nil
}

// Allows reports whether host is a consented destination.
//
// host is the hostname alone — the caller strips any port first, because a
// grant is about a destination and not about which door of it is knocked on.
func (a Allowlist) Allows(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return false
	}
	if _, ok := a.exact[h]; ok {
		return true
	}
	for _, suffix := range a.suffixes {
		// `.slack.com` admits `api.slack.com` but not `slack.com` itself: a
		// wildcard grants the children, and consenting to a parent is a
		// separate decision the admin can also make explicitly.
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// Empty reports whether the list grants nothing. Used for logging, not for
// control flow — an empty list already refuses everything through Allows.
func (a Allowlist) Empty() bool {
	return len(a.exact) == 0 && len(a.suffixes) == 0
}

// Size reports how many grants the list holds.
func (a Allowlist) Size() int { return len(a.exact) + len(a.suffixes) }
