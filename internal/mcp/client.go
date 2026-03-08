// Package mcp implements the MCP HTTP transport client and tool registry.
// This package must not import internal/agent (package boundary, ADR-001).
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// DiscoverTools calls the MCP server's tool list endpoint and returns all
// available tools. Used during server registration to populate mcp_tools.
func (c *Client) DiscoverTools(ctx context.Context) ([]Tool, error) {
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/list",
		Params:  struct{}{},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tools/list request: %w", err)
	}

	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("post tools/list: %w", err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
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
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
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

	resp, err := c.post(ctx, body)
	if err != nil {
		return ToolResult{}, fmt.Errorf("post tools/call: %w", err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
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

// post sends a JSON-RPC request body to c.serverURL and returns the HTTP
// response. It returns an error for non-200 status codes.
func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Drain and close the body so the connection can be reused.
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		return nil, fmt.Errorf("mcp server returned status %d", resp.StatusCode)
	}

	return resp, nil
}
