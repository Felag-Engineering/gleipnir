package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/events"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// Event kinds the plugin emits.
const (
	kindChannelMessage  = "channel_message"
	kindDirectMessage   = "direct_message"
	kindSlashCommand    = "slash_command"
	kindMessageShortcut = "message_shortcut"
	kindGlobalShortcut  = "global_shortcut"
)

// dropReason says why a translation produced no event, so callers can log it
// without formatting.
type dropReason string

const (
	dropSubType                dropReason = "subtype"
	dropThreadReply            dropReason = "threaded_reply"
	dropSelfTrigger            dropReason = "self_trigger"
	dropUnsupported            dropReason = "unsupported_event"
	dropUnsupportedInteraction dropReason = "unsupported_interaction"
)

// botIdentity is who the plugin's own bot is, as Slack reports it for the bot
// token (auth.test). It is what lets the plugin recognise its own posts.
type botIdentity struct {
	UserID string
	BotID  string
}

// authoredBy reports whether a message with this author and bot_id came from
// the bot itself. An empty identity field never matches, so an unknown half of
// the identity cannot make every message look like the bot's.
func (b botIdentity) authoredBy(userID, botID string) bool {
	if b.UserID != "" && userID == b.UserID {
		return true
	}
	return b.BotID != "" && botID == b.BotID
}

// mentions reports whether text contains an @-mention of the bot.
func (b botIdentity) mentions(text string) bool {
	return b.UserID != "" && strings.Contains(text, "<@"+b.UserID+">")
}

// translation is one Slack payload turned into an event, plus what scope
// filtering needs to know about it.
type translation struct {
	Event events.Event
	// ChannelID and Mentioned feed the subscription scope check; they are only
	// meaningful for the message kinds.
	ChannelID string
	Mentioned bool
}

// The event data. Every field a binding can name is always present (no
// omitempty), so an absent value reads as "" rather than as a field the
// payload lacks; the declared binding fields are checked against these by
// TestEveryBindingFieldIsPopulated.
//
// text_pattern duplicates text on purpose: binding operators are chosen by the
// schema shape of one field, so contains and regex matching on the same Slack
// text are two fields, and the evaluator reads each from the payload under its
// own name.

type messageData struct {
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	Text        string `json:"text"`
	TextPattern string `json:"text_pattern"`
	User        string `json:"user"`
	Mentioned   bool   `json:"mentioned"`
	Ts          string `json:"ts"`
	ThreadTs    string `json:"thread_ts,omitempty"`
	EventTs     string `json:"event_ts,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
}

type slashCommandData struct {
	Command     string `json:"command"`
	Channel     string `json:"channel"`
	Text        string `json:"text"`
	TextPattern string `json:"text_pattern"`
	User        string `json:"user"`
	ChannelName string `json:"channel_name,omitempty"`
	TriggerID   string `json:"trigger_id,omitempty"`
	ResponseURL string `json:"response_url,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
}

type messageShortcutData struct {
	CallbackID  string `json:"callback_id"`
	Channel     string `json:"channel"`
	Text        string `json:"text"`
	TextPattern string `json:"text_pattern"`
	User        string `json:"user"`
	Ts          string `json:"ts"`
	TriggerID   string `json:"trigger_id,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
}

type globalShortcutData struct {
	CallbackID string `json:"callback_id"`
	User       string `json:"user"`
	TriggerID  string `json:"trigger_id,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
}

// parseSlackTS parses a Slack timestamp ("1700000000.123456"). It returns the
// zero time when ts is not one, so the event store stamps its own clock rather
// than this code inventing a time.
func parseSlackTS(ts string) time.Time {
	secs, frac, _ := strings.Cut(ts, ".")
	sec, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return time.Time{}
	}
	var micros int64
	if frac != "" {
		micros, err = strconv.ParseInt((frac + "000000")[:6], 10, 64)
		if err != nil {
			return time.Time{}
		}
	}
	return time.Unix(sec, micros*1000).UTC()
}

// The event ids. Each is built from identifiers Slack itself assigns, never
// from a clock or a counter, so Slack redelivering the same payload over Socket
// Mode produces the same id and the host's dedup absorbs it. A message and its
// app_mention twin deliberately share an id: they are one occurrence.

func messageEventID(channel, ts string) string { return "msg:" + channel + ":" + ts }

func slashCommandEventID(channel, triggerID string) string {
	return "cmd:" + channel + ":" + triggerID
}

func messageShortcutEventID(channel, triggerID string) string {
	return "msgshortcut:" + channel + ":" + triggerID
}

func globalShortcutEventID(triggerID string) string { return "shortcut:" + triggerID }

// translateInner converts an Events API inner event into a message event. A
// non-empty drop reason means the event is to be discarded silently.
//
// slackevents.ParseEvent fills InnerEvent.Data with a pointer, so the type
// switch must use pointer cases or every event falls through to the default.
//
// Threaded replies are dropped: a reply is not a new conversation to start a
// run from. If thread triggers are ever wanted they should be their own kind
// rather than a relaxation of this filter.
func translateInner(inner slackevents.EventsAPIInnerEvent, teamID string, self botIdentity) (translation, dropReason) {
	switch ev := inner.Data.(type) {
	case *slackevents.MessageEvent:
		// bot_message, message_changed, message_deleted and friends carry a
		// subtype; only plain new messages start a run.
		if ev.SubType != "" {
			return translation{}, dropSubType
		}
		if ev.ThreadTimeStamp != "" {
			return translation{}, dropThreadReply
		}
		// The subtype filter does not catch everything the bot posts: it can
		// appear under its own user ID with no subtype, notably in DMs.
		if self.authoredBy(ev.User, ev.BotID) {
			return translation{}, dropSelfTrigger
		}
		// Computed from the text for both this event and its app_mention twin,
		// so whichever survives dedup carries the same value.
		mentioned := self.mentions(ev.Text)
		kind := kindChannelMessage
		if ev.ChannelType == slackevents.ChannelTypeIM {
			kind = kindDirectMessage
		}
		return translation{
			Event: events.Event{
				Type: kind,
				ID:   messageEventID(ev.Channel, ev.TimeStamp),
				Time: parseSlackTS(ev.TimeStamp),
				Data: messageData{
					Channel:     ev.Channel,
					ChannelType: ev.ChannelType,
					Text:        ev.Text,
					TextPattern: ev.Text,
					User:        ev.User,
					Mentioned:   mentioned,
					Ts:          ev.TimeStamp,
					EventTs:     ev.EventTimeStamp,
					TeamID:      teamID,
				},
			},
			ChannelID: ev.Channel,
			Mentioned: mentioned,
		}, ""

	case *slackevents.AppMentionEvent:
		if ev.ThreadTimeStamp != "" {
			return translation{}, dropThreadReply
		}
		if self.authoredBy(ev.User, ev.BotID) {
			return translation{}, dropSelfTrigger
		}
		mentioned := self.mentions(ev.Text)
		return translation{
			Event: events.Event{
				Type: kindChannelMessage,
				ID:   messageEventID(ev.Channel, ev.TimeStamp),
				Time: parseSlackTS(ev.TimeStamp),
				Data: messageData{
					Channel: ev.Channel,
					// app_mention only fires in channels the bot was invited to.
					ChannelType: slackevents.ChannelTypeChannel,
					Text:        ev.Text,
					TextPattern: ev.Text,
					User:        ev.User,
					Mentioned:   mentioned,
					Ts:          ev.TimeStamp,
					EventTs:     ev.EventTimeStamp,
					TeamID:      teamID,
				},
			},
			ChannelID: ev.Channel,
			Mentioned: mentioned,
		}, ""

	default:
		return translation{}, dropUnsupported
	}
}

// translateSlashCommand converts a slash command invocation. A command is
// explicit intent, so nothing is filtered out.
func translateSlashCommand(cmd slack.SlashCommand) translation {
	return translation{
		Event: events.Event{
			Type: kindSlashCommand,
			ID:   slashCommandEventID(cmd.ChannelID, cmd.TriggerID),
			Data: slashCommandData{
				Command:     cmd.Command,
				Channel:     cmd.ChannelID,
				Text:        cmd.Text,
				TextPattern: cmd.Text,
				User:        cmd.UserID,
				ChannelName: cmd.ChannelName,
				TriggerID:   cmd.TriggerID,
				ResponseURL: cmd.ResponseURL,
				TeamID:      cmd.TeamID,
			},
		},
	}
}

// translateShortcut converts a message or global shortcut invocation. Any other
// interaction (block_actions above all) is dropped here so the channel click
// path is never hijacked.
func translateShortcut(cb slack.InteractionCallback) (translation, dropReason) {
	switch cb.Type {
	case slack.InteractionTypeMessageAction:
		return translation{
			Event: events.Event{
				Type: kindMessageShortcut,
				ID:   messageShortcutEventID(cb.Channel.ID, cb.TriggerID),
				Data: messageShortcutData{
					CallbackID:  cb.CallbackID,
					Channel:     cb.Channel.ID,
					Text:        cb.Message.Text,
					TextPattern: cb.Message.Text,
					User:        cb.User.ID,
					Ts:          cb.Message.Timestamp,
					TriggerID:   cb.TriggerID,
					TeamID:      cb.Team.ID,
				},
			},
		}, ""

	case slack.InteractionTypeShortcut:
		return translation{
			Event: events.Event{
				Type: kindGlobalShortcut,
				ID:   globalShortcutEventID(cb.TriggerID),
				Data: globalShortcutData{
					CallbackID: cb.CallbackID,
					User:       cb.User.ID,
					TriggerID:  cb.TriggerID,
					TeamID:     cb.Team.ID,
				},
			},
		}, ""
	}
	return translation{}, dropUnsupportedInteraction
}
