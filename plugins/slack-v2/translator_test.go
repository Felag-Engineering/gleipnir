package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

const (
	testBotUser = "U07BOT"
	testBotID   = "B07BOT"
)

var testSelf = botIdentity{UserID: testBotUser, BotID: testBotID}

// dataMap renders an event's data the way it goes on the wire.
func dataMap(t *testing.T, data any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal event data: %v", err)
	}
	return out
}

func TestTranslateInner(t *testing.T) {
	message := func(mod func(*slackevents.MessageEvent)) slackevents.EventsAPIInnerEvent {
		ev := &slackevents.MessageEvent{
			Channel: "C01ABC", ChannelType: "channel", User: "U01USER", Text: "hello world",
			TimeStamp: "1700000000.123456", EventTimeStamp: "1700000000.234567",
		}
		if mod != nil {
			mod(ev)
		}
		return slackevents.EventsAPIInnerEvent{Type: "message", Data: ev}
	}
	mention := func(mod func(*slackevents.AppMentionEvent)) slackevents.EventsAPIInnerEvent {
		ev := &slackevents.AppMentionEvent{
			Channel: "C09INCIDENT", User: "U01USER", Text: "<@U07BOT> the db is down",
			TimeStamp: "1700001000.000200", EventTimeStamp: "1700001000.000200",
		}
		if mod != nil {
			mod(ev)
		}
		return slackevents.EventsAPIInnerEvent{Type: "app_mention", Data: ev}
	}

	tests := []struct {
		name       string
		inner      slackevents.EventsAPIInnerEvent
		self       botIdentity
		wantReason dropReason
		wantKind   string
		wantID     string
		check      func(t *testing.T, got map[string]any)
	}{
		{
			name: "plain message", inner: message(nil), self: testSelf,
			wantKind: "channel_message", wantID: "msg:C01ABC:1700000000.123456",
			check: func(t *testing.T, got map[string]any) {
				if got["channel"] != "C01ABC" || got["user"] != "U01USER" || got["text"] != "hello world" || got["text_pattern"] != "hello world" {
					t.Errorf("data = %v", got)
				}
				if got["mentioned"] != false {
					t.Errorf("mentioned = %v, want false", got["mentioned"])
				}
			},
		},
		{
			name:     "message mentioning the bot is mentioned",
			inner:    message(func(e *slackevents.MessageEvent) { e.Text = "<@U07BOT> ping" }),
			self:     testSelf,
			wantKind: "channel_message", wantID: "msg:C01ABC:1700000000.123456",
			check: func(t *testing.T, got map[string]any) {
				if got["mentioned"] != true {
					t.Errorf("mentioned = %v, want true", got["mentioned"])
				}
			},
		},
		{
			name:     "direct message routes to direct_message",
			inner:    message(func(e *slackevents.MessageEvent) { e.Channel, e.ChannelType = "D01DM", "im" }),
			self:     testSelf,
			wantKind: "direct_message", wantID: "msg:D01DM:1700000000.123456",
			check: func(t *testing.T, got map[string]any) {
				if got["channel_type"] != "im" {
					t.Errorf("channel_type = %v", got["channel_type"])
				}
			},
		},
		{
			name:     "private channel stays channel_message",
			inner:    message(func(e *slackevents.MessageEvent) { e.ChannelType = "group" }),
			self:     testSelf,
			wantKind: "channel_message", wantID: "msg:C01ABC:1700000000.123456",
		},
		{name: "message subtype dropped", inner: message(func(e *slackevents.MessageEvent) { e.SubType = "bot_message" }), self: testSelf, wantReason: dropSubType},
		{name: "message_changed dropped", inner: message(func(e *slackevents.MessageEvent) { e.SubType = "message_changed" }), self: testSelf, wantReason: dropSubType},
		{name: "threaded reply dropped", inner: message(func(e *slackevents.MessageEvent) { e.ThreadTimeStamp = "1700000000.000001" }), self: testSelf, wantReason: dropThreadReply},
		{name: "own user dropped", inner: message(func(e *slackevents.MessageEvent) { e.User = testBotUser }), self: testSelf, wantReason: dropSelfTrigger},
		{name: "own bot_id dropped", inner: message(func(e *slackevents.MessageEvent) { e.User, e.BotID = "UOTHER", testBotID }), self: testSelf, wantReason: dropSelfTrigger},
		{
			name:  "unknown identity cannot drop anything",
			inner: message(func(e *slackevents.MessageEvent) { e.User = testBotUser }), self: botIdentity{},
			wantKind: "channel_message", wantID: "msg:C01ABC:1700000000.123456",
		},
		{
			name: "app_mention emits channel_message", inner: mention(nil), self: testSelf,
			wantKind: "channel_message", wantID: "msg:C09INCIDENT:1700001000.000200",
			check: func(t *testing.T, got map[string]any) {
				if got["mentioned"] != true || got["channel_type"] != "channel" {
					t.Errorf("data = %v", got)
				}
			},
		},
		{name: "threaded app_mention dropped", inner: mention(func(e *slackevents.AppMentionEvent) { e.ThreadTimeStamp = "1.1" }), self: testSelf, wantReason: dropThreadReply},
		{name: "app_mention by the bot dropped", inner: mention(func(e *slackevents.AppMentionEvent) { e.User = testBotUser }), self: testSelf, wantReason: dropSelfTrigger},
		{name: "app_mention by the bot_id dropped", inner: mention(func(e *slackevents.AppMentionEvent) { e.BotID = testBotID }), self: testSelf, wantReason: dropSelfTrigger},
		{
			name:       "other inner events dropped",
			inner:      slackevents.EventsAPIInnerEvent{Type: "reaction_added", Data: &slackevents.ReactionAddedEvent{}},
			self:       testSelf,
			wantReason: dropUnsupported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := translateInner(tt.inner, "T01TEAM", tt.self)
			if reason != tt.wantReason {
				t.Fatalf("drop reason = %q, want %q", reason, tt.wantReason)
			}
			if reason != "" {
				return
			}
			if got.Event.Type != tt.wantKind {
				t.Errorf("kind = %q, want %q", got.Event.Type, tt.wantKind)
			}
			if got.Event.ID != tt.wantID {
				t.Errorf("id = %q, want %q", got.Event.ID, tt.wantID)
			}
			if tt.check != nil {
				tt.check(t, dataMap(t, got.Event.Data))
			}
		})
	}
}

func TestMessageAndMentionTwinsShareAnID(t *testing.T) {
	text := "<@U07BOT> deploy"
	msg, _ := translateInner(slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1", Text: text, TimeStamp: "1700000000.000100",
	}}, "T1", testSelf)
	mention, _ := translateInner(slackevents.EventsAPIInnerEvent{Data: &slackevents.AppMentionEvent{
		Channel: "C1", User: "U1", Text: text, TimeStamp: "1700000000.000100",
	}}, "T1", testSelf)
	if msg.Event.ID == "" || msg.Event.ID != mention.Event.ID {
		t.Errorf("twin ids differ: %q vs %q", msg.Event.ID, mention.Event.ID)
	}
	// What a redelivery or a dedup survivor carries must not depend on which twin it is.
	if a, b := dataMap(t, msg.Event.Data)["mentioned"], dataMap(t, mention.Event.Data)["mentioned"]; a != b {
		t.Errorf("mentioned differs between twins: %v vs %v", a, b)
	}
}

func TestEventIDsAreDeterministicAndDistinct(t *testing.T) {
	cmd := slack.SlashCommand{Command: "/deploy", ChannelID: "C1", TriggerID: "trig-1"}
	again := slack.SlashCommand{Command: "/deploy", ChannelID: "C1", TriggerID: "trig-1"}
	other := slack.SlashCommand{Command: "/deploy", ChannelID: "C1", TriggerID: "trig-2"}
	if a, b := translateSlashCommand(cmd).Event.ID, translateSlashCommand(again).Event.ID; a != b {
		t.Errorf("same command redelivered: ids %q and %q", a, b)
	}
	if a, b := translateSlashCommand(cmd).Event.ID, translateSlashCommand(other).Event.ID; a == b {
		t.Errorf("different invocations share id %q", a)
	}
}

func TestEventTimeIsTheSlackTimestamp(t *testing.T) {
	got, _ := translateInner(slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1", TimeStamp: "1700000000.123456",
	}}, "T1", testSelf)
	if want := time.Unix(1700000000, 123456000).UTC(); !got.Event.Time.Equal(want) {
		t.Errorf("time = %v, want %v", got.Event.Time, want)
	}
	if !parseSlackTS("garbage").IsZero() {
		t.Error("an unparseable timestamp should give the zero time, not the clock")
	}
}

func TestTranslateSlashCommand(t *testing.T) {
	got := translateSlashCommand(slack.SlashCommand{
		Command: "/gleipnir", Text: "deploy staging", UserID: "U1", ChannelID: "C1", ChannelName: "ops",
		TriggerID: "trig-1", ResponseURL: "https://hooks.slack.test/x", TeamID: "T1",
	})
	if got.Event.Type != "slash_command" || got.Event.ID != "cmd:C1:trig-1" {
		t.Errorf("kind/id = %q/%q", got.Event.Type, got.Event.ID)
	}
	data := dataMap(t, got.Event.Data)
	if data["command"] != "/gleipnir" || data["user"] != "U1" || data["channel"] != "C1" || data["text_pattern"] != "deploy staging" {
		t.Errorf("data = %v", data)
	}
}

func TestTranslateShortcut(t *testing.T) {
	channel := slack.Channel{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "C1"}}}
	tests := []struct {
		name       string
		cb         slack.InteractionCallback
		wantKind   string
		wantID     string
		wantReason dropReason
	}{
		{
			name: "message shortcut",
			cb: slack.InteractionCallback{
				Type: slack.InteractionTypeMessageAction, CallbackID: "run_agent", TriggerID: "trig-2",
				User: slack.User{ID: "U1"}, Channel: channel, Team: slack.Team{ID: "T1"},
				Message: slack.Message{Msg: slack.Msg{Text: "the db is down", Timestamp: "1700001000.000200"}},
			},
			wantKind: "message_shortcut", wantID: "msgshortcut:C1:trig-2",
		},
		{
			name: "global shortcut",
			cb: slack.InteractionCallback{
				Type: slack.InteractionTypeShortcut, CallbackID: "start_agent", TriggerID: "trig-3", User: slack.User{ID: "U1"},
			},
			wantKind: "global_shortcut", wantID: "shortcut:trig-3",
		},
		{name: "block_actions is not ours", cb: slack.InteractionCallback{Type: slack.InteractionTypeBlockActions}, wantReason: dropUnsupportedInteraction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := translateShortcut(tt.cb)
			if reason != tt.wantReason {
				t.Fatalf("drop reason = %q, want %q", reason, tt.wantReason)
			}
			if reason != "" {
				return
			}
			if got.Event.Type != tt.wantKind || got.Event.ID != tt.wantID {
				t.Errorf("kind/id = %q/%q, want %q/%q", got.Event.Type, got.Event.ID, tt.wantKind, tt.wantID)
			}
		})
	}
}
