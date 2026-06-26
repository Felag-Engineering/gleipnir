package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// dropReason identifies why a translate call returned emit=false. The reason
// is structured so callers can log it without string formatting.
type dropReason string

const (
	dropSubType                dropReason = "subtype"
	dropThreadReply            dropReason = "threaded_reply"
	dropSelfTrigger            dropReason = "self_trigger"
	dropUnsupported            dropReason = "unsupported_event"
	dropUnsupportedInteraction dropReason = "unsupported_interaction"
)

// logLevelIsDebug returns true when GLEIPNIR_LOG_LEVEL is "debug" (case-insensitive).
// Used to gate per-event host Log RPCs so no extra RPCs are issued at the
// default info level.
func logLevelIsDebug() bool {
	return strings.EqualFold(os.Getenv("GLEIPNIR_LOG_LEVEL"), "debug")
}

// parseSlackTS parses a Slack message timestamp of the form "1700000000.123456"
// into a time.Time. Falls back to time.Now() on parse error so callers always
// get a valid timestamp for ULID generation.
func parseSlackTS(ts string) time.Time {
	parts := strings.SplitN(ts, ".", 2)
	if len(parts) == 0 || parts[0] == "" {
		return time.Now()
	}
	sec, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Now()
	}
	return time.Unix(sec, 0)
}

// deriveEventID produces a stable, time-sortable event ID from a Slack channel
// ID and message timestamp. The same (channelID, ts) pair always produces the
// same ULID, which lets the host deduplicate redelivered events within its
// 1-hour dedup window (spec §4.3.2).
//
// Implementation: SHA-256 of "channelID:ts" provides 32 bytes of deterministic
// entropy; we take the first 10 bytes as the ULID entropy source. The ULID
// timestamp component is derived from parseSlackTS(ts).
func deriveEventID(channelID, ts string) string {
	h := sha256.Sum256([]byte(channelID + ":" + ts))
	entropy := bytes.NewReader(h[:10])
	id := ulid.MustNew(ulid.Timestamp(parseSlackTS(ts)), entropy)
	return id.String()
}

// translate converts a Slack EventsAPIInnerEvent into a trigger event payload.
// Returns emit=false (with no error) for events that should be silently dropped:
// non-message/mention inner event types, message events with a non-empty
// SubType (bot_message, message_changed, etc.), and events where the sender
// is the bot itself (self-trigger guard).
//
// The teamID parameter is sourced from the outer EventsAPIEvent.TeamID and is
// best-effort — it may be empty for some Slack event shapes.
//
// The botUserID parameter is the bot's own Slack user ID, used to:
//   - Compute Mentioned via text-scan (`<@botUserID>` in message text) for both
//     MessageEvent and AppMentionEvent, replacing the old hardcoded false/true.
//     This makes the two dedup twins (message + app_mention for the same event)
//     carry the same Mentioned value so whichever survives dedup is correct.
//   - Guard against self-triggers: events posted by the bot are dropped.
//
// Pass "" when the bot user ID is not yet known (degraded path). In that case
// Mentioned is always false and the self-trigger guard is inert — strictly no
// worse than the pre-fix behavior.
//
// IMPORTANT: slackevents.ParseEvent populates InnerEvent.Data via reflect.New,
// which returns a pointer. The type switch MUST use pointer cases (*MessageEvent,
// *AppMentionEvent) or the default case will always match.
func translate(inner slackevents.EventsAPIInnerEvent, teamID, botUserID string) (kind, eventID string, payload []byte, emit bool, reason dropReason, err error) {
	switch ev := inner.Data.(type) {
	case *slackevents.MessageEvent:
		// Drop subtypes: bot_message, message_changed, message_deleted, etc.
		// Only plain human-authored new messages (SubType == "") are emitted.
		if ev.SubType != "" {
			return "", "", nil, false, dropSubType, nil
		}
		// Skip threaded replies — they must not fire trigger events.
		// Feedback thread replies are handled by ChannelService's handleThreadReply,
		// not the trigger pipeline.
		// Known limitation: all threaded replies are suppressed here. If threaded
		// trigger events are needed in the future, a separate event kind (e.g.
		// "thread_reply") should be added rather than removing this filter.
		if ev.ThreadTimeStamp != "" {
			return "", "", nil, false, dropThreadReply, nil
		}
		// Self-trigger guard: drop events posted by the bot itself. The bot_message
		// subtype filter above catches most cases, but a bot can also post with its
		// human-like user ID (e.g. in DMs), so we check user == botUserID explicitly.
		if botUserID != "" && ev.User == botUserID {
			return "", "", nil, false, dropSelfTrigger, nil
		}
		// Compute Mentioned via text-scan rather than hardcoding false. This makes
		// the MessageEvent and AppMentionEvent twins for the same Slack mention carry
		// the same Mentioned value, so whichever wins the host dedup check is correct.
		mentioned := botUserID != "" && strings.Contains(ev.Text, "<@"+botUserID+">")

		// Route by channel type: 1:1 DMs (channel_type=="im") go to direct_message;
		// everything else (channel, group, mpim, or unset) goes to channel_message.
		eventKind := "channel_message"
		if ev.ChannelType == "im" {
			eventKind = "direct_message"
		}
		p := SlackChannelMessagePayload{
			Channel:     ev.Channel,
			ChannelID:   ev.Channel, // MessageEvent has no separate channel_id field
			Text:        ev.Text,
			User:        ev.User,
			Ts:          ev.TimeStamp,
			ThreadTs:    ev.ThreadTimeStamp,
			EventTs:     ev.EventTimeStamp,
			TeamID:      teamID,
			ChannelType: ev.ChannelType,
			Mentioned:   mentioned,
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", "", nil, false, "", fmt.Errorf("translate: marshal message payload: %w", err)
		}
		return eventKind, deriveEventID(ev.Channel, ev.TimeStamp), b, true, "", nil

	case *slackevents.AppMentionEvent:
		// Skip threaded mentions for the same reason as MessageEvent above.
		// If thread-scoped mention triggers are needed later, add a separate event kind.
		if ev.ThreadTimeStamp != "" {
			return "", "", nil, false, dropThreadReply, nil
		}
		// Self-trigger guard: symmetry with MessageEvent — drop if bot mentions itself.
		if botUserID != "" && ev.User == botUserID {
			return "", "", nil, false, dropSelfTrigger, nil
		}
		// Compute Mentioned via text-scan (same logic as MessageEvent) so both twins
		// carry the same value. A real app_mention will contain the tag, so in
		// practice this is still deterministically true — but the text-scan makes it
		// explicit and consistent with the MessageEvent twin, fixing the dedup race.
		mentioned := botUserID != "" && strings.Contains(ev.Text, "<@"+botUserID+">")
		p := SlackChannelMessagePayload{
			Channel:     ev.Channel,
			ChannelID:   ev.Channel, // AppMentionEvent has no separate channel_id field
			Text:        ev.Text,
			User:        ev.User,
			Ts:          ev.TimeStamp,
			ThreadTs:    ev.ThreadTimeStamp,
			EventTs:     ev.EventTimeStamp,
			TeamID:      teamID,
			ChannelType: "channel", // app_mention only fires in channels
			Mentioned:   mentioned,
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", "", nil, false, "", fmt.Errorf("translate: marshal mention payload: %w", err)
		}
		return "channel_message", deriveEventID(ev.Channel, ev.TimeStamp), b, true, "", nil

	default:
		// All other inner event types (reactions, channel events, etc.) are
		// not subscribed to by this plugin version. Drop silently.
		return "", "", nil, false, dropUnsupported, nil
	}
}

// translateSlashCommand converts a Slack SlashCommand into a slash_command event
// payload. Slash commands are always delivered with explicit user intent — no
// drop/subtype filtering; this function always returns a payload.
//
// eventID is derived from (channelID, triggerID): TriggerID is unique per
// invocation and serves as the dedup key; deriveEventID's second arg (normally
// a Slack timestamp) is reused here with the TriggerID. The ULID time component
// falls back to now() since TriggerID is not a "sec.frac" string — harmless,
// as dedup is driven by SHA-256("channelID:triggerID"), not the time component.
func translateSlashCommand(cmd slack.SlashCommand) (eventID string, payload []byte, err error) {
	p := SlackSlashCommandPayload{
		Command:     cmd.Command,
		Text:        cmd.Text,
		User:        cmd.UserID, // NOTE: UserID, not cmd.User (no such field)
		ChannelID:   cmd.ChannelID,
		ChannelName: cmd.ChannelName,
		TriggerID:   cmd.TriggerID,
		ResponseURL: cmd.ResponseURL,
		TeamID:      cmd.TeamID,
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", nil, fmt.Errorf("translateSlashCommand: marshal payload: %w", err)
	}
	return deriveEventID(cmd.ChannelID, cmd.TriggerID), b, nil
}

// translateShortcut converts a Slack InteractionCallback into a message_shortcut
// or global_shortcut event. Returns emit=false for block_actions and any other
// interaction type so the ChannelService approval/feedback path is not hijacked.
//
// Field notes:
//   - cb.Message.Timestamp: promoted from the embedded slack.Msg (json tag "ts").
//   - cb.Channel.ID: promoted via Channel→GroupConversation→Conversation.ID.
//   - cb.User.ID: cb.User is slack.User; use .ID, not cb.UserID (no such field).
//   - cb.Team.ID: cb.Team is slack.Team; use .ID, not cb.TeamID (no such field).
//   - For global shortcuts cb.Channel is zero-valued (no message context), so
//     deriveEventID is called with an empty channelID.
func translateShortcut(cb slack.InteractionCallback) (kind, eventID string, payload []byte, emit bool, reason dropReason, err error) {
	switch cb.Type {
	case slack.InteractionTypeMessageAction:
		p := SlackMessageShortcutPayload{
			Text:       cb.Message.Text,
			Ts:         cb.Message.Timestamp, // promoted from embedded slack.Msg (json "ts")
			ChannelID:  cb.Channel.ID,        // promoted via Channel→GroupConversation→Conversation.ID
			User:       cb.User.ID,           // cb.User is slack.User; NOT cb.UserID
			TriggerID:  cb.TriggerID,
			CallbackID: cb.CallbackID,
			TeamID:     cb.Team.ID, // cb.Team is slack.Team; NOT cb.TeamID
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", "", nil, false, "", fmt.Errorf("translateShortcut message_action: marshal payload: %w", err)
		}
		return "message_shortcut", deriveEventID(cb.Channel.ID, cb.TriggerID), b, true, "", nil

	case slack.InteractionTypeShortcut:
		p := SlackGlobalShortcutPayload{
			User:       cb.User.ID,
			TriggerID:  cb.TriggerID,
			CallbackID: cb.CallbackID,
			TeamID:     cb.Team.ID,
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", "", nil, false, "", fmt.Errorf("translateShortcut global: marshal payload: %w", err)
		}
		// Global shortcuts have no channel context — use empty channelID.
		return "global_shortcut", deriveEventID("", cb.TriggerID), b, true, "", nil

	default:
		// block_actions and any other type: emit=false so the ChannelService
		// approval/feedback path falls through untouched.
		return "", "", nil, false, dropUnsupportedInteraction, nil
	}
}
