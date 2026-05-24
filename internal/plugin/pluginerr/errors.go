// Package pluginerr is a stdlib-only leaf package holding sentinel errors
// that cross the plugin dispatch ↔ agent runtime boundary.
//
// Why a separate package: internal/plugin/dispatch is the producer of these
// errors (Pool.Call returns them on per-call timeout or queue rejection) and
// internal/execution/agent is the consumer (classifyPluginError checks them
// via errors.Is to surface a sanitized message to the agent). Putting them
// here lets both sides reference the same sentinel value without importing
// each other — no main.go adapter translation needed.
package pluginerr

import "errors"

// ErrCallTimeout is returned when a plugin tool call exceeds its per-call
// deadline while the parent run context is still healthy.
var ErrCallTimeout = errors.New("plugin: call timed out")

// ErrQueueFull is returned when a plugin instance's concurrency slots and
// bounded queue are both saturated and the call is rejected immediately.
var ErrQueueFull = errors.New("plugin: queue full")
