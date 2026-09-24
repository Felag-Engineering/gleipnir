package mcp

import (
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// MinCallTimeoutSeconds and MaxCallTimeoutSeconds bound
// mcp_servers.call_timeout_seconds (issue #939): 1s floor because a
// sub-second call timeout is never a real need, 600s ceiling because at that
// value one call can hold a per-server concurrency slot (gate.go) for 10
// minutes -- already longer than GLEIPNIR_DRAIN_TIMEOUT's 5-minute default,
// so graceful shutdown abandons a longer call. Anything above 10 minutes
// belongs in MCP Tasks, not one blocking HTTP call.
const (
	MinCallTimeoutSeconds int64 = 1
	MaxCallTimeoutSeconds int64 = 600
)

// ValidateCallTimeoutSeconds reports whether n is a valid stored
// call_timeout_seconds override. Callers that also accept 0 as "clear the
// override" (the API write path) must check for 0 before calling this — 0 is
// out of range here on purpose, so a caller cannot mistake "clear" for a
// validated value.
func ValidateCallTimeoutSeconds(n int64) error {
	if n < MinCallTimeoutSeconds || n > MaxCallTimeoutSeconds {
		return fmt.Errorf("must be between %d and %d seconds", MinCallTimeoutSeconds, MaxCallTimeoutSeconds)
	}
	return nil
}

// DefaultCallTimeout returns the instance-wide MCP call timeout: the
// Registry's configured mcpTimeout (GLEIPNIR_MCP_TIMEOUT) when set, else the
// same 30s default NewClient falls back to. This is the value shown to an
// operator as "Default (Ns)" for a server with no override.
func (r *Registry) DefaultCallTimeout() time.Duration {
	if r.mcpTimeout > 0 {
		return r.mcpTimeout
	}
	return defaultProbeTimeout
}

// CallTimeoutFor returns the call timeout that srv's Clients actually get:
// its stored override when one is set, else the Registry's instance default.
//
// A non-positive stored value is treated as unset, never as "no timeout" --
// http.Client.Timeout 0 means unbounded, which must never be reachable from
// the DB. This only matters for a hand-edited row; the API never writes one.
// A stored value above MaxCallTimeoutSeconds is clamped rather than honored
// verbatim, for the same reason.
func (r *Registry) CallTimeoutFor(srv db.McpServer) time.Duration {
	if srv.CallTimeoutSeconds != nil && *srv.CallTimeoutSeconds > 0 {
		seconds := *srv.CallTimeoutSeconds
		if seconds > MaxCallTimeoutSeconds {
			seconds = MaxCallTimeoutSeconds
		}
		return time.Duration(seconds) * time.Second
	}
	return r.DefaultCallTimeout()
}
