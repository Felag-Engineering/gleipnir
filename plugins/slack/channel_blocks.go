package main

import (
	"fmt"
	"strings"

	"github.com/slack-go/slack"
)

// responseButton defines one response option shown to the operator in a
// Request Block Kit message. Operators can override these in channel config.
type responseButton struct {
	OptionID string `json:"option_id"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	// Style is one of "default", "primary", or "danger". Slack renders
	// primary as green and danger as red; default has no colour treatment.
	Style string `json:"style,omitempty"`
}

// defaultResponseButtons returns the two standard response buttons shown when
// the operator has not configured custom buttons. Approve is primary (green),
// Reject is danger (red).
func defaultResponseButtons() []responseButton {
	return []responseButton{
		{OptionID: "approve", Label: "Approve", Value: "approve", Style: "primary"},
		{OptionID: "reject", Label: "Reject", Value: "reject", Style: "danger"},
	}
}

// actionIDFor returns the action_id string for a given request + option.
// Format: "feedback_response:<requestID>:<optionID>".
func actionIDFor(requestID, optionID string) string {
	return fmt.Sprintf("feedback_response:%s:%s", requestID, optionID)
}

// parseActionID parses an action_id produced by actionIDFor.
// Returns the requestID and optionID, and ok=true on success.
// Returns ok=false for any action_id that does not match the expected format.
func parseActionID(actionID string) (requestID, optionID string, ok bool) {
	const prefix = "feedback_response:"
	if !strings.HasPrefix(actionID, prefix) {
		return "", "", false
	}
	rest := actionID[len(prefix):]
	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return "", "", false
	}
	requestID = rest[:idx]
	optionID = rest[idx+1:]
	if requestID == "" || optionID == "" {
		return "", "", false
	}
	return requestID, optionID, true
}

// buildRequestBlocks constructs the Block Kit blocks for a Request message.
// The blocks include a header section with the prompt (and optional mention),
// and one button per responseButton. If mention is non-empty, it is prepended
// to the prompt text so the mentioned user receives a notification.
func buildRequestBlocks(requestID, prompt string, buttons []responseButton, mention string) []slack.Block {
	text := prompt
	if mention != "" {
		text = mention + " " + prompt
	}

	headerBlock := slack.NewSectionBlock(
		slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
		nil, nil,
	)

	actionBlock := buildActionBlock(requestID, buttons)

	return []slack.Block{headerBlock, actionBlock}
}

// buildActionBlock constructs a Slack ActionBlock containing one button per
// responseButton with the correct action_id, style, and value.
func buildActionBlock(requestID string, buttons []responseButton) *slack.ActionBlock {
	elements := make([]slack.BlockElement, 0, len(buttons))
	for _, b := range buttons {
		btn := slack.NewButtonBlockElement(
			actionIDFor(requestID, b.OptionID),
			b.Value,
			slack.NewTextBlockObject(slack.PlainTextType, b.Label, false, false),
		)
		switch b.Style {
		case "primary":
			btn.Style = slack.StylePrimary
		case "danger":
			btn.Style = slack.StyleDanger
		}
		elements = append(elements, btn)
	}
	return slack.NewActionBlock("", elements...)
}
