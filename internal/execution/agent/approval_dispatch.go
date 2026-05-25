// Package agent — this file defines the ApprovalChannelDispatcher interface and
// the types it operates on.  The interface lives in the agent package so the agent
// can depend on it without importing internal/plugin/dispatch (which would violate
// the package-boundary constraint: agent must not import execution-layer packages).
// The concrete adapter lives in internal/execution/run (approval_adapter.go) where
// both agent and dispatch are already imported.
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
	DispatchApproval(ctx context.Context, req ApprovalDispatchRequest) (approved bool, err error)
}

// ApprovalDispatchRequest carries everything the dispatcher needs to route a
// single approval gate through a plugin channel.
type ApprovalDispatchRequest struct {
	AudienceID string
	RunID      string
	PolicyID   string
	ToolName   string
	Prompt     string
	// ExpiresAt is the absolute time at which the approval gate expires.
	// Nil means no expiry — the adapter defaults to a 1-hour timeout.
	// The adapter derives the Timeout duration via time.Until(*ExpiresAt) so
	// callers compute ExpiresAt once; the adapter owns the derivation.
	ExpiresAt *time.Time
}

// ErrApprovalRouteToInApp is returned by the adapter when the audience resolves
// to the synthetic gleipnir.in-app entry.  ApprovalHandler.Wait treats this as
// a signal to fall through to the existing approvalCh path unchanged.
var ErrApprovalRouteToInApp = errors.New("approval: route to in-app channel")
