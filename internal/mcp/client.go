// Package mcp implements the MCP HTTP transport client and tool registry.
// This package must not import internal/agent (package boundary, ADR-001).
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Tool is a tool discovered from an MCP server.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage // JSON Schema object
}

// ToolResult is the response from a tool invocation.
type ToolResult struct {
	Output  json.RawMessage
	IsError bool
}

// JSON-RPC 2.0 wire types used for MCP HTTP transport.

type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonrpcError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

type toolsListResult struct {
	Tools []toolWire `json:"tools"`
}

type toolWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type toolsCallResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Client calls a single MCP server over HTTP transport.
type Client struct {
	serverURL  string
	httpClient *http.Client
}

// NewClient returns a Client targeting serverURL.
func NewClient(serverURL string) *Client {
	return &Client{
		serverURL: serverURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// initializeResult holds the fields we care about from the MCP initialize response.
type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// initialize performs the MCP handshake and returns the session ID assigned by
// the server. Callers must include this ID as "Mcp-Session-Id" on all
// subsequent requests to the same server.
//
// The streamable-HTTP transport requires:
//  1. POST initialize → server replies with Mcp-Session-Id header
//  2. POST notifications/initialized (no response body expected)
//
// Only after that will the server accept method calls like tools/list.
func (c *Client) initialize(ctx context.Context) (string, error) {
	initBody, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "gleipnir",
				"version": "0.1.0",
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal initialize: %w", err)
	}

	resp, err := c.postRaw(ctx, initBody, "")
	if err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	defer resp.Body.Close()

	sessionID := resp.Header.Get("Mcp-Session-Id")

	var initEnvelope jsonrpcResponse
	if err := decodeResponse(resp, &initEnvelope); err != nil {
		return "", fmt.Errorf("decode initialize response: %w", err)
	}
	if initEnvelope.Error != nil {
		return "", fmt.Errorf("initialize error: %w", initEnvelope.Error)
	}
	// Notify the server that initialisation is complete (fire-and-forget; the
	// server sends no response to notifications).
	notifyBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	})
	if err != nil {
		return "", fmt.Errorf("marshal notifications/initialized: %w", err)
	}
	nresp, err := c.postRaw(ctx, notifyBody, sessionID)
	if err != nil {
		return "", fmt.Errorf("notifications/initialized: %w", err)
	}
	io.Copy(io.Discard, nresp.Body) //nolint:errcheck
	nresp.Body.Close()

	return sessionID, nil
}

// DiscoverTools calls the MCP server's tool list endpoint and returns all
// available tools. Used during server registration to populate mcp_tools.
func (c *Client) DiscoverTools(ctx context.Context) ([]Tool, error) {
	sessionID, err := c.initialize(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}

	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
		Params:  struct{}{},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tools/list request: %w", err)
	}

	resp, err := c.postRaw(ctx, body, sessionID)
	if err != nil {
		return nil, fmt.Errorf("post tools/list: %w", err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := decodeResponse(resp, &envelope); err != nil {
		return nil, fmt.Errorf("decode tools/list response: %w", err)
	}
	if envelope.Error != nil {
		return nil, envelope.Error
	}

	var result toolsListResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tools/list result: %w", err)
	}

	tools := make([]Tool, len(result.Tools))
	for i, tw := range result.Tools {
		tools[i] = Tool{
			Name:        tw.Name,
			Description: tw.Description,
			InputSchema: tw.InputSchema,
		}
	}
	return tools, nil
}

// CallTool invokes a named tool on the MCP server with the given input.
// The input must be a JSON-serialisable value matching the tool's inputSchema.
func (c *Client) CallTool(ctx context.Context, name string, input map[string]any) (ToolResult, error) {
	sessionID, err := c.initialize(ctx)
	if err != nil {
		return ToolResult{}, fmt.Errorf("initialize: %w", err)
	}

	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/call",
		Params: struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}{
			Name:      name,
			Arguments: input,
		},
	})
	if err != nil {
		return ToolResult{}, fmt.Errorf("marshal tools/call request: %w", err)
	}

	resp, err := c.postRaw(ctx, body, sessionID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("post tools/call: %w", err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := decodeResponse(resp, &envelope); err != nil {
		return ToolResult{}, fmt.Errorf("decode tools/call response: %w", err)
	}
	if envelope.Error != nil {
		return ToolResult{}, envelope.Error
	}

	var result toolsCallResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return ToolResult{}, fmt.Errorf("unmarshal tools/call result: %w", err)
	}

	output, err := json.Marshal(result.Content)
	if err != nil {
		return ToolResult{}, fmt.Errorf("marshal content array: %w", err)
	}

	return ToolResult{
		Output:  output,
		IsError: result.IsError,
	}, nil
}

// decodeResponse decodes a JSON-RPC response from resp.Body into dst.
// The MCP streamable-HTTP transport may return either plain JSON or an SSE
// stream (Content-Type: text/event-stream). In SSE mode each response is a
// "data: <json>" line; we extract the first such line and decode it.
func decodeResponse(resp *http.Response, dst any) error {
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				return json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), dst)
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read SSE stream: %w", err)
		}
		return fmt.Errorf("no data line found in SSE response")
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// postRaw sends a JSON-RPC request body to c.serverURL and returns the HTTP
// response. sessionID is included as "Mcp-Session-Id" when non-empty.
// It returns an error for non-2xx status codes.
func (c *Client) postRaw(ctx context.Context, body []byte, sessionID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// MCP streamable-HTTP transport requires the client to accept both JSON
	// (for single-response calls) and SSE (for streaming responses).
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		// Drain and close the body so the connection can be reused.
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		return nil, fmt.Errorf("mcp server returned status %d", resp.StatusCode)
	}

	return resp, nil
}
