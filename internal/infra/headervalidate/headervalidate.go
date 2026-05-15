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
// plugin or MCP auth header configuration. These are either managed by the
// MCP client itself or are required HTTP transport headers that must remain
// under client control.
var ReservedHeaderNames = []string{
	"Mcp-Session-Id",
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
