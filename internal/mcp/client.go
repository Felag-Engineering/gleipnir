// Package mcp implements the MCP HTTP transport client and tool registry.
// This package must not import internal/agent (package boundary, ADR-001).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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
	// TODO: POST /mcp with JSON-RPC tools/list request, parse response
	panic("not implemented")
}

// CallTool invokes a named tool on the MCP server with the given input.
// The input must be a JSON-serialisable value matching the tool's inputSchema.
func (c *Client) CallTool(ctx context.Context, name string, input map[string]any) (ToolResult, error) {
	// TODO: POST /mcp with JSON-RPC tools/call request, parse response
	_ = fmt.Sprintf // prevent unused import during stub phase
	panic("not implemented")
}
