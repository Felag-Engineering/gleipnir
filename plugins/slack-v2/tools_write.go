package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
	"github.com/invopop/jsonschema"
	"github.com/slack-go/slack"
)

const (
	toolPostMessage   = "post_message"
	toolUpdateMessage = "update_message"
	toolDeleteMessage = "delete_message"
	toolReact         = "react"
	toolSetTopic      = "set_topic"

	maxTopicRunes = 250
)

// blockKitBlocks is a JSON array of Slack Block Kit block objects, passed
// through to Slack untouched. A plain json.RawMessage would reflect to a
// schema with no "type", which provider schema translators (Gemini's among
// them) reject before the first LLM call; this type advertises an explicit
// array-of-objects schema instead. The items stay unconstrained because
// Block Kit shapes vary widely.
type blockKitBlocks json.RawMessage

func (b blockKitBlocks) MarshalJSON() ([]byte, error) {
	if len(b) == 0 {
		return []byte("null"), nil
	}
	return b, nil
}

func (b *blockKitBlocks) UnmarshalJSON(data []byte) error {
	if b == nil {
		return errors.New("blockKitBlocks: UnmarshalJSON on nil pointer")
	}
	*b = append((*b)[0:0], data...)
	return nil
}

func (blockKitBlocks) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "array",
		Items:       &jsonschema.Schema{Type: "object"},
		Title:       "Blocks",
		Description: `JSON array of Slack Block Kit block objects, e.g. [{"type":"section","text":{...}}]. Must be a JSON array, not an object. When set, takes precedence over plain text for rendering; text is used as the notification fallback.`,
	}
}

// present reports whether the caller supplied blocks; an explicit JSON null
// counts as absent.
func (b blockKitBlocks) present() bool {
	return len(b) > 0 && string(b) != "null"
}

type postMessageInput struct {
	Channel  string         `json:"channel"             jsonschema:"required,title=Channel,description=Channel ID (C…) or name (#general)"`
	Text     string         `json:"text,omitempty"      jsonschema:"title=Text,description=Message text. Used as the notification / fallback text when blocks are present\\, or as the message body when blocks are absent."`
	Blocks   blockKitBlocks `json:"blocks,omitempty"`
	ThreadTS string         `json:"thread_ts,omitempty" jsonschema:"title=Thread TS,description=Optional parent thread timestamp to reply in-thread"`
}

type updateMessageInput struct {
	Channel string         `json:"channel"        jsonschema:"required,title=Channel,description=Channel ID containing the message to update"`
	Ts      string         `json:"ts"             jsonschema:"required,title=Message TS,description=Timestamp of the message to update"`
	Text    string         `json:"text,omitempty" jsonschema:"title=Text,description=Message text. Used as the notification / fallback text when blocks are present\\, or as the message body when blocks are absent."`
	Blocks  blockKitBlocks `json:"blocks,omitempty"`
}

type deleteMessageInput struct {
	Channel string `json:"channel" jsonschema:"required,title=Channel,description=Channel ID containing the message to delete"`
	Ts      string `json:"ts"      jsonschema:"required,title=Message TS,description=Timestamp of the message to delete"`
}

type reactInput struct {
	Channel   string `json:"channel"   jsonschema:"required,title=Channel,description=Channel ID containing the message"`
	Timestamp string `json:"timestamp" jsonschema:"required,title=Timestamp,description=Message ts (e.g. 1700000000.123456)"`
	Name      string `json:"name"      jsonschema:"required,title=Emoji name,description=Reaction name without colons (e.g. thumbsup)"`
}

type setTopicInput struct {
	Channel string `json:"channel" jsonschema:"required,title=Channel,description=Channel ID"`
	Topic   string `json:"topic"   jsonschema:"required,title=Topic,description=New channel topic (max 250 chars),maxLength=250"`
}

func requireNonEmpty(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func validateTextOrBlocks(text string, blocks blockKitBlocks) error {
	if !blocks.present() {
		if text == "" {
			return errors.New("text or blocks is required")
		}
		return nil
	}
	var parsed []json.RawMessage
	if err := json.Unmarshal(blocks, &parsed); err != nil {
		return errors.New("blocks must be a JSON array of Block Kit block objects")
	}
	return nil
}

func (in postMessageInput) validate() error {
	if err := requireNonEmpty("channel", in.Channel); err != nil {
		return err
	}
	return validateTextOrBlocks(in.Text, in.Blocks)
}

func (in updateMessageInput) validate() error {
	if err := requireNonEmpty("channel", in.Channel); err != nil {
		return err
	}
	if err := requireNonEmpty("ts", in.Ts); err != nil {
		return err
	}
	return validateTextOrBlocks(in.Text, in.Blocks)
}

func (in deleteMessageInput) validate() error {
	if err := requireNonEmpty("channel", in.Channel); err != nil {
		return err
	}
	return requireNonEmpty("ts", in.Ts)
}

func (in reactInput) validate() error {
	if err := requireNonEmpty("channel", in.Channel); err != nil {
		return err
	}
	if err := requireNonEmpty("timestamp", in.Timestamp); err != nil {
		return err
	}
	return requireNonEmpty("name", in.Name)
}

func (in setTopicInput) validate() error {
	if err := requireNonEmpty("channel", in.Channel); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(in.Topic); n > maxTopicRunes {
		return fmt.Errorf("topic is %d characters; Slack allows at most %d", n, maxTopicRunes)
	}
	return nil
}

func (p *plugin) writeTools() []mcpserver.Tool {
	return []mcpserver.Tool{
		slackTool(p, toolPostMessage, "Post a message to a Slack channel or thread, as plain text or Block Kit blocks.", postMessage),
		slackTool(p, toolUpdateMessage, "Edit a message the bot previously posted.", updateMessage),
		slackTool(p, toolDeleteMessage, "Delete a message the bot previously posted.", deleteMessage),
		slackTool(p, toolReact, "Add an emoji reaction to a Slack message.", react),
		slackTool(p, toolSetTopic, "Set a Slack channel's topic.", setTopic),
	}
}

// messageOptions builds the Slack options for text and/or Block Kit blocks.
// Text is sent alongside blocks because Slack uses it for notifications and
// clients that do not render blocks.
func messageOptions(text string, rawBlocks blockKitBlocks) ([]slack.MsgOption, error) {
	var opts []slack.MsgOption
	if rawBlocks.present() {
		var blocks slack.Blocks
		if err := json.Unmarshal(rawBlocks, &blocks); err != nil {
			return nil, fmt.Errorf("decode blocks: %w", err)
		}
		opts = append(opts, slack.MsgOptionBlocks(blocks.BlockSet...))
	}
	if text != "" {
		opts = append(opts, slack.MsgOptionText(text, false))
	}
	return opts, nil
}

func postMessage(ctx context.Context, sc *slack.Client, in postMessageInput) (any, error) {
	opts, err := messageOptions(in.Text, in.Blocks)
	if err != nil {
		return nil, err
	}
	if in.ThreadTS != "" {
		opts = append(opts, slack.MsgOptionTS(in.ThreadTS))
	}
	channel, ts, err := sc.PostMessageContext(ctx, in.Channel, opts...)
	if err != nil {
		return nil, err
	}
	return map[string]string{"channel": channel, "ts": ts}, nil
}

func updateMessage(ctx context.Context, sc *slack.Client, in updateMessageInput) (any, error) {
	opts, err := messageOptions(in.Text, in.Blocks)
	if err != nil {
		return nil, err
	}
	channel, ts, text, err := sc.UpdateMessageContext(ctx, in.Channel, in.Ts, opts...)
	if err != nil {
		return nil, err
	}
	return map[string]string{"channel": channel, "ts": ts, "text": text}, nil
}

func deleteMessage(ctx context.Context, sc *slack.Client, in deleteMessageInput) (any, error) {
	channel, ts, err := sc.DeleteMessageContext(ctx, in.Channel, in.Ts)
	if err != nil {
		return nil, err
	}
	return map[string]string{"channel": channel, "ts": ts}, nil
}

func react(ctx context.Context, sc *slack.Client, in reactInput) (any, error) {
	if err := sc.AddReactionContext(ctx, in.Name, slack.NewRefToMessage(in.Channel, in.Timestamp)); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func setTopic(ctx context.Context, sc *slack.Client, in setTopicInput) (any, error) {
	channel, err := sc.SetTopicOfConversationContext(ctx, in.Channel, in.Topic)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "topic": channel.Topic.Value}, nil
}
