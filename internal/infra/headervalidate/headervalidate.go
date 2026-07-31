// Package headervalidate provides shared HTTP header name validation used by
// internal/mcp (ADR-039 auth headers), internal/plugin/oauth (header_set
// credential strategy), and internal/admin credential handlers.
//
// It lives in internal/infra rather than in any of those packages to prevent
// the import cycle that would arise if internal/plugin/oauth imported
// internal/mcp: internal/mcp/registry.go imports internal/admin (for
// admin.Decrypt), and internal/admin/plugin_oauth_handler.go imports
// internal/plugin/oauth, so internal/plugin/oauth → internal/mcp →
// internal/admin → internal/plugin/oauth would be circular.
package headervalidate

import (
	"fmt"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// ReservedHeaderNames lists HTTP headers that callers must not override via
// plugin or MCP auth header configuration. Two groups:
//
//   - MCP-protocol headers, reserved for the MCP client's own use:
//     "Mcp-Session-Id" is the client-managed session id for the current
//     protocol and is retained through the 12-month deprecation window even
//     after the newer protocol lands. "Mcp-Method", "Mcp-Name", and
//     "Mcp-Protocol-Version" are client-owned headers on the newer
//     protocol's POSTs. "Mcp-Method"/"Mcp-Name" were reserved ahead of the
//     client-side work (#734); "Mcp-Protocol-Version" was reserved by that
//     client-side work itself (#737) — it is required on every modern POST
//     and set-last in internal/mcp.Client.post.
//     ValidateName alone only gates new POST/PUT writes; it cannot
//     retroactively scrub a row that was already persisted before a name
//     joined this list (a "grandfathered" row). Closing that gap for
//     existing rows requires a second, injection-time check at the point a
//     Client is built from stored headers — see
//     internal/mcp.dropReservedAuthHeaders, which drops any stored header
//     matching this list (logging a WARN) before it ever reaches
//     WithAuthHeaders. The two checks together, not ValidateName alone,
//     are what keep a reserved-name header off the wire regardless of when
//     it was configured.
//   - Required HTTP transport headers that must remain under client control.
var ReservedHeaderNames = []string{
	"Mcp-Session-Id",
	"Mcp-Method",
	"Mcp-Name",
	"Mcp-Protocol-Version",
	"Content-Type",
	"Accept",
	"Content-Length",
	"Host",
}

// ValidateName returns an error if name is not a valid HTTP header field name
// for use as an auth header. It rejects:
//   - empty names
//   - names that fail RFC 7230 token syntax (checked via httpguts, which covers
//     CR/LF/NUL/colon/whitespace and all non-token chars)
//   - names that collide with headers managed by the MCP client or the HTTP
//     transport layer (see ReservedHeaderNames)
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("header name must not be empty")
	}
	// httpguts.ValidHeaderFieldName implements RFC 7230 §3.2 token syntax.
	// It is the single source of truth for CR/LF/colon/whitespace rejection.
	if !httpguts.ValidHeaderFieldName(name) {
		return fmt.Errorf("header name %q contains invalid characters", name)
	}
	for _, reserved := range ReservedHeaderNames {
		if strings.EqualFold(name, reserved) {
			return fmt.Errorf("header name %q is reserved and cannot be overridden", name)
		}
	}
	return nil
}
