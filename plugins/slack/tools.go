package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
	"github.com/slack-go/slack"

	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
)

// ── Typed parameter structs ───────────────────────────────────────────────────
//
// invopop/jsonschema reflects these into JSON Schema objects used in both
// ListTools (wire surface, JSON string) and manifest.go (YAML node).
// additionalProperties: false is the library's default for struct reflections.

type PostMessageParams struct {
	Channel  string `json:"channel"   jsonschema:"required,title=Channel,description=Channel ID (C…) or name (#general)"`
	Text     string `json:"text"      jsonschema:"required,title=Text,description=Plain-text message body. Block Kit not supported in v0.1."`
	ThreadTS string `json:"thread_ts,omitempty" jsonschema:"title=Thread TS,description=Optional parent thread timestamp to reply in-thread"`
}

type ListChannelsParams struct {
	ExcludeArchived bool   `json:"exclude_archived,omitempty" jsonschema:"title=Exclude archived,default=true"`
	Limit           int    `json:"limit,omitempty"            jsonschema:"title=Limit,description=Max channels to return (1-1000). Defaults to 200,minimum=1,maximum=1000"`
	Types           string `json:"types,omitempty"            jsonschema:"title=Types,description=Comma-separated channel types (public_channel\\,private_channel\\,mpim\\,im). Defaults to public_channel."`
}

type ReactParams struct {
	Channel   string `json:"channel"   jsonschema:"required,title=Channel,description=Channel ID containing the message"`
	Timestamp string `json:"timestamp" jsonschema:"required,title=Timestamp,description=Message ts (e.g. 1700000000.123456)"`
	Name      string `json:"name"      jsonschema:"required,title=Emoji name,description=Reaction name without colons (e.g. thumbsup)"`
}

type SetTopicParams struct {
	Channel string `json:"channel" jsonschema:"required,title=Channel,description=Channel ID"`
	Topic   string `json:"topic"   jsonschema:"required,title=Topic,description=New channel topic (max 250 chars)"`
}

// ── JSON Schema reflection ────────────────────────────────────────────────────

// schemaReflector mirrors plugin-sdk/manifest/reflect.go:42-48 settings so the
// reflected shapes are consistent between ListTools (JSON-string wire surface)
// and manifest.go (YAML node). Difference: ToolSchema.InputSchema on the wire
// is a JSON string (toolv1.ToolSchema.InputSchema at plugin-sdk/gen/.../tool.pb.go:39),
// whereas manifest.ToolDecl.InputSchema is *yaml.Node — we cannot reuse
// manifest.MustReflectSchema for the wire surface.
var schemaReflector = &jsonschema.Reflector{
	Anonymous:      true,
	ExpandedStruct: true,
	DoNotReference: true,
}

// reflectInputSchemaJSON returns the JSON Schema for v as a JSON string.
// The $schema URL is stripped to keep output byte-shape consistent with
// manifest.ReflectSchema (see plugin-sdk/manifest/reflect.go:48-49).
func reflectInputSchemaJSON(v any) string {
	s := schemaReflector.Reflect(v)
	s.Version = "" // strip $schema
	b, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Errorf("reflectInputSchemaJSON: %w", err))
	}
	return string(b)
}

// ── Credentials mirror ────────────────────────────────────────────────────────

// slackCreds mirrors the .token sub-object of internal/plugin/oauth.StoredCredentials.
// Source of truth: host-side credentials.go:30-61. No internal/* imports are
// permitted (scripts/lint-plugins.sh), so we mirror the shape locally. Unknown
// fields are tolerated by json.Unmarshal's default behavior.
type slackCreds struct {
	Token struct {
		AccessToken string `json:"access_token"`
	} `json:"token"`
}

// ── Error classification ──────────────────────────────────────────────────────

// healthHint signals whether a Call failure should also update plugin health.
type healthHint int

const (
	healthNone          healthHint = iota // transient or caller error — no health update
	healthAuthExpired                     // persistent auth failure — set UNHEALTHY
	healthAuthMissing                     // no credentials configured — set UNHEALTHY
	healthConfigMissing                   // required instance config field missing — set UNHEALTHY
)

// mapErr maps a Slack API error to an ErrorCode and optional health hint.
// It uses errors.As to unwrap typed slack-go errors before falling through
// to string-based classification of the Slack error code string.
//
// Target types per R7: *slack.RateLimitedError (pointer), slack.SlackErrorResponse
// (value), slack.StatusCodeError (value).
func mapErr(err error) (commonv1.ErrorCode, healthHint) {
	if err == nil {
		return commonv1.ErrorCode_ERROR_CODE_UNSPECIFIED, healthNone
	}

	// Context cancellation or deadline — transient, caller may retry or was cancelled.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE, healthNone
	}

	// Rate limit — caller should back off.
	var rateErr *slack.RateLimitedError
	if errors.As(err, &rateErr) {
		return commonv1.ErrorCode_ERROR_CODE_RATE_LIMITED, healthNone
	}

	// HTTP 5xx or other non-OK status codes Slack returns outside of the JSON envelope.
	var statusErr slack.StatusCodeError
	if errors.As(err, &statusErr) {
		if statusErr.Code >= 500 {
			return commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE, healthNone
		}
		return commonv1.ErrorCode_ERROR_CODE_INTERNAL, healthNone
	}

	// Slack API-level error strings carried in SlackErrorResponse.Err.
	var slackErr slack.SlackErrorResponse
	if errors.As(err, &slackErr) {
		return classifySlackErrCode(slackErr.Err)
	}

	// Fallback for plain string errors or unrecognized wrapped types.
	return classifySlackErrCode(err.Error())
}

// classifySlackErrCode maps a Slack API error-code string to an ErrorCode and
// health hint. All Slack error strings that indicate a persistent auth problem
// return healthAuthExpired so the host can surface them in the operator UI.
func classifySlackErrCode(code string) (commonv1.ErrorCode, healthHint) {
	switch code {
	case "channel_not_found", "not_in_channel", "user_not_found", "message_not_found":
		return commonv1.ErrorCode_ERROR_CODE_NOT_FOUND, healthNone

	case "invalid_arguments", "msg_too_long", "message_text_required", "bad_query", "invalid_name":
		return commonv1.ErrorCode_ERROR_CODE_INVALID_ARG, healthNone

	case "invalid_auth", "account_inactive", "token_revoked", "missing_scope", "not_authed":
		// Persistent auth failures. The operator UI shows UNHEALTHY + "auth_expired"
		// so operators know they need to reauthorize or grant missing scopes.
		return commonv1.ErrorCode_ERROR_CODE_PERMISSION, healthAuthExpired

	case "ratelimited":
		return commonv1.ErrorCode_ERROR_CODE_RATE_LIMITED, healthNone

	case "service_unavailable", "fatal_error", "internal_error":
		return commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE, healthNone
	}

	return commonv1.ErrorCode_ERROR_CODE_INTERNAL, healthNone
}

// ── Rate-limit retry helper ───────────────────────────────────────────────────

// callWithRetry wraps a Slack call with a single retry on RateLimitedError
// when RetryAfter fits in the remaining ctx deadline (plus 250ms slop for
// HTTP overhead). Beyond that budget, surface RateLimitedError as-is so the
// caller can map it to RATE_LIMITED.
//
// Single retry only — never loop. The 30s ToolService deadline is the guardrail.
func callWithRetry[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	v, err := fn(ctx)
	var rl *slack.RateLimitedError
	if !errors.As(err, &rl) {
		return v, err
	}
	if dl, ok := ctx.Deadline(); ok {
		budget := time.Until(dl) - 250*time.Millisecond
		if rl.RetryAfter > budget {
			return zero, err // not enough budget; let caller map to RATE_LIMITED
		}
	}
	// If ctx has no deadline (rare in production — the host always enforces 30s)
	// the retry still fires; ctx.Done() in the select below is the safety net.
	select {
	case <-time.After(rl.RetryAfter):
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	return fn(ctx) // single retry; surface its error as-is
}

// ── Per-tool handler functions ────────────────────────────────────────────────
//
// Each handler takes a context, a fully-constructed slack.Client, and the
// raw inputJSON string from the Call request. It returns the output JSON
// bytes and an error (which callers pass through mapErr).

func handlePostMessage(ctx context.Context, sc *slack.Client, inputJSON string) ([]byte, error) {
	var p PostMessageParams
	if err := json.Unmarshal([]byte(inputJSON), &p); err != nil {
		return nil, slack.SlackErrorResponse{Err: "invalid_arguments"}
	}

	opts := []slack.MsgOption{slack.MsgOptionText(p.Text, false)}
	if p.ThreadTS != "" {
		opts = append(opts, slack.MsgOptionTS(p.ThreadTS))
	}

	type postResult struct{ channel, ts string }
	res, err := callWithRetry(ctx, func(ctx context.Context) (postResult, error) {
		ch, ts, err := sc.PostMessageContext(ctx, p.Channel, opts...)
		return postResult{ch, ts}, err
	})
	if err != nil {
		return nil, err
	}

	return json.Marshal(map[string]string{"channel": res.channel, "ts": res.ts})
}

func handleListChannels(ctx context.Context, sc *slack.Client, inputJSON string) ([]byte, error) {
	var p ListChannelsParams
	if err := json.Unmarshal([]byte(inputJSON), &p); err != nil {
		return nil, slack.SlackErrorResponse{Err: "invalid_arguments"}
	}

	limit := p.Limit
	if limit == 0 {
		limit = 200
	}
	types := p.Types
	if types == "" {
		types = "public_channel"
	}

	params := &slack.GetConversationsParameters{
		ExcludeArchived: p.ExcludeArchived,
		Limit:           limit,
		Types:           strings.Split(types, ","),
	}

	type listResult struct {
		channels   []slack.Channel
		nextCursor string
	}
	res, err := callWithRetry(ctx, func(ctx context.Context) (listResult, error) {
		chs, cursor, err := sc.GetConversationsContext(ctx, params)
		return listResult{chs, cursor}, err
	})
	if err != nil {
		return nil, err
	}
	channels, nextCursor := res.channels, res.nextCursor

	type channelItem struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsPrivate  bool   `json:"is_private"`
		IsArchived bool   `json:"is_archived"`
		NumMembers int    `json:"num_members"`
	}
	items := make([]channelItem, len(channels))
	for i, c := range channels {
		items[i] = channelItem{
			ID:         c.ID,
			Name:       c.Name,
			IsPrivate:  c.IsPrivate,
			IsArchived: c.IsArchived,
			NumMembers: c.NumMembers,
		}
	}

	return json.Marshal(map[string]any{
		"channels":    items,
		"next_cursor": nextCursor,
	})
}

func handleReact(ctx context.Context, sc *slack.Client, inputJSON string) ([]byte, error) {
	var p ReactParams
	if err := json.Unmarshal([]byte(inputJSON), &p); err != nil {
		return nil, slack.SlackErrorResponse{Err: "invalid_arguments"}
	}

	item := slack.NewRefToMessage(p.Channel, p.Timestamp)
	_, err := callWithRetry(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, sc.AddReactionContext(ctx, p.Name, item)
	})
	if err != nil {
		return nil, err
	}

	return json.Marshal(map[string]bool{"ok": true})
}

func handleSetTopic(ctx context.Context, sc *slack.Client, inputJSON string) ([]byte, error) {
	var p SetTopicParams
	if err := json.Unmarshal([]byte(inputJSON), &p); err != nil {
		return nil, slack.SlackErrorResponse{Err: "invalid_arguments"}
	}

	channel, err := callWithRetry(ctx, func(ctx context.Context) (*slack.Channel, error) {
		return sc.SetTopicOfConversationContext(ctx, p.Channel, p.Topic)
	})
	if err != nil {
		return nil, err
	}

	return json.Marshal(map[string]any{
		"ok":    true,
		"topic": channel.Topic.Value,
	})
}
