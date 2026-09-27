// Package run — this file implements agent.ApprovalChannelDispatcher and
// agent.FeedbackChannelDispatcher over the MCP-realignment route: hitl.Router
// picks the audience entry, mcp.Client.ChannelRequest opens a Tasks task, and
// TaskWaiter blocks until it settles (mcp-realignment-spec.md §4.1, §6.4).
//
// This is the v2-assembly-only replacement for ApprovalChannelAdapter /
// FeedbackChannelAdapter (approval_adapter.go, feedback_adapter.go), which
// stay untouched and stay what the default build wires — #962 is the only
// caller that will ever construct a TaskChannelAdapters.
//
// # Why the in-app entry never reaches hitl.Router here
//
// hitl.Router can itself open the synthetic gleipnir.in-app entry (it always
// carries an InApp opener), but ApprovalHandler.Wait and FeedbackHandler.Wait
// already have their OWN in-app path — the pre-existing approvalCh / inApp
// waiter flow, unrelated to the mcp_tasks task lifecycle. Letting the router
// open a second, different in-app implementation for the same fallback would
// give an operator's decision two different shapes depending on which one
// happened to run. So this file excludes InApp entries from what it hands to
// Route, and reads "nothing eligible was left" as the signal to return the
// existing ErrApprovalRouteToInApp / ErrFeedbackRouteToInApp sentinels,
// exactly as the v1 adapters do today.
//
// One consequence of that exclusion is worth stating plainly: an audience's
// own disable_in_app_fallback setting only ever controlled whether
// hitl.Router's SYNTHETIC entry existed. Since this file never hands that
// entry to Route in the first place, disabling it on the audience changes
// nothing about what this file does — an audience with no eligible
// non-in-app entry (whether because none exist or every one is weak-only for
// a permission ask) ALWAYS falls through to ApprovalHandler.Wait /
// FeedbackHandler.Wait's own pre-existing in-app path, matching the v1
// adapters (approval_adapter.go / feedback_adapter.go), which make the exact
// same unconditional fallback via dispatch.ErrNoRequestCapableEntry. An
// operator who disables in-app fallback on an audience is opting a
// TOOL-INITIATED ask (routed through internal/plugin/hitl directly, not
// through this file) out of it — not a policy-gated approval or an
// agent-initiated feedback request.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
	"github.com/felag-engineering/gleipnir/internal/plugin/inapptask"
)

// errRouteToInApp is the internal signal that no non-in-app entry could take
// the ask. DispatchApproval and DispatchFeedback each map it onto their own
// public sentinel (agent.ErrApprovalRouteToInApp / agent.ErrFeedbackRouteToInApp).
var errRouteToInApp = errors.New("task channel adapter: route to in-app channel")

// defaultChannelWaitTimeout is the fallback wait when the caller supplies no
// ExpiresAt — matching ApprovalChannelAdapter / FeedbackChannelAdapter's own
// "sensible default when no expiry is set". Production callers
// (ApprovalHandler.Wait, FeedbackHandler.Wait) always set ExpiresAt, so this
// only applies to a hand-built request (e.g. in a test).
const defaultChannelWaitTimeout = time.Hour

// Approval option vocabulary. The host chooses these IDs (channel/request's
// payload is host-authored), so the mapping back to a bool is strict by
// construction — see decodeApprovalOptionID.
const (
	approveOptionID = "approve"
	rejectOptionID  = "reject"
)

var approvalOptions = []inapptask.Option{
	{ID: approveOptionID, Label: "Approve"},
	{ID: rejectOptionID, Label: "Reject"},
}

// feedbackRequestedSchema is the form the host asks a channel to render for a
// gleipnir.ask_operator request: one freeform text field. Content on the
// resolution therefore comes back shaped as {"text": "..."}, the same
// envelope parseFeedbackResponse (feedback.go) already expects from the v1
// path, so DispatchFeedback's return value needs no extra translation.
var feedbackRequestedSchema = json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"Your response"}},"required":["text"]}`)

// Audit event types this file writes directly (alongside, not instead of, a
// decision.Record — see writeUndecodableEvidence). Both reuse
// hostendpoint.EventTypeUnauthorizedApproval's name deliberately for the
// actor case: it is the same fact ("a permission settlement arrived with no
// actor this host will vouch for") wherever it is caught.
const (
	eventTypeUnauthorizedApprovalActor = "unauthorized_approval_attempt"
	eventTypeChannelAnswerUndecodable  = "channel_answer_undecodable"
)

// deadlineSourceExpiresAt / deadlineSourceDefault record which clock produced
// a settlement's EffectiveDeadline, for the decision record. Deliberately its
// own small vocabulary rather than agent.DeadlineSource (the tool-initiated
// HITL path's multi-clock §6.3 vocabulary): this file has exactly two
// sources — the caller's ExpiresAt, or this file's own fallback when none was
// given — and borrowing a vocabulary built for a different, richer clock set
// would claim a precision ("policy timeout" vs. "server TTL" vs. "request
// state TTL") this file does not have.
const (
	deadlineSourceExpiresAt = "expires_at"
	deadlineSourceDefault   = "default"
)

func deadlineSourceFor(expiresAt *time.Time) string {
	if expiresAt == nil {
		return deadlineSourceDefault
	}
	return deadlineSourceExpiresAt
}

// TaskChannelAdapters implements both agent.ApprovalChannelDispatcher and
// agent.FeedbackChannelDispatcher over one hitl.Router + TaskWaiter pair —
// the two requests differ only in elicitation kind and payload shape, so one
// type backs both interfaces rather than duplicating the routing/waiting
// machinery per kind.
type TaskChannelAdapters struct {
	router    *hitl.Router
	waiter    *TaskWaiter
	entries   AudienceEntriesResolver
	store     decision.Store
	decisions *decision.Recorder
}

// NewTaskChannelAdapters constructs a TaskChannelAdapters. router and waiter
// are typically shared with #995's tool-initiated HITL path; entries and
// store are narrow enough that tests can substitute fakes for either without
// standing up a database. store backs both the §6.6 decision record
// (decision.NewRecorder) and the raw high-severity plugin_audit_events rows
// this file writes directly for an undecodable or unauthorized settlement.
func NewTaskChannelAdapters(router *hitl.Router, waiter *TaskWaiter, entries AudienceEntriesResolver, store decision.Store) (*TaskChannelAdapters, error) {
	if router == nil {
		return nil, fmt.Errorf("task channel adapters: router is required")
	}
	if waiter == nil {
		return nil, fmt.Errorf("task channel adapters: waiter is required")
	}
	if entries == nil {
		return nil, fmt.Errorf("task channel adapters: entries resolver is required")
	}
	if store == nil {
		return nil, fmt.Errorf("task channel adapters: decision store is required")
	}
	return &TaskChannelAdapters{
		router:    router,
		waiter:    waiter,
		entries:   entries,
		store:     store,
		decisions: decision.NewRecorder(store),
	}, nil
}

// dispatchParams collects what one ask needs, whether it came from an
// approval gate or a gleipnir.ask_operator feedback request. now is captured
// ONCE, at DispatchApproval/DispatchFeedback's own entry — before
// ResolveEntries or Route ever run — so the deadline this ask waits against
// is anchored to when the ask STARTED, not to whatever moment happens to run
// last inside askChannel; every other time-derived value below (waitDuration,
// the decision record's EffectiveDeadline) reads from this one value rather
// than re-sampling the clock.
type dispatchParams struct {
	AudienceID      string
	RunID           string
	ToolName        string
	Prompt          string
	ExpiresAt       *time.Time
	Now             time.Time
	Kind            model.ElicitationKind
	Options         []inapptask.Option
	RequestedSchema json.RawMessage
}

// askResult is what askChannel resolved to, before kind-specific mapping.
type askResult struct {
	routed     hitl.Routed
	resolution mcp.ChannelResolution
}

// DispatchApproval routes a policy-gated approval through the mcp_tasks
// route and blocks until the operator approves, rejects, the request times
// out, or ctx is cancelled.
//
//   - Returns (Settlement{Approved: true}, nil) when the operator approved.
//   - Returns (Settlement{Approved: false}, nil) when the operator rejected.
//   - Returns (_, agent.ErrApprovalRouteToInApp) when no non-in-app
//     audience entry could take the ask.
//   - Returns (_, err) on any other routing, timeout, or decode failure —
//     an unrecognized channel answer never maps to true (security review:
//     "approve must map strictly; unknown is a rejection or a failure, never
//     an approve").
func (a *TaskChannelAdapters) DispatchApproval(ctx context.Context, req agent.ApprovalDispatchRequest) (agent.ApprovalSettlement, error) {
	p := dispatchParams{
		AudienceID: req.AudienceID,
		RunID:      req.RunID,
		ToolName:   req.ToolName,
		Prompt:     req.Prompt,
		ExpiresAt:  req.ExpiresAt,
		Now:        timeNow(),
		Kind:       model.ElicitationKindPermission,
		Options:    approvalOptions,
	}

	result, err := a.askChannel(ctx, p)
	if err != nil {
		if errors.Is(err, errRouteToInApp) {
			return agent.ApprovalSettlement{}, agent.ErrApprovalRouteToInApp
		}
		return agent.ApprovalSettlement{}, err
	}

	// Defense in depth (belt-and-suspenders over two invariants this file and
	// hitl.Router already enforce elsewhere): a permission settlement must
	// never come from the in-app route (excludeInApp already keeps InApp
	// entries out of what Route ever sees) or from a channel whose declared
	// assurance may not resolve a permission ask (hitl.Router's own §4.1 gate
	// already refuses to issue channel/request to one). Neither branch below
	// should be reachable; if one ever is, that is itself the finding worth
	// recording, not something to paper over by proceeding anyway.
	if result.routed.InApp || !result.routed.Assurance.MayResolve(mcp.ElicitationKindPermission) {
		a.writeUndecodableEvidence(ctx, p, result.routed, decision.OutcomeCancelled, eventTypeChannelAnswerUndecodable,
			fmt.Sprintf("permission settlement routed via an ineligible entry (in_app=%t assurance=%s)", result.routed.InApp, result.routed.Assurance))
		return agent.ApprovalSettlement{}, fmt.Errorf("approval request: settled via an entry that may not resolve a permission ask")
	}

	// Trust decision (#1028, owner-approved for now): the plugin's own
	// AuthorizeActor click-time pre-check is trusted as the actor
	// verification for a plugin-routed approval; this host does not
	// re-verify the identity a completed task's result carries. What IS
	// enforced here, unconditionally, is the floor beneath that trust: a
	// permission settlement naming NO actor at all is refused outright,
	// exactly as an unauthorized attempt would be. #1028 tracks closing the
	// remaining gap — a plugin that skipped or forged AuthorizeActor could
	// still stamp a completed task with a non-empty actor id this host has
	// no way to have independently checked.
	if result.resolution.ActorExternalID == "" {
		a.writeUndecodableEvidence(ctx, p, result.routed, decision.OutcomeRejected, eventTypeUnauthorizedApprovalActor,
			"permission settlement carried no actor_external_id")
		return agent.ApprovalSettlement{}, fmt.Errorf("approval request: channel resolution carries no actor identity")
	}

	approved, err := decodeApprovalOptionID(result.resolution.OptionID)
	if err != nil {
		a.writeUndecodableEvidence(ctx, p, result.routed, decision.OutcomeCancelled, eventTypeChannelAnswerUndecodable, err.Error())
		return agent.ApprovalSettlement{}, fmt.Errorf("approval request: %w", err)
	}

	actorExternalID := result.resolution.ActorExternalID
	return agent.ApprovalSettlement{
		Approved: approved,
		Settle: func(settleCtx context.Context, won bool) {
			// won=false means ApprovalHandler's own approval_requests CAS
			// lost the race with the timeout scanner: this decision never
			// actually took effect, so it must not be recorded as one that
			// did (issue #961 review item 1d).
			if !won {
				logctx.Logger(settleCtx).WarnContext(settleCtx, "task channel adapter: approval CAS lost the race with the timeout scanner; decision not recorded as answered",
					"run_id", p.RunID, "request_id", result.routed.RowID)
				return
			}
			outcome := decision.OutcomeRejected
			if approved {
				outcome = decision.OutcomeAnswered
			}
			a.recordSettlement(settleCtx, p, result.routed, outcome, actorExternalID)
		},
	}, nil
}

// decodeApprovalOptionID maps a channel's chosen option strictly onto
// approve/reject. Anything else — a malformed plugin, a protocol version
// this host cannot fully trust, an option ID that is neither, near-miss
// casing/whitespace/synonyms — is an error, never a silent approval and
// never a silent rejection either: DispatchApproval fails the call rather
// than fabricating a decision nobody made.
func decodeApprovalOptionID(optionID string) (bool, error) {
	switch optionID {
	case approveOptionID:
		return true, nil
	case rejectOptionID:
		return false, nil
	default:
		return false, fmt.Errorf("channel returned option %q, want %q or %q", optionID, approveOptionID, rejectOptionID)
	}
}

// DispatchFeedback routes a gleipnir.ask_operator request through the
// mcp_tasks route and blocks until the operator replies, the request times
// out, or ctx is cancelled.
//
//   - Returns (Settlement{Response: responseJSON}, nil) when the operator
//     replied; the caller (FeedbackHandler.Wait) passes it through
//     parseFeedbackResponse exactly as it does for the v1 path, since
//     feedbackRequestedSchema produces the same {"text": "..."} envelope.
//   - Returns (_, agent.ErrFeedbackRouteToInApp) when no non-in-app
//     audience entry could take the ask.
//   - Returns (_, err) on any other routing, timeout, or decode failure.
func (a *TaskChannelAdapters) DispatchFeedback(ctx context.Context, req agent.FeedbackDispatchRequest) (agent.FeedbackSettlement, error) {
	p := dispatchParams{
		AudienceID:      req.AudienceID,
		RunID:           req.RunID,
		ToolName:        req.ToolName,
		Prompt:          req.Prompt,
		ExpiresAt:       req.ExpiresAt,
		Now:             timeNow(),
		Kind:            model.ElicitationKindInformation,
		RequestedSchema: feedbackRequestedSchema,
	}

	result, err := a.askChannel(ctx, p)
	if err != nil {
		if errors.Is(err, errRouteToInApp) {
			return agent.FeedbackSettlement{}, agent.ErrFeedbackRouteToInApp
		}
		return agent.FeedbackSettlement{}, err
	}
	if len(result.resolution.Content) == 0 {
		// The channel answered a form ask with an option ID instead of
		// content — a protocol violation on the plugin's part. Refusing
		// rather than inventing a response keeps this consistent with the
		// approval side's "never fabricate an answer".
		a.writeUndecodableEvidence(ctx, p, result.routed, decision.OutcomeCancelled, eventTypeChannelAnswerUndecodable,
			"feedback settlement carried no form content")
		return agent.FeedbackSettlement{}, fmt.Errorf("feedback request: channel resolved with no form content")
	}

	response := string(result.resolution.Content)
	actorExternalID := result.resolution.ActorExternalID
	return agent.FeedbackSettlement{
		Response: response,
		Settle: func(settleCtx context.Context, won bool) {
			if !won {
				logctx.Logger(settleCtx).WarnContext(settleCtx, "task channel adapter: feedback CAS lost the race with the timeout scanner; decision not recorded as answered",
					"run_id", p.RunID, "request_id", result.routed.RowID)
				return
			}
			a.recordSettlement(settleCtx, p, result.routed, decision.OutcomeAnswered, actorExternalID)
		},
	}, nil
}

// askChannel is the routing + waiting machinery shared by DispatchApproval
// and DispatchFeedback. It resolves the audience, excludes the in-app entry,
// routes to the first eligible plugin channel, and blocks on TaskWaiter until
// the resulting task settles or the caller's own deadline (derived from
// p.ExpiresAt as of p.Now, never the task's server-declared TTL — spec
// §6.3's "the policy clock is authoritative") runs out first.
//
// Every non-success path here ends the ask without an answer: the task is
// cancelled (best-effort) and a decision record is written before the error
// is returned, so oversight evidence exists for a timeout or a cancellation
// exactly as it does for an answer.
func (a *TaskChannelAdapters) askChannel(ctx context.Context, p dispatchParams) (askResult, error) {
	entries, err := a.entries.ResolveEntries(ctx, p.AudienceID)
	if err != nil {
		return askResult{}, fmt.Errorf("resolve audience %s: %w", p.AudienceID, err)
	}

	routed, err := a.router.Route(ctx, hitl.Ask{
		RunID:           p.RunID,
		Message:         p.Prompt,
		Options:         p.Options,
		RequestedSchema: p.RequestedSchema,
		Kind:            p.Kind,
	}, excludeInApp(entries))
	if err != nil {
		if errors.Is(err, hitl.ErrNoEligibleEntry) {
			return askResult{}, errRouteToInApp
		}
		return askResult{}, fmt.Errorf("routing %s request: %w", p.Kind, err)
	}

	// TaskWaiter.Wait races a timer against a channel delivery (its own
	// select). If the channel's answer and the deadline become ready at
	// nearly the same instant, Go's select makes no promise about which
	// branch wins — an answer that arrived essentially AT the deadline may
	// still be reported as ErrTaskWaitTimeout below rather than decoded, and
	// this file must not assume the opposite either. Cancelling the task on
	// a timeout (settleWithoutAnswer) closes that window going forward, but
	// cannot retroactively decide who won a race that already happened.
	task, waitErr := a.waiter.Wait(ctx, routed.RowID, waitDuration(p.Now, p.ExpiresAt))
	if waitErr == nil {
		resolution, decodeErr := mcp.DecodeChannelResolution(taskResultJSON(task))
		if decodeErr != nil {
			logctx.Logger(ctx).WarnContext(ctx, "task channel adapter: decoding channel resolution failed",
				"run_id", p.RunID, "row_id", routed.RowID, "err", decodeErr)
			a.writeUndecodableEvidence(ctx, p, routed, decision.OutcomeCancelled, eventTypeChannelAnswerUndecodable, decodeErr.Error())
			return askResult{}, fmt.Errorf("%s request: %w", p.Kind, decodeErr)
		}
		return askResult{routed: routed, resolution: resolution}, nil
	}

	return askResult{}, a.settleWithoutAnswer(ctx, p, routed, waitErr)
}

// settleWithoutAnswer classifies why the wait ended without a decoded
// resolution, cancels the task where the host — not the channel — is the one
// giving up, records the outcome for oversight evidence, and returns the
// error DispatchApproval/DispatchFeedback surface to the caller.
//
// The ErrTaskWaitTimeout case in particular is reached only because
// TaskWaiter.Wait's own select (task_waiter.go) picked its timer branch over
// a channel delivery — and if the channel's answer became ready at nearly the
// same instant as the deadline, Go's select made no promise about which one
// won. A genuine answer that arrived essentially AT the deadline can still
// land here as a timeout rather than a decoded resolution; cancelling the
// task afterwards closes the window for anything FUTURE, not this one.
func (a *TaskChannelAdapters) settleWithoutAnswer(ctx context.Context, p dispatchParams, routed hitl.Routed, waitErr error) error {
	var failed *mcp.TaskFailedError

	switch {
	case errors.Is(waitErr, ErrTaskWaitTimeout):
		// The policy deadline passed with the task still open on the
		// channel's side: tell it to stop waiting (replaces the v1
		// dispatcher's RequestTerminated notification).
		a.cancelBestEffort(ctx, routed.RowID)
		a.recordSettlement(ctx, p, routed, decision.OutcomeTimeout, "")
		return fmt.Errorf("%s request timed out waiting for an operator response", p.Kind)

	case errors.Is(waitErr, mcp.ErrTaskExpired):
		// The server's own declared TTL elapsed before the policy deadline
		// did (spec §6.3: "server-side TTLs are weather"). Replaying the
		// answer against a freshly-opened task belongs to a later milestone
		// (#799); today the ask simply ends unanswered, same as a timeout.
		a.recordSettlement(ctx, p, routed, decision.OutcomeTimeout, "")
		return fmt.Errorf("%s request's channel task expired before an operator responded", p.Kind)

	case errors.Is(waitErr, mcp.ErrTaskCanceled):
		// The channel side cancelled the task on its own (e.g. an operator
		// dismissed the prompt in the plugin's own UI) — not something this
		// host asked for, so there is nothing to cancel back.
		a.recordSettlement(ctx, p, routed, decision.OutcomeCancelled, "")
		return fmt.Errorf("%s request's channel task was cancelled", p.Kind)

	case errors.As(waitErr, &failed):
		a.recordSettlement(ctx, p, routed, decision.OutcomeCancelled, "")
		return fmt.Errorf("%s request's channel task failed: %s", p.Kind, failed.Message)

	default:
		// ctx.Done() (the run was cancelled) or an unexpected TaskWaiter
		// error. Either way nobody answered, so this ends the same as a
		// cancellation — a fresh, bounded context is needed for the cleanup
		// writes below, since ctx itself may already be past its deadline.
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a.cancelBestEffort(cancelCtx, routed.RowID)
		a.recordSettlement(cancelCtx, p, routed, decision.OutcomeCancelled, "")
		return fmt.Errorf("%s request: %w", p.Kind, waitErr)
	}
}

// cancelBestEffort sends tasks/cancel and logs, rather than propagating, any
// failure: by the time this runs the ask is already ending unanswered, and a
// cancel that itself fails must not turn "the operator never responded" into
// a harder error than it already is.
func (a *TaskChannelAdapters) cancelBestEffort(ctx context.Context, rowID string) {
	if err := a.waiter.Cancel(ctx, rowID); err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "task channel adapter: tasks/cancel failed",
			"row_id", rowID, "err", err)
	}
}

// recordSettlement writes a §6.6 decision record for a settled ask — a plain
// answer, or one of the no-answer outcomes settleWithoutAnswer classifies.
// ctx is the caller's own context for a live answer, or a fresh bounded one
// when the run's own context is already past its deadline.
func (a *TaskChannelAdapters) recordSettlement(ctx context.Context, p dispatchParams, routed hitl.Routed, outcome decision.Outcome, actorExternalID string) {
	rec := decision.Record{
		RunID:             p.RunID,
		RequestID:         routed.RowID,
		Kind:              p.Kind,
		ToolName:          p.ToolName,
		ChannelEntryID:    routed.EntryID,
		ChannelInstance:   routed.InstanceID,
		ChannelAssurance:  decision.AssuranceOf(routed.Assurance),
		Outcome:           outcome,
		EffectiveDeadline: deadlineOf(p.ExpiresAt),
		DeadlineSource:    deadlineSourceFor(p.ExpiresAt),
		Considered:        toCandidates(routed.Skipped),
		LinkMethod:        decision.LinkNone,
	}
	if outcome.HadActor() && actorExternalID != "" {
		rec.ActorExternalID = actorExternalID
		rec.LinkMethod = decision.LinkUnverified
	}
	if err := a.decisions.Record(ctx, rec); err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "task channel adapter: writing decision record failed",
			"request_id", rec.RequestID, "run_id", rec.RunID, "outcome", string(rec.Outcome), "err", err)
	}
}

// writeUndecodableEvidence records BOTH a §6.6 decision record and a raw
// high-severity plugin_audit_events row for a settlement this adapter
// refuses to trust: an answer it could not decode, could not recognize, or
// could not attribute to any actor at all (#1028). Two records, not one,
// because they answer different questions for two different readers: the
// decision record is "what happened to this request", read alongside every
// other settlement of it on the run's timeline; the raw high-severity event
// is what an operator scanning for trouble sees without already knowing to
// look at decisions for this run.
func (a *TaskChannelAdapters) writeUndecodableEvidence(ctx context.Context, p dispatchParams, routed hitl.Routed, outcome decision.Outcome, eventType, detail string) {
	a.recordSettlement(ctx, p, routed, outcome, "")

	payload, err := json.Marshal(map[string]string{
		"run_id":           p.RunID,
		"request_id":       routed.RowID,
		"tool_name":        p.ToolName,
		"channel_entry_id": routed.EntryID,
		"detail":           detail,
	})
	if err != nil {
		payload = []byte("{}")
	}
	params := db.InsertPluginAuditEventParams{
		EventType:   eventType,
		Severity:    "high",
		PayloadJson: string(payload),
		CreatedAt:   timeNow().UTC().Format(time.RFC3339Nano),
		RunID:       &p.RunID,
	}
	if routed.InstanceID != "" {
		params.PluginInstanceID = &routed.InstanceID
	}
	if _, err := a.store.InsertPluginAuditEvent(ctx, params); err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "task channel adapter: writing high-severity audit event failed",
			"event_type", eventType, "run_id", p.RunID, "err", err)
	}
}

// excludeInApp drops the synthetic gleipnir.in-app entry so hitl.Router never
// sees it — see the package doc for why.
func excludeInApp(entries []hitl.Entry) []hitl.Entry {
	out := make([]hitl.Entry, 0, len(entries))
	for _, e := range entries {
		if e.InApp {
			continue
		}
		out = append(out, e)
	}
	return out
}

// toCandidates renders hitl.Router's skip list as decision.Candidate values —
// the §6.6 "why THIS channel" evidence.
func toCandidates(skipped []hitl.Skip) []decision.Candidate {
	if len(skipped) == 0 {
		return nil
	}
	out := make([]decision.Candidate, len(skipped))
	for i, s := range skipped {
		reason := string(s.Reason)
		if s.Detail != "" {
			reason = fmt.Sprintf("%s: %s", s.Reason, s.Detail)
		}
		out[i] = decision.Candidate{EntryID: s.EntryID, InstanceID: s.InstanceID, Reason: reason}
	}
	return out
}

// waitDuration derives TaskWaiter's deadline from the caller's ExpiresAt
// relative to now, falling back to defaultChannelWaitTimeout when
// expiresAt is nil. A deadline already in the past waits zero, which
// resolves as an immediate timeout rather than a negative or infinite one.
// now is the caller's own captured clock reading (dispatchParams.Now), not a
// fresh call here, so the whole dispatch call reasons about one instant.
func waitDuration(now time.Time, expiresAt *time.Time) time.Duration {
	if expiresAt == nil {
		return defaultChannelWaitTimeout
	}
	d := expiresAt.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

// deadlineOf renders ExpiresAt for the decision record, or the zero time
// when none was set.
func deadlineOf(expiresAt *time.Time) time.Time {
	if expiresAt == nil {
		return time.Time{}
	}
	return *expiresAt
}

// taskResultJSON extracts a terminal mcp_tasks row's result payload.
func taskResultJSON(task db.McpTask) json.RawMessage {
	if task.Result != nil {
		return json.RawMessage(*task.Result)
	}
	return nil
}
