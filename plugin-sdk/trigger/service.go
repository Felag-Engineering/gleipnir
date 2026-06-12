// Package trigger defines the ergonomic TriggerService interface for plugin authors.
// It is proto-free: no types from plugin-sdk/gen/ appear in its signatures.
// The serve package adapts this interface onto the generated triggerv1.TriggerServiceServer.
package trigger

import "context"

// Event is a single substrate event emitted by a plugin to the host. The serve
// adapter routes each Event through the canonical HostService.EmitEvent Host RPC
// (issue #495, spec §4.3); the fields map 1:1 onto EmitEventRequest.
type Event struct {
	// EventID is a stable, substrate-derived identifier for this specific event
	// occurrence. Used by the host for deduplication within a 1-hour rolling
	// window (spec §4.3). Use a ULID where possible; fall back to SHA-256 of
	// the canonical payload (see serve package doc for ULID guidance).
	EventID string
	// EventKind must match one of the event_kinds declared in the plugin manifest.
	EventKind string
	// Payload is the event payload serialized as a JSON object. The host
	// evaluates per-policy bindings against this payload using field-typed
	// operators (spec §7.3).
	Payload []byte
}

// StartScope carries the subscription scope the host passes when opening the
// trigger stream. Fields map 1:1 to the triggerv1.StartRequest proto message.
type StartScope struct {
	// WatchScope is the coarse subscription scope set by the admin on the
	// plugin instance (e.g. {"channels": ["#incidents"]} for Slack).
	// Validated against the plugin's manifest watch_scope_schema at instance
	// config save time.
	WatchScope []byte
}

// Service is the ergonomic interface plugin authors implement to emit events
// from an external substrate. Authors deal in plain Go types and []byte JSON
// without touching proto messages.
//
// # Cancellation
//
// Start MUST return when ctx.Done() is closed. The ctx is derived from the
// gRPC stream; the host closes the stream on shutdown or hot-reload. Every
// blocking I/O operation inside Start MUST select on ctx.Done() and return
// promptly when cancelled.
//
// # Emit errors
//
// emit routes each event through the canonical HostService.EmitEvent Host RPC
// (issue #495, spec §4.3). If emit(e) returns an error, the EmitEvent RPC
// failed (host unavailable, generation drain, or stream closed). Start SHOULD
// return at that point (the error from emit propagates as the stream return
// status). Note: a non-nil error from emit means delivery did NOT succeed; a
// nil error means the host accepted the event (subject to its rate limiter and
// dedup window, which never surface as an emit error).
//
// # Host RPCs
//
// To make additional outbound host RPCs from a background goroutine started
// inside Start, call serve.WithCallContext(ctx) passing a context derived from
// the stream context. The emit callback already propagates the stream's call
// context; this note covers any OTHER host RPCs the author makes directly.
type Service interface {
	// Start opens the event stream. The plugin monitors its substrate and calls
	// emit for each incoming event. Start runs for the lifetime of the stream;
	// the host holds one stream open indefinitely until shutdown or hot-reload.
	//
	// The error returned by Start propagates as the gRPC stream return status.
	Start(ctx context.Context, scope StartScope, emit func(Event) error) error
}
