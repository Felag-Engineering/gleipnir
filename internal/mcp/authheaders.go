package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/infra/headervalidate"
)

// AuthHeader is a single HTTP header to be injected on every outbound MCP request.
type AuthHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ValidateHeaderName returns an error if name is not a valid HTTP header field
// name for use as an auth header. It rejects:
//   - empty names
//   - names that fail RFC 7230 token syntax (checked via httpguts, which covers
//     CR/LF/NUL/colon/whitespace and all non-token chars)
//   - names that collide with headers managed by the MCP client or the HTTP
//     transport layer (Mcp-Session-Id, Content-Type, Accept, Content-Length, Host)
//
// Delegates to headervalidate.ValidateName; kept here for backward compatibility
// with existing call sites in internal/http/api/mcp_handler.go.
func ValidateHeaderName(name string) error {
	return headervalidate.ValidateName(name)
}

// MarshalAuthHeaders serializes headers to a JSON byte slice suitable for
// encryption. Returns nil, nil for an empty slice (treated identically to
// no configured headers).
func MarshalAuthHeaders(headers []AuthHeader) ([]byte, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return nil, fmt.Errorf("marshal auth headers: %w", err)
	}
	return data, nil
}

// UnmarshalAuthHeaders deserializes headers from the JSON byte slice produced
// by MarshalAuthHeaders. An empty or nil input returns an empty slice.
func UnmarshalAuthHeaders(data []byte) ([]AuthHeader, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var headers []AuthHeader
	if err := json.Unmarshal(data, &headers); err != nil {
		return nil, fmt.Errorf("unmarshal auth headers: %w", err)
	}
	return headers, nil
}
