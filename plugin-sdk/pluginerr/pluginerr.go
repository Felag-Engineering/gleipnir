// Package pluginerr provides proto-free error codes and constructors for
// plugin authors. It mirrors the error codes in commonv1.ErrorCode without
// importing the generated proto package (gen/), keeping the ergonomic service
// interfaces in tool/, channel/, and trigger/ free of proto coupling.
//
// The serve package maps pluginerr.CodedError values back onto the
// corresponding commonv1.ErrorCode 1:1 when building an ErrorEnvelope.
package pluginerr

import "fmt"

// Code classifies a plugin-side error. Values mirror commonv1.ErrorCode
// (same numeric values so the adapter mapping in serve/ is a simple cast).
type Code int32

const (
	// CodeUnspecified is the zero value; callers should never construct it directly.
	CodeUnspecified Code = 0
	// CodeInternal signals an unexpected plugin-internal failure.
	CodeInternal Code = 1
	// CodeInvalidArg signals that the caller passed invalid arguments.
	CodeInvalidArg Code = 2
	// CodeNotFound signals that the requested resource does not exist.
	CodeNotFound Code = 3
	// CodeUnavailable signals a transient failure; the caller may retry.
	CodeUnavailable Code = 4
	// CodePermission signals that the plugin lacks a required credential or scope.
	CodePermission Code = 5
	// CodeRateLimited signals that the substrate rate-limited the request;
	// the caller should back off.
	CodeRateLimited Code = 6
	// CodeUnimplemented signals that the RPC is declared but not implemented
	// by this plugin (e.g. Request on a Notify-only channel plugin).
	CodeUnimplemented Code = 7
)

// CodedError is an error that carries a plugin-level error code understood by
// the serve adapter. Plugin authors return CodedError (via the constructors
// below) to control the ErrorCode written into the response ErrorEnvelope.
// A plain (non-coded) error returned from a service method is treated as
// CodeInternal by the adapter.
type CodedError struct {
	Code    Code
	Message string
}

func (e *CodedError) Error() string {
	return fmt.Sprintf("pluginerr[%d]: %s", e.Code, e.Message)
}

// InvalidArg returns a CodedError with CodeInvalidArg. Use when the caller
// passed invalid or malformed arguments (bad JSON, unknown tool name, …).
func InvalidArg(msg string) error {
	return &CodedError{Code: CodeInvalidArg, Message: msg}
}

// NotFound returns a CodedError with CodeNotFound. Use when a requested
// resource (tool, topic, channel, …) does not exist.
func NotFound(msg string) error {
	return &CodedError{Code: CodeNotFound, Message: msg}
}

// Internal returns a CodedError with CodeInternal. Use for unexpected
// plugin-internal failures where the caller cannot meaningfully retry.
func Internal(msg string) error {
	return &CodedError{Code: CodeInternal, Message: msg}
}

// Unavailable returns a CodedError with CodeUnavailable. Use for transient
// substrate failures that the caller may retry after backoff.
func Unavailable(msg string) error {
	return &CodedError{Code: CodeUnavailable, Message: msg}
}

// Permission returns a CodedError with CodePermission. Use when the plugin
// lacks a required credential or scope to fulfil the request.
func Permission(msg string) error {
	return &CodedError{Code: CodePermission, Message: msg}
}

// RateLimited returns a CodedError with CodeRateLimited. Use when the
// external substrate has rate-limited the plugin; the caller should back off.
func RateLimited(msg string) error {
	return &CodedError{Code: CodeRateLimited, Message: msg}
}

// Unimplemented returns a CodedError with CodeUnimplemented. Use when the
// RPC method is declared in the proto but not implemented by this plugin
// (e.g. Request on a Notify-only channel plugin). The serve adapter converts
// this to an application-level UNIMPLEMENTED envelope rather than a gRPC
// status error, so the host's response path remains normal.
func Unimplemented(msg string) error {
	return &CodedError{Code: CodeUnimplemented, Message: msg}
}
