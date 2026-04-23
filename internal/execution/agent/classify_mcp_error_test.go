package agent

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/rapp992/gleipnir/internal/mcp"
)

func TestClassifyMCPError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "HTTP status error",
			err:  &mcp.HTTPStatusError{StatusCode: 503},
			want: "MCP server myserver returned HTTP 503",
		},
		{
			name: "HTTP 404",
			err:  &mcp.HTTPStatusError{StatusCode: 404},
			want: "MCP server myserver returned HTTP 404",
		},
		{
			name: "DNS error",
			err:  &net.DNSError{Err: "no such host", Name: "mcp-test-server"},
			want: "MCP server myserver DNS resolution failed",
		},
		{
			name: "connection refused",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connection refused")},
			want: "MCP server myserver connection refused",
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: "MCP server myserver timed out",
		},
		{
			name: "JSON-RPC error",
			err:  &mcp.JSONRPCError{Code: -32600, Message: "bad request"},
			want: "MCP server myserver returned an error: bad request",
		},
		{
			name: "unknown error",
			err:  fmt.Errorf("something unexpected"),
			want: "MCP server myserver is unavailable",
		},
		{
			name: "wrapped HTTP status error",
			err:  fmt.Errorf("post tools/call: %w", &mcp.HTTPStatusError{StatusCode: 502}),
			want: "MCP server myserver returned HTTP 502",
		},
		{
			name: "wrapped DNS error",
			err:  fmt.Errorf("http do: %w", &net.DNSError{Err: "server misbehaving", Name: "mcp-test"}),
			want: "MCP server myserver DNS resolution failed",
		},
		{
			name: "wrapped deadline exceeded",
			err:  fmt.Errorf("post tools/call: %w", context.DeadlineExceeded),
			want: "MCP server myserver timed out",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyMCPError("myserver", tt.err)
			if got != tt.want {
				t.Errorf("classifyMCPError() = %q, want %q", got, tt.want)
			}
		})
	}
}
