package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// MaskedHeaderValue is the sentinel string used in API requests to indicate
// "preserve the existing value" when updating auth headers. A client that
// receives this value in a PUT body signals that it has not changed the header
// — the backend copies the existing decrypted value from storage.
//
// Any other value (including empty string) is written as-is.
// This mirrors the isMaskedKey pattern in internal/admin/openai_compat_handler.go.
const MaskedHeaderValue = "••••••••"

// reservedHeaderNames lists the headers that operators must not override via
// auth headers. These are either managed by the MCP client itself or are
// required HTTP transport headers that must remain under client control.
var reservedHeaderNames = []string{
	"Mcp-Session-Id",
	"Content-Type",
	"Accept",
	"Content-Length",
	"Host",
}

// AuthHeader is a single HTTP header to be injected on every outbound MCP request.
type AuthHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// IsMaskedHeaderValue reports whether v is the masked sentinel constant.
func IsMaskedHeaderValue(v string) bool {
	return v == MaskedHeaderValue
}

// ValidateHeaderName returns an error if name is not a valid HTTP header field
// name for use as an auth header. It rejects:
//   - empty names
//   - names that fail RFC 7230 token syntax (checked via httpguts, which covers
//     CR/LF/NUL/colon/whitespace and all non-token chars)
//   - names that collide with headers managed by the MCP client or the HTTP
//     transport layer (Mcp-Session-Id, Content-Type, Accept, Content-Length, Host)
func ValidateHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("header name must not be empty")
	}
	// httpguts.ValidHeaderFieldName implements RFC 7230 §3.2 token syntax.
	// It is the single source of truth for CR/LF/colon/whitespace rejection.
	if !httpguts.ValidHeaderFieldName(name) {
		return fmt.Errorf("header name %q contains invalid characters", name)
	}
	for _, reserved := range reservedHeaderNames {
		if strings.EqualFold(name, reserved) {
			return fmt.Errorf("header name %q is reserved and cannot be overridden", name)
		}
	}
	return nil
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
