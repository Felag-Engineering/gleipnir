package agent

// This file routes a tool-initiated ask (ADR-055, spec §6) through the policy's
// audience instead of always asking the in-app channel. The router itself lives
// in internal/execution/run, behind the narrow InputChannelRouter interface
// declared here, for the same reason ApprovalChannelDispatcher does: agent must
// not import the plugin dispatch packages.
//
// Only a single-entry ask is routed. A channel/request carries exactly one ask
// (a message plus options, or one form), while an input_required result can
// bundle several; a bundle stays on the in-app path and its decision record
// says so, rather than being split into several asks or squeezed into one form
// that cannot express a per-entry decline.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
)

// InputChannelRouter routes one tool-initiated ask to the first eligible plugin
// entry of an audience and waits for that channel's answer.
type InputChannelRouter interface {
	// AskInput routes ask and blocks until the channel answers, ask.Wait
	// elapses, or ctx ends. It returns *InputRouteToInAppError when no plugin
	// entry could take the ask and the audience keeps its in-app fallback (the
	// caller asks in-app instead), *InputNoChannelError when no entry could
	// take it and the fallback is disabled, and *InputChannelWaitError when the
	// ask was routed but no usable answer came. On a wait failure the router
	// has already cancelled the channel's task.
	// It never writes a decision record: the caller owns the request row and
	// records exactly one decision for it.
	AskInput(ctx context.Context, ask InputChannelAsk) (InputChannelAnswer, error)
}

// InputChannelAsk is one routable ask.
type InputChannelAsk struct {
	AudienceID string
	RunID      string
	// PolicyName names the run in the fixed notify message; the ask's own
	// text never reaches a notify channel.
	PolicyName string
	// Request is the single entry being asked. Its Message is
	// server-controlled text and is delivered as untrusted content.
	Request mcp.InputRequest
	Kind    model.ElicitationKind
	// Wait bounds the wait for an answer: the request's effective deadline.
	Wait time.Duration
}

// InputChannel identifies the audience entry an ask was routed to, and the
// entries passed over before it.
type InputChannel struct {
	EntryID    string
	InstanceID string
	// Assurance is the decision-record form of the channel's declared
	// assurance (decision.AssuranceOf).
	Assurance  string
	Considered []decision.Candidate
}

// InputChannelActor is what the host established about the person a channel
// named on its answer.
type InputChannelActor struct {
	// ExternalID is the channel's claim. Untrusted on its own.
	ExternalID string
	// UserID and Username are the Gleipnir user the identity directory linked
	// ExternalID to, on the instance that answered. Empty means unverified.
	UserID   string
	Username string
	// MayResolve reports whether the linked user holds the role the ask's
	// kind requires.
	MayResolve bool
}

// InputChannelAnswer is a channel's answer to a routed ask, already mapped onto
// the elicitation vocabulary.
type InputChannelAnswer struct {
	Channel InputChannel
	// Action is inputActionAccept or inputActionDecline.
	Action string
	// Content is the submitted form, for an accepted information ask.
	Content json.RawMessage
	Actor   InputChannelActor
}

// InputRouteToInAppError reports that no plugin entry could take the ask.
// Considered records why each one was passed over.
type InputRouteToInAppError struct {
	Considered []decision.Candidate
}

func (e *InputRouteToInAppError) Error() string {
	return fmt.Sprintf("no plugin channel could take the ask (%d considered)", len(e.Considered))
}

// InputNoChannelError reports that no entry could take the ask and the
// audience's in-app fallback is disabled. The request fails loudly: an
// operator who turned the fallback off asked for exactly that, and the
// alternative is a question nobody is ever shown, waiting out its deadline.
type InputNoChannelError struct {
	Considered []decision.Candidate
}

func (e *InputNoChannelError) Error() string {
	return fmt.Sprintf("no audience entry may settle this request and the in-app fallback is disabled (%d considered)", len(e.Considered))
}

// InputChannelWaitError reports a routed ask that ended without an answer.
type InputChannelWaitError struct {
	Channel  InputChannel
	TimedOut bool
	Err      error
}

func (e *InputChannelWaitError) Error() string {
	return fmt.Sprintf("waiting on channel %s: %v", e.Channel.EntryID, e.Err)
}

func (e *InputChannelWaitError) Unwrap() error { return e.Err }

// considerMultiEntryBundle is the Considered reason recorded when a bundle of
// several asks is kept on the in-app path because a channel request carries
// only one.
const considerMultiEntryBundle = "multi_entry_bundle"

// WithInputChannelRouter routes single-entry asks through audienceID. A nil
// router or an empty audience leaves every ask in-app.
func WithInputChannelRouter(router InputChannelRouter, audienceID, policyName string) InputRequiredHandlerOption {
	return func(h *InputRequiredHandler) {
		if router == nil || audienceID == "" {
			return
		}
		h.channel = router
		h.audienceID = audienceID
		h.policyName = policyName
	}
}

// channelAnswerResponse maps a channel answer onto the MRTR response for the
// one request it answers. The responder _meta is asserted only for a linked
// user: an unverified channel claim is not an identity the host vouches for.
func channelAnswerResponse(req mcp.InputRequest, ans InputChannelAnswer, kind model.ElicitationKind) (mcp.InputResponse, error) {
	resp := mcp.InputResponse{ID: req.ID, Action: ans.Action}
	if ans.Action == inputActionAccept && len(ans.Content) > 0 {
		resp.Content = ans.Content
	}
	if ans.Actor.UserID != "" {
		meta, err := responderMeta(Responder{UserID: ans.Actor.UserID, Username: ans.Actor.Username}, kind)
		if err != nil {
			return mcp.InputResponse{}, fmt.Errorf("marshaling responder identity: %w", err)
		}
		resp.Meta = meta
	}
	return resp, nil
}

// channelDecision fills the channel and actor fields of a decision record.
func channelDecision(base decision.Record, ch InputChannel, actor InputChannelActor, outcome decision.Outcome) decision.Record {
	rec := base
	rec.ChannelEntryID = ch.EntryID
	rec.ChannelInstance = ch.InstanceID
	rec.ChannelAssurance = ch.Assurance
	rec.Considered = ch.Considered
	rec.Outcome = outcome
	rec.LinkMethod = decision.LinkNone
	if outcome.HadActor() {
		rec.ActorExternalID = actor.ExternalID
		rec.LinkMethod = decision.LinkUnverified
		if actor.UserID != "" {
			rec.ActorUserID = actor.UserID
			rec.LinkMethod = decision.LinkDirectory
		}
	}
	return rec
}

// askThroughChannel routes a single-entry ask and settles it. handled=false
// means no plugin entry could take it: base.Considered has been filled in and
// the caller asks in-app as it always has.
func (h *InputRequiredHandler) askThroughChannel(ctx context.Context, req InputRoutingRequest, requestID string, kind model.ElicitationKind, waitFor, timeout time.Duration, source DeadlineSource, base *decision.Record) (handled bool, responses []mcp.InputResponse, err error) {
	entry := req.Result.InputRequests[0]
	ans, askErr := h.channel.AskInput(ctx, InputChannelAsk{
		AudienceID: h.audienceID,
		RunID:      req.RunID,
		PolicyName: h.policyName,
		Request:    entry,
		Kind:       kind,
		Wait:       waitFor,
	})

	var toInApp *InputRouteToInAppError
	if errors.As(askErr, &toInApp) {
		base.Considered = toInApp.Considered
		return false, nil, nil
	}

	var noChannel *InputNoChannelError
	if errors.As(askErr, &noChannel) {
		failed := *base
		failed.Considered = noChannel.Considered
		failed.Outcome = decision.OutcomeCancelled
		failed.LinkMethod = decision.LinkNone
		return true, nil, h.settleCancelled(ctx, req, requestID, failed, askErr)
	}

	var waitErr *InputChannelWaitError
	if errors.As(askErr, &waitErr) {
		routed := *base
		routed.ChannelEntryID = waitErr.Channel.EntryID
		routed.ChannelInstance = waitErr.Channel.InstanceID
		routed.ChannelAssurance = waitErr.Channel.Assurance
		routed.Considered = waitErr.Channel.Considered
		if waitErr.TimedOut {
			return true, nil, h.settleTimeout(ctx, req, requestID, timeout, source, routed)
		}
		routed.Outcome = decision.OutcomeCancelled
		routed.LinkMethod = decision.LinkNone
		return true, nil, h.settleCancelled(ctx, req, requestID, routed, waitErr)
	}

	if askErr != nil {
		// Routing itself failed (an unreadable audience, say). Nothing was
		// asked of anyone, so the request is cancelled rather than left
		// pending for an answer that cannot come.
		failed := *base
		failed.Outcome = decision.OutcomeCancelled
		failed.LinkMethod = decision.LinkNone
		return true, nil, h.settleCancelled(ctx, req, requestID, failed, fmt.Errorf("routing tool input through the audience: %w", askErr))
	}

	// A permission granted by an actor the host cannot tie to an approver is
	// refused, exactly as a plugin-routed policy approval is (#1028): the
	// channel's claim alone must not be able to satisfy a consent gate. A
	// decline needs no such check; refusing is never an escalation.
	if kind == model.ElicitationKindPermission && ans.Action == inputActionAccept && !ans.Actor.MayResolve {
		refused := channelDecision(*base, ans.Channel, ans.Actor, decision.OutcomeRejected)
		cancelErr := h.settleCancelled(ctx, req, requestID, refused,
			fmt.Errorf("permission granted by actor %q, who is not a verified approver on that channel", ans.Actor.ExternalID))
		return true, nil, cancelErr
	}

	resp, err := channelAnswerResponse(entry, ans, kind)
	if err != nil {
		return true, nil, &InputRoutingError{RequestID: requestID, Err: err}
	}
	responses = []mcp.InputResponse{resp}

	if rows := h.resolveRecord(ctx, req.RunID, requestID, responses); rows == 0 {
		return true, nil, &InputRoutingError{RequestID: requestID, Err: errors.New("request already resolved by the timeout scanner")}
	}
	h.recordDecision(ctx, channelDecision(*base, ans.Channel, ans.Actor, outcomeFor(responses)))

	if err := h.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
		return true, nil, fmt.Errorf("transitioning run back to running after tool input: %w", err)
	}
	return true, responses, nil
}
