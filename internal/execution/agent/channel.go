// Package agent — this file defines the internal channel abstraction used by
// FeedbackHandler to route feedback requests. See plugin-system-spec.md §4.2.
package agent

import (
	"context"
	"errors"
	"sync"
)

// ErrUnknownRequestID is returned by inAppChannel.Resolve when the given
// request_id has no registered waiter. This is the precursor to the
// plugin-system-spec §4.2 feedback_response_late hard-rejection rule; #179
// surfaces this through SubmitFeedback.
var ErrUnknownRequestID = errors.New("unknown request_id: no waiter registered")

// inAppResponse wraps the operator's response text. The struct wrapper preserves
// forward-compat for #179 (e.g. adding metadata without changing the channel type).
type inAppResponse struct {
	text string
}

// notifyRequest carries the arguments for a one-way notification.
type notifyRequest struct {
	RunID   string
	Message string
}

// inAppChannel is the in-process feedback channel implementation. It stores
// pending requests in a waiter map keyed by feedback_id and routes Resolve calls
// back to the FeedbackHandler.Wait select that registered the waiter.
type inAppChannel struct {
	audit   *AuditWriter
	sm      *RunStateMachine
	mu      sync.Mutex
	waiters map[string]chan inAppResponse
}

// newInAppChannel constructs an inAppChannel. audit and sm must not be nil.
func newInAppChannel(audit *AuditWriter, sm *RunStateMachine) *inAppChannel {
	return &inAppChannel{
		audit:   audit,
		sm:      sm,
		waiters: make(map[string]chan inAppResponse),
	}
}

// Notify is a no-op for the in-app channel. In-app operators see the
// feedback_request step directly in the UI; no separate notification is needed.
func (c *inAppChannel) Notify(_ context.Context, _ notifyRequest) error {
	return nil
}

// RegisterWaiter allocates a buffered channel (cap 1) for feedbackID, stores it
// in the waiters map, and returns the receive-only end.  The caller must call
// UnregisterWaiter when done — successful response, timeout, or ctx cancel.
//
// The registration-before-transition invariant is preserved by the caller (see
// inAppChannel.Request for the pattern): register first, transition second, so
// a fast Resolve cannot miss the channel.
func (c *inAppChannel) RegisterWaiter(feedbackID string) <-chan inAppResponse {
	ch := make(chan inAppResponse, 1)
	c.mu.Lock()
	c.waiters[feedbackID] = ch
	c.mu.Unlock()
	return ch
}

// UnregisterWaiter removes the waiter entry for feedbackID.  Safe to call when
// no entry exists (e.g. after a successful Resolve already deleted it).
func (c *inAppChannel) UnregisterWaiter(feedbackID string) {
	c.mu.Lock()
	delete(c.waiters, feedbackID)
	c.mu.Unlock()
}

// waiter is a registered in-app waiter bound to its own cleanup. It is returned
// by registerWaiter already registered, and the response channel is reachable
// only through responses() — you cannot obtain something to wait on without
// having registered first. That makes the register-before-transition ordering
// structural (the caller holds a registered waiter before it transitions) rather
// than a comment-enforced convention, and couples release() to the exact
// feedbackID so cleanup cannot target the wrong entry or be forgotten.
type waiter struct {
	c          *inAppChannel
	feedbackID string
	ch         <-chan inAppResponse
}

// registerWaiter registers an in-app waiter for feedbackID and returns a handle
// bundling its response channel with its cleanup.
func (c *inAppChannel) registerWaiter(feedbackID string) *waiter {
	return &waiter{
		c:          c,
		feedbackID: feedbackID,
		ch:         c.RegisterWaiter(feedbackID),
	}
}

// responses returns the receive-only channel the operator response arrives on.
func (w *waiter) responses() <-chan inAppResponse { return w.ch }

// release unregisters the waiter. Safe to call more than once (UnregisterWaiter
// tolerates a missing entry), so it works as a deferred cleanup regardless of
// which Wait branch returns.
func (w *waiter) release() { w.c.UnregisterWaiter(w.feedbackID) }

// Resolve delivers a response to the waiter registered for requestID.
//
// The ch <- send is intentionally OUTSIDE the lock. ch is buffered (cap 1) and
// has a single reader (Request's select), so the send never blocks. Moving the
// send inside the lock would serialize all Resolve calls and risk deadlock if a
// future change makes the channel unbuffered. Do not "optimize" by hoisting the
// send into the lock.
func (c *inAppChannel) Resolve(requestID, body string) error {
	c.mu.Lock()
	ch, ok := c.waiters[requestID]
	if ok {
		delete(c.waiters, requestID)
	}
	c.mu.Unlock()

	if !ok {
		return ErrUnknownRequestID
	}
	ch <- inAppResponse{text: body}
	return nil
}
