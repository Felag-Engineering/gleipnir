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
	// Echo this in WriteAuditStep when the human replies.
	RequestID string
	// Prompt is the message to display to the human operator.
	Prompt string
	// ChannelConfig is the per-audience-entry config block.
	ChannelConfig []byte
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
	// synchronously ack (return nil) within 5s and later call the host's
	// WriteAuditStep Host RPC with a feedback_response step when the human
	// replies (spec §4.2).
	//
	// Notify-only plugins SHOULD return pluginerr.Unimplemented("Request not
	// supported"). This produces an application-level UNIMPLEMENTED envelope,
	// not a gRPC status error, because the host normally never calls Request on
	// a Notify-only plugin (manifest guards it). Returning a plain error here
	// results in ERROR_CODE_INTERNAL.
	Request(ctx context.Context, r FeedbackRequest) error
}
