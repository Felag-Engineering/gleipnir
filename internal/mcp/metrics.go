package mcp

import (
	"context"
	"errors"
	"net"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

var mcpCallDurationSeconds = promauto.With(metrics.Registry()).NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gleipnir_mcp_call_duration_seconds",
		Help:    "MCP tool invocation latency by server and tool.",
		Buckets: metrics.BucketsFast,
	},
	[]string{metrics.LabelServer, metrics.LabelTool},
)

var mcpErrorsTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_mcp_errors_total",
		Help: "MCP tool call failures by server and error class.",
	},
	[]string{metrics.LabelServer, metrics.LabelErrorType},
)

// ClassifyMCPErrorType maps a raw MCP transport error to the fixed error_type
// label enum. HTTP status errors are classified by their status code; network
// errors (DNS, TCP) map to connection; JSON-RPC envelope errors (HTTP succeeded
// but the server rejected the call) map to protocol. context errors take
// precedence over all others because a canceled/timed-out HTTP call may also
// wrap a net.OpError.
func ClassifyMCPErrorType(err error) string {
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return metrics.ErrorTypeTimeout
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == 401 || httpErr.StatusCode == 403:
			return metrics.ErrorTypeAuth
		case httpErr.StatusCode == 429:
			return metrics.ErrorTypeRateLimit
		case httpErr.StatusCode >= 500:
			return metrics.ErrorTypeServerError
		default:
			// 4xx other than 401/403/429 indicates a client-protocol mismatch.
			return metrics.ErrorTypeProtocol
		}
	}
	var hpErr *HeaderParamError
	if errors.As(err, &hpErr) {
		// The server's own tool schema declares an unusable header
		// annotation: a protocol-level defect, not a connection failure.
		return metrics.ErrorTypeProtocol
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return metrics.ErrorTypeConnection
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return metrics.ErrorTypeConnection
	}
	var rpcErr *JSONRPCError
	if errors.As(err, &rpcErr) {
		// HTTP round-trip succeeded; server rejected the call at the
		// application level. Classification is deliberately code-INDEPENDENT:
		// the MCP-reserved -32020/-32021/-32022, the JSON-RPC base codes, and
		// the -32002/-32602 resource-not-found pair (see errorcodes.go) all
		// map to this same label today. This function is the ONLY place a
		// code-specific label may ever be introduced, and it must reuse the
		// fixed metrics.ErrorType* enum rather than invent a new label value.
		// Classification cannot be version-gated: CallTool's deferred metric
		// closure has no client or protocol version in scope.
		return metrics.ErrorTypeProtocol
	}
	return metrics.ErrorTypeConnection
}
