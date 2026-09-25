package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

// ToolHandler executes one registered tool. args is the tools/call
// arguments object, verbatim — never nil; an absent or empty arguments
// object normalizes to "{}" before the handler runs.
type ToolHandler func(ctx context.Context, args json.RawMessage) (result any, err error)

// Tool declares one tool RegisterTool backs with tools/list and tools/call.
type Tool struct {
	Name        string
	Description string

	// InputSchema is the tool's JSON Schema, rendered verbatim in tools/list.
	// A nil/empty schema renders as {"type":"object"} — a real schema is
	// always better, but an absent one must not make the tool unlistable.
	InputSchema json.RawMessage

	Handler ToolHandler
}

// defaultInputSchema backs a Tool registered with no InputSchema.
var defaultInputSchema = json.RawMessage(`{"type":"object"}`)

// RegisterTool adds t to this server's tools/list and tools/call inventory.
// Returns an error on an empty name, a nil handler, or a name already
// registered — a plugin wiring two tools under the same name is a startup
// bug to fail loudly on, not a race to resolve at call time.
func (s *Server) RegisterTool(t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("mcpserver: tool name is required")
	}
	if t.Handler == nil {
		return fmt.Errorf("mcpserver: tool %q registered with a nil handler", t.Name)
	}
	if _, dup := s.tools[t.Name]; dup {
		return fmt.Errorf("mcpserver: tool %q registered twice", t.Name)
	}
	s.tools[t.Name] = t
	return nil
}

// handleToolsList renders the registered inventory sorted by name — a
// deterministic order matching internal/mcp's own tools/list ordering
// discipline (#748), so two discover passes against an unchanged server
// produce byte-identical tool lists.
func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	if !s.enforceMethodHeaders(w, r, req, "") {
		return
	}

	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		t := s.tools[name]
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = defaultInputSchema
		}
		tools = append(tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	WriteResult(w, req.ID, map[string]any{"tools": tools})
}

// toolsCallParams is the params shape of a tools/call request.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// handleToolsCall dispatches one tool invocation. Transport-level problems
// (headers, unknown tool) are JSON-RPC errors; a handler failure — returned
// or recovered from a panic — is an isError tool result, per the standard
// tools/call contract: a caller distinguishes "the call never happened" from
// "the tool ran and refused".
func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeInvalidParams, "tools/call requires params.name", nil)
		return
	}
	if !s.enforceMethodHeaders(w, r, req, params.Name) {
		return
	}

	t, ok := s.tools[params.Name]
	if !ok {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeInvalidParams,
			"unknown tool: "+params.Name, nil)
		return
	}

	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	result, err := invokeTool(r.Context(), t, args)
	if err != nil {
		WriteResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		})
		return
	}

	payload, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		WriteResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "internal: marshal tool result: " + marshalErr.Error()}},
			"isError": true,
		})
		return
	}
	WriteResult(w, req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(payload)}},
		"isError": false,
	})
}

// invokeTool calls t.Handler, recovering a panic into an error rather than
// crashing the whole process — a bug in one tool must not take down every
// other capability this server serves.
func invokeTool(ctx context.Context, t Tool, args json.RawMessage) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return t.Handler(ctx, args)
}
