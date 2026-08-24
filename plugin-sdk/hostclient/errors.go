package hostclient

import (
	"fmt"
	"strings"
)

// HostError is a tool-execution refusal: the call reached the host endpoint
// and a handler ran, but declined. Code is one of hostendpoint's stable
// machine-readable strings (invalid_argument, permission_denied,
// failed_precondition, unauthenticated, internal, cardinality_cap_exceeded,
// metric_name_cap_exceeded, reserved_label, inconsistent_label_keys,
// unauthorized_request_id, unauthorized_tier2_call, …) — match on Code, not
// on Message, since Message is free text meant for logs, not branching.
type HostError struct {
	Code    string
	Message string
}

func (e *HostError) Error() string {
	return fmt.Sprintf("hostclient: %s: %s", e.Code, e.Message)
}

// parseHostError decodes an isError tool result's text field, which the host
// endpoint formats as "code: message" (internal/plugin/hostendpoint's
// ToolError.Error()). Splitting on the first ": " rather than a stricter
// grammar mirrors the host's own contract exactly — a message containing a
// colon must not fracture the code.
func parseHostError(text string) *HostError {
	code, message, ok := strings.Cut(text, ": ")
	if !ok {
		// The host endpoint always formats isError text as "code: message";
		// a text with no separator is not a shape this client's contract
		// defines, so it is reported as an unclassified internal failure
		// rather than dropped.
		return &HostError{Code: "internal", Message: text}
	}
	return &HostError{Code: code, Message: message}
}

// JSONRPCError is a transport/JSON-RPC-level failure: the request never
// reached a tool handler (bad headers, unsupported protocol version, unknown
// method, malformed params). Code follows JSON-RPC 2.0 numbering, mirroring
// internal/mcp's error-code constants (ErrCodeHeaderMismatch,
// ErrCodeUnsupportedProtocolVersion, ErrCodeInvalidParams,
// ErrCodeMethodNotFound); Data carries method-specific detail, e.g.
// {"supported": [...]} on an unsupported-version rejection.
type JSONRPCError struct {
	Code    int
	Message string
	Data    map[string]any
}

func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("hostclient: jsonrpc error %d: %s", e.Code, e.Message)
}
