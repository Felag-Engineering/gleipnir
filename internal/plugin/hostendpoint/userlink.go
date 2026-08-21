package hostendpoint

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// SubmitIdentityProof and GetUserConfig — the last two methods in the spec §8
// inventory (ADR-058, spec §9). Both are thin entry points: the host-side
// state machines and storage they front are #18's scope, not this file's.
// This file wires the two methods to the seams #18 needs and enforces the
// invariants that must hold regardless of what sits behind those seams.

// ReasonNoPendingLink is SubmitIdentityProof's rejection reason when no link
// flow is configured on this host (Binder is nil) or the configured binder
// found no pending link to bind. It is a result value, not an error code —
// the plugin needs a clean rejection to relay in its own ephemeral reply,
// not a transport fault.
const ReasonNoPendingLink = "no_pending_link"

// BindResult is SubmitIdentityProof's entire result vocabulary: an
// accept/reject outcome and, on rejection, a machine-readable reason. It
// deliberately carries nothing else — no user_id, no role, no echoed
// external_user_id — because ADR-058 makes an unverified self-asserted
// identity disqualifying: it must never be able to resolve a permission
// request by riding this method's RESPONSE into actor authorization. Only
// the host's own durable link record, written by the binder BEFORE it
// returns Accepted, may feed authorization; nothing in this struct does.
type BindResult struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// PendingLinkBinder is #18's implementation point for the `inbound_code`
// link method (spec §9.1). ADR-058: the host owns the pending-link state
// machine — code generation and expiry live behind this seam, entirely on
// the host side. BindInboundCode BINDS an already-pending link; it does not
// create identity, and a plugin cannot make one exist by calling it.
//
// A nil Binder means this host has no link flow configured at all: every
// proof is rejected with ReasonNoPendingLink rather than an error, since
// "no link flow configured" and "no pending link found" are the same
// outcome from the plugin's side.
type PendingLinkBinder interface {
	// BindInboundCode attempts to bind a pending `inbound_code` link for
	// instanceID using the code the external user supplied through the
	// medium (e.g. a slash command). externalUserID is opaque and
	// namespaced by instance (ADR-058 — no medium-specific vocabulary here
	// or anywhere else in this contract; not "slack_user_id").
	BindInboundCode(ctx context.Context, instanceID, externalUserID, code string) (BindResult, error)
}

// UserConfigReader is #18's implementation point for per-user config reads
// (spec §9.2). It resolves (instanceID, externalUserID) to that external
// user's per-plugin config, already validated by the reader against the
// manifest's user_config_schema — validation is the reader's job, not this
// handler's, since #18 owns both the storage and the schema it is validated
// against.
//
// ADR-058: user config may never grant capability — role gates and policy
// grants are untouched by anything this seam returns. A routing-affecting
// preference in that config uses the channel-neutral `delivery: direct |
// shared` vocabulary (never medium-specific words like "DM"), and only the
// HOST's audience dispatcher interprets it; a plugin reads its own user
// config for presentation only.
//
// A nil ConfigReader means no per-user config storage is configured on this
// host: GetUserConfig returns an empty config, not an error.
type UserConfigReader interface {
	GetUserConfig(ctx context.Context, instanceID, externalUserID string) (json.RawMessage, error)
}

// UserLinkQuerier is the slice of the sqlc surface SubmitIdentityProof and
// GetUserConfig need to resolve the caller's own instance row. *db.Queries
// satisfies it.
type UserLinkQuerier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
}

// UserLinkDeps carries the collaborators the two handlers share.
type UserLinkDeps struct {
	Querier UserLinkQuerier

	// Binder is the #18 seam behind SubmitIdentityProof. Nil ⇒ no link
	// flow configured; every proof rejects with ReasonNoPendingLink.
	Binder PendingLinkBinder

	// ConfigReader is the #18 seam behind GetUserConfig. Nil ⇒ empty
	// config, not an error.
	ConfigReader UserConfigReader
}

// UserLinkTools returns SubmitIdentityProof and GetUserConfig as
// host-endpoint tool definitions, ready for Server.Register.
func UserLinkTools(deps UserLinkDeps) []ToolDef {
	return []ToolDef{
		{
			Name:        ToolSubmitIdentityProof,
			Description: "Bind a pending inbound_code identity link using a code the external user supplied through the medium.",
			Handler:     deps.submitIdentityProof,
		},
		{
			Name:        ToolGetUserConfig,
			Description: "Read one external user's per-plugin config: presentation and routing preferences only, never capability.",
			Handler:     deps.getUserConfig,
		},
	}
}

// resolveInstance fetches the authenticated caller's instance row. Mirrors
// Tier1Deps.resolveInstance — same package, same shape, kept as a separate
// method on this deps struct rather than shared, since the two dependency
// structs are otherwise independent and a shared helper would couple them
// for no reason.
func (d UserLinkDeps) resolveInstance(ctx context.Context) (db.PluginInstance, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return db.PluginInstance{}, &ToolError{Code: "unauthenticated", Message: "no plugin instance identity on request"}
	}
	inst, err := d.Querier.GetPluginInstanceByID(ctx, id.InstanceID)
	if err != nil {
		return db.PluginInstance{}, &ToolError{Code: "internal", Message: fmt.Sprintf("fetch instance: %v", err)}
	}
	return inst, nil
}

type submitIdentityProofArgs struct {
	// ExternalUserID is opaque and namespaced by instance (ADR-058): the
	// host never interprets it, and neither does this handler beyond
	// checking it is present.
	ExternalUserID string `json:"external_user_id"`
	Code           string `json:"code"`
}

// submitIdentityProof is the plugin's leg of the `inbound_code` link flow
// (spec §9.1): the user sends a code through the medium, the plugin relays
// it here, and the host accepts or rejects. The host owns the state machine
// behind PendingLinkBinder; this handler only validates the wire arguments
// and forwards to it, returning the outcome verbatim — it never inspects or
// reshapes what the binder decided, which is what keeps the disqualifying
// property (see BindResult) true regardless of how #18 implements the
// binder.
func (d UserLinkDeps) submitIdentityProof(ctx context.Context, args json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	var a submitIdentityProofArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if a.ExternalUserID == "" || a.Code == "" {
		return nil, &ToolError{Code: "invalid_argument", Message: "external_user_id and code are both required"}
	}
	if d.Binder == nil {
		return BindResult{Accepted: false, Reason: ReasonNoPendingLink}, nil
	}
	result, bindErr := d.Binder.BindInboundCode(ctx, inst.ID, a.ExternalUserID, a.Code)
	if bindErr != nil {
		return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("bind inbound code: %v", bindErr)}
	}
	return result, nil
}

type getUserConfigArgs struct {
	// ExternalUserID is opaque and namespaced by instance (ADR-058), same
	// as SubmitIdentityProof's argument of the same name — the plugin
	// names which of its own users it wants presentation config for.
	ExternalUserID string `json:"external_user_id"`
}

// getUserConfig reads one external user's per-plugin config (spec §9.2). No
// audit event, same posture as host/get_instance_config: this is a read of
// presentation and routing preference, not a security-relevant action.
func (d UserLinkDeps) getUserConfig(ctx context.Context, args json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	var a getUserConfigArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if a.ExternalUserID == "" {
		return nil, &ToolError{Code: "invalid_argument", Message: "external_user_id is required"}
	}
	if d.ConfigReader == nil {
		// No per-user config storage configured on this host: absent
		// config is empty, not an error — a plugin asking about a user
		// with no preferences set yet must get a usable default, not a
		// fault (#18 owns the storage; this is the seam's no-op zero
		// value).
		return map[string]any{"user_config_json": "{}"}, nil
	}
	cfg, readErr := d.ConfigReader.GetUserConfig(ctx, inst.ID, a.ExternalUserID)
	if readErr != nil {
		return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("read user config: %v", readErr)}
	}
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	return map[string]any{"user_config_json": string(cfg)}, nil
}
