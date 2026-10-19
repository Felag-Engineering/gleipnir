package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/events"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

const (
	// eventSourceName is the CloudEvents source on every event.
	eventSourceName = "slack-v2"

	// eventRetention is how long a delivered event stays resumable. It bounds
	// how long the host can be away and still catch up without a gap.
	eventRetention = 24 * time.Hour
)

// eventScopes is the OAuth scope each Slack event subscription needs, in the
// same shape and for the same reason as toolScopes. manifest.yaml's scopes
// must be the union of every such table; the manifest test enforces it.
// Shortcuts need no scope.
var eventScopes = map[string][]string{
	"message.channels": {"channels:history"},
	"message.groups":   {"groups:history"},
	"message.im":       {"im:history"},
	"message.mpim":     {"mpim:history"},
	"app_mention":      {"app_mentions:read"},
	"slash_command":    {"commands"},
}

// Binding field schemas. Operators follow the Option C scheme: the schema shape
// of a field decides how a policy's value is matched (plain string = equals,
// format contains, format regex, boolean = equals), and operators below
// attests exactly that, so what the manifest says a policy may express is what
// the evaluator will do.

func stringField(title, description string, extra map[string]any) map[string]any {
	field := map[string]any{"type": "string", "title": title, "description": description}
	for k, v := range extra {
		field[k] = v
	}
	return field
}

func channelField(description, pattern string) map[string]any {
	return stringField("Channel", description, map[string]any{"pattern": pattern})
}

func textFields() map[string]any {
	return map[string]any{
		"text": stringField("Text contains",
			"Case-sensitive substring; matches anywhere in the text (not anchored to the start).",
			map[string]any{"format": "contains"}),
		"text_pattern": stringField("Text matches",
			"Regular expression (RE2) matched against the text. Use text for a plain substring.",
			map[string]any{"format": "regex"}),
	}
}

func userField() map[string]any {
	return stringField("User",
		"Exact match (case-sensitive) on the sender's Slack user ID (e.g. U012AB3CD). Leave empty to match all users.", nil)
}

func bindingSchema(properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
}

func mergeFields(parts ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, part := range parts {
		for k, v := range part {
			out[k] = v
		}
	}
	return out
}

// textOperators is the operator attestation for the two text fields.
var textOperators = map[string][]string{"text": {"contains"}, "text_pattern": {"regex"}}

// operators is fields plus the text operators every text-bearing kind shares.
func operators(fields map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range textOperators {
		out[k] = v
	}
	for k, v := range fields {
		out[k] = v
	}
	return out
}

// eventKinds is declared once: events.NewHandler serves it as events/discover,
// and the manifest's event_kinds attests it. TestManifestAttestsTheServedEventKinds
// compares the two through the live server.
var eventKinds = []events.Kind{
	{
		Kind: kindChannelMessage,
		Guidance: "A human posts a message in a channel the bot can see, or @-mentions the bot in one. " +
			"The bot's own posts and threaded replies are dropped. Which channels are watched is the instance's " +
			"subscription scope; bind on channel, text, mentioned or user to narrow it per policy.",
		BindingSchema: bindingSchema(mergeFields(map[string]any{
			"channel": channelField("Exact match on the channel's Slack ID (e.g. C012AB3CD). Channel names are not accepted: events carry only IDs.", `^[CG][A-Z0-9]+$`),
			"channel_type": stringField("Channel type",
				"Exact match on the Slack channel kind: channel (public), group (private), mpim (multi-party DM). Leave empty to match any.",
				map[string]any{"enum": []string{"channel", "group", "mpim"}}),
			"mentioned": map[string]any{
				"type": "boolean", "title": "Mentioned",
				"description": "Fire only when the message @-mentions the bot (true), or only when it does not (false).",
			},
			"user": userField(),
		}, textFields())),
		Operators: operators(map[string][]string{
			"channel": {"equals"}, "channel_type": {"equals"}, "mentioned": {"equals"}, "user": {"equals"},
		}),
	},
	{
		Kind: kindDirectMessage,
		Guidance: "A human sends the bot a 1:1 direct message. Delivered only when the instance's subscription scope " +
			"enables direct messages. The bot's own messages are dropped.",
		BindingSchema: bindingSchema(mergeFields(map[string]any{
			"channel": channelField("Exact match on the DM conversation's Slack ID (e.g. D012AB3CD).", `^D[A-Z0-9]+$`),
			"user":    userField(),
		}, textFields())),
		Operators: operators(map[string][]string{"channel": {"equals"}, "user": {"equals"}}),
	},
	{
		Kind: kindSlashCommand,
		Guidance: "A user runs a /command registered in the Slack app. Not channel-scoped: it is delivered whatever the " +
			"subscription scope's channel list says. Bind on command (e.g. /deploy) and/or text.",
		BindingSchema: bindingSchema(mergeFields(map[string]any{
			"command": stringField("Command", "Exact match on the slash command name (e.g. /deploy). Leave empty to match any command.", nil),
			"channel": channelField("Exact match on the channel the command was typed in (e.g. C012AB3CD).", `^[CDG][A-Z0-9]+$`),
			"user":    userField(),
		}, textFields())),
		Operators: operators(map[string][]string{"command": {"equals"}, "channel": {"equals"}, "user": {"equals"}}),
	},
	{
		Kind: kindMessageShortcut,
		Guidance: "A user opens a message's More actions menu and picks this app's shortcut. Carries the target message's " +
			"text. Bind on callback_id, the shortcut's configured callback ID.",
		BindingSchema: bindingSchema(mergeFields(map[string]any{
			"callback_id": stringField("Callback ID", "Exact match on the shortcut's callback_id as registered in the Slack app. Leave empty to match any message shortcut.", nil),
			"channel":     channelField("Exact match on the channel holding the target message (e.g. C012AB3CD).", `^[CDG][A-Z0-9]+$`),
			"user":        userField(),
		}, textFields())),
		Operators: operators(map[string][]string{"callback_id": {"equals"}, "channel": {"equals"}, "user": {"equals"}}),
	},
	{
		Kind: kindGlobalShortcut,
		Guidance: "A user invokes the app's global shortcut from the composer's lightning menu, not tied to a message. " +
			"Bind on callback_id, the shortcut's configured callback ID.",
		BindingSchema: bindingSchema(map[string]any{
			"callback_id": stringField("Callback ID", "Exact match on the shortcut's callback_id as registered in the Slack app. Leave empty to match any global shortcut.", nil),
			"user":        userField(),
		}),
		Operators: map[string][]string{"callback_id": {"equals"}, "user": {"equals"}},
	},
}

// publisher is the slice of *events.Handler that emission needs.
type publisher interface {
	Publish(ctx context.Context, e events.Event) (uint64, error)
}

// eventSource turns Socket Mode envelopes into durable events the host pulls
// over events/listen.
type eventSource struct {
	handler  *events.Handler
	store    *events.FileStore
	scope    *scopeStore
	pub      publisher
	identity *botIdentityCache
}

// newEventSource opens the durable event store and scope under dataDir.
func newEventSource(dataDir string, provider *slackProvider) (*eventSource, error) {
	store, err := events.NewFileStore(filepath.Join(dataDir, "events"), eventRetention)
	if err != nil {
		return nil, fmt.Errorf("open event store: %w", err)
	}
	buffer, err := events.NewBufferWithStore(context.Background(), store)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open event buffer: %w", err), store.Close())
	}
	scope, err := newScopeStore(dataDir)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	handler := events.NewHandler(eventSourceName, eventKinds, events.WithBuffer(buffer))
	return &eventSource{
		handler:  handler,
		store:    store,
		scope:    scope,
		pub:      handler,
		identity: newBotIdentityCache(provider),
	}, nil
}

// listenHandler is the events/listen stream, with the scope each listen
// carries recorded on the way through.
func (e *eventSource) listenHandler() http.Handler {
	return captureScope(e.handler.ListenHandler(), e.scope)
}

// closeStreams ends every open events/listen stream cleanly; do it before
// shutting the HTTP server down, which would otherwise wait on them.
func (e *eventSource) closeStreams() { e.handler.Close("plugin shutting down") }

func (e *eventSource) close() error { return e.store.Close() }

// handleEventsAPI is the socketHandler for events_api envelopes.
func (e *eventSource) handleEventsAPI(ctx context.Context, evt socketmode.Event) (any, error) {
	api, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return nil, fmt.Errorf("events_api envelope carried %T, not an events API event", evt.Data)
	}
	if api.Type != slackevents.CallbackEvent {
		return nil, nil
	}
	scope, subscribed := e.scope.current()
	if !subscribed {
		return nil, nil
	}
	// Resolved before translating, and failing closed: without knowing who the
	// bot is the self-trigger guard cannot run, and a bot reacting to its own
	// posts is a loop, so the event is dropped (and logged) instead.
	self, err := e.identity.get(ctx)
	if err != nil {
		return nil, fmt.Errorf("drop %s event: cannot tell which messages are the bot's own: %w", api.InnerEvent.Type, err)
	}
	t, reason := translateInner(api.InnerEvent, api.TeamID, self)
	if reason != "" {
		return nil, nil
	}
	if !scope.allowsMessage(t.ChannelID, t.Mentioned, t.Event.Type == kindDirectMessage) {
		return nil, nil
	}
	return nil, e.publish(ctx, t)
}

// handleSlashCommand is the socketHandler for slash_commands envelopes.
func (e *eventSource) handleSlashCommand(ctx context.Context, evt socketmode.Event) (any, error) {
	cmd, ok := evt.Data.(slack.SlashCommand)
	if !ok {
		return nil, fmt.Errorf("slash_commands envelope carried %T, not a slash command", evt.Data)
	}
	if _, subscribed := e.scope.current(); !subscribed {
		return nil, nil
	}
	return nil, e.publish(ctx, translateSlashCommand(cmd))
}

// handleShortcut handles a shortcut or message_action interaction.
func (e *eventSource) handleShortcut(ctx context.Context, cb slack.InteractionCallback) error {
	t, reason := translateShortcut(cb)
	if reason != "" {
		return nil
	}
	if _, subscribed := e.scope.current(); !subscribed {
		return nil
	}
	return e.publish(ctx, t)
}

// publish makes the event durable before returning, which is before the hub
// acks the envelope: an event Slack considers delivered is on disk.
func (e *eventSource) publish(ctx context.Context, t translation) error {
	if _, err := e.pub.Publish(ctx, t.Event); err != nil {
		return fmt.Errorf("publish %s event %s: %w", t.Event.Type, t.Event.ID, err)
	}
	return nil
}

// botIdentityCache resolves who the bot is with auth.test and keeps the answer
// for as long as the bot token stays the same.
type botIdentityCache struct {
	provider *slackProvider

	mu       sync.Mutex
	token    string
	identity botIdentity
}

func newBotIdentityCache(provider *slackProvider) *botIdentityCache {
	return &botIdentityCache{provider: provider}
}

func (c *botIdentityCache) get(ctx context.Context) (botIdentity, error) {
	token, err := c.provider.token(ctx)
	if err != nil {
		return botIdentity{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == token {
		return c.identity, nil
	}
	client, err := c.provider.client(ctx)
	if err != nil {
		return botIdentity{}, err
	}
	resp, err := client.AuthTestContext(ctx)
	if err != nil {
		return botIdentity{}, fmt.Errorf("auth.test: %w", err)
	}
	if resp.UserID == "" {
		return botIdentity{}, errors.New("auth.test returned no user_id")
	}
	c.token = token
	c.identity = botIdentity{UserID: resp.UserID, BotID: resp.BotID}
	return c.identity, nil
}
