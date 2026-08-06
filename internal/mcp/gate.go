package mcp

import (
	"context"
	"errors"
	"fmt"
)

// Per-server concurrency and queue-depth gating for tool calls (ADR-053, spec
// §3; issue #819).
//
// This is the plugin dispatch pool's per-instance semaphore and bounded queue,
// moved onto the shared client where the realignment says it belongs. The
// defaults match what that pool applies today so a plugin's behaviour does not
// change as it moves onto this transport — but the gate now covers external
// servers too, which previously had no ceiling at all.
//
// The bound and the queue answer different questions and both are needed. The
// semaphore bounds how much work a server is doing at once, which is about the
// server. The queue bounds how many callers are WAITING, which is about
// Gleipnir: without it, a wedged server converts every run that touches it into
// a blocked goroutine holding a run slot, and the failure spreads from one
// server to the whole host. Rejecting immediately past the queue depth turns
// that into a fast, attributable error.

const (
	// DefaultMaxConcurrentCalls and DefaultMaxQueueDepth match
	// internal/plugin/dispatch.Config's per-instance defaults, deliberately.
	// A plugin moving from the gRPC dispatcher to this transport should meet
	// the same ceilings it met before; changing them in the same step as
	// changing the transport would make a regression impossible to attribute.
	DefaultMaxConcurrentCalls = 50
	DefaultMaxQueueDepth      = 50
)

// ErrQueueFull reports that a server already has its queue depth worth of
// callers waiting for a concurrency slot. It is returned immediately, without
// waiting: the whole point of the depth is to fail fast rather than add one
// more blocked goroutine.
var ErrQueueFull = errors.New("mcp: server call queue is full")

// ServerLimits bounds one server's call concurrency. A zero field takes the
// package default; a negative field means unbounded, which is how a caller
// says "I know, and I want no ceiling here" rather than having to guess a
// number large enough to never bind.
type ServerLimits struct {
	MaxConcurrent int
	MaxQueueDepth int
}

// resolve fills zero fields with the defaults.
func (l ServerLimits) resolve() ServerLimits {
	if l.MaxConcurrent == 0 {
		l.MaxConcurrent = DefaultMaxConcurrentCalls
	}
	if l.MaxQueueDepth == 0 {
		l.MaxQueueDepth = DefaultMaxQueueDepth
	}
	return l
}

// unbounded reports whether these limits impose no ceiling at all.
func (l ServerLimits) unbounded() bool {
	return l.MaxConcurrent < 0 && l.MaxQueueDepth < 0
}

// testHookQueueSlotClaimed fires immediately after a caller claims a queue
// slot. Always nil in production; set only by tests in this package.
//
// It exists because "the queue is full" cannot be observed from outside without
// probing, and a probe that acquires is a probe that TAKES the slot it is
// looking for — so a spinning prober competes with the waiter it is waiting
// for, and on a loaded machine it can keep winning indefinitely. Mirrors
// internal/plugin/dispatch's testHookQueueSlotClaimed, which exists for the
// identical reason.
//
// A test that sets this mutates package state and must not call t.Parallel.
var testHookQueueSlotClaimed func()

// serverGate is one server's semaphore plus its bounded waiting room.
//
// Two channels rather than one counter: the queue slot is claimed BEFORE
// blocking on the semaphore and released after, so "waiting" is a state the
// gate can refuse to enter, not a number it discovers it has exceeded once
// everyone is already blocked.
type serverGate struct {
	sem   chan struct{}
	queue chan struct{}
}

func newServerGate(limits ServerLimits) *serverGate {
	limits = limits.resolve()
	if limits.unbounded() {
		return nil
	}
	g := &serverGate{}
	if limits.MaxConcurrent > 0 {
		g.sem = make(chan struct{}, limits.MaxConcurrent)
	}
	if limits.MaxQueueDepth > 0 {
		g.queue = make(chan struct{}, limits.MaxQueueDepth)
	}
	return g
}

// acquire claims a call slot, returning a release function.
//
// It fails three ways, and they are deliberately distinguishable. ErrQueueFull
// means the server is saturated and this caller never waited — an operator
// reading it learns the ceiling was hit, not that something was slow. A context
// error means this caller's own deadline expired while waiting, which is the
// per-call timeout doing its job. And a nil gate means no ceiling is
// configured, which is the direct-NewClient path.
func (g *serverGate) acquire(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}

	// The queue slot is non-blocking on purpose: a caller that cannot claim one
	// is over the depth, and making it wait for room in the waiting room would
	// be the unbounded queue this exists to prevent.
	if g.queue != nil {
		select {
		case g.queue <- struct{}{}:
			if testHookQueueSlotClaimed != nil {
				testHookQueueSlotClaimed()
			}
		default:
			return nil, ErrQueueFull
		}
	}
	releaseQueue := func() {
		if g.queue != nil {
			<-g.queue
		}
	}

	if g.sem == nil {
		return releaseQueue, nil
	}

	// A caller whose context is already dead must not be handed a slot. Checked
	// explicitly rather than left to the select below, because a select with
	// both cases ready picks at random — so an already-cancelled caller would
	// sometimes consume a slot it is about to abandon, and the outcome would
	// depend on a coin flip.
	//
	// It is checked AFTER the queue, so "you were over the ceiling" wins over
	// "you gave up": the first is a fact about the server's saturation that the
	// caller could not have known, and it is decided without any waiting.
	if err := ctx.Err(); err != nil {
		releaseQueue()
		return nil, fmt.Errorf("waiting for a call slot: %w", err)
	}

	select {
	case g.sem <- struct{}{}:
		return func() {
			<-g.sem
			releaseQueue()
		}, nil
	case <-ctx.Done():
		releaseQueue()
		return nil, fmt.Errorf("waiting for a call slot: %w", ctx.Err())
	}
}

// WithServerLimits bounds concurrent calls to this server and the queue of
// callers waiting for a slot. Zero fields take the package defaults; a Client
// constructed without this option is unbounded, which keeps every direct
// NewClient caller (probes, tests) behaving as it did.
func WithServerLimits(l ServerLimits) ClientOption {
	return func(cl *Client) { cl.callGate = newServerGate(l) }
}

// WithTrustTier declares whether this server is a managed plugin endpoint or an
// operator-registered external one (spec §3).
//
// It gates the `io.gleipnir/*` extensions. Those are host-plane: a server that
// declares `io.gleipnir/channel` and is believed can be asked to settle a human
// approval, and nothing about an external URL an operator pasted in makes it a
// channel Gleipnir should route consent through. External declarations are
// therefore DROPPED at the client rather than trusted and filtered later —
// §5 leaves external extension opt-in explicitly deferred, and "deferred"
// has to mean the path does not exist yet, not that it exists unguarded.
func WithTrustTier(t TrustTier) ClientOption {
	return func(cl *Client) { cl.trustTier = t }
}

// negotiatesGleipnirExtensions reports whether this client may act on an
// `io.gleipnir/*` declaration.
func (c *Client) negotiatesGleipnirExtensions() bool {
	return c.trustTier == TrustTierManaged
}

// TrustTier reports the tier this client was built with. External is the zero
// value, so a client built without an opinion reports the safe answer.
func (c *Client) TrustTier() TrustTier {
	if c.trustTier == "" {
		return TrustTierExternal
	}
	return c.trustTier
}
