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
			input: `{"channels":["#incidents","C01ABC"]}`,
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

// TestSlackSubscriptionScopeMatches is a table-driven test for the matches method.
func TestSlackSubscriptionScopeMatches(t *testing.T) {
	cases := []struct {
		name        string
		scope       SlackSubscriptionScope
		channelID   string
		channelName string
		isMention   bool
		want        bool
	}{
		{
			name:        "empty scope matches everything",
			scope:       SlackSubscriptionScope{},
			channelID:   "C01ANY",
			channelName: "anything",
			isMention:   false,
			want:        true,
		},
		{
			name:        "empty scope matches mention too",
			scope:       SlackSubscriptionScope{},
			channelID:   "C01ANY",
			channelName: "anything",
			isMention:   true,
			want:        true,
		},
		{
			name:        "channel matched by ID",
			scope:       SlackSubscriptionScope{Channels: []string{"C01ALLOWED"}},
			channelID:   "C01ALLOWED",
			channelName: "some-channel",
			isMention:   false,
			want:        true,
		},
		{
			name:        "channel matched by name without hash",
			scope:       SlackSubscriptionScope{Channels: []string{"incidents"}},
			channelID:   "C01ANY",
			channelName: "incidents",
			isMention:   false,
			want:        true,
		},
		{
			name:        "channel matched by name with hash in scope",
			scope:       SlackSubscriptionScope{Channels: []string{"#incidents"}},
			channelID:   "C01ANY",
			channelName: "incidents",
			isMention:   false,
			want:        true,
		},
		{
			name:        "channel matched by name with hash in both",
			scope:       SlackSubscriptionScope{Channels: []string{"#incidents"}},
			channelID:   "C01ANY",
			channelName: "#incidents",
			isMention:   false,
			want:        true,
		},
		{
			name:        "channel not in scope",
			scope:       SlackSubscriptionScope{Channels: []string{"#incidents"}},
			channelID:   "C99OTHER",
			channelName: "general",
			isMention:   false,
			want:        false,
		},
		{
			name:        "mention_only=true with non-mention",
			scope:       SlackSubscriptionScope{MentionOnly: true},
			channelID:   "C01ANY",
			channelName: "general",
			isMention:   false,
			want:        false,
		},
		{
			name:        "mention_only=true with mention",
			scope:       SlackSubscriptionScope{MentionOnly: true},
			channelID:   "C01ANY",
			channelName: "general",
			isMention:   true,
			want:        true,
		},
		{
			name: "mention_only + channel filter both satisfied",
			scope: SlackSubscriptionScope{
				MentionOnly: true,
				Channels:    []string{"#incidents"},
			},
			channelID:   "C01INC",
			channelName: "incidents",
			isMention:   true,
			want:        true,
		},
		{
			name: "mention_only + channel filter: channel fails",
			scope: SlackSubscriptionScope{
				MentionOnly: true,
				Channels:    []string{"#incidents"},
			},
			channelID:   "C99OTHER",
			channelName: "general",
			isMention:   true,
			want:        false,
		},
		{
			name: "mention_only + channel filter: mention fails",
			scope: SlackSubscriptionScope{
				MentionOnly: true,
				Channels:    []string{"#incidents"},
			},
			channelID:   "C01INC",
			channelName: "incidents",
			isMention:   false,
			want:        false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.scope.matches(tc.channelID, tc.channelName, tc.isMention)
			if got != tc.want {
				t.Errorf("matches(%q, %q, %v): want %v, got %v",
					tc.channelID, tc.channelName, tc.isMention, tc.want, got)
			}
		})
	}
}
