package main

import (
	"encoding/json"
	"strings"
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

// matches reports whether an event for the given channel should be delivered
// to a subscriber with this scope.
//
// Rules:
//   - If Channels is non-empty, the event's channelID or channelName must
//     appear in the list. Channel names may be specified with or without the
//     leading "#" — both forms are accepted.
//   - If MentionOnly is true, isMention must be true for the event to match.
//   - An empty scope (zero value) matches all events.
func (s SlackSubscriptionScope) matches(channelID, channelName string, isMention bool) bool {
	if s.MentionOnly && !isMention {
		return false
	}
	if len(s.Channels) == 0 {
		return true
	}
	// Normalize the channel name by stripping a leading "#" so operators can
	// write either "incidents" or "#incidents" in the scope config.
	normalizedName := strings.TrimPrefix(channelName, "#")

	for _, allowed := range s.Channels {
		normalized := strings.TrimPrefix(allowed, "#")
		if normalized == channelID || normalized == normalizedName {
			return true
		}
	}
	return false
}
