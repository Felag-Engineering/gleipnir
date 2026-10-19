package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/slack-go/slack"
)

// Slack error codes that mean "this token cannot call this method", as opposed
// to "this call was wrong". Only these two flip a capability unhealthy.
const (
	slackErrMissingScope        = "missing_scope"
	slackErrNotAllowedTokenType = "not_allowed_token_type"
)

// toolScopes is the OAuth scope each tool needs, one entry per Slack
// method it calls. An entry with several scopes means ONE of them, depending
// on the kind of conversation the call targets (public vs private channel).
//
// slack-go drops the needed/provided fields Slack returns with missing_scope,
// so this static table is where the scope named in a health detail comes from.
// manifest.yaml's auth.oauth_defaults.scopes must be the union of these; the
// manifest test enforces it.
var toolScopes = map[string][]string{
	"list_channels": {"channels:read", "groups:read"},       // conversations.list
	"read_history":  {"channels:history", "groups:history"}, // conversations.history
	"read_thread":   {"channels:history", "groups:history"}, // conversations.replies
	"lookup_user":   {"users:read"},                         // users.info

	"post_message":   {"chat:write"},                      // chat.postMessage
	"update_message": {"chat:write"},                      // chat.update
	"delete_message": {"chat:write"},                      // chat.delete
	"react":          {"reactions:write"},                 // reactions.add
	"set_topic":      {"channels:manage", "groups:write"}, // conversations.setTopic
}

// channelScopeHint is the operator-facing sentence naming the scope a
// channel/notify step needs.
func channelScopeHint(step string) string {
	return fmt.Sprintf("channel delivery (%s) needs the %s OAuth scope", step, strings.Join(channelScopes[step], ", "))
}

// channelHealthKey keys the human_channel report in healthReporter.reported;
// the leading character keeps it from colliding with any tool name.
const channelHealthKey = "profile:human_channel"

// healthSetter is the slice of hostclient.Client the reporter needs.
type healthSetter interface {
	SetHealthState(ctx context.Context, req hostclient.SetHealthStateRequest) (*hostclient.SetHealthStateResponse, error)
}

// healthReporter marks exactly one tool capability unhealthy when Slack says
// the token lacks its scope. Health is per capability (#814): a missing
// channels:history must not take lookup_user down with it.
type healthReporter struct {
	host healthSetter

	mu       sync.Mutex
	reported map[string]bool
}

func newHealthReporter(host healthSetter) *healthReporter {
	return &healthReporter{host: host, reported: make(map[string]bool)}
}

// scopeErrorCode returns the scope-class Slack error code carried by err, or ""
// when err is anything else. The code is read from slack-go's typed error and
// compared exactly, so an unrelated error whose message merely mentions a
// scope never flips a capability unhealthy.
func scopeErrorCode(err error) string {
	var slackErr slack.SlackErrorResponse
	if !errors.As(err, &slackErr) {
		return ""
	}
	switch slackErr.Err {
	case slackErrMissingScope, slackErrNotAllowedTokenType:
		return slackErr.Err
	}
	return ""
}

// scopeHint is the operator-facing sentence naming the scope(s) a tool needs.
func scopeHint(tool string) string {
	scopes := toolScopes[tool]
	switch len(scopes) {
	case 0:
		return fmt.Sprintf("the %q tool needs an OAuth scope the token does not have", tool)
	case 1:
		return fmt.Sprintf("the %q tool needs the %s OAuth scope", tool, scopes[0])
	default:
		return fmt.Sprintf("the %q tool needs one of these OAuth scopes (depending on the channel type): %s",
			tool, strings.Join(scopes, ", "))
	}
}

// reportScopeFailure reports tool's capability unhealthy once per process.
// The host clears a self-reported fault only when it observes this container
// restart, so repeating the report on every failing call would add traffic
// and no information. A failed report is not remembered, so the next failing
// call tries again.
func (h *healthReporter) reportScopeFailure(ctx context.Context, tool, code string) {
	h.mu.Lock()
	already := h.reported[tool]
	h.mu.Unlock()
	if already {
		return
	}

	_, err := h.host.SetHealthState(ctx, hostclient.SetHealthStateRequest{
		Profile:    hostclient.ProfileToolProvider,
		Capability: tool,
		State:      hostclient.HealthStateUnhealthy,
		Detail:     fmt.Sprintf("%s: %s", code, scopeHint(tool)),
	})
	if err != nil {
		log.Printf("slack-v2: report %s unhealthy: %v", tool, err)
		return
	}

	h.mu.Lock()
	h.reported[tool] = true
	h.mu.Unlock()
}

// reportChannelScopeFailure reports the whole human_channel profile unhealthy
// once per process. It is profile-wide, not per step, because the host's notify
// gate asks Serves for the profile: a channel that cannot open DMs or cannot
// post is skipped as a whole and the audience falls through to the next entry.
func (h *healthReporter) reportChannelScopeFailure(ctx context.Context, step, code string) {
	h.mu.Lock()
	already := h.reported[channelHealthKey]
	h.mu.Unlock()
	if already {
		return
	}

	_, err := h.host.SetHealthState(ctx, hostclient.SetHealthStateRequest{
		Profile: hostclient.ProfileHumanChannel,
		State:   hostclient.HealthStateUnhealthy,
		Detail:  fmt.Sprintf("%s: %s", code, channelScopeHint(step)),
	})
	if err != nil {
		log.Printf("slack-v2: report human_channel unhealthy: %v", err)
		return
	}

	h.mu.Lock()
	h.reported[channelHealthKey] = true
	h.mu.Unlock()
}

// errNoCredentials is returned when the instance has no usable Slack token.
// It is deliberately not a health event: an unconfigured instance is the
// host's to surface (config_missing), not something a plugin reports.
var errNoCredentials = errors.New("no Slack credentials configured; authorize the plugin in the admin UI")
