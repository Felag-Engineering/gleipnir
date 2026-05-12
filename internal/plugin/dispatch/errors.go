// Package dispatch routes agent tool calls to plugin instances over gRPC.
// It owns per-call deadlines, cancellation propagation, and concurrency limits.
package dispatch

import "errors"

// ErrCallTimeout is returned by Pool.Call when the per-call deadline fires
// and the parent run context is still healthy (i.e. the timeout originated
// here, not from the run being cancelled).
var ErrCallTimeout = errors.New("plugin: call exceeded deadline")

// ErrQueueFull is returned by Pool.Call when all concurrency slots are taken
// and the bounded queue is also at capacity — the call is rejected immediately
// rather than blocking indefinitely.
var ErrQueueFull = errors.New("plugin: queue full")

// ErrPreAckFailed is returned by Dispatcher.Request when the plugin does not
// acknowledge the request within the pre-ack deadline, or returns acked: false,
// or returns a non-nil error envelope.  Callers should map this to
// runstate.TransitionRunFailed.
var ErrPreAckFailed = errors.New("plugin: channel request pre-ack failed")

// ErrNoRequestCapableEntry is returned by Dispatcher.Request when the audience
// has no entries with request: true (either the audience is empty, or all
// entries have notify-only capability).
var ErrNoRequestCapableEntry = errors.New("plugin: no request-capable entry in audience")

// ErrUnknownRequestID is returned by Dispatcher.Resolve when the requestID is
// not in the in-memory waiters map — either it was never issued by this
// Dispatcher instance, or it has already been resolved or expired.
var ErrUnknownRequestID = errors.New("plugin: unknown request ID")

// ErrInstanceNotRunning is returned by a ConnFactory when no subprocess is
// currently registered for the requested instance name. The host dispatcher
// wraps this with the instance name before returning it to callers.
var ErrInstanceNotRunning = errors.New("plugin: instance not running")

// ErrManagerUnavailable is returned by a ConnFactory when the host plugin
// subsystem is disabled or the process.Manager has not yet been constructed
// (i.e. setManager has not been called). Callers can distinguish this from
// ErrInstanceNotRunning to tell apart "subsystem off" from "instance crashed".
var ErrManagerUnavailable = errors.New("plugin: process manager unavailable")
