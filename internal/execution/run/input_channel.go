// Package run — this file implements agent.InputChannelRouter on
// TaskChannelAdapters: a tool-initiated ask (MRTR input_required, ADR-055
// spec §6) routed through the policy's audience over the same hitl.Router +
// TaskWaiter route approvals and feedback already use.
//
// What differs from DispatchApproval / DispatchFeedback is who owns the
// request. A tool-initiated ask is a tool_input_requests row the agent's
// InputRequiredHandler created and must settle exactly once, so this file
// writes NO decision record of its own: it returns what the channel did, and
// the handler records it against its own request ID. The one thing written
// here is the high-severity unauthorized_approval_attempt event for a
// permission granted by an actor the host cannot vouch for — the same event a
// refused policy approval writes (#1028), so the operator's feed reads the
// same whichever gate was forged.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
)

// Elicitation actions an answer is mapped onto. They match agent's own
// vocabulary (inputrequired.go), which validates them again before the wire.
const (
	elicitationAccept  = "accept"
	elicitationDecline = "decline"
)

// PublicURLReader reads Gleipnir's configured public URL for the notify
// message's run link. *settings.Service satisfies it.
type PublicURLReader interface {
	GetPublicURL(ctx context.Context) (string, error)
}

// notifications is the optional "input requested" fan-out for tool-initiated
// asks (hitl.NotifyRequestRouted). Held apart from TaskChannelAdapters' own
// fields because it is wired after construction and joined on shutdown.
type notifications struct {
	notifier  *hitl.Notifier
	publicURL PublicURLReader
	wg        sync.WaitGroup
}

// WithNotifier fans a fixed "input requested" message out to the audience's
// notify-enabled entries whenever a tool-initiated ask is routed, whether to a
// plugin channel or to in-app. publicURL may be nil (the message then carries
// no link).
func (a *TaskChannelAdapters) WithNotifier(n *hitl.Notifier, publicURL PublicURLReader) *TaskChannelAdapters {
	a.notify = &notifications{notifier: n, publicURL: publicURL}
	return a
}

// WaitNotifications blocks until every notify fan-out already started has
// finished. Shutdown and tests join on it; a run never does.
func (a *TaskChannelAdapters) WaitNotifications() {
	if a.notify != nil {
		a.notify.wg.Wait()
	}
}

// AskInput routes one tool-initiated ask to the first eligible plugin entry
// of ask.AudienceID and blocks until that channel answers. See
// agent.InputChannelRouter for the contract.
func (a *TaskChannelAdapters) AskInput(ctx context.Context, ask agent.InputChannelAsk) (agent.InputChannelAnswer, error) {
	entries, err := a.entries.ResolveEntries(ctx, ask.AudienceID)
	if err != nil {
		return agent.InputChannelAnswer{}, fmt.Errorf("resolve audience %s: %w", ask.AudienceID, err)
	}

	hitlAsk := hitl.Ask{
		RunID:   ask.RunID,
		Message: ask.Request.Message,
		Kind:    ask.Kind,
	}
	if ask.Kind == model.ElicitationKindPermission {
		// A permission ask is consent, so the channel renders Approve/Reject
		// rather than whatever (field-less) schema the server sent.
		hitlAsk.Options = approvalOptions
	} else {
		hitlAsk.RequestedSchema = ask.Request.RequestedSchema
	}

	routed, err := a.router.Route(ctx, hitlAsk, excludeInApp(entries))
	if err != nil {
		if errors.Is(err, hitl.ErrNoEligibleEntry) {
			considered := toCandidates(routed.Skipped)
			// Unlike an approval or feedback request (see the package doc in
			// channel_adapter.go), a tool-initiated ask honors the audience's
			// disable_in_app_fallback: the synthetic entry is in entries only
			// when the fallback is on.
			if !hasInApp(entries) {
				return agent.InputChannelAnswer{}, &agent.InputNoChannelError{Considered: considered}
			}
			a.notifyRouted(ctx, ask, entries, "")
			return agent.InputChannelAnswer{}, &agent.InputRouteToInAppError{Considered: considered}
		}
		return agent.InputChannelAnswer{}, fmt.Errorf("routing %s ask: %w", ask.Kind, err)
	}
	a.notifyRouted(ctx, ask, entries, routed.EntryID)

	channel := agent.InputChannel{
		EntryID:    routed.EntryID,
		InstanceID: routed.InstanceID,
		Assurance:  decision.AssuranceOf(routed.Assurance),
		Considered: toCandidates(routed.Skipped),
	}

	task, waitErr := a.waiter.Wait(ctx, routed.RowID, ask.Wait)
	if waitErr != nil {
		return agent.InputChannelAnswer{}, a.inputWaitFailed(ctx, routed, channel, waitErr)
	}

	resolution, err := mcp.DecodeChannelResolution(taskResultJSON(task))
	if err != nil {
		return agent.InputChannelAnswer{}, a.inputUndecodable(ctx, ask, routed, channel, err.Error())
	}

	answer := agent.InputChannelAnswer{Channel: channel}
	switch ask.Kind {
	case model.ElicitationKindPermission:
		approved, err := decodeApprovalOptionID(resolution.OptionID)
		if err != nil {
			return agent.InputChannelAnswer{}, a.inputUndecodable(ctx, ask, routed, channel, err.Error())
		}
		answer.Action = elicitationDecline
		if approved {
			answer.Action = elicitationAccept
		}
	default:
		if len(resolution.Content) == 0 {
			return agent.InputChannelAnswer{}, a.inputUndecodable(ctx, ask, routed, channel, "information settlement carried no form content")
		}
		answer.Action = elicitationAccept
		answer.Content = resolution.Content
	}

	actor, err := a.resolveActor(ctx, ask.Kind, routed.InstanceID, resolution.ActorExternalID)
	if err != nil {
		// A failed lookup leaves the actor unverified, which the agent treats
		// exactly like an unlinked one: a grant is refused, anything else is
		// recorded as the channel's claim.
		logctx.Logger(ctx).WarnContext(ctx, "task channel adapter: resolving tool input actor failed",
			"run_id", ask.RunID, "row_id", routed.RowID, "err", err)
		actor = settlementActor{externalID: resolution.ActorExternalID}
	}
	if !actor.mayResolve {
		// A user without the role this kind requires is not a responder the
		// host asserts to the server (the responder _meta names the gate it
		// enforced), so the record keeps the channel's claim, unverified.
		actor = settlementActor{externalID: actor.externalID}
	}
	answer.Actor = agent.InputChannelActor{
		ExternalID: actor.externalID,
		UserID:     actor.userID,
		Username:   actor.username,
		MayResolve: actor.mayResolve,
	}

	if ask.Kind == model.ElicitationKindPermission && answer.Action == elicitationAccept && !actor.mayResolve {
		a.writeInputAuditEvent(ctx, ask, routed, eventTypeUnauthorizedApprovalActor,
			unverifiedApproverDetail(resolution.ActorExternalID, actor, a.actors != nil))
	}
	return answer, nil
}

// hasInApp reports whether the audience kept its in-app fallback.
func hasInApp(entries []hitl.Entry) bool {
	for _, e := range entries {
		if e.InApp {
			return true
		}
	}
	return false
}

// inputWaitFailed classifies a wait that ended without a resolution, cancels
// the channel's task where the host is the one giving up, and returns the
// error the agent settles the request from.
func (a *TaskChannelAdapters) inputWaitFailed(ctx context.Context, routed hitl.Routed, channel agent.InputChannel, waitErr error) error {
	var failed *mcp.TaskFailedError
	switch {
	case errors.Is(waitErr, ErrTaskWaitTimeout):
		a.cancelBestEffort(ctx, routed.RowID)
		return &agent.InputChannelWaitError{Channel: channel, TimedOut: true, Err: waitErr}
	case errors.Is(waitErr, mcp.ErrTaskExpired):
		// The channel's own TTL ran out first; nothing left to cancel.
		return &agent.InputChannelWaitError{Channel: channel, TimedOut: true, Err: waitErr}
	case errors.Is(waitErr, mcp.ErrTaskCanceled):
		return &agent.InputChannelWaitError{Channel: channel, Err: fmt.Errorf("the channel cancelled the ask: %w", waitErr)}
	case errors.As(waitErr, &failed):
		return &agent.InputChannelWaitError{Channel: channel, Err: fmt.Errorf("the channel's task failed: %s", failed.Message)}
	default:
		// The run was cancelled, or the waiter itself failed. ctx may already
		// be done, so the cancel gets its own bounded context.
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a.cancelBestEffort(cancelCtx, routed.RowID)
		return &agent.InputChannelWaitError{Channel: channel, Err: waitErr}
	}
}

// inputUndecodable refuses an answer this host cannot read as a decision,
// with the same high-severity event the approval path writes. Nobody's
// decision is fabricated from it: the agent settles the request cancelled.
func (a *TaskChannelAdapters) inputUndecodable(ctx context.Context, ask agent.InputChannelAsk, routed hitl.Routed, channel agent.InputChannel, detail string) error {
	logctx.Logger(ctx).WarnContext(ctx, "task channel adapter: tool input answer undecodable",
		"run_id", ask.RunID, "row_id", routed.RowID, "detail", detail)
	a.writeInputAuditEvent(ctx, ask, routed, eventTypeChannelAnswerUndecodable, detail)
	return &agent.InputChannelWaitError{Channel: channel, Err: fmt.Errorf("%s ask: channel answer refused: %s", ask.Kind, detail)}
}

// writeInputAuditEvent writes a raw high-severity plugin_audit_events row for
// a tool-initiated settlement this adapter refuses to trust. Best-effort.
func (a *TaskChannelAdapters) writeInputAuditEvent(ctx context.Context, ask agent.InputChannelAsk, routed hitl.Routed, eventType, detail string) {
	payload, err := json.Marshal(map[string]string{
		"run_id":           ask.RunID,
		"task_row_id":      routed.RowID,
		"elicitation_kind": string(ask.Kind),
		"channel_entry_id": routed.EntryID,
		"detail":           detail,
	})
	if err != nil {
		payload = []byte("{}")
	}
	runID := ask.RunID
	params := db.InsertPluginAuditEventParams{
		EventType:   eventType,
		Severity:    "high",
		PayloadJson: string(payload),
		CreatedAt:   timeNow().UTC().Format(time.RFC3339Nano),
		RunID:       &runID,
	}
	if routed.InstanceID != "" {
		instanceID := routed.InstanceID
		params.PluginInstanceID = &instanceID
	}
	if _, err := a.store.InsertPluginAuditEvent(ctx, params); err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "task channel adapter: writing high-severity audit event failed",
			"event_type", eventType, "run_id", ask.RunID, "err", err)
	}
}

// notifyRouted fires the fixed "input requested" notification in the
// background, skipping askedEntryID (that channel already has the question).
// It never carries the ask's own text — NotifyRequestRouted builds the message
// from the run alone — and never delays or fails the ask.
func (a *TaskChannelAdapters) notifyRouted(ctx context.Context, ask agent.InputChannelAsk, entries []hitl.Entry, askedEntryID string) {
	if a.notify == nil || a.notify.notifier == nil {
		return
	}
	targets := make([]hitl.Entry, 0, len(entries))
	for _, e := range entries {
		if e.EntryID != askedEntryID {
			targets = append(targets, e)
		}
	}
	// Detached from the run: a run that ends a moment later must not cut the
	// fan-out short. The notifier bounds it with its own timeout.
	bg := context.WithoutCancel(ctx)
	a.notify.wg.Add(1)
	go func() {
		defer a.notify.wg.Done()
		ref := hitl.RunRef{RunID: ask.RunID, PolicyName: ask.PolicyName}
		if a.notify.publicURL != nil {
			url, err := a.notify.publicURL.GetPublicURL(bg)
			if err != nil {
				logctx.Logger(bg).WarnContext(bg, "task channel adapter: reading public_url for the notify link failed", "err", err)
			}
			ref.PublicURL = url
		}
		hitl.NotifyRequestRouted(bg, a.notify.notifier, targets, ref)
	}()
}
