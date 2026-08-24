package hostclient

import (
	"context"
	"encoding/json"
	"fmt"
)

// _meta field names for server/discover, matching internal/mcp's meta.go
// (MetaKeyProtocolVersion / MetaKeyClientCapabilities / MetaKeyServerInfo)
// exactly. Duplicated rather than imported for the same reason as every
// other host-side constant in this package: internal/mcp lives in the main
// module, and this module must not depend on it.
const (
	metaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaKeyServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// DiscoverResult is server/discover's response, narrowed to what a plugin
// author needs: which protocol versions the endpoint speaks, and its
// self-reported name/version (the host endpoint's ADR-042 per-service
// SemVer, reported here in place of a proto package version).
type DiscoverResult struct {
	SupportedVersions []string
	ServerName        string
	ServerVersion     string
}

// Discover calls server/discover. Authors do not need to call this before
// using the other Client methods — the host endpoint is modern-only and this
// client speaks exactly the one version it supports — but it is useful for a
// plugin's own startup health check, and for confirming which endpoint
// version it is talking to.
func (c *Client) Discover(ctx context.Context) (*DiscoverResult, error) {
	params := map[string]any{
		"_meta": map[string]any{
			metaKeyProtocolVersion:    ProtocolVersion,
			metaKeyClientCapabilities: map[string]any{},
		},
	}
	resultRaw, err := c.doRequest(ctx, "server/discover", "", params)
	if err != nil {
		return nil, err
	}

	var decoded struct {
		SupportedVersions []string                   `json:"supportedVersions"`
		Meta              map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(resultRaw, &decoded); err != nil {
		return nil, fmt.Errorf("hostclient: server/discover: decode result: %w", err)
	}

	result := &DiscoverResult{SupportedVersions: decoded.SupportedVersions}
	if raw, ok := decoded.Meta[metaKeyServerInfo]; ok {
		var info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			return nil, fmt.Errorf("hostclient: server/discover: decode serverInfo: %w", err)
		}
		result.ServerName = info.Name
		result.ServerVersion = info.Version
	}
	return result, nil
}
