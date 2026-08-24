// Package channel defines the ergonomic ChannelService interface for plugin authors.
// It is proto-free: no types from plugin-sdk/gen/ appear in its signatures.
// The serve package adapts this interface onto the generated channelv1.ChannelServiceServer.
package channel

import "context"

// Notification carries a fire-and-forget notification from the host to the
// plugin. Fields map 1:1 to the channelv1.NotifyRequest proto message;
// the adapter in serve/ translates between them.
type Notification struct {
	// EventType classifies the notification (e.g. "run_failed", "approval_requested").
	EventType string
	// Payload is the notification body serialized as a JSON object.
	// Schema is EventType-specific; authors should tolerate unknown fields
	// to handle future Gleipnir additions gracefully.
	Payload []byte
	// ChannelConfig is the per-audience-entry config block validated against
	// the plugin's manifest channel_config_schema at audience save time.
	ChannelConfig []byte
}

// FeedbackRequest asks the plugin to open a request/response feedback channel.
// Fields map 1:1 to the channelv1.RequestRequest proto message.
type FeedbackRequest struct {
	// RequestID is a host-generated token that is instance-scoped (not
	// generation-scoped) so callbacks can be matched across hot-reloads.
	// WriteAuditStep — the RPC that used to settle a Request with this token —
	// left the host surface (#880); there is no host-side settlement path
	// until the milestone #19 ADR-055 task-based rewrite lands.
	RequestID string
	// Prompt is the message to display to the human operator.
	Prompt string
	// ChannelConfig is the per-audience-entry config block.
	ChannelConfig []byte
}

// TerminalReason describes why the host terminated a pending Request. It
// mirrors the proto TerminalReason ordinals so the adapter can translate
// without importing the generated package into author-facing code.
type TerminalReason int

const (
	TerminalReasonUnspecified TerminalReason = 0
	TerminalReasonApproved    TerminalReason = 1
	TerminalReasonRejected    TerminalReason = 2
	TerminalReasonAnswered    TerminalReason = 3
	TerminalReasonTimedOut    TerminalReason = 4
	TerminalReasonSuperseded  TerminalReason = 5 // reserved; not yet wired
	TerminalReasonCanceled    TerminalReason = 6 // reserved; not yet wired
)

// Termination carries the details of a host-initiated Request termination.
// Plugins that wish to update their UI on host-driven timeouts implement the
// optional TerminationAware interface and receive this struct.
type Termination struct {
	// RequestID is the request_id originally issued by the host in FeedbackRequest.
	RequestID string
	// Reason describes the terminal state (e.g. TerminalReasonTimedOut).
	Reason TerminalReason
	// Resolver is the user ID or name who resolved the request, or empty for
	// automated resolutions such as timeout.
	Resolver string
}

// TerminationAware is an optional interface plugins can implement alongside
// channel.Service. When the host calls RequestTerminated (e.g. on timeout),
// the ergonomic adapter checks whether the wrapped service implements this
// interface and, if so, calls RequestTerminated with the translated Termination.
// Plugins that do not implement this interface receive a silent no-op — the
// adapter returns {Ok:true} without calling the host RPC.
//
// This interface is NOT a method on Service so that existing plugins do not
// need to implement it. Only plugins that want to update their UI on
// host-initiated timeouts need to implement it.
type TerminationAware interface {
	RequestTerminated(ctx context.Context, t Termination) error
}

// Service is the ergonomic interface plugin authors implement to deliver
// notifications and feedback requests. Authors deal in plain Go types and
// []byte JSON without touching proto messages or ErrorEnvelope construction.
//
// # Cancellation
//
// Both Notify and Request receive a context that the host cancels when the
// gRPC deadline fires. Every blocking I/O operation MUST select on ctx.Done().
//
// # Host RPCs
//
// To make outbound host RPCs (GetInstanceConfig, GetCredentials, …), call
// serve.WithCallContext(ctx) BEFORE each host RPC. The adapter does NOT apply
// it automatically, so that authors can control context propagation in
// detached goroutines correctly.
//
// # Request and UNIMPLEMENTED
//
// Plugins that support Notify-only SHOULD return pluginerr.Unimplemented("…")
// from Request. The adapter translates this to an application-level
// RequestResponse{Acked: false, Error: {UNIMPLEMENTED}} — not a gRPC-level
// codes.Unimplemented status — because the host never routes Request to a
// plugin whose manifest declares Notify-only channel_capabilities.
type Service interface {
	// Notify delivers a fire-and-forget notification. Failure does not fail
	// the run; return a descriptive error so the host can audit and count it.
	Notify(ctx context.Context, n Notification) error

	// Request opens a request/response feedback channel. The plugin must
	// synchronously ack (return nil) within 5s and, per spec §4.2, later
	// settle the request with a feedback_response step when the human
	// replies. That settlement rode the host's WriteAuditStep Host RPC,
	// which left the host surface (#880) — settlement is non-functional
	// until the milestone #19 ADR-055 task-based rewrite lands.
	//
	// Notify-only plugins SHOULD return pluginerr.Unimplemented("Request not
	// supported"). This produces an application-level UNIMPLEMENTED envelope,
	// not a gRPC status error, because the host normally never calls Request on
	// a Notify-only plugin (manifest guards it). Returning a plain error here
	// results in ERROR_CODE_INTERNAL.
	Request(ctx context.Context, r FeedbackRequest) error
}
