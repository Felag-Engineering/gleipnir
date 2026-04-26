package mcp_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rapp992/gleipnir/internal/infra/metrics"
	"github.com/rapp992/gleipnir/internal/mcp"
)

func TestClassifyMCPErrorType(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "context deadline exceeded → timeout",
			err:  context.DeadlineExceeded,
			want: "timeout",
		},
		{
			name: "context canceled → timeout",
			err:  context.Canceled,
			want: "timeout",
		},
		{
			name: "HTTP 401 → auth",
			err:  &mcp.HTTPStatusError{StatusCode: 401},
			want: "auth",
		},
		{
			name: "HTTP 403 → auth",
			err:  &mcp.HTTPStatusError{StatusCode: 403},
			want: "auth",
		},
		{
			name: "HTTP 429 → rate_limit",
			err:  &mcp.HTTPStatusError{StatusCode: 429},
			want: "rate_limit",
		},
		{
			name: "HTTP 500 → server_error",
			err:  &mcp.HTTPStatusError{StatusCode: 500},
			want: "server_error",
		},
		{
			name: "HTTP 404 → protocol",
			err:  &mcp.HTTPStatusError{StatusCode: 404},
			want: "protocol",
		},
		{
			name: "HTTP 400 → protocol",
			err:  &mcp.HTTPStatusError{StatusCode: 400},
			want: "protocol",
		},
		{
			name: "net.DNSError → connection",
			err:  &net.DNSError{Name: "example.com"},
			want: "connection",
		},
		{
			name: "net.OpError → connection",
			err:  &net.OpError{Op: "dial"},
			want: "connection",
		},
		{
			name: "JSONRPCError → protocol",
			err:  &mcp.JSONRPCError{Code: -32600, Message: "invalid request"},
			want: "protocol",
		},
		{
			name: "unknown error → connection",
			err:  fmt.Errorf("something completely unknown"),
			want: "connection",
		},
		{
			name: "wrapped deadline exceeded → timeout",
			err:  fmt.Errorf("outer: %w", context.DeadlineExceeded),
			want: "timeout",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mcp.ClassifyMCPErrorType(tc.err)
			if got != tc.want {
				t.Errorf("ClassifyMCPErrorType(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestMCPMetricFamiliesRegistered(t *testing.T) {
	want := map[string]bool{
		"gleipnir_mcp_call_duration_seconds": false,
		"gleipnir_mcp_errors_total":          false,
	}

	ch := make(chan *prometheus.Desc, 1024)
	metrics.Registry().Describe(ch)
	close(ch)

	for d := range ch {
		desc := d.String()
		for name := range want {
			if strings.Contains(desc, `"`+name+`"`) {
				want[name] = true
			}
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("metric family %q not registered", name)
		}
	}
}
