package main

import (
	"encoding/json"
)

// decodeSubscriptionScope parses watch_scope_json into a SlackSubscriptionScope.
// An empty string or "{}" decodes to the zero value (matches everything).
// Returns an error if the JSON is present but malformed.
func decodeSubscriptionScope(jsonStr string) (SlackSubscriptionScope, error) {
	if jsonStr == "" || jsonStr == "{}" {
		return SlackSubscriptionScope{}, nil
	}
	var s SlackSubscriptionScope
	if err := json.Unmarshal([]byte(jsonStr), &s); err != nil {
		return SlackSubscriptionScope{}, err
	}
	return s, nil
}

// matches reports whether an event for the given channelID should be delivered
// to a subscriber with this scope.
//
// Rules:
//   - If Channels is non-empty, channelID must appear in the list. Channel
//     names are not accepted — the subscription_schema pattern rejects them
//     at save time (see SlackChannelID in manifest.go) so this check is
//     ID-only.
//   - If MentionOnly is true, isMention must be true for the event to match.
//   - An empty scope (zero value) matches all events.
func (s SlackSubscriptionScope) matches(channelID string, isMention bool) bool {
	if s.MentionOnly && !isMention {
		return false
	}
	if len(s.Channels) == 0 {
		return true
	}
	for _, allowed := range s.Channels {
		if string(allowed) == channelID {
			return true
		}
	}
	return false
}
