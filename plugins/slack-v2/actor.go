package main

import "github.com/slack-go/slack"

// actorExternalID is the identity the plugin asserts to the host for a click:
// the stable Slack user ID. The display name and real name are chosen by the
// user (and can be changed or duplicated), so they must never identify an
// approver. Every actor_external_id the plugin sends, whether to
// host/authorize_actor or in a task resolution, comes from here.
//
// An empty result means Slack gave no ID; the caller must treat the click as
// unverifiable.
func actorExternalID(user slack.User) string {
	return user.ID
}
