package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack/slackevents"
)

// TestTranslate is a table-driven test for the translate() pure function.
// All inner.Data values are POINTER types to match what slackevents.ParseEvent
// produces at runtime via reflect.New (see slackevents/parsers.go:122-143).
func TestTranslate(t *testing.T) {
	cases := []struct {
		name     string
		inner    slackevents.EventsAPIInnerEvent
		teamID   string
		wantEmit bool
		wantKind string
		wantErr  bool
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
					t.Error("mentioned: want false for plain message")
				}
			},
		},
		{
			name: "AppMentionEvent sets mentioned=true",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "app_mention",
				Data: &slackevents.AppMentionEvent{
					Channel:        "C09INCIDENT",
					User:           "U01USER",
					Text:           "<@U07BOT> help",
					TimeStamp:      "1700001000.000100",
					EventTimeStamp: "1700001000.000100",
				},
			},
			teamID:   "T01TEAM",
			wantEmit: true,
			wantKind: "channel_message",
			checkPayload: func(t *testing.T, payload []byte) {
				t.Helper()
				var p SlackChannelMessagePayload
				if err := json.Unmarshal(payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if !p.Mentioned {
					t.Error("mentioned: want true for app_mention")
				}
				if p.ChannelType != "channel" {
					t.Errorf("channel_type: want channel, got %q", p.ChannelType)
				}
			},
		},
		{
			name: "threaded reply includes thread_ts",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:         "C09OPS",
					ChannelType:     "channel",
					User:            "U01USER",
					Text:            "rolling back now",
					TimeStamp:       "1700002000.000300",
					ThreadTimeStamp: "1700002000.000100",
					EventTimeStamp:  "1700002000.000300",
				},
			},
			teamID:   "T01TEAM",
			wantEmit: true,
			wantKind: "channel_message",
			checkPayload: func(t *testing.T, payload []byte) {
				t.Helper()
				var p SlackChannelMessagePayload
				if err := json.Unmarshal(payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if p.ThreadTs != "1700002000.000100" {
					t.Errorf("thread_ts: want 1700002000.000100, got %q", p.ThreadTs)
				}
			},
		},
		{
			name: "SubType bot_message is dropped",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C01ABC",
					User:           "U01USER",
					Text:           "bot says hi",
					TimeStamp:      "1700003000.000001",
					EventTimeStamp: "1700003000.000001",
					SubType:        "bot_message",
				},
			},
			teamID:   "T01TEAM",
			wantEmit: false,
		},
		{
			name: "SubType message_changed is dropped",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: &slackevents.MessageEvent{
					Channel:        "C01ABC",
					User:           "U01USER",
					Text:           "edited message",
					TimeStamp:      "1700004000.000001",
					EventTimeStamp: "1700004000.000001",
					SubType:        "message_changed",
				},
			},
			teamID:   "T01TEAM",
			wantEmit: false,
		},
		{
			name: "nil inner Data is dropped",
			inner: slackevents.EventsAPIInnerEvent{
				Type: "message",
				Data: nil,
			},
			teamID:   "T01TEAM",
			wantEmit: false,
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
			teamID:   "T01TEAM",
			wantEmit: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, eventID, payload, emit, err := translate(tc.inner, tc.teamID)

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

	_, eventID, _, emit, err := translate(inner, "T01TEAM")
	if err != nil || !emit {
		t.Fatalf("translate: emit=%v err=%v", emit, err)
	}

	want := deriveEventID(channelID, ts)
	if eventID != want {
		t.Errorf("event_id mismatch: translate produced %q, deriveEventID produced %q", eventID, want)
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
	_, _, payload, emit, err := translate(inner, "")
	if err != nil || !emit {
		t.Fatalf("translate: emit=%v err=%v", emit, err)
	}
	// Verify channel_id appears in the payload JSON.
	if !strings.Contains(string(payload), `"CMYCHAN"`) {
		t.Errorf("payload should contain channel ID CMYCHAN: %s", payload)
	}
}
