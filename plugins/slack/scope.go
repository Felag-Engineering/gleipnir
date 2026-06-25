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

// matches reports whether an event should be delivered to a subscriber with
// this scope.
//
// Rules (evaluated in order):
//   - If isDM is true, the event matches only when DirectMessages is true.
//     The Channels allow-list and MentionOnly gate do NOT apply to DMs — a DM
//     has no C… channel ID and is never a mention, so applying those filters
//     would silently block DM events even when DirectMessages is true.
//   - For non-DM events: if MentionOnly is true, isMention must be true.
//   - If Channels is non-empty, channelID must appear in the list. Channel
//     names are not accepted — the subscription_schema pattern rejects them
//     at save time (see SlackChannelID in manifest.go) so this check is
//     ID-only.
//   - An empty scope (zero value) matches all non-DM events.
func (s SlackSubscriptionScope) matches(channelID string, isMention bool, isDM bool) bool {
	// DMs are routed solely by the DirectMessages flag; the channel allow-list
	// and MentionOnly gate are channel-surface concepts that must not apply here.
	if isDM {
		return s.DirectMessages
	}
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
