package main

import (
	"testing"
)

// TestDecodeSubscriptionScope checks that empty/minimal JSON decodes correctly.
func TestDecodeSubscriptionScope(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, s SlackSubscriptionScope)
	}{
		{
			name:  "empty string returns zero value",
			input: "",
			check: func(t *testing.T, s SlackSubscriptionScope) {
				t.Helper()
				if len(s.Channels) != 0 {
					t.Errorf("channels: want empty, got %v", s.Channels)
				}
				if s.MentionOnly {
					t.Error("mention_only: want false")
				}
				if s.DirectMessages {
					t.Error("direct_messages: want false")
				}
			},
		},
		{
			name:  "empty object returns zero value",
			input: "{}",
			check: func(t *testing.T, s SlackSubscriptionScope) {
				t.Helper()
				if len(s.Channels) != 0 {
					t.Errorf("channels: want empty, got %v", s.Channels)
				}
			},
		},
		{
			name:  "channels list decoded correctly",
			input: `{"channels":["C012INC","C09OTHER"]}`,
			check: func(t *testing.T, s SlackSubscriptionScope) {
				t.Helper()
				if len(s.Channels) != 2 {
					t.Fatalf("channels: want 2, got %d", len(s.Channels))
				}
			},
		},
		{
			name:  "mention_only decoded correctly",
			input: `{"mention_only":true}`,
			check: func(t *testing.T, s SlackSubscriptionScope) {
				t.Helper()
				if !s.MentionOnly {
					t.Error("mention_only: want true")
				}
			},
		},
		{
			name:  "direct_messages decoded correctly",
			input: `{"direct_messages":true}`,
			check: func(t *testing.T, s SlackSubscriptionScope) {
				t.Helper()
				if !s.DirectMessages {
					t.Error("direct_messages: want true")
				}
			},
		},
		{
			name:    "malformed JSON returns error",
			input:   `{not-valid`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := decodeSubscriptionScope(tc.input)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantErr && tc.check != nil {
				tc.check(t, s)
			}
		})
	}
}

// TestSlackSubscriptionScopeMatches is a table-driven test for the matches
// method. After ID-only scope enforcement, channel names are no longer a
// valid input — the schema rejects them at save time — so the runtime check
// is ID-only.
func TestSlackSubscriptionScopeMatches(t *testing.T) {
	cases := []struct {
		name      string
		scope     SlackSubscriptionScope
		channelID string
		isMention bool
		isDM      bool
		want      bool
	}{
		{
			name:      "empty scope matches everything (non-DM)",
			scope:     SlackSubscriptionScope{},
			channelID: "C01ANY",
			isMention: false,
			isDM:      false,
			want:      true,
		},
		{
			name:      "empty scope matches mention too",
			scope:     SlackSubscriptionScope{},
			channelID: "C01ANY",
			isMention: true,
			isDM:      false,
			want:      true,
		},
		{
			name:      "channel matched by ID",
			scope:     SlackSubscriptionScope{Channels: []SlackChannelID{"C01ALLOWED"}},
			channelID: "C01ALLOWED",
			isMention: false,
			isDM:      false,
			want:      true,
		},
		{
			name:      "channel not in scope",
			scope:     SlackSubscriptionScope{Channels: []SlackChannelID{"C01INC"}},
			channelID: "C99OTHER",
			isMention: false,
			isDM:      false,
			want:      false,
		},
		{
			name:      "mention_only=true with non-mention",
			scope:     SlackSubscriptionScope{MentionOnly: true},
			channelID: "C01ANY",
			isMention: false,
			isDM:      false,
			want:      false,
		},
		{
			name:      "mention_only=true with mention",
			scope:     SlackSubscriptionScope{MentionOnly: true},
			channelID: "C01ANY",
			isMention: true,
			isDM:      false,
			want:      true,
		},
		{
			name: "mention_only + channel filter both satisfied",
			scope: SlackSubscriptionScope{
				MentionOnly: true,
				Channels:    []SlackChannelID{"C01INC"},
			},
			channelID: "C01INC",
			isMention: true,
			isDM:      false,
			want:      true,
		},
		{
			name: "mention_only + channel filter: channel fails",
			scope: SlackSubscriptionScope{
				MentionOnly: true,
				Channels:    []SlackChannelID{"C01INC"},
			},
			channelID: "C99OTHER",
			isMention: true,
			isDM:      false,
			want:      false,
		},
		{
			name: "mention_only + channel filter: mention fails",
			scope: SlackSubscriptionScope{
				MentionOnly: true,
				Channels:    []SlackChannelID{"C01INC"},
			},
			channelID: "C01INC",
			isMention: false,
			isDM:      false,
			want:      false,
		},
		// ── DM cases ──────────────────────────────────────────────────────────────
		{
			name:      "DM + DirectMessages=true matches",
			scope:     SlackSubscriptionScope{DirectMessages: true},
			channelID: "D05DMCHAN",
			isMention: false,
			isDM:      true,
			want:      true,
		},
		{
			name:      "DM + DirectMessages=false does not match",
			scope:     SlackSubscriptionScope{DirectMessages: false},
			channelID: "D05DMCHAN",
			isMention: false,
			isDM:      true,
			want:      false,
		},
		{
			// Regression test: isDM short-circuit means MentionOnly and a non-empty
			// Channels allow-list must NOT block a DM even when both are set. This
			// is the footgun the isDM-first ordering prevents.
			name: "DM + DirectMessages=true overrides MentionOnly and Channels",
			scope: SlackSubscriptionScope{
				DirectMessages: true,
				MentionOnly:    true,
				Channels:       []SlackChannelID{"C01INC"},
			},
			channelID: "D05DMCHAN",
			isMention: false,
			isDM:      true,
			want:      true,
		},
		{
			// A channel event is unaffected by DirectMessages.
			name: "channel event with DirectMessages=true still uses channel logic",
			scope: SlackSubscriptionScope{
				DirectMessages: true,
				Channels:       []SlackChannelID{"C01INC"},
			},
			channelID: "C99OTHER",
			isMention: false,
			isDM:      false,
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.scope.matches(tc.channelID, tc.isMention, tc.isDM)
			if got != tc.want {
				t.Errorf("matches(%q, isMention=%v, isDM=%v): want %v, got %v",
					tc.channelID, tc.isMention, tc.isDM, tc.want, got)
			}
		})
	}
}
