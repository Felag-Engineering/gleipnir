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
		want      bool
	}{
		{
			name:      "empty scope matches everything",
			scope:     SlackSubscriptionScope{},
			channelID: "C01ANY",
			isMention: false,
			want:      true,
		},
		{
			name:      "empty scope matches mention too",
			scope:     SlackSubscriptionScope{},
			channelID: "C01ANY",
			isMention: true,
			want:      true,
		},
		{
			name:      "channel matched by ID",
			scope:     SlackSubscriptionScope{Channels: []SlackChannelID{"C01ALLOWED"}},
			channelID: "C01ALLOWED",
			isMention: false,
			want:      true,
		},
		{
			name:      "channel not in scope",
			scope:     SlackSubscriptionScope{Channels: []SlackChannelID{"C01INC"}},
			channelID: "C99OTHER",
			isMention: false,
			want:      false,
		},
		{
			name:      "mention_only=true with non-mention",
			scope:     SlackSubscriptionScope{MentionOnly: true},
			channelID: "C01ANY",
			isMention: false,
			want:      false,
		},
		{
			name:      "mention_only=true with mention",
			scope:     SlackSubscriptionScope{MentionOnly: true},
			channelID: "C01ANY",
			isMention: true,
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
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.scope.matches(tc.channelID, tc.isMention)
			if got != tc.want {
				t.Errorf("matches(%q, %v): want %v, got %v",
					tc.channelID, tc.isMention, tc.want, got)
			}
		})
	}
}
