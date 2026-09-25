package mcpserver

import "net/http"

// handleDiscover serves server/discover per the 2026-07-28 profile,
// enforcing the same two request-validation regimes
// internal/plugin/hostendpoint.Server does on the host side of this
// transport:
//
//   - A4 headers: MCP-Protocol-Version and Mcp-Method are required and must
//     match the body. Mcp-Name does not apply to server/discover.
//   - A1 _meta body fields: protocolVersion and clientCapabilities are
//     required; a missing one is ErrCodeInvalidParams — a caller bug,
//     deliberately distinct from the header-mismatch code so it cannot be
//     misread as a version problem.
//
// The result's capabilities merge every mounted extension's Declaration
// under its own ExtensionID, and declare "tools" only when at least one
// tool is registered — "no tools yet" is a true answer for a channel- or
// events-only plugin, and this server does not claim a capability it does
// not have.
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	meta := decodeMeta(req.Params)

	headerVersion := r.Header.Get("MCP-Protocol-Version")
	if headerVersion == "" {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header is missing", nil)
		return
	}
	if methodHeader := r.Header.Get("Mcp-Method"); methodHeader == "" {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header is missing", nil)
		return
	} else if methodHeader != req.Method {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header value does not match body method", nil)
		return
	}

	bodyVersion, hasVersionField := metaString(meta, MetaKeyProtocolVersion)
	if !hasVersionField {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeInvalidParams,
			"Invalid params: missing required _meta field "+MetaKeyProtocolVersion, nil)
		return
	}
	if _, ok := meta[MetaKeyClientCapabilities]; !ok {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeInvalidParams,
			"Invalid params: missing required _meta field "+MetaKeyClientCapabilities, nil)
		return
	}
	if headerVersion != bodyVersion {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header value does not match body value", nil)
		return
	}

	if bodyVersion != ProtocolVersion {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeUnsupportedProtocolVersion,
			"Unsupported protocol version", map[string]any{
				"supported": []string{ProtocolVersion},
				"requested": bodyVersion,
			})
		return
	}

	capabilities := map[string]any{}
	if len(s.tools) > 0 {
		capabilities["tools"] = map[string]any{}
	}
	if len(s.extensionDecls) > 0 {
		extensions := make(map[string]any, len(s.extensionDecls))
		for id, decl := range s.extensionDecls {
			extensions[id] = decl
		}
		capabilities["extensions"] = extensions
	}

	WriteResult(w, req.ID, map[string]any{
		"resultType":        "complete",
		"supportedVersions": []string{ProtocolVersion},
		"capabilities":      capabilities,
		"_meta": map[string]any{
			MetaKeyServerInfo: map[string]any{
				"name":    s.name,
				"version": s.version,
			},
		},
	})
}
