package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/slack-go/slack/slackevents"
)

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
func translate(inner slackevents.EventsAPIInnerEvent, teamID, botUserID string) (kind, eventID string, payload []byte, emit bool, err error) {
	switch ev := inner.Data.(type) {
	case *slackevents.MessageEvent:
		// Drop subtypes: bot_message, message_changed, message_deleted, etc.
		// Only plain human-authored new messages (SubType == "") are emitted.
		if ev.SubType != "" {
			return "", "", nil, false, nil
		}
		// Skip threaded replies — they must not fire trigger events.
		// Feedback thread replies are handled by ChannelService's handleThreadReply,
		// not the trigger pipeline.
		// Known limitation: all threaded replies are suppressed here. If threaded
		// trigger events are needed in the future, a separate event kind (e.g.
		// "thread_reply") should be added rather than removing this filter.
		if ev.ThreadTimeStamp != "" {
			return "", "", nil, false, nil
		}
		// Self-trigger guard: drop events posted by the bot itself. The bot_message
		// subtype filter above catches most cases, but a bot can also post with its
		// human-like user ID (e.g. in DMs), so we check user == botUserID explicitly.
		if botUserID != "" && ev.User == botUserID {
			return "", "", nil, false, nil
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
			return "", "", nil, false, fmt.Errorf("translate: marshal message payload: %w", err)
		}
		return eventKind, deriveEventID(ev.Channel, ev.TimeStamp), b, true, nil

	case *slackevents.AppMentionEvent:
		// Skip threaded mentions for the same reason as MessageEvent above.
		// If thread-scoped mention triggers are needed later, add a separate event kind.
		if ev.ThreadTimeStamp != "" {
			return "", "", nil, false, nil
		}
		// Self-trigger guard: symmetry with MessageEvent — drop if bot mentions itself.
		if botUserID != "" && ev.User == botUserID {
			return "", "", nil, false, nil
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
			return "", "", nil, false, fmt.Errorf("translate: marshal mention payload: %w", err)
		}
		return "channel_message", deriveEventID(ev.Channel, ev.TimeStamp), b, true, nil

	default:
		// All other inner event types (reactions, channel events, etc.) are
		// not subscribed to by this plugin version. Drop silently.
		return "", "", nil, false, nil
	}
}
