package main

import (
	"context"
	"log"
	"sync/atomic"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// eventsHandlerSlot wraps the events handler function for atomic.Pointer storage.
// atomic.Pointer[func(...)] does not compile — the type parameter must be a named type.
type eventsHandlerSlot struct {
	fn func(socketmode.Event)
}

// interactiveHandlerSlot wraps the interactive handler function for atomic.Pointer storage.
type interactiveHandlerSlot struct {
	fn func(socketmode.Event, slack.InteractionCallback)
}

// messageHandlerSlot wraps the message handler function for atomic.Pointer storage.
// Used by ChannelService to observe EventsAPI events for thread-reply watching without
// interferring with TriggerService's eventsHandler (which owns Ack).
type messageHandlerSlot struct {
	fn func(socketmode.Event)
}

// socketHub multiplexes a single socketModeRunner between an EventsAPI handler
// (used by TriggerService) and an interactive handler (used by ChannelService).
// One hub per xapp-token; created and managed by hubRegistry.
//
// Race window on TriggerService restart: between the old Start's ctx-cancel
// (which releases the events handler via releaseFn) and new Start's
// RegisterEventsHandler call, EventsAPI events on the wire are silently dropped
// (slot briefly nil). Interactive callbacks are unaffected — ChannelService
// registers its interactive handler once at NewChannelService and never releases it.
type socketHub struct {
	runner             socketModeRunner
	xappToken          string
	eventsHandler      atomic.Pointer[eventsHandlerSlot]
	interactiveHandler atomic.Pointer[interactiveHandlerSlot]
	messageHandler     atomic.Pointer[messageHandlerSlot]

	// done is closed (and doneErr written) when the hub's Run goroutine exits.
	// Multiple goroutines may read from Done(); the channel is never sent to
	// again after the first error, so readers only need to range or select once.
	done    chan struct{}
	doneErr error
}

// newSocketHub creates a socketHub using the provided factory and xapp-token.
func newSocketHub(factory socketModeFactory, xappToken string) (*socketHub, error) {
	runner, err := factory(xappToken)
	if err != nil {
		return nil, err
	}
	return &socketHub{
		runner:    runner,
		xappToken: xappToken,
		done:      make(chan struct{}),
	}, nil
}

// Done returns a channel that is closed when the hub's Run goroutine exits.
// Safe to call before Run; the channel is closed exactly once.
func (h *socketHub) Done() <-chan struct{} {
	return h.done
}

// DoneErr returns the error from Run. Only valid to call after Done() is closed.
func (h *socketHub) DoneErr() error {
	return h.doneErr
}

// RegisterEventsHandler registers the handler called for EventsAPI events.
// Replaces any previously registered handler atomically.
func (h *socketHub) RegisterEventsHandler(fn func(socketmode.Event)) {
	h.eventsHandler.Store(&eventsHandlerSlot{fn: fn})
}

// RegisterInteractiveHandler registers the handler called for Interactive events.
// Replaces any previously registered handler atomically.
func (h *socketHub) RegisterInteractiveHandler(fn func(socketmode.Event, slack.InteractionCallback)) {
	h.interactiveHandler.Store(&interactiveHandlerSlot{fn: fn})
}

// RegisterMessageHandler registers a secondary handler that receives EventsAPI
// events.  Unlike eventsHandler, this handler must NOT call Ack — Ack is owned
// by TriggerService's eventsHandler.  Used by ChannelService to watch for
// threaded replies without creating a second Socket Mode connection.
// Replaces any previously registered handler atomically.
func (h *socketHub) RegisterMessageHandler(fn func(socketmode.Event)) {
	h.messageHandler.Store(&messageHandlerSlot{fn: fn})
}

// Ack acknowledges a Slack Socket Mode request.
func (h *socketHub) Ack(req socketmode.Request) {
	h.runner.Ack(req)
}

// Run opens the Socket Mode WebSocket connection and dispatches events to the
// registered handlers. Dispatches sequentially to preserve per-stream ordering
// (ADR-001; service.go:247). Blocks until ctx is cancelled or the runner exits.
// When Run returns, h.done is closed and h.doneErr carries the error (may be nil).
//
// Interactive events are Acked by the hub before dispatching to the handler —
// the handler must not call Ack. EventsAPI events are Acked by TriggerService's
// onEvent (service.go:385-387) to avoid double-Ack.
func (h *socketHub) Run(ctx context.Context) error {
	err := h.runner.Run(ctx, func(evt socketmode.Event) {
		switch evt.Type {
		case socketmode.EventTypeEventsAPI:
			if slot := h.eventsHandler.Load(); slot != nil {
				slot.fn(evt)
			}
			// Dispatch to the secondary message handler AFTER eventsHandler so Ack
			// (owned by eventsHandler/TriggerService) fires first.  messageHandler
			// must not call Ack.
			if mslot := h.messageHandler.Load(); mslot != nil {
				mslot.fn(evt)
			}

		case socketmode.EventTypeInteractive:
			// InteractionCallback is a VALUE type in evt.Data (not a pointer),
			// per socket_mode_managed_conn.go:602-615 (unlike slackevents which uses reflect.New).
			cb, ok := evt.Data.(slack.InteractionCallback)
			if !ok {
				log.Printf("sockethub: interactive event has unexpected data type %T", evt.Data)
				return
			}
			// Ack FIRST so Slack's 3-second redelivery window is satisfied before
			// any handler processing. The handler must not call Ack.
			if evt.Request != nil {
				h.runner.Ack(*evt.Request)
			}
			if slot := h.interactiveHandler.Load(); slot != nil {
				slot.fn(evt, cb)
			}

		default:
			// Hello, Connected, Disconnect, ConnectionError, InvalidAuth, etc.
			// Route to the events handler so TriggerService can log/update health.
			if slot := h.eventsHandler.Load(); slot != nil {
				slot.fn(evt)
			}
		}
	})
	h.doneErr = err
	close(h.done)
	return err
}
