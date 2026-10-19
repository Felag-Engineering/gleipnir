package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/slack-go/slack"
)

// channelScopes is the OAuth scope each channel/notify step needs, in the same
// shape and for the same reason as toolScopes. The manifest test requires
// manifest.yaml's scopes to be the union of both tables.
var channelScopes = map[string][]string{
	"open_direct":  {"im:write"},   // conversations.open
	"post_message": {"chat:write"}, // chat.postMessage
}

var (
	// Slack user IDs: U… (workspace) or W… (enterprise grid).
	userIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)
	// Conversation IDs: C… (public), G… (private/legacy group).
	channelIDPattern = regexp.MustCompile(`^[CG][A-Z0-9]{2,}$`)
	// Channel names as Slack allows them, with the optional leading '#'.
	channelNamePattern = regexp.MustCompile(`^#?[a-z0-9][a-z0-9._-]{0,79}$`)
)

// slackMrkdwnEscaper applies the three escapes Slack documents for message
// text. Everything that can become a mention, channel link, URL link or
// broadcast (<@U…>, <#C…>, <!channel>, <http://x|y>) needs a literal '<', so
// neutralizing these three characters is sufficient. '&' goes first by way of
// the replacer's single pass, so an existing "&lt;" in the input becomes
// "&amp;lt;" and renders literally.
//
// The host's run link therefore shows as plain text rather than a clickable
// <url|label>: the message is host-authored but may embed an untrusted
// payload, and the plugin cannot tell the host's own markup from an injected
// one. Safe text beats a clickable link.
var slackMrkdwnEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escapeMrkdwn(s string) string { return slackMrkdwnEscaper.Replace(s) }

// channelDeclaration is the io.gleipnir/channel entry the server advertises;
// manifest.yaml's human_channel assurance must agree with it.
var channelDeclaration = channelext.Declaration{
	Version:    channelext.ExtensionVersion,
	Assurance:  manifestv2.AssuranceAuthenticated,
	Deliveries: []string{channelext.DeliveryDirect, channelext.DeliveryShared},
}

// notify is the channelext.NotifyFunc: it delivers one host-authored message
// to a Slack user's DM (direct) or a channel (shared).
func (p *plugin) notify(ctx context.Context, n channelext.Notification) error {
	channel, err := p.resolveTarget(ctx, n.Target)
	if err != nil {
		return err
	}
	return p.postNotification(ctx, channel, n.Message)
}

// validateTarget rejects a malformed address before any Slack call.
func validateTarget(t channelext.Target) error {
	switch t.Delivery {
	case channelext.DeliveryDirect:
		if !userIDPattern.MatchString(t.Address) {
			return fmt.Errorf("direct address %q is not a Slack user ID (expected U… or W…)", t.Address)
		}
	case channelext.DeliveryShared:
		if !channelIDPattern.MatchString(t.Address) && !channelNamePattern.MatchString(t.Address) {
			return fmt.Errorf("shared address %q is not a Slack channel ID (C…, G…) or channel name", t.Address)
		}
	default:
		return fmt.Errorf("unsupported delivery %q", t.Delivery)
	}
	return nil
}

// resolveTarget returns the conversation to post into: the DM channel for a
// direct target, the address itself for a shared one.
func (p *plugin) resolveTarget(ctx context.Context, t channelext.Target) (string, error) {
	if err := validateTarget(t); err != nil {
		return "", err
	}
	if t.Delivery == channelext.DeliveryShared {
		return t.Address, nil
	}

	sc, err := p.slack.client(ctx)
	if err != nil {
		return "", err
	}
	dm, _, _, err := sc.OpenConversationContext(ctx, &slack.OpenConversationParameters{Users: []string{t.Address}})
	if err != nil {
		p.reportChannelFailure(ctx, "open_direct", err)
		return "", fmt.Errorf("open direct message: %w", err)
	}
	if dm == nil || dm.ID == "" {
		return "", errors.New("open direct message: Slack returned no conversation")
	}
	return dm.ID, nil
}

func (p *plugin) postNotification(ctx context.Context, channel, message string) error {
	sc, err := p.slack.client(ctx)
	if err != nil {
		return err
	}
	_, _, err = sc.PostMessageContext(ctx, channel,
		slack.MsgOptionText(escapeMrkdwn(message), false),
		slack.MsgOptionParse(false),
		slack.MsgOptionLinkNames(false),
		slack.MsgOptionDisableLinkUnfurl(),
		slack.MsgOptionDisableMediaUnfurl(),
	)
	if err != nil {
		p.reportChannelFailure(ctx, "post_message", err)
		return fmt.Errorf("post message: %w", err)
	}
	return nil
}

func (p *plugin) reportChannelFailure(ctx context.Context, step string, err error) {
	if code := scopeErrorCode(err); code != "" {
		p.health.reportChannelScopeFailure(ctx, step, code)
	}
}

// newChannelExtension builds the notify-only io.gleipnir/channel extension.
func (p *plugin) newChannelExtension() (*channelext.Extension, error) {
	return channelext.New(channelDeclaration, p.notify)
}
