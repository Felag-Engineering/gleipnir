package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/slack-go/slack"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	optionsv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/options/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

// optionsUserField is an EqualsField variant that also carries the
// x-gleipnir-options annotation for the "users" source. The host admin UI
// renders an async combobox (single-select) for this field instead of a plain
// text input. The binding evaluator still uses OpEquals semantics (no format
// key) — the annotation is purely a UI hint.
type optionsUserField string

// JSONSchema implements jsonschema.SchemaCustomizer so that ReflectSchema emits
// {type: string, x-gleipnir-options: {source: users}} for this field.
func (optionsUserField) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "string",
		Extras: map[string]any{
			manifest.OptionsAnnotationKey: manifest.OptionsAnnotation{Source: "users", Multi: false},
		},
	}
}

// OptionsService implements ConfigOptionsService.ListOptions for the Slack plugin.
// It returns dynamic, searchable option lists for schema fields annotated with
// x-gleipnir-options. Currently supports two sources:
//   - "channels" — Slack conversations visible to the bot (public + private; joined annotated)
//   - "users"    — Slack workspace users (filtered: non-deleted, non-bot)
//
// No call_id context is needed here — this is a config-time admin call.
// The plugin authenticates via its instance token (TokenInterceptorFromEnv)
// and fetches credentials via host.GetCredentials on every call (spec §9.4).
type OptionsService struct {
	optionsv1.UnimplementedConfigOptionsServiceServer

	host       hostv1.HostServiceClient
	httpClient *http.Client
	apiURL     string // empty = Slack production default; tests pass httptest.Server URL
}

// NewOptionsService creates an OptionsService using hostClient for host RPCs,
// httpClient for outbound Slack API calls, and apiURL to override the Slack
// API base URL (empty string uses the production default).
func NewOptionsService(host hostv1.HostServiceClient, httpClient *http.Client, apiURL string) *OptionsService {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OptionsService{host: host, httpClient: httpClient, apiURL: apiURL}
}

// ListOptions dispatches to the appropriate source handler. Unknown sources
// return InvalidArgument. Missing or invalid credentials return Unauthenticated.
func (s *OptionsService) ListOptions(ctx context.Context, req *optionsv1.ListOptionsRequest) (*optionsv1.ListOptionsResponse, error) {
	// WithCallContext is harmless when the context carries no call metadata.
	hostCtx := serve.WithCallContext(ctx)

	// Fetch the bot token on every call (spec §9.4: no caching).
	credResp, err := s.host.GetCredentials(hostCtx, &hostv1.GetCredentialsRequest{})
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "GetCredentials: %v", err)
	}
	raw := credResp.GetCredentialsJson()
	if raw == "" || raw == "{}" {
		return nil, status.Error(codes.Unauthenticated, "no Slack credentials configured; authorize the plugin in the admin UI")
	}
	var creds slackCreds
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return nil, status.Errorf(codes.Internal, "parse credentials: %v", err)
	}
	if creds.Token.AccessToken == "" {
		return nil, status.Error(codes.Unauthenticated, "Slack access_token is empty; re-authorize the plugin in the admin UI")
	}

	opts := []slack.Option{slack.OptionHTTPClient(s.httpClient)}
	if s.apiURL != "" {
		opts = append(opts, slack.OptionAPIURL(s.apiURL))
	}
	sc := slack.New(creds.Token.AccessToken, opts...)

	switch req.GetSource() {
	case "channels":
		return s.listChannels(ctx, sc, req.GetQuery(), req.GetCursor())
	case "users":
		return s.listUsers(ctx, sc, req.GetQuery(), req.GetCursor())
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown options source %q; supported: channels, users", req.GetSource())
	}
}

// conversationsResult bundles the three return values of GetConversationsContext
// into a single value for use with the 2-return-value callWithRetry helper.
type conversationsResult struct {
	channels   []slack.Channel
	nextCursor string
}

// listChannels returns a paged list of Slack conversation options.
//
// Types includes public_channel and private_channel. NOTE: private_channel
// requires the groups:read OAuth scope. The default Slack plugin oauth_defaults
// only includes groups:history (for read_history), NOT groups:read. Private
// channels will fail to appear until groups:read is granted. To enable private
// channel listing, add groups:read to the plugin's OAuth scopes and have
// operators re-authorize. This is intentionally NOT done silently to avoid
// unexpected scope expansion.
//
// Non-member channels are returned with a "(not joined)" label and
// disabled=true, because the bot cannot post or read history in channels it
// has not been invited to. They are shown so operators know the channels exist
// but must invite the bot first.
func (s *OptionsService) listChannels(ctx context.Context, sc *slack.Client, query, cursor string) (*optionsv1.ListOptionsResponse, error) {
	params := &slack.GetConversationsParameters{
		ExcludeArchived: true,
		// Public and private channels. Private channels require groups:read scope
		// which is NOT in the default oauth_defaults (only groups:history is there).
		Types:  []string{"public_channel", "private_channel"},
		Limit:  200,
		Cursor: cursor,
	}

	res, err := callWithRetry(ctx, func(ctx context.Context) (conversationsResult, error) {
		chs, next, e := sc.GetConversationsContext(ctx, params)
		return conversationsResult{channels: chs, nextCursor: next}, e
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "conversations.list: %v", err)
	}

	lowerQuery := strings.ToLower(query)
	var options []*optionsv1.Option
	for _, ch := range res.channels {
		name := ch.Name
		if lowerQuery != "" && !strings.Contains(strings.ToLower(name), lowerQuery) {
			continue
		}
		label := "#" + name
		group := "Joined"
		disabled := false
		if !ch.IsMember {
			label = fmt.Sprintf("#%s (not joined)", name)
			group = "Not joined"
			disabled = true
		}
		options = append(options, &optionsv1.Option{
			Value:    ch.ID,
			Label:    label,
			Group:    group,
			Disabled: disabled,
		})
	}

	return &optionsv1.ListOptionsResponse{
		Options:    options,
		NextCursor: res.nextCursor,
	}, nil
}

// usersResult bundles GetUsersContext return values for callWithRetry.
type usersResult struct {
	users []slack.User
}

// listUsers returns a paged list of Slack user options.
// Deleted users and bots are excluded. Real name is preferred as the label;
// the display name (Name) is used as the fallback.
//
// NOTE: slack-go's GetUsersContext does not expose cursor-based pagination
// in its parameters directly; it returns all users (up to Slack's server-side
// limit). For very large workspaces, GetUsersPaginated provides cursor support
// via an iterator but requires a different call pattern. The current
// implementation returns all users in a single call with an empty next_cursor.
func (s *OptionsService) listUsers(ctx context.Context, sc *slack.Client, query, cursor string) (*optionsv1.ListOptionsResponse, error) {
	// GetUsersContext returns all workspace users. cursor is accepted but not
	// forwarded because the slack-go parameter struct does not expose it for
	// this method; an empty cursor on the first call is fine.
	_ = cursor // not forwarded — see NOTE above

	res, err := callWithRetry(ctx, func(ctx context.Context) (usersResult, error) {
		us, e := sc.GetUsersContext(ctx)
		return usersResult{users: us}, e
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "users.list: %v", err)
	}

	lowerQuery := strings.ToLower(query)
	var options []*optionsv1.Option
	for _, u := range res.users {
		if u.Deleted || u.IsBot || u.ID == "USLACKBOT" {
			continue
		}
		label := u.RealName
		if label == "" {
			label = u.Name
		}
		if lowerQuery != "" &&
			!strings.Contains(strings.ToLower(label), lowerQuery) &&
			!strings.Contains(strings.ToLower(u.Name), lowerQuery) {
			continue
		}
		options = append(options, &optionsv1.Option{
			Value: u.ID,
			Label: label,
		})
	}

	return &optionsv1.ListOptionsResponse{
		Options:    options,
		NextCursor: "", // no cursor pagination in this implementation
	}, nil
}
