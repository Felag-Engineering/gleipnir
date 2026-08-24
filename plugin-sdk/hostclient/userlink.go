package hostclient

import "context"

// ReasonNoPendingLink is SubmitIdentityProofResponse's Reason value when no
// link flow is configured on the host, or the configured binder found no
// pending link to bind — the same value from either cause, matching
// internal/plugin/hostendpoint.ReasonNoPendingLink, since a plugin has no
// use for telling the two apart.
const ReasonNoPendingLink = "no_pending_link"

// SubmitIdentityProofRequest is host/submit_identity_proof's arguments.
// ExternalUserID is opaque and namespaced by instance — never a
// medium-specific identifier like a raw Slack user ID interpreted by the
// host; the plugin's own external user id, as it uses everywhere else.
type SubmitIdentityProofRequest struct {
	ExternalUserID string `json:"external_user_id"`
	Code           string `json:"code"`
}

// SubmitIdentityProofResponse is host/submit_identity_proof's result. It
// carries only Accepted and Reason — deliberately no user_id, no role, no
// echoed external_user_id. An unverified self-asserted identity must never
// be able to resolve a permission request by riding this response into actor
// authorization (ADR-058); only the host's own durable link record does
// that.
type SubmitIdentityProofResponse struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// SubmitIdentityProof relays a code the external user supplied through the
// plugin's medium (e.g. a slash command) to bind a pending inbound_code
// identity link. The host owns the link state machine; this call only
// forwards the code and returns the outcome.
func (c *Client) SubmitIdentityProof(ctx context.Context, req SubmitIdentityProofRequest) (*SubmitIdentityProofResponse, error) {
	var out SubmitIdentityProofResponse
	if err := c.callTool(ctx, toolSubmitIdentityProof, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetUserConfigRequest is host/get_user_config's arguments.
type GetUserConfigRequest struct {
	ExternalUserID string `json:"external_user_id"`
}

// GetUserConfigResponse is host/get_user_config's result. UserConfigJSON is
// "{}" when no per-user config storage is configured on the host, or when
// the user has never set any — never an error, since a plugin asking about a
// user with no preferences yet needs a usable default. This config is
// presentation and routing preference only; it can never grant capability
// (ADR-058).
type GetUserConfigResponse struct {
	UserConfigJSON string `json:"user_config_json"`
}

// GetUserConfig reads one external user's per-plugin config.
func (c *Client) GetUserConfig(ctx context.Context, req GetUserConfigRequest) (*GetUserConfigResponse, error) {
	var out GetUserConfigResponse
	if err := c.callTool(ctx, toolGetUserConfig, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
