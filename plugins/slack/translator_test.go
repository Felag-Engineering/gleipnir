package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// TestTranslate is a table-driven test for the translate() pure function.
// All inner.Data values are POINTER types to match what slackevents.ParseEvent
// produces at runtime via reflect.New (see slackevents/parsers.go:122-143).
func TestTranslate(t *testing.T) {
	const botID = "U07BOT"

	cases := []struct {
		name       string
		inner      slackevents.EventsAPIInnerEvent
		teamID     string
		botID      string // empty string tests the degraded path
		wantEmit   bool
		wantKind   string
		wantErr    bool
		wantReason dropReason // expected drop reason when wantEmit=false
		// checkPayload is called when wantEmit=true to assert specific payload fields.
		checkPayload func(t *testing.T, payload []byte)
	}{
		{
			name: "plain MessageEvent emits channel_message",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C01ABC",
					ChannelType:    "channel",
					User:           "U01USER",
					Text:           "hello world",
					TimeStamp:      "1700000000.123456",
					EventTimeStamp: "1700000000.234567",
				},
			},
			teamID:   "T01TEAM",
			botID:    botID,
			wantEmit: true,
			wantKind: "channel_message",
			checkPayload: func(t *testing.T, payload []byte) {
				t.Helper()
				var p SlackChannelMessagePayload
				if err := json.Unmarshal(payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if p.ChannelID != "C01ABC" {
					t.Errorf("channel_id: want C01ABC, got %q", p.ChannelID)
				}
				if p.User != "U01USER" {
					t.Errorf("user: want U01USER, got %q", p.User)
				}
				if p.Text != "hello world" {
					t.Errorf("text: want %q, got %q", "hello world", p.Text)
				}
				if p.TeamID != "T01TEAM" {
					t.Errorf("team_id: want T01TEAM, got %q", p.TeamID)
				}
				if p.Mentioned {
					t.Error("mentioned: want false for plain message (no bot tag in text)")
				}
			},
		},
		{
			// Mention via text-scan: MessageEvent whose text contains <@botID>.
			// Previously Mentioned was hardcoded false for MessageEvent; now it
			// is computed via text-scan so both the message and app_mention twins
			// for the same event carry the same value (fixing the dedup race).
			name: "MessageEvent with bot mention tag sets mentioned=true",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C09INCIDENT",
					ChannelType:    "channel",
					User:           "U01USER",
					Text:           "<@" + botID + "> the database is degraded",
					TimeStamp:      "1700001000.000200",
					EventTimeStamp: "1700001000.000200",
				},
			},
			teamID:   "T01TEAM",
			botID:    botID,
			wantEmit: true,
			wantKind: "channel_message",
			checkPayload: func(t *testing.T, payload []byte) {
				t.Helper()
				var p SlackChannelMessagePayload
				if err := json.Unmarshal(payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if !p.Mentioned {
					t.Error("mentioned: want true for MessageEvent with bot tag in text")
				}
			},
		},
		{
			// AppMentionEvent: text-scan replaces the hardcoded true, but a real
			// app_mention will always contain the tag so the result is still true.
			// The critical property is that this twin carries the SAME value as
			// the MessageEvent twin above — fixing the host dedup race.
			name: "AppMentionEvent with bot mention tag sets mentioned=true (text-scan)",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "app_mention",
				Data: &slackevents.AppMentionEvent{
					Channel:        "C09INCIDENT",
					User:           "U01USER",
					Text:           "<@" + botID + "> the database is degraded",
					TimeStamp:      "1700001000.000200",
					EventTimeStamp: "1700001000.000200",
				},
			},
			teamID:   "T01TEAM",
			botID:    botID,
			wantEmit: true,
			wantKind: "channel_message",
			checkPayload: func(t *testing.T, payload []byte) {
				t.Helper()
				var p SlackChannelMessagePayload
				if err := json.Unmarshal(payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				// Both the MessageEvent twin and AppMentionEvent twin for the same
				// Slack mention must carry Mentioned=true so dedup is deterministic.
				if !p.Mentioned {
					t.Error("mentioned: want true for AppMentionEvent with bot tag in text")
				}
				if p.ChannelType != "channel" {
					t.Errorf("channel_type: want channel, got %q", p.ChannelType)
				}
			},
		},
		{
			// DM (channel_type=="im") routes to direct_message kind, not channel_message.
			name: "MessageEvent with channel_type=im emits direct_message",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "D05DMCHAN",
					ChannelType:    "im",
					User:           "U01USER",
					Text:           "what's on my calendar?",
					TimeStamp:      "1700002000.000100",
					EventTimeStamp: "1700002000.000100",
				},
			},
			teamID:   "T01TEAM",
			botID:    botID,
			wantEmit: true,
			wantKind: "direct_message",
			checkPayload: func(t *testing.T, payload []byte) {
				t.Helper()
				var p SlackChannelMessagePayload
				if err := json.Unmarshal(payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if p.ChannelType != "im" {
					t.Errorf("channel_type: want im, got %q", p.ChannelType)
				}
				if p.ChannelID != "D05DMCHAN" {
					t.Errorf("channel_id: want D05DMCHAN, got %q", p.ChannelID)
				}
			},
		},
		{
			// channel/group events still go to channel_message.
			name: "MessageEvent with channel_type=group emits channel_message",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C01PRIVATE",
					ChannelType:    "group",
					User:           "U01USER",
					Text:           "private channel message",
					TimeStamp:      "1700002001.000100",
					EventTimeStamp: "1700002001.000100",
				},
			},
			teamID:   "T01TEAM",
			botID:    botID,
			wantEmit: true,
			wantKind: "channel_message",
		},
		{
			// Self-trigger guard for MessageEvent: bot's own posts must be dropped.
			name: "MessageEvent from bot user is dropped (self-trigger guard)",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C01ABC",
					ChannelType:    "channel",
					User:           botID, // the bot itself
					Text:           "I just posted something",
					TimeStamp:      "1700003000.000001",
					EventTimeStamp: "1700003000.000001",
				},
			},
			teamID:     "T01TEAM",
			botID:      botID,
			wantEmit:   false,
			wantReason: dropSelfTrigger,
		},
		{
			// Self-trigger guard for AppMentionEvent.
			name: "AppMentionEvent from bot user is dropped (self-trigger guard)",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "app_mention",
				Data: &slackevents.AppMentionEvent{
					Channel:        "C01ABC",
					User:           botID, // the bot itself
					Text:           "<@" + botID + "> self-mention",
					TimeStamp:      "1700003001.000001",
					EventTimeStamp: "1700003001.000001",
				},
			},
			teamID:     "T01TEAM",
			botID:      botID,
			wantEmit:   false,
			wantReason: dropSelfTrigger,
		},
		{
			// Degraded path: when botID is "", Mentioned must be false and the
			// self-trigger guard must be inert — strictly no worse than before.
			name: "empty botID: Mentioned false, self-trigger guard inert",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C01ABC",
					ChannelType:    "channel",
					User:           "U01USER",
					Text:           "<@SOMEBOT> hello",
					TimeStamp:      "1700004000.000001",
					EventTimeStamp: "1700004000.000001",
				},
			},
			teamID:   "T01TEAM",
			botID:    "", // degraded: no bot ID cached
			wantEmit: true,
			wantKind: "channel_message",
			checkPayload: func(t *testing.T, payload []byte) {
				t.Helper()
				var p SlackChannelMessagePayload
				if err := json.Unmarshal(payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if p.Mentioned {
					t.Error("mentioned: want false when botID is empty (degraded path)")
				}
			},
		},
		{
			// Threaded replies are suppressed to prevent feedback replies from
			// firing trigger events (BLOCKING #2).  The filter is intentionally broad;
			// a future "thread_reply" event kind could re-enable threaded triggers.
			name: "threaded reply is dropped (feedback reply filter)",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:         "C09OPS",
					ChannelType:     "channel",
					User:            "U01USER",
					Text:            "rolling back now",
					TimeStamp:       "1700005000.000300",
					ThreadTimeStamp: "1700005000.000100",
					EventTimeStamp:  "1700005000.000300",
				},
			},
			teamID:     "T01TEAM",
			botID:      botID,
			wantEmit:   false,
			wantReason: dropThreadReply,
		},
		{
			name: "AppMentionEvent threaded reply is dropped",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "app_mention",
				Data: &slackevents.AppMentionEvent{
					Channel:         "C09OPS",
					User:            "U01USER",
					Text:            "<@" + botID + "> rolling back now",
					TimeStamp:       "1700005001.000300",
					ThreadTimeStamp: "1700005001.000100",
					EventTimeStamp:  "1700005001.000300",
				},
			},
			teamID:     "T01TEAM",
			botID:      botID,
			wantEmit:   false,
			wantReason: dropThreadReply,
		},
		{
			name: "SubType bot_message is dropped",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C01ABC",
					User:           "U01USER",
					Text:           "bot says hi",
					TimeStamp:      "1700006000.000001",
					EventTimeStamp: "1700006000.000001",
					SubType:        "bot_message",
				},
			},
			teamID:     "T01TEAM",
			botID:      botID,
			wantEmit:   false,
			wantReason: dropSubType,
		},
		{
			name: "SubType message_changed is dropped",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C01ABC",
					User:           "U01USER",
					Text:           "edited message",
					TimeStamp:      "1700007000.000001",
					EventTimeStamp: "1700007000.000001",
					SubType:        "message_changed",
				},
			},
			teamID:     "T01TEAM",
			botID:      botID,
			wantEmit:   false,
			wantReason: dropSubType,
		},
		{
			name: "nil inner Data is dropped",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: nil,
			},
			teamID:     "T01TEAM",
			botID:      botID,
			wantEmit:   false,
			wantReason: dropUnsupported,
		},
		{
			name: "malformed ts falls back gracefully (no error)",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C01ABC",
					ChannelType:    "channel",
					User:           "U01USER",
					Text:           "hi",
					TimeStamp:      "not-a-ts",
					EventTimeStamp: "not-a-ts",
				},
			},
			teamID:   "T01TEAM",
			botID:    botID,
			wantEmit: true,
			wantKind: "channel_message",
			// No specific payload check; we just confirm no error and emit=true.
		},
		{
			name: "unknown inner event type is dropped",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "reaction_added",
				// reaction_added events come back as a different concrete type;
				// using a string pointer here ensures the default case matches.
				Data: new(string),
			},
			teamID:     "T01TEAM",
			botID:      botID,
			wantEmit:   false,
			wantReason: dropUnsupported,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, eventID, payload, emit, reason, err := translate(tc.inner, tc.teamID, tc.botID)

			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if emit != tc.wantEmit {
				t.Errorf("emit: want %v, got %v", tc.wantEmit, emit)
			}
			if !emit {
				if reason != tc.wantReason {
					t.Errorf("reason: want %q, got %q", tc.wantReason, reason)
				}
				return
			}

			if kind != tc.wantKind {
				t.Errorf("kind: want %q, got %q", tc.wantKind, kind)
			}
			if eventID == "" {
				t.Error("eventID: want non-empty ULID string")
			}
			if len(payload) == 0 {
				t.Error("payload: want non-empty bytes")
			}

			if tc.checkPayload != nil {
				tc.checkPayload(t, payload)
			}
		})
	}
}

// TestDeriveEventIDIsDeterministic asserts that the same (channelID, ts) pair
// always produces the same ULID across two independent calls. This is the
// property that enables host-side deduplication of Slack's at-least-once delivery.
func TestDeriveEventIDIsDeterministic(t *testing.T) {
	channelID := "C01DEDUP"
	ts := "1700000000.123456"

	id1 := deriveEventID(channelID, ts)
	id2 := deriveEventID(channelID, ts)

	if id1 != id2 {
		t.Errorf("deriveEventID not deterministic: got %q then %q", id1, id2)
	}
	if id1 == "" {
		t.Error("deriveEventID returned empty string")
	}
}

// TestDeriveEventIDDiffersForDifferentInputs ensures distinct (channelID, ts)
// pairs produce distinct IDs, ruling out a constant or trivially broken hash.
func TestDeriveEventIDDiffersForDifferentInputs(t *testing.T) {
	id1 := deriveEventID("C01ABC", "1700000000.000001")
	id2 := deriveEventID("C01ABC", "1700000000.000002")
	id3 := deriveEventID("C02DEF", "1700000000.000001")

	if id1 == id2 {
		t.Error("same channel, different ts: IDs should differ")
	}
	if id1 == id3 {
		t.Error("different channel, same ts: IDs should differ")
	}
}

// TestParseSlackTS covers the happy path and the fallback behaviour.
func TestParseSlackTS(t *testing.T) {
	t.Run("valid unix seconds", func(t *testing.T) {
		got := parseSlackTS("1700000000.123456")
		want := time.Unix(1700000000, 0)
		if !got.Equal(want) {
			t.Errorf("want %v, got %v", want, got)
		}
	})

	t.Run("malformed falls back to now", func(t *testing.T) {
		before := time.Now().Add(-time.Second)
		got := parseSlackTS("not-a-number")
		after := time.Now().Add(time.Second)
		if got.Before(before) || got.After(after) {
			t.Errorf("expected fallback to time.Now(), got %v", got)
		}
	})

	t.Run("empty string falls back to now", func(t *testing.T) {
		before := time.Now().Add(-time.Second)
		got := parseSlackTS("")
		after := time.Now().Add(time.Second)
		if got.Before(before) || got.After(after) {
			t.Errorf("expected fallback to time.Now(), got %v", got)
		}
	})
}

// TestTranslateEventIDMatchesDeriveEventID verifies that the event_id embedded
// in the translate() output is consistent with a direct call to deriveEventID
// using the same inputs.
func TestTranslateEventIDMatchesDeriveEventID(t *testing.T) {
	channelID := "C01MATCH"
	ts := "1700005000.000500"

	inner := slackevents.EventsAPIInnerEvent{
		Type: "message",
		Data: &slackevents.MessageEvent{
			Channel:        channelID,
			ChannelType:    "channel",
			User:           "U01USER",
			Text:           "consistency check",
			TimeStamp:      ts,
			EventTimeStamp: ts,
		},
	}

	_, eventID, _, emit, _, err := translate(inner, "T01TEAM", "U07BOT")
	if err != nil || !emit {
		t.Fatalf("translate: emit=%v err=%v", emit, err)
	}

	want := deriveEventID(channelID, ts)
	if eventID != want {
		t.Errorf("event_id mismatch: translate produced %q, deriveEventID produced %q", eventID, want)
	}
}

// TestTranslate_SkipsThreadedReplies pins BLOCKING #2: a MessageEvent with a
// non-empty ThreadTimeStamp must not produce a trigger event regardless of SubType.
func TestTranslate_SkipsThreadedReplies(t *testing.T) {
	inner := slackevents.EventsAPIInnerEvent{
		Type: "message",
		Data: &slackevents.MessageEvent{
			Channel:         "C01CHAN",
			ChannelType:     "channel",
			User:            "U01USER",
			Text:            "I confirm the rollback",
			TimeStamp:       "1700010000.000200",
			ThreadTimeStamp: "1700010000.000100", // non-empty → threaded reply
			EventTimeStamp:  "1700010000.000200",
		},
	}
	_, _, _, emit, _, err := translate(inner, "T01TEAM", "U07BOT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if emit {
		t.Error("threaded MessageEvent must not emit a trigger event (feedback reply filter)")
	}
}

// TestTranslate_SkipsThreadedMentions pins BLOCKING #2 for AppMentionEvent:
// a threaded @-mention must not produce a trigger event.
func TestTranslate_SkipsThreadedMentions(t *testing.T) {
	inner := slackevents.EventsAPIInnerEvent{
		Type: "app_mention",
		Data: &slackevents.AppMentionEvent{
			Channel:         "C01CHAN",
			User:            "U01USER",
			Text:            "<@UBOT> help in thread",
			TimeStamp:       "1700011000.000200",
			ThreadTimeStamp: "1700011000.000100", // non-empty → threaded mention
			EventTimeStamp:  "1700011000.000200",
		},
	}
	_, _, _, emit, _, err := translate(inner, "T01TEAM", "U07BOT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if emit {
		t.Error("threaded AppMentionEvent must not emit a trigger event (feedback reply filter)")
	}
}

// TestTranslatePayloadChannelForMessage verifies the channel field in the payload
// for a MessageEvent matches the channel from the event (MessageEvent has no
// separate channel_id field so Channel is used for both).
func TestTranslatePayloadChannelForMessage(t *testing.T) {
	inner := slackevents.EventsAPIInnerEvent{
		Type: "message",
		Data: &slackevents.MessageEvent{
			Channel:        "CMYCHAN",
			ChannelType:    "channel",
			Text:           "hello",
			TimeStamp:      "1700000000.000001",
			EventTimeStamp: "1700000000.000001",
		},
	}
	_, _, payload, emit, _, err := translate(inner, "", "")
	if err != nil || !emit {
		t.Fatalf("translate: emit=%v err=%v", emit, err)
	}
	// Verify channel_id appears in the payload JSON.
	if !strings.Contains(string(payload), `"CMYCHAN"`) {
		t.Errorf("payload should contain channel ID CMYCHAN: %s", payload)
	}
}

// TestTranslateSlashCommand verifies that translateSlashCommand maps all fields
// correctly, including cmd.UserID (not cmd.User, which doesn't exist in slack-go).
func TestTranslateSlashCommand(t *testing.T) {
	cmd := slack.SlashCommand{
		Command:     "/gleipnir",
		Text:        "deploy staging",
		UserID:      "U01USER001",
		ChannelID:   "C012ABCDEF",
		ChannelName: "#ops",
		TriggerID:   "trig-1.abc",
		ResponseURL: "https://hooks.slack.com/commands/T01/...",
		TeamID:      "T03TEAM001",
	}

	eventID, payload, err := translateSlashCommand(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eventID == "" {
		t.Error("eventID: want non-empty ULID string")
	}

	var p SlackSlashCommandPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Command != "/gleipnir" {
		t.Errorf("command: want /gleipnir, got %q", p.Command)
	}
	if p.Text != "deploy staging" {
		t.Errorf("text: want %q, got %q", "deploy staging", p.Text)
	}
	// cmd.UserID maps to p.User — ensures we access UserID, not a non-existent User field.
	if p.User != "U01USER001" {
		t.Errorf("user: want U01USER001, got %q (check cmd.UserID access)", p.User)
	}
	if p.ChannelID != "C012ABCDEF" {
		t.Errorf("channel_id: want C012ABCDEF, got %q", p.ChannelID)
	}
	if p.TriggerID != "trig-1.abc" {
		t.Errorf("trigger_id: want trig-1.abc, got %q", p.TriggerID)
	}
	if p.ResponseURL != "https://hooks.slack.com/commands/T01/..." {
		t.Errorf("response_url: want non-empty, got %q", p.ResponseURL)
	}
	if p.TeamID != "T03TEAM001" {
		t.Errorf("team_id: want T03TEAM001, got %q", p.TeamID)
	}
}

// TestTranslateSlashCommandDedup verifies that the same SlashCommand input
// always produces the same eventID (dedup determinism).
func TestTranslateSlashCommandDedup(t *testing.T) {
	cmd := slack.SlashCommand{
		Command:   "/gleipnir",
		Text:      "deploy staging",
		UserID:    "U01USER001",
		ChannelID: "C012ABCDEF",
		TriggerID: "trig-dedup-1.xyz",
		TeamID:    "T03TEAM001",
	}

	id1, _, err1 := translateSlashCommand(cmd)
	id2, _, err2 := translateSlashCommand(cmd)
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v %v", err1, err2)
	}
	if id1 != id2 {
		t.Errorf("eventID not deterministic: got %q then %q", id1, id2)
	}
}

// TestTranslateShortcut_MessageAction verifies message_shortcut translation,
// including the promoted fields: cb.Message.Timestamp (from embedded slack.Msg)
// and cb.Channel.ID (from Channel→GroupConversation→Conversation.ID).
func TestTranslateShortcut_MessageAction(t *testing.T) {
	cb := slack.InteractionCallback{
		Type:       slack.InteractionTypeMessageAction,
		TriggerID:  "trig-2.def",
		CallbackID: "run_agent_on_message",
		User:       slack.User{ID: "U01USER002"},
		Team:       slack.Team{ID: "T03TEAM001"},
		Channel: slack.Channel{
			GroupConversation: slack.GroupConversation{
				Conversation: slack.Conversation{ID: "C09INCIDENT"},
			},
		},
		Message: slack.Message{
			Msg: slack.Msg{
				Text:      "the database is degraded",
				Timestamp: "1700001000.000200", // cb.Message.Timestamp — promoted from embedded slack.Msg
			},
		},
	}

	kind, eventID, payload, emit, _, err := translateShortcut(cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !emit {
		t.Fatal("emit: want true for message_action")
	}
	if kind != "message_shortcut" {
		t.Errorf("kind: want message_shortcut, got %q", kind)
	}
	if eventID == "" {
		t.Error("eventID: want non-empty ULID string")
	}

	var p SlackMessageShortcutPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Text != "the database is degraded" {
		t.Errorf("text: want %q, got %q", "the database is degraded", p.Text)
	}
	// cb.Message.Timestamp is promoted from the embedded slack.Msg.
	if p.Ts != "1700001000.000200" {
		t.Errorf("ts: want 1700001000.000200, got %q (check cb.Message.Timestamp promoted field)", p.Ts)
	}
	// cb.Channel.ID is promoted via Channel→GroupConversation→Conversation.ID.
	if p.ChannelID != "C09INCIDENT" {
		t.Errorf("channel_id: want C09INCIDENT, got %q (check cb.Channel.ID promotion)", p.ChannelID)
	}
	// cb.User.ID — not cb.UserID.
	if p.User != "U01USER002" {
		t.Errorf("user: want U01USER002, got %q (check cb.User.ID)", p.User)
	}
	if p.TriggerID != "trig-2.def" {
		t.Errorf("trigger_id: want trig-2.def, got %q", p.TriggerID)
	}
	if p.CallbackID != "run_agent_on_message" {
		t.Errorf("callback_id: want run_agent_on_message, got %q", p.CallbackID)
	}
	// cb.Team.ID — not cb.TeamID.
	if p.TeamID != "T03TEAM001" {
		t.Errorf("team_id: want T03TEAM001, got %q (check cb.Team.ID)", p.TeamID)
	}
}

// TestTranslateShortcut_Global verifies global_shortcut translation.
// For global shortcuts, cb.Channel is zero-valued (no message context);
// deriveEventID is called with an empty channelID.
func TestTranslateShortcut_Global(t *testing.T) {
	cb := slack.InteractionCallback{
		Type:       slack.InteractionTypeShortcut,
		TriggerID:  "trig-3.ghi",
		CallbackID: "start_agent",
		User:       slack.User{ID: "U01USER003"},
		Team:       slack.Team{ID: "T03TEAM001"},
		// Channel intentionally zero-valued — global shortcuts have no channel context.
	}

	kind, eventID, payload, emit, _, err := translateShortcut(cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !emit {
		t.Fatal("emit: want true for global shortcut")
	}
	if kind != "global_shortcut" {
		t.Errorf("kind: want global_shortcut, got %q", kind)
	}
	if eventID == "" {
		t.Error("eventID: want non-empty ULID string")
	}

	var p SlackGlobalShortcutPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.User != "U01USER003" {
		t.Errorf("user: want U01USER003, got %q", p.User)
	}
	if p.CallbackID != "start_agent" {
		t.Errorf("callback_id: want start_agent, got %q", p.CallbackID)
	}
}

// TestTranslateShortcut_BlockActionsNotEmitted verifies that block_actions
// callbacks return emit=false with reason=dropUnsupportedInteraction, preserving
// the ChannelService approval/feedback path.
func TestTranslateShortcut_BlockActionsNotEmitted(t *testing.T) {
	cb := slack.InteractionCallback{
		Type: slack.InteractionTypeBlockActions,
		ActionCallback: slack.ActionCallbacks{
			BlockActions: []*slack.BlockAction{
				{ActionID: "approve-req-123-accept", Value: "accept"},
			},
		},
	}

	_, _, _, emit, reason, err := translateShortcut(cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if emit {
		t.Error("emit: want false for block_actions (ChannelService path must not be hijacked)")
	}
	if reason != dropUnsupportedInteraction {
		t.Errorf("reason: want %q, got %q", dropUnsupportedInteraction, reason)
	}
}
