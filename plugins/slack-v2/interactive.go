package main

import (
	"context"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// routeInteractive is the one handler for interactive envelopes. The hub allows
// a single handler per envelope type, and two consumers share this one:
// shortcuts and message actions are events, everything else (block_actions
// above all) belongs to the channel click path. Anything that is not a
// shortcut goes to the click handler unchanged, so its fail-closed handling of
// odd payloads is exactly what it was before shortcuts existed.
func routeInteractive(clicks socketHandler, events *eventSource) socketHandler {
	return func(ctx context.Context, evt socketmode.Event) (any, error) {
		if cb, ok := evt.Data.(slack.InteractionCallback); ok && isShortcut(cb) {
			return nil, events.handleShortcut(ctx, cb)
		}
		return clicks(ctx, evt)
	}
}

func isShortcut(cb slack.InteractionCallback) bool {
	return cb.Type == slack.InteractionTypeShortcut || cb.Type == slack.InteractionTypeMessageAction
}
