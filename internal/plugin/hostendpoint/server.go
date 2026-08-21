package hostendpoint

import (
	"encoding/json"
	"net/http"

	"github.com/felag-engineering/gleipnir/internal/mcp"
)

// ServerName identifies the host endpoint in server/discover's
// _meta serverInfo. One fixed name for the one host endpoint; the per-service
// version next to it is what ADR-042's SemVer discipline attaches to.
const ServerName = "gleipnir-host-endpoint"

// Version is the host endpoint's own contract version, reported via
// server/discover (spec §8 "Versioning": the endpoint declares its version
// through its own discover, in place of proto package versions). Bump per
// ADR-042's per-service SemVer discipline as method issues land; 0.x while
// the milestone is in flight.
const Version = "0.1.0"

// Server is the host-side MCP server. It implements http.Handler so the
// ListenerSet can serve the same instance across every per-instance network
// listener; it holds no per-instance state itself — the caller's identity
// arrives with the request (token middleware, #876), not with the listener.
//
// The skeleton serves server/discover only. Tool dispatch is attached by the
// method issues (#877–#881); until then tools/list reports an empty
// inventory rather than method-not-found, because "no tools yet" is a true
// answer and a modern client's first move after discover is tools/list.
type Server struct {
	// tools is the registered host-tool set, mounted via Register at
	// construction time and read-only afterwards — no lock, because a tool
	// registered after the listeners start would be a tool the boot-time
	// host-plane assertion never saw.
	tools map[string]ToolDef
}

// NewServer returns the host-endpoint MCP server. Mount tools with Register
// before handing it to a ListenerSet.
func NewServer() *Server {
	return &Server{tools: make(map[string]ToolDef)}
}

// jsonrpcRequest is the wire shape this server accepts. The transport is the
// stateless 2026-07-28 streamable HTTP profile: one JSON-RPC request per
// POST, no session, no batching.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// The transport is POST-only; anything else is not MCP traffic.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req jsonrpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, nil, http.StatusBadRequest, mcp.ErrCodeInvalidParams, "invalid JSON-RPC request body", nil)
		return
	}

	switch req.Method {
	case "server/discover":
		s.handleDiscover(w, r, req)
	case "tools/list":
		s.handleToolsList(w, r, req)
	case "tools/call":
		s.handleToolsCall(w, r, req)
	default:
		// Includes legacy `initialize`: the host endpoint is modern-only by
		// construction (see doc.go), so the legacy handshake is an
		// unimplemented method here, not a dialect to negotiate down to.
		writeError(w, req.ID, http.StatusNotFound, mcp.ErrCodeMethodNotFound,
			"method not found: "+req.Method, nil)
	}
}

// handleDiscover serves server/discover per the 2026-07-28 profile,
// enforcing the same two request-validation regimes internal/mcp's
// FakeMCPServer pins for the client (fakeserver.go is the in-repo oracle for
// this transport; the shapes here deliberately match it):
//
//   - A4 headers: MCP-Protocol-Version and Mcp-Method are required and must
//     match the body. Mcp-Name does not apply to server/discover, and a
//     superfluous one has no spec-defined rejection, so it is ignored.
//   - A1 _meta body fields: protocolVersion and clientCapabilities are
//     required; a missing one is ErrCodeInvalidParams — a client bug, and
//     deliberately distinct from the header-mismatch code so it cannot be
//     misread as a version problem.
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	meta := decodeMeta(req.Params)

	headerVersion := r.Header.Get("MCP-Protocol-Version")
	if headerVersion == "" {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header is missing", nil)
		return
	}
	if methodHeader := r.Header.Get("Mcp-Method"); methodHeader == "" {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header is missing", nil)
		return
	} else if methodHeader != req.Method {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header value does not match body method", nil)
		return
	}

	bodyVersion, hasVersionField := metaString(meta, mcp.MetaKeyProtocolVersion)
	if !hasVersionField {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeInvalidParams,
			"Invalid params: missing required _meta field "+mcp.MetaKeyProtocolVersion, nil)
		return
	}
	if _, ok := meta[mcp.MetaKeyClientCapabilities]; !ok {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeInvalidParams,
			"Invalid params: missing required _meta field "+mcp.MetaKeyClientCapabilities, nil)
		return
	}
	if headerVersion != bodyVersion {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header value does not match body value", nil)
		return
	}

	if bodyVersion != mcp.ProtocolVersion20260728 {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeUnsupportedProtocolVersion,
			"Unsupported protocol version", map[string]any{
				"supported": []string{mcp.ProtocolVersion20260728},
				"requested": bodyVersion,
			})
		return
	}

	writeResult(w, req.ID, map[string]any{
		"resultType":        "complete",
		"supportedVersions": []string{mcp.ProtocolVersion20260728},
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"_meta": map[string]any{
			mcp.MetaKeyServerInfo: map[string]any{
				"name":    ServerName,
				"version": Version,
			},
		},
	})
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
// request never carried one (per JSON-RPC 2.0 error handling for requests
// whose id could not be read).
func normalizeID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}
