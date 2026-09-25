package mcpserver

import (
	"encoding/json"
	"net/http"
)

// ProtocolVersion is the sole MCP transport revision a Server speaks. There
// is no legacy `initialize` handshake anywhere in this package; the constant
// exists so a caller building its own test client can pin against it rather
// than repeating the literal.
const ProtocolVersion = "2026-07-28"

// _meta keys used on every 2026-07-28 request/response. These mirror
// internal/mcp/meta.go byte for byte, duplicated rather than imported
// because plugin-sdk must not depend on the host module (see the package
// doc) — the strings are part of the wire contract both sides implement
// independently, not an implementation detail either side could change on
// its own.
const (
	MetaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	MetaKeyServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// JSON-RPC / MCP error codes this package returns. Mirrors
// internal/mcp/errorcodes.go for the reason given above.
const (
	// ErrCodeHeaderMismatch covers every required standard transport header
	// failure: a required header (MCP-Protocol-Version, Mcp-Method, Mcp-Name)
	// is missing, or a header value does not match the corresponding request
	// body value.
	ErrCodeHeaderMismatch = -32020

	// ErrCodeUnsupportedProtocolVersion is returned when a caller requests a
	// protocol version this server does not speak. Its "data.supported"
	// lists this server's ProtocolVersion.
	ErrCodeUnsupportedProtocolVersion = -32022

	// ErrCodeInvalidParams covers a missing required `_meta` body field (a
	// caller compliance bug), or a malformed tools/call params object. Kept
	// distinct from ErrCodeHeaderMismatch so a caller cannot misread one for
	// the other.
	ErrCodeInvalidParams = -32602

	// ErrCodeMethodNotFound is returned for the legacy `initialize`
	// handshake and any other method this server does not implement.
	ErrCodeMethodNotFound = -32601
)

const (
	methodServerDiscover = "server/discover"
	methodToolsList      = "tools/list"
	methodToolsCall      = "tools/call"
)

// jsonrpcRequest is the wire shape this server accepts: the stateless
// 2026-07-28 streamable-HTTP profile, one JSON-RPC request per POST, no
// session, no batching.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// WriteResult writes a successful JSON-RPC 2.0 response. Exported so a
// MethodFunc mounted via Mount renders exactly the same wire shape this
// server's own methods do.
func WriteResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      normalizeID(id),
		"result":  result,
	})
}

// WriteError writes a JSON-RPC 2.0 error response with the given HTTP
// status. Exported for the same reason WriteResult is.
func WriteError(w http.ResponseWriter, id json.RawMessage, status, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	errObj := map[string]any{
		"code":    code,
		"message": message,
	}
	if data != nil {
		errObj["data"] = data
	}
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      normalizeID(id),
		"error":   errObj,
	})
}

// normalizeID echoes the caller's request id, or explicit null when the
// request never carried one (per JSON-RPC 2.0 error handling for requests
// whose id could not be read).
func normalizeID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

// decodeMeta extracts params._meta as a raw map so key presence is
// distinguishable from a zero value. nil on any decode failure — an absent
// or malformed _meta is a validation finding the caller reports.
func decodeMeta(params json.RawMessage) map[string]json.RawMessage {
	var body struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return nil
	}
	return body.Meta
}

// metaString reads a string-valued _meta field. The bool reports key
// presence, which matters independently of the value: a present-but-empty
// protocolVersion is a header-comparison problem, not a missing-field one.
func metaString(meta map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := meta[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", true
	}
	return s, true
}
