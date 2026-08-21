package hostendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/felag-engineering/gleipnir/internal/mcp"
)

// ToolHandler executes one host tool. args is the tools/call arguments
// object, verbatim. A returned *ToolError becomes an isError tool result
// with a machine-readable "code: message" text; any other error is reported
// as code "internal".
type ToolHandler func(ctx context.Context, args json.RawMessage) (result any, err error)

// ToolDef declares one tool on the host endpoint.
type ToolDef struct {
	Name        string
	Description string
	Handler     ToolHandler
}

// ToolError is a tool-execution failure with a stable machine-readable code.
// Codes reuse hostsvc's error-code strings (cardinality_cap_exceeded,
// permission_denied, …) so a plugin author migrating from the gRPC plane
// matches on the same identifiers.
type ToolError struct {
	Code    string
	Message string
}

func (e *ToolError) Error() string { return e.Code + ": " + e.Message }

// Register mounts tool definitions on the server. Registering a name outside
// ToolNames() panics: the host-plane assertion (#875) guards exactly the
// declared inventory, and a tool it never heard of would be a tool it never
// guarded. Panic-on-boot, same posture as an unknown route.
func (s *Server) Register(defs ...ToolDef) {
	inventory := make(map[string]bool, len(ToolNames()))
	for _, n := range ToolNames() {
		inventory[n] = true
	}
	for _, def := range defs {
		if !inventory[def.Name] {
			panic(fmt.Sprintf("hostendpoint: tool %q is not in the declared host-tool inventory (tools.go)", def.Name))
		}
		if def.Handler == nil {
			panic(fmt.Sprintf("hostendpoint: tool %q registered with a nil handler", def.Name))
		}
		if _, dup := s.tools[def.Name]; dup {
			panic(fmt.Sprintf("hostendpoint: tool %q registered twice", def.Name))
		}
		s.tools[def.Name] = def
	}
}

// handleToolsList serves the registered inventory. Input schemas are
// deliberately permissive objects for now — the SDK client (#882) calls by
// name with typed wrappers, and the milestone-20 conformance suite is where
// the argument shapes get pinned; a hand-maintained schema here would drift
// from the handlers with nothing to catch it.
func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	if !s.enforceToolTransport(w, r, req, "") {
		return
	}
	tools := make([]map[string]any, 0, len(s.tools))
	for _, name := range ToolNames() { // inventory order: stable across processes
		def, ok := s.tools[name]
		if !ok {
			continue
		}
		tools = append(tools, map[string]any{
			"name":        def.Name,
			"description": def.Description,
			"inputSchema": map[string]any{"type": "object"},
		})
	}
	writeResult(w, req.ID, map[string]any{"tools": tools})
}

// toolsCallParams is the params shape of a tools/call request.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// handleToolsCall dispatches one tool invocation. Transport-level problems
// (headers, unknown tool) are JSON-RPC errors; handler failures are tool
// results with isError, per the standard tools/call contract — an agent-side
// caller distinguishes "the call never happened" from "the tool ran and
// refused", and the plugin SDK needs the same distinction.
func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeInvalidParams, "tools/call requires params.name", nil)
		return
	}
	if !s.enforceToolTransport(w, r, req, params.Name) {
		return
	}
	def, ok := s.tools[params.Name]
	if !ok {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeInvalidParams,
			"unknown tool: "+params.Name, nil)
		return
	}

	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	result, err := def.Handler(r.Context(), args)
	if err != nil {
		var te *ToolError
		if !errors.As(err, &te) {
			te = &ToolError{Code: "internal", Message: err.Error()}
		}
		writeResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": te.Error()}},
			"isError": true,
		})
		return
	}

	payload, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		writeResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "internal: marshal tool result: " + marshalErr.Error()}},
			"isError": true,
		})
		return
	}
	writeResult(w, req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(payload)}},
		"isError": false,
	})
}

// enforceToolTransport applies the A4 header rules to tool traffic:
// MCP-Protocol-Version and Mcp-Method always; Mcp-Name must equal the called
// tool's name on tools/call (expectedName != ""). Tool traffic carries no
// required _meta body fields — that is server/discover's A1 rule.
func (s *Server) enforceToolTransport(w http.ResponseWriter, r *http.Request, req jsonrpcRequest, expectedName string) bool {
	if r.Header.Get("MCP-Protocol-Version") == "" {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header is missing", nil)
		return false
	}
	methodHeader := r.Header.Get("Mcp-Method")
	if methodHeader == "" {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header is missing", nil)
		return false
	}
	if methodHeader != req.Method {
		writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header value does not match body method", nil)
		return false
	}
	if expectedName != "" {
		if name := r.Header.Get("Mcp-Name"); name != expectedName {
			writeError(w, req.ID, http.StatusBadRequest, mcp.ErrCodeHeaderMismatch,
				"Header mismatch: Mcp-Name header value does not match called tool", nil)
			return false
		}
	}
	return true
}
