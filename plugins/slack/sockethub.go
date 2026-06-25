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

// slashCommandHandlerSlot wraps the slash command handler for atomic.Pointer storage.
// The hub has already acked before dispatching; handler must NOT call Ack.
type slashCommandHandlerSlot struct {
	fn func(slack.SlashCommand)
}

// triggerInteractiveHandlerSlot wraps a handler that receives a pre-decoded
// InteractionCallback for shortcut delivery. The socketmode.Event wrapper is
// dropped (the hub has already acked and decoded the callback).
// Handler must NOT call Ack.
type triggerInteractiveHandlerSlot struct {
	fn func(slack.InteractionCallback)
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
	runner                    socketModeRunner
	xappToken                 string
	eventsHandler             atomic.Pointer[eventsHandlerSlot]
	interactiveHandler        atomic.Pointer[interactiveHandlerSlot]
	messageHandler            atomic.Pointer[messageHandlerSlot]
	slashCommandHandler       atomic.Pointer[slashCommandHandlerSlot]
	triggerInteractiveHandler atomic.Pointer[triggerInteractiveHandlerSlot]

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

// RegisterSlashCommandHandler registers the handler called for slash command events.
// The hub acks the event before dispatching; the handler must NOT call Ack.
// Replaces any previously registered handler atomically.
func (h *socketHub) RegisterSlashCommandHandler(fn func(slack.SlashCommand)) {
	h.slashCommandHandler.Store(&slashCommandHandlerSlot{fn: fn})
}

// RegisterTriggerInteractiveHandler registers a secondary handler for interactive
// events (shortcuts) that runs after the existing interactiveHandler. The hub
// acks before dispatching; the handler must NOT call Ack. Receives the same
// pre-decoded InteractionCallback as the primary handler; both are isolated by
// cb.Type (block_actions vs shortcut/message_action). Replaces atomically.
func (h *socketHub) RegisterTriggerInteractiveHandler(fn func(slack.InteractionCallback)) {
	h.triggerInteractiveHandler.Store(&triggerInteractiveHandlerSlot{fn: fn})
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
// Interactive events (block_actions and shortcuts) and slash commands are Acked
// by the hub before dispatching to any handler — handlers must not call Ack.
// EventsAPI events are Acked by TriggerService's onEvent to avoid double-Ack.
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

		case socketmode.EventTypeSlashCommand:
			// SlashCommand is a VALUE type in evt.Data (not a pointer),
			// per socket_mode_managed_conn.go:639-645.
			cmd, ok := evt.Data.(slack.SlashCommand)
			if !ok {
				log.Printf("sockethub: slash command event has unexpected data type %T", evt.Data)
				return
			}
			// Ack FIRST to satisfy Slack's ~3s budget before any handler processing.
			if evt.Request != nil {
				h.runner.Ack(*evt.Request)
			}
			if slot := h.slashCommandHandler.Load(); slot != nil {
				slot.fn(cmd)
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
			// Primary handler: ChannelService owns block_actions (approval/feedback).
			if slot := h.interactiveHandler.Load(); slot != nil {
				slot.fn(evt, cb)
			}
			// Secondary handler: TriggerService handles shortcut types.
			// Both handlers receive the same cb and are isolated by cb.Type —
			// interactiveHandler early-returns on non-block_actions, and
			// translateShortcut returns emit=false on block_actions.
			if tslot := h.triggerInteractiveHandler.Load(); tslot != nil {
				tslot.fn(cb)
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
