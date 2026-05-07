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
