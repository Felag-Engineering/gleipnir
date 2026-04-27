package arcade

import (
	"net/url"
	"strings"
)

const (
	arcadeHost       = "api.arcade.dev"
	arcadePathPrefix = "/mcp/"
)

// IsArcadeGateway reports whether the given MCP server is an Arcade gateway.
// Detection requires all three conditions:
//   - The host is exactly api.arcade.dev (case-insensitive).
//   - The URL path starts with /mcp/.
//   - The header names include both "Authorization" and "Arcade-User-ID"
//     (case-insensitive match).
//
// Returns false on URL parse failure. This heuristic avoids the need for
// a DB discriminator column (ADR-040).
func IsArcadeGateway(rawURL string, headerNames []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Hostname(), arcadeHost) {
		return false
	}
	if !strings.HasPrefix(u.Path, arcadePathPrefix) {
		return false
	}

	hasAuth := false
	hasUserID := false
	for _, name := range headerNames {
		if strings.EqualFold(name, "Authorization") {
			hasAuth = true
		}
		if strings.EqualFold(name, "Arcade-User-ID") {
			hasUserID = true
		}
	}
	return hasAuth && hasUserID
}
