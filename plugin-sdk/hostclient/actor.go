package hostclient

import "context"

// AuthorizeActorRequest is host/authorize_actor's arguments. RequestID is
// the plugin's own Tasks-extension task id for the pending ask (the one
// value it holds at click time); ActorExternalID is the externally-asserted
// identity (a Slack user.id or similar) attempting to settle it.
type AuthorizeActorRequest struct {
	RequestID       string `json:"request_id"`
	ActorExternalID string `json:"actor_external_id"`
}

// AuthorizeActorResponse is host/authorize_actor's result. An unauthorized
// actor is NOT an error — Authorized is false, UserID is empty, and the
// request stays open for a legitimately authorized actor to try again. A
// plugin must check Authorized before letting the actor's action proceed.
type AuthorizeActorResponse struct {
	Authorized bool   `json:"authorized"`
	UserID     string `json:"user_id,omitempty"`
}

// AuthorizeActor checks whether ActorExternalID may settle a pending
// tool-initiated HITL request (spec §6.4, §8) before the plugin lets it, and
// — when authorized — hints the host to poll the associated task
// immediately rather than waiting for the next scheduled tick. Only
// approver/operator/admin roles authorize; auditor deliberately does not.
func (c *Client) AuthorizeActor(ctx context.Context, req AuthorizeActorRequest) (*AuthorizeActorResponse, error) {
	var out AuthorizeActorResponse
	if err := c.callTool(ctx, toolAuthorizeActor, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
