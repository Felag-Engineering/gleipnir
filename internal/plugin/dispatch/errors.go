// Package dispatch routes agent tool calls to plugin instances over gRPC.
// It owns per-call deadlines, cancellation propagation, and concurrency limits.
package dispatch

import (
	"errors"

	"github.com/felag-engineering/gleipnir/internal/plugin/pluginerr"
)

// ErrCallTimeout is returned by Pool.Call when the per-call deadline fires
// and the parent run context is still healthy (i.e. the timeout originated
// here, not from the run being cancelled). Aliased from pluginerr so the
// agent runtime can errors.Is against the same sentinel without importing
// internal/plugin/dispatch (see internal/plugin/pluginerr/errors.go).
var ErrCallTimeout = pluginerr.ErrCallTimeout

// ErrQueueFull is returned by Pool.Call when all concurrency slots are taken
// and the bounded queue is also at capacity — the call is rejected immediately
// rather than blocking indefinitely. Aliased from pluginerr (see above).
var ErrQueueFull = pluginerr.ErrQueueFull

// ErrRunCancelled is returned by Pool.Call when the call was aborted by a
// concurrent CancelRun (spec §13.8) while the caller's parent context was still
// healthy. CancelRun is the pool's own cancellation control surface: it cancels
// each in-flight (or about-to-be-in-flight) call's per-call context directly,
// independently of the caller's parent ctx (e.g. Pool.Close during shutdown
// calls CancelRun without cancelling any agent ctx). Without a distinct sentinel,
// a CancelRun-driven gRPC abort would surface as codes.Canceled with a healthy
// parent ctx and be misclassified as a nil (success) error — a cancelled
// side-effecting tool call would then look like it had completed. This sentinel
// keeps the cancellation observable to callers via errors.Is.
var ErrRunCancelled = errors.New("plugin: run cancelled")

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

// ErrRequestAlreadyResolved is returned by Dispatcher.Wait when its local
// timer fired but the background scanner had already CAS-transitioned the
// plugin_pending_requests row to timed_out. The scanner already wrote the
// plugin_request_timeout audit step, so callers MUST NOT write a second one.
var ErrRequestAlreadyResolved = errors.New("plugin: channel request already resolved by scanner")

// ErrInstanceNotRunning is returned by a ConnFactory when no subprocess is
// currently registered for the requested instance name. The host dispatcher
// wraps this with the instance name before returning it to callers. Aliased from
// pluginerr so the agent runtime can errors.Is against the same sentinel without
// importing internal/plugin/dispatch (see internal/plugin/pluginerr/errors.go).
var ErrInstanceNotRunning = pluginerr.ErrInstanceNotRunning

// ErrManagerUnavailable is returned by a ConnFactory when the host plugin
// subsystem is disabled or the process.Manager has not yet been constructed
// (i.e. setManager has not been called). Callers can distinguish this from
// ErrInstanceNotRunning to tell apart "subsystem off" from "instance crashed".
// Aliased from pluginerr (see above).
var ErrManagerUnavailable = pluginerr.ErrManagerUnavailable
