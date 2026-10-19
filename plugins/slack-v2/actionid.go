package main

import (
	"fmt"
	"strings"
)

const actionIDPrefix = "feedback_response:"

// actionIDFor returns the Block Kit action_id for a request + option.
// Format: "feedback_response:<requestID>:<optionID>". The format is the same
// one the v1 plugin used, so an in-flight message keeps working across the
// cutover.
func actionIDFor(requestID, optionID string) string {
	return fmt.Sprintf("%s%s:%s", actionIDPrefix, requestID, optionID)
}

// parseActionID parses an action_id produced by actionIDFor. The option id is
// everything after the last colon, so a request id may itself contain colons.
// ok is false for any action_id that does not match the format.
func parseActionID(actionID string) (requestID, optionID string, ok bool) {
	rest, found := strings.CutPrefix(actionID, actionIDPrefix)
	if !found {
		return "", "", false
	}
	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return "", "", false
	}
	requestID, optionID = rest[:idx], rest[idx+1:]
	if requestID == "" || optionID == "" {
		return "", "", false
	}
	return requestID, optionID, true
}
