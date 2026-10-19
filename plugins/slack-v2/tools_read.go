package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
	"github.com/invopop/jsonschema"
	"github.com/slack-go/slack"
)

// The tools are named bare (no dot): the host prefixes the instance name
// itself, and manifestv2 does not require a dotted form.
const (
	toolListChannels = "list_channels"
	toolReadHistory  = "read_history"
	toolReadThread   = "read_thread"
	toolLookupUser   = "lookup_user"
)

type listChannelsInput struct {
	ExcludeArchived bool   `json:"exclude_archived,omitempty" jsonschema:"title=Exclude archived,default=true"`
	Limit           int    `json:"limit,omitempty"            jsonschema:"title=Limit,description=Max channels to return (1-1000). Defaults to 200,minimum=1,maximum=1000"`
	Types           string `json:"types,omitempty"            jsonschema:"title=Types,description=Comma-separated channel types (public_channel\\,private_channel). Defaults to public_channel."`
	Cursor          string `json:"cursor,omitempty"           jsonschema:"title=Cursor,description=Pagination cursor from a previous response"`
}

type readHistoryInput struct {
	Channel string `json:"channel"          jsonschema:"required,title=Channel,description=Channel ID (C… or G…) to read history from"`
	Limit   int    `json:"limit,omitempty"  jsonschema:"title=Limit,description=Max messages to return (1-200). Defaults to 100,minimum=1,maximum=200"`
	Oldest  string `json:"oldest,omitempty" jsonschema:"title=Oldest,description=Return messages after this Unix timestamp (exclusive)"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"title=Cursor,description=Pagination cursor from a previous response"`
}

type readThreadInput struct {
	Channel string `json:"channel"          jsonschema:"required,title=Channel,description=Channel ID (C… or G…) containing the thread"`
	Ts      string `json:"ts"               jsonschema:"required,title=Thread TS,description=Parent message timestamp (ts) that identifies the thread"`
	Limit   int    `json:"limit,omitempty"  jsonschema:"title=Limit,description=Max messages to return (1-200). Defaults to 200,minimum=1,maximum=200"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"title=Cursor,description=Pagination cursor from a previous response"`
}

type lookupUserInput struct {
	User string `json:"user" jsonschema:"required,title=User ID,description=Slack user ID (U… format) to look up"`
}

// schemaReflector produces self-contained schemas: no $ref/$defs, which
// internal/schemanorm would have to resolve and which a model-facing schema
// does not need.
var schemaReflector = &jsonschema.Reflector{
	Anonymous:      true,
	ExpandedStruct: true,
	DoNotReference: true,
}

func reflectInputSchema(v any) json.RawMessage {
	s := schemaReflector.Reflect(v)
	s.Version = ""
	b, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Errorf("reflect input schema: %w", err))
	}
	return b
}

// argsValidator is implemented by input types that have constraints a JSON
// schema cannot enforce on its own (non-empty strings, text-or-blocks). A
// failure is reported before any credential lookup or Slack call.
type argsValidator interface {
	validate() error
}

// slackTool builds an mcpserver.Tool whose handler decodes arguments into In,
// validates them, obtains a Slack client for the current credentials, runs
// call, and routes any Slack failure through slackToolError (which owns
// health reporting).
func slackTool[In any](p *plugin, name, description string, call func(ctx context.Context, sc *slack.Client, in In) (any, error)) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        name,
		Description: description,
		InputSchema: reflectInputSchema(new(In)),
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in In
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if v, ok := any(in).(argsValidator); ok {
				if err := v.validate(); err != nil {
					return nil, fmt.Errorf("invalid arguments: %w", err)
				}
			}
			sc, err := p.slack.client(ctx)
			if err != nil {
				return nil, err
			}
			result, err := call(ctx, sc, in)
			if err != nil {
				return nil, p.slackToolError(ctx, name, err)
			}
			return result, nil
		},
	}
}

// slackToolError turns a Slack failure into the error a tool call reports.
// A scope-class failure additionally flips only this tool's capability
// unhealthy and names the scope the operator must add.
func (p *plugin) slackToolError(ctx context.Context, tool string, err error) error {
	code := scopeErrorCode(err)
	if code == "" {
		return fmt.Errorf("slack %s: %w", tool, err)
	}
	p.health.reportScopeFailure(ctx, tool, code)
	return fmt.Errorf("slack %s: %s — %s; add it in the Slack app's OAuth & Permissions page, reinstall the app, and re-authorize",
		tool, code, scopeHint(tool))
}

func (p *plugin) readTools() []mcpserver.Tool {
	return []mcpserver.Tool{
		slackTool(p, toolListChannels, "List Slack channels the token can see.", listChannels),
		slackTool(p, toolReadHistory, "Read recent messages from a Slack channel.", readHistory),
		slackTool(p, toolReadThread, "Read the replies in a Slack message thread.", readThread),
		slackTool(p, toolLookupUser, "Look up a Slack user by ID.", lookupUser),
	}
}

type messageItem struct {
	Text     string `json:"text"`
	User     string `json:"user"`
	Ts       string `json:"ts"`
	ThreadTs string `json:"thread_ts,omitempty"`
}

func toMessageItems(msgs []slack.Message) []messageItem {
	items := make([]messageItem, len(msgs))
	for i, m := range msgs {
		items[i] = messageItem{Text: m.Text, User: m.User, Ts: m.Timestamp, ThreadTs: m.ThreadTimestamp}
	}
	return items
}

func listChannels(ctx context.Context, sc *slack.Client, in listChannelsInput) (any, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 200
	}
	types := in.Types
	if types == "" {
		types = "public_channel"
	}
	channels, next, err := sc.GetConversationsContext(ctx, &slack.GetConversationsParameters{
		ExcludeArchived: in.ExcludeArchived,
		Limit:           limit,
		Cursor:          in.Cursor,
		Types:           strings.Split(types, ","),
	})
	if err != nil {
		return nil, err
	}

	type channelItem struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsPrivate  bool   `json:"is_private"`
		IsArchived bool   `json:"is_archived"`
		NumMembers int    `json:"num_members"`
	}
	items := make([]channelItem, len(channels))
	for i, c := range channels {
		items[i] = channelItem{ID: c.ID, Name: c.Name, IsPrivate: c.IsPrivate, IsArchived: c.IsArchived, NumMembers: c.NumMembers}
	}
	return map[string]any{"channels": items, "next_cursor": next}, nil
}

func readHistory(ctx context.Context, sc *slack.Client, in readHistoryInput) (any, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 100
	}
	resp, err := sc.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID: in.Channel,
		Limit:     limit,
		Oldest:    in.Oldest,
		Cursor:    in.Cursor,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"messages":    toMessageItems(resp.Messages),
		"has_more":    resp.HasMore,
		"next_cursor": resp.ResponseMetaData.NextCursor,
	}, nil
}

func readThread(ctx context.Context, sc *slack.Client, in readThreadInput) (any, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 200
	}
	msgs, hasMore, next, err := sc.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: in.Channel,
		Timestamp: in.Ts,
		Limit:     limit,
		Cursor:    in.Cursor,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"messages": toMessageItems(msgs), "has_more": hasMore, "next_cursor": next}, nil
}

func lookupUser(ctx context.Context, sc *slack.Client, in lookupUserInput) (any, error) {
	u, err := sc.GetUserInfoContext(ctx, in.User)
	if err != nil {
		return nil, err
	}
	return map[string]string{"id": u.ID, "name": u.Name, "real_name": u.RealName, "tz": u.TZ}, nil
}
