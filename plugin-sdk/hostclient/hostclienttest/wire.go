package hostclienttest

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The wire shapes and error codes below are reproduced from the real host
// endpoint (internal/plugin/hostendpoint/{server,toolcall,middleware}.go)
// rather than imported — this module must not depend on internal/* (see
// doc.go). Values are wire facts pinned by
// docs/developer/host-endpoint-contract.md, not implementation details, so
// duplicating them here is the same trade the rest of plugin-sdk already
// makes (hostclient/client.go's InstanceTokenEnvVar comment explains why).

// JSON-RPC error codes (contract §2.2).
const (
	errCodeHeaderMismatch             = -32020
	errCodeUnsupportedProtocolVersion = -32022
	errCodeInvalidParams              = -32602
	errCodeMethodNotFound             = -32601
)

// protocolVersion is the one MCP revision the host endpoint speaks (contract
// §2). Matches hostclient.ProtocolVersion; kept as an unexported local
// constant rather than referencing hostclient's so this file reads the same
// way the real server's own wire code does.
const protocolVersion = "2026-07-28"

// _meta field names (contract §2.1 A1), matching internal/mcp's meta.go.
const (
	metaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaKeyServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// callIDHeader carries the Gleipnir-Call-Id correlation header (contract
// §3.3). Matches hostclient.CallIDHeader.
const callIDHeader = "Gleipnir-Call-Id"

// jsonrpcRequest is the wire shape server/discover, tools/list, and
// tools/call all take: one JSON-RPC request per POST, no session, no
// batching (the stateless 2026-07-28 streamable HTTP profile).
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// toolError is a tool-execution failure with a stable machine-readable code,
// reported as an isError tool result rather than a JSON-RPC error (contract
// §2.2) — a handler ran and refused, as opposed to the call never reaching
// one. Mirrors hostendpoint.ToolError.
type toolError struct {
	Code    string
	Message string
}

func (e *toolError) Error() string { return e.Code + ": " + e.Message }

// toolHandler executes one host/* tool. Mirrors hostendpoint.ToolHandler.
type toolHandler func(r *http.Request, args json.RawMessage) (result any, err error)

// bearerToken extracts the credential from an Authorization header, the same
// rule the real middleware applies (internal/plugin/hostendpoint/middleware.go):
// exactly the Bearer scheme, case-insensitive, with a non-empty credential.
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

// decodeMeta extracts params._meta as a raw map so key presence is
// distinguishable from a zero value. nil on any decode failure.
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
// presence, independent of value.
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

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      normalizeID(id),
		"result":  result,
	})
}

func writeError(w http.ResponseWriter, id json.RawMessage, status, code int, message string, data any) {
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
// request never carried one.
func normalizeID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}
