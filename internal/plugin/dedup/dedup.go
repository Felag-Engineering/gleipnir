// Package dedup defines the pluggable deduplication interface for plugin-emitted
// events. The host checks each incoming event against the store before dispatching
// to subscribed policies — a hit short-circuits the fan-out so the same event
// cannot fire duplicate runs within the dedup window.
//
// # Noop implementation
//
// Noop always returns (false, nil): every event is treated as new. Production
// uses the SQLite-backed rolling-window dbStore (store.go, #562); Noop is retained
// for tests and for callers that do not need dedup semantics. Wiring Noop means a
// host restart will replay events that were already dispatched; at-least-once
// delivery on the plugin side is the design assumption (spec §4.3).
//
// # Interface contract
//
// Implementations must be safe for concurrent use — the trigger supervisor may
// call Seen from multiple goroutines (one per plugin instance stream) in parallel.
// Seen must not modify state on error; fail-open (proceed on error) is the host's
// policy and is handled by the caller, not the implementation.
package dedup

import "context"

// Key identifies a single event observation. All three fields are required;
// the combination (InstanceID, EventKind, EventID) uniquely identifies an event
// occurrence within a plugin instance.
type Key struct {
	InstanceID string
	EventKind  string
	EventID    string
}

// Store is the pluggable deduplication back-end. The host calls Seen once per
// received event before invoking the policy dispatcher.
//
// Seen returns (true, nil) when the key was observed within the current window,
// (false, nil) on a miss, or (false, err) when the store is unavailable.
// Callers treat errors as misses (fail-open) so a degraded store does not
// silently drop events.
//
// Implementations must be safe for concurrent use.
type Store interface {
	Seen(ctx context.Context, k Key) (bool, error)
}

// Noop is a Store that never deduplicates: every Seen call returns (false, nil).
// Production wires the real rolling-window store (dbStore, store.go); Noop is for
// tests and callers that do not need dedup semantics.
type Noop struct{}

// Seen always returns (false, nil). With Noop, every event proceeds to dispatch.
func (Noop) Seen(_ context.Context, _ Key) (bool, error) { return false, nil }
