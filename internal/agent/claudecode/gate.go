package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
)

// jsonrpcRequest is the wire type for an incoming JSON-RPC 2.0 request.
// ID is json.RawMessage because the spec allows string, number, or null —
// we echo the raw bytes back verbatim to guarantee correct round-tripping.
// Notifications (e.g. notifications/initialized) have no ID field; after
// unmarshal, ID will be nil.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse is the wire type for an outgoing JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ApprovalBridge bundles callbacks the gate server needs to interact with
// Gleipnir's approval infrastructure. The caller (ClaudeCodeAgent) provides
// implementations that close over AuditWriter and RunStateMachine.
//
// Using callbacks keeps the gate server decoupled from AuditWriter and
// RunStateMachine, which simplifies testing and keeps gate.go's import
// footprint minimal (only internal/model needed).
type ApprovalBridge struct {
	// RequestApproval atomically writes the approval_request audit step,
	// creates the DB approval record, transitions the run to
	// waiting_for_approval, and publishes the SSE event. Returns the
	// approval ID so the gate can log it.
	RequestApproval func(ctx context.Context, toolName string, proposedInput map[string]any) (approvalID string, err error)

	// ResumeRunning transitions the run back to running after approval.
	ResumeRunning func(ctx context.Context) error
}

// ToolGrant describes the approval policy for a single tool.
type ToolGrant struct {
	Approval model.ApprovalMode
	Timeout  time.Duration // zero means use the 1-hour default
}

// GateConfig holds all the configuration needed to run a GateServer.
type GateConfig struct {
	RunID      string
	ToolGrants map[string]ToolGrant // keyed by tool name as Claude Code sees it
	Bridge     ApprovalBridge
	ApprovalCh <-chan bool
}

// GateServer is a minimal stdio MCP server that implements Claude Code's
// --permission-prompt-tool contract. It receives tool permission requests and
// responds with allow or deny based on the run's capability grants.
//
// One request is processed at a time — Claude Code's permission-prompt-tool
// protocol is synchronous.
type GateServer struct {
	config  GateConfig
	scanner *bufio.Scanner
	encoder *json.Encoder
}

// NewGateServer creates a GateServer that reads from stdin and writes to stdout.
// Uses bufio.Scanner (not json.Decoder) for line-oriented reading consistent
// with ParseStream; this allows clean shutdown when stdin is closed (EOF).
func NewGateServer(stdin io.Reader, stdout io.Writer, cfg GateConfig) *GateServer {
	scanner := bufio.NewScanner(stdin)
	// 2MB buffer matches ParseStream — tool inputs can be large.
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	return &GateServer{
		config:  cfg,
		scanner: scanner,
		encoder: json.NewEncoder(stdout),
	}
}

// Run is the main request/response loop. It reads JSON-RPC requests line by
// line, dispatches each to the appropriate handler, and writes responses.
// Returns nil on clean EOF (stdin closed). Returns ctx.Err() on cancellation.
func (g *GateServer) Run(ctx context.Context) error {
	for {
		// Check for cancellation before blocking on Scan.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !g.scanner.Scan() {
			// EOF or scanner error — clean shutdown.
			return nil
		}

		line := g.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonrpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			slog.WarnContext(ctx, "gate: malformed JSON-RPC request", "err", err)
			continue
		}

		// Notifications have no ID (nil after unmarshal). Per JSON-RPC 2.0,
		// a server must not send a response to a notification.
		isNotification := req.ID == nil || string(req.ID) == "null"
		if isNotification {
			// Still dispatch so the server can update any local state if needed,
			// but the handler must not return a response we should send.
			continue
		}

		resp := g.dispatch(ctx, req)
		if err := g.encoder.Encode(resp); err != nil {
			slog.WarnContext(ctx, "gate: failed to write response", "err", err)
		}
	}
}

// dispatch routes a request to the correct handler.
func (g *GateServer) dispatch(ctx context.Context, req jsonrpcRequest) jsonrpcResponse {
	switch req.Method {
	case "initialize":
		return g.handleInitialize(req.ID)
	case "notifications/initialized":
		// Should not reach here — notifications are skipped before dispatch.
		// Return a no-op; this path is a safety net only.
		return makeError(req.ID, -32600, "notifications must not be responded to")
	case "tools/list":
		return g.handleToolsList(req.ID)
	case "tools/call":
		return g.handleToolCall(ctx, req.ID, req.Params)
	default:
		return makeError(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

// handleInitialize responds to the MCP initialize handshake. The exact payload
// is required by the MCP protocol — clients validate protocolVersion.
func (g *GateServer) handleInitialize(id json.RawMessage) jsonrpcResponse {
	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "gleipnir-gate", "version": "0.1.0"},
	}
	return makeResult(id, result)
}

// handleToolsList returns the single gleipnir_gate tool that Claude Code will
// invoke for every permission check.
func (g *GateServer) handleToolsList(id json.RawMessage) jsonrpcResponse {
	result := map[string]any{
		"tools": []map[string]any{
			{
				"name":        "gleipnir_gate",
				"description": "Checks whether a tool call is permitted by Gleipnir policy",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"toolName": map[string]string{
							"type":        "string",
							"description": "The MCP tool name being invoked",
						},
						"input": map[string]string{
							"type":        "object",
							"description": "The proposed tool input",
						},
					},
					"required": []string{"toolName", "input"},
				},
			},
		},
	}
	return makeResult(id, result)
}

// toolCallParams is the outer MCP envelope for a tools/call request.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// gateArguments is the permission-prompt-tool payload inside the MCP envelope.
type gateArguments struct {
	ToolName string         `json:"toolName"`
	Input    map[string]any `json:"input"`
}

// handleToolCall processes a tools/call request. Claude Code sends permission
// checks as:
//
//	{ "name": "gleipnir_gate", "arguments": { "toolName": "...", "input": {...} } }
//
// Two-step parse: first unwrap the MCP envelope, then unwrap the gate payload.
// Fail-closed: any parse or bridge error returns a deny response.
func (g *GateServer) handleToolCall(ctx context.Context, id json.RawMessage, params json.RawMessage) jsonrpcResponse {
	var mcp toolCallParams
	if err := json.Unmarshal(params, &mcp); err != nil {
		slog.WarnContext(ctx, "gate: failed to parse tools/call params", "err", err)
		return makeError(id, -32602, "invalid params: cannot parse tools/call envelope")
	}
	if mcp.Name != "gleipnir_gate" {
		return makeError(id, -32602, fmt.Sprintf("unknown tool: %s", mcp.Name))
	}

	var args gateArguments
	if err := json.Unmarshal(mcp.Arguments, &args); err != nil {
		slog.WarnContext(ctx, "gate: failed to parse gate arguments", "err", err)
		return makeError(id, -32602, "invalid params: cannot parse gate arguments")
	}

	grant, granted := g.config.ToolGrants[args.ToolName]
	if !granted {
		behavior, _ := json.Marshal(map[string]string{
			"behavior": "deny",
			"message":  fmt.Sprintf("tool %q not granted by policy", args.ToolName),
		})
		return makeToolResult(id, string(behavior))
	}

	if grant.Approval == model.ApprovalModeNone {
		behavior, _ := json.Marshal(map[string]any{
			"behavior":     "allow",
			"updatedInput": args.Input,
		})
		return makeToolResult(id, string(behavior))
	}

	// Approval required — block until the operator decides, times out, or context is cancelled.
	approved, err := g.waitForApproval(ctx, args.ToolName, args.Input, grant)
	if err != nil {
		slog.WarnContext(ctx, "gate: approval wait error", "tool", args.ToolName, "err", err)
		behavior, _ := json.Marshal(map[string]string{
			"behavior": "deny",
			"message":  fmt.Sprintf("approval error for tool %q: %s", args.ToolName, err.Error()),
		})
		return makeToolResult(id, string(behavior))
	}
	if approved {
		behavior, _ := json.Marshal(map[string]any{
			"behavior":     "allow",
			"updatedInput": args.Input,
		})
		return makeToolResult(id, string(behavior))
	}

	behavior, _ := json.Marshal(map[string]string{
		"behavior": "deny",
		"message":  fmt.Sprintf("tool %q was rejected by operator", args.ToolName),
	})
	return makeToolResult(id, string(behavior))
}

// waitForApproval calls the bridge to record the approval request and then
// blocks until the operator approves/rejects, the timeout fires, or ctx is
// cancelled. Returns (true, nil) for approval, (false, nil) for operator
// rejection or closed channel, (false, err) for timeout or other errors.
func (g *GateServer) waitForApproval(ctx context.Context, toolName string, input map[string]any, grant ToolGrant) (bool, error) {
	approvalID, err := g.config.Bridge.RequestApproval(ctx, toolName, input)
	if err != nil {
		return false, fmt.Errorf("requesting approval for %q: %w", toolName, err)
	}
	slog.InfoContext(ctx, "gate: approval requested", "run_id", g.config.RunID, "tool", toolName, "approval_id", approvalID)

	timeout := grant.Timeout
	if timeout <= 0 {
		timeout = time.Hour
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case approved, ok := <-g.config.ApprovalCh:
		if !ok {
			// Channel was closed — treat as rejection (fail-closed).
			slog.WarnContext(ctx, "gate: approval channel closed", "run_id", g.config.RunID, "tool", toolName)
			return false, nil
		}
		if !approved {
			return false, nil
		}
		if err := g.config.Bridge.ResumeRunning(ctx); err != nil {
			return false, fmt.Errorf("resuming run after approval of %q: %w", toolName, err)
		}
		return true, nil

	case <-timer.C:
		slog.WarnContext(ctx, "gate: approval timeout", "run_id", g.config.RunID, "tool", toolName, "timeout", timeout)
		// Return a non-nil error so handleToolCall can include "timeout" in the
		// deny message rather than "rejected". (false, nil) is reserved for
		// operator rejection, which has a different denial message.
		return false, fmt.Errorf("approval timeout for tool %q after %s", toolName, timeout)

	case <-ctx.Done():
		return false, fmt.Errorf("context cancelled waiting for approval: %w", ctx.Err())
	}
}

// makeResult builds a successful JSON-RPC response. The result is marshalled;
// if marshalling fails the response will have an empty result (the caller
// logged the error already).
func makeResult(id json.RawMessage, result any) jsonrpcResponse {
	raw, _ := json.Marshal(result)
	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  raw,
	}
}

// makeError builds a JSON-RPC error response.
func makeError(id json.RawMessage, code int, message string) jsonrpcResponse {
	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcError{Code: code, Message: message},
	}
}

// makeToolResult wraps a behavior JSON string in the MCP tool-result content
// envelope. isError stays false for all outcomes — deny is a policy decision,
// not an execution error.
func makeToolResult(id json.RawMessage, behaviorJSON string) jsonrpcResponse {
	content := map[string]any{
		"content": []map[string]string{
			{"type": "text", "text": behaviorJSON},
		},
		"isError": false,
	}
	return makeResult(id, content)
}
