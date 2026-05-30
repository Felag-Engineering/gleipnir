// Package tool defines the ergonomic ToolService interface for plugin authors.
// It is proto-free: no types from plugin-sdk/gen/ appear in its signatures.
// The serve package adapts this interface onto the generated toolv1.ToolServiceServer.
package tool

import "context"

// ToolSpec describes a single tool exposed by a plugin. Fields map 1:1 to the
// toolv1.ToolSchema proto message; the adapter in serve/ translates between them.
type ToolSpec struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema document (serialized as a JSON string)
	// describing the tool's input parameters. The host uses this for
	// per-policy parameter scoping (ADR-017) and agent schema narrowing.
	InputSchema string
}

// Service is the ergonomic interface plugin authors implement to expose
// agent-callable tools. It is deliberately minimal: authors deal in plain Go
// types and []byte JSON without touching proto messages or ErrorEnvelope
// construction.
//
// # Cancellation
//
// Both ListTools and Call receive a context that the host cancels when the run
// is cancelled or the gRPC deadline fires. Every blocking I/O operation inside
// Call MUST select on ctx.Done() so the goroutine returns promptly. The host
// enforces a 5s grace period before force-disconnecting (spec §13.8).
//
// # Cancel RPC
//
// The generated ToolServiceServer requires a Cancel method; the adapter in
// serve/ implements it as a no-op. The documented cancellation contract for
// ergonomic plugins is via ctx.Done() inside Call; there is no separate
// cancel callback.
//
// # Host RPCs
//
// To make outbound host RPCs (Log, EmitMetric, GetInstanceConfig, …), call
// serve.WithCallContext(ctx) BEFORE each host RPC. The adapter does NOT apply
// it automatically, so that authors can control context propagation in
// detached goroutines correctly.
//
// # ListTools errors
//
// Errors returned from ListTools are translated to a gRPC-level
// codes.Internal status (not an application-level ErrorEnvelope), because
// ListToolsResponse has no Error field. Log and return a descriptive error;
// the host will surface it as a capability discovery failure.
type Service interface {
	// ListTools returns all tools this plugin instance currently exposes.
	// Called once on startup and after hot-reload.
	ListTools(ctx context.Context) ([]ToolSpec, error)

	// Call invokes the named tool with the JSON-encoded input and returns
	// JSON-encoded output. input is the raw input_json string from the
	// CallRequest; output is written to the output_json field of CallResponse.
	//
	// Return a pluginerr.CodedError for well-typed errors (InvalidArg,
	// NotFound, …); plain errors become ERROR_CODE_INTERNAL in the response
	// envelope. A nil error with non-nil output is the success path.
	Call(ctx context.Context, tool string, input []byte) (output []byte, err error)
}
