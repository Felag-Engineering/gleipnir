// Package agent — this file defines the ApprovalChannelDispatcher interface and
// the types it operates on.  The interface lives in the agent package so the agent
// can depend on it without importing internal/plugin/dispatch (which would violate
// the package-boundary constraint: agent must not import execution-layer packages).
// The concrete adapters live in internal/execution/run (approval_adapter.go for
// v1, channel_adapter.go for v2) where both agent and dispatch/hitl are already
// imported.
package agent

import (
	"context"
	"errors"
	"time"
)

// ApprovalChannelDispatcher routes an approval request through a plugin channel
// entry (e.g. Slack DM with approve/deny buttons).  It is narrow — only the
// types the agent package already knows about are used.
//
// The implementation lives in internal/execution/run to avoid the circular
// import that would result from agent importing internal/plugin/dispatch.
type ApprovalChannelDispatcher interface {
	DispatchApproval(ctx context.Context, req ApprovalDispatchRequest) (ApprovalSettlement, error)
}

// ApprovalDispatchRequest carries everything the dispatcher needs to route a
// single approval gate through a plugin channel.
type ApprovalDispatchRequest struct {
	AudienceID string
	RunID      string
	PolicyID   string
	ToolName   string
	Prompt     string
	// ExpiresAt is the absolute deadline for this gate. ApprovalHandler.Wait
	// always sets it to the exact same instant it wrote to the
	// approval_requests row's expires_at column — including the no-timeout
	// default (1h) — so the dispatcher's own wait can never diverge from the
	// deadline the timeout scanner enforces. Dispatchers must not treat nil
	// as meaningful; it exists only so a hand-built request in a test can
	// omit it.
	ExpiresAt *time.Time
}

// ApprovalSettlement is what DispatchApproval resolved to.
type ApprovalSettlement struct {
	// Approved is the operator's decision, valid only once the caller's own
	// approval_requests CAS (ApprovalHandler.resolveApprovalRecord) confirms
	// the request actually settled through this call — see Settle.
	Approved bool

	// Settle, when non-nil, is called exactly once by ApprovalHandler.Wait
	// after it attempts the approval_requests CAS: won=true means this
	// call's decision is the one that is actually taking effect; won=false
	// means the timeout scanner already claimed the row first, and whatever
	// this dispatcher would otherwise record as a settled decision must not
	// be recorded as one, since the request never resolved through this
	// route. nil for a dispatcher with nothing to settle (the v1 gRPC
	// adapter has no decision-record concept).
	Settle func(ctx context.Context, won bool)
}

// ErrApprovalRouteToInApp is returned by the adapter when the audience resolves
// to the synthetic gleipnir.in-app entry.  ApprovalHandler.Wait treats this as
// a signal to fall through to the existing approvalCh path unchanged.
var ErrApprovalRouteToInApp = errors.New("approval: route to in-app channel")

// FeedbackChannelDispatcher routes a feedback request through a plugin channel
// entry (e.g. Slack message with threaded reply watching).  Parallel to
// ApprovalChannelDispatcher; the interface lives here to keep all channel-dispatch
// interfaces together in the agent package.
//
// The concrete adapter lives in internal/execution/run (feedback_adapter.go for
// v1, channel_adapter.go for v2).
type FeedbackChannelDispatcher interface {
	DispatchFeedback(ctx context.Context, req FeedbackDispatchRequest) (FeedbackSettlement, error)
}

// FeedbackDispatchRequest carries everything the dispatcher needs to route a
// single feedback request through a plugin channel.
type FeedbackDispatchRequest struct {
	AudienceID string
	RunID      string
	PolicyID   string
	ToolName   string
	Prompt     string
	// ExpiresAt is the absolute deadline for this request — see the matching
	// field on ApprovalDispatchRequest for why it is always set, never left
	// nil, by FeedbackHandler.Wait.
	ExpiresAt *time.Time
}

// FeedbackSettlement is what DispatchFeedback resolved to.
type FeedbackSettlement struct {
	// Response is the operator's freeform reply, valid only once won=true
	// reaches Settle — see ApprovalSettlement.Settle's doc for why.
	Response string

	// Settle mirrors ApprovalSettlement.Settle for the feedback path.
	Settle func(ctx context.Context, won bool)
}

// ErrFeedbackRouteToInApp is returned by the adapter when the audience resolves
// to the synthetic gleipnir.in-app entry.  FeedbackHandler.Wait treats this as
// a signal to fall through to the existing inAppChannel path unchanged.
var ErrFeedbackRouteToInApp = errors.New("feedback: route to in-app channel")
