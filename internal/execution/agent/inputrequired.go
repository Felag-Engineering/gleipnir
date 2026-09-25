// Package agent — this file implements the third HITL source (ADR-055, spec §6):
// a granted tools/call answers with MRTR input_required instead of a result, so
// the host pauses the run, records the pause durably, waits for an operator
// answer, and re-issues the SAME call with that answer attached.
//
// Two properties define this path and are worth stating up front:
//
//   - The agent never sees the exchange. No MRTR plumbing is written to
//     run_steps — the trace for a paused-and-resumed call is the ordinary
//     tool_call → tool_result pair, exactly as if the server had answered on the
//     first round trip. This is the ADR-046 split: operational detail belongs in
//     plugin_audit_events, not in the LLM-visible trace.
//   - It is cooperative, unlike the ADR-008 policy gate. The server asked; the
//     operator's answer (including a refusal) is handed BACK to the server,
//     which decides what to do with it. A decline is a legitimate MRTR round
//     trip, not a host-side abort. Both may occur on one call — the ADR-008
//     gate runs first, pre-execution, and the trace shows that sequence.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/audience"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
)

// responderMetaKey is the _meta key asserting who answered an elicitation
// (ADR-061, relay-646 §5): inputResponses[id]._meta["io.gleipnir/responder"] =
// {username, user_id}. Set server-side only, from the authenticated session of
// the person who answered — decodeInputResponses reads only action and
// content from the operator's own body, so neither the operator nor the model
// can set or alter this value. Sent to whichever server asked, with no
// opt-in.
const responderMetaKey = "io.gleipnir/responder"

// Responder is the authenticated Gleipnir user who answered a tool-initiated
// request, threaded from the HTTP handler's session down to the MRTR retry's
// response _meta and to the decision record.
type Responder struct {
	UserID   string
	Username string
}

// responderMetaValue is the wire shape of one responderMetaKey entry. Gate
// names which role Gleipnir required to reach this answer — "permission" or
// "information" — so a server can see which of its own two authorities was
// actually enforced (security review finding 4) rather than trusting that
// Gleipnir applied the role it asked for.
type responderMetaValue struct {
	Username string `json:"username"`
	UserID   string `json:"user_id"`
	Gate     string `json:"gate,omitempty"`
}

// responderMeta marshals r as inputResponses[id]._meta, or returns nil when r
// is the zero value — an unidentified responder (e.g. Decline's non-production
// call path) asserts nothing rather than an empty identity.
func responderMeta(r Responder, gate model.ElicitationKind) (json.RawMessage, error) {
	if r.UserID == "" {
		return nil, nil
	}
	return json.Marshal(map[string]responderMetaValue{
		responderMetaKey: {Username: r.Username, UserID: r.UserID, Gate: string(gate)},
	})
}

// Elicitation action vocabulary. Gleipnir validates against this set before the
// answer is handed back to the server: internal/mcp deliberately round-trips
// Action without interpreting it, so the host is the only place a bogus action
// can be caught before it reaches the wire.
const (
	inputActionAccept  = "accept"
	inputActionDecline = "decline"
	inputActionCancel  = "cancel"
)

// Elicitation kinds persisted in tool_input_requests.elicitation_kind (spec
// §6.1). The vocabulary lives in internal/model because the API layer's role
// gate reads the same values back out of the row.
const (
	elicitationKindPermission  = model.ElicitationKindPermission
	elicitationKindInformation = model.ElicitationKindInformation
)

// maxInputRequiredRounds bounds how many times ONE tools/call may pause for
// operator input before the call is abandoned. This is a structural backstop
// against a server that answers every retry with another input_required, not
// the spec §6.2 budget: that one is per-run, policy-configurable, and lands
// with the abuse-controls work. Exhausting this limit fails the call, not the
// run — see BoundAgent.handleToolCall, which renders it as a correctable
// tool_result so the agent can route around a misbehaving tool.
const maxInputRequiredRounds = 8

// fallbackInputTimeout bounds a tool-initiated wait when neither the policy nor
// the system default supplies a timeout. tool_input_requests.expires_at is NOT
// NULL — every pause carries a deadline — and an unbounded wait on a server's
// say-so is exactly the shape §6.2 exists to prevent.
const fallbackInputTimeout = 30 * time.Minute

// ErrUnknownInputRequestID is returned by InputRequiredHandler.Resolve when no
// waiter is registered for the given request ID: the wait already timed out,
// was already answered, or its run was cancelled. Callers should treat it as a
// benign late-callback signal, the same way ErrUnknownRequestID is treated on
// the feedback path.
var ErrUnknownInputRequestID = errors.New("unknown tool input request_id: no waiter registered")

// InputRoutingError reports that a tool-initiated pause failed on the HUMAN
// leg — the operator did not answer in time, or the run was cancelled while
// waiting. It is fatal to the run, and deliberately distinct from an ordinary
// MCP transport error (which the agent gets to see and reason about): nobody
// answered, so there is nothing for the agent to correct toward.
type InputRoutingError struct {
	RequestID string
	Err       error
}

func (e *InputRoutingError) Error() string {
	return fmt.Sprintf("tool input request %s: %s", e.RequestID, e.Err)
}

func (e *InputRoutingError) Unwrap() error { return e.Err }

// InputCallAbandonedError reports that the host gave up on one tools/call's
// input_required exchange without ever routing it to a human — the server
// asked too many times, or asked for something the host will not put in front
// of an operator at all.
//
// Unlike InputRoutingError this is not fatal to the run: nobody was waiting,
// so there is no unanswered operator wait to account for. The caller renders
// it as a tool_result error and the agent tries something else.
type InputCallAbandonedError struct {
	ToolName string
	Reason   string
	Err      error // the underlying refusal, when there is one
}

func (e *InputCallAbandonedError) Error() string {
	return fmt.Sprintf("tool %s: %s; the call was abandoned", e.ToolName, e.Reason)
}

func (e *InputCallAbandonedError) Unwrap() error { return e.Err }

// ElicitationBudget is the per-run elicitation budget check (spec §6.2 cap 1).
// The handler calls Check before routing each pause, which is the choke point
// no pause can reach an operator without passing. A nil budget means no
// accounting.
type ElicitationBudget interface {
	// Check reports whether runID may raise requests more elicitations. A
	// non-nil error abandons the pause; wrapping ErrElicitationBudgetExhausted
	// marks it as a budget refusal rather than an infrastructure failure.
	Check(ctx context.Context, runID string, requests int) error
}

// ErrElicitationBudgetExhausted marks a budget refusal. The distinction from
// any other Check error matters at the call site: an exhausted budget fails the
// CALL structurally and lets the run continue (spec §6.2), while an
// infrastructure failure in the budget itself is not something to paper over.
var ErrElicitationBudgetExhausted = errors.New("per-run elicitation budget exhausted")

// runElicitationBudget is the per-run counter behind the policy's
// max_elicitations_per_run. One instance belongs to one run, so it needs no
// run keying — the runID argument is checked as a defensive assertion, not
// used as a map key.
//
// Fail-closed means the refusal is unconditional once the budget is spent:
// there is no grace, no decay, and no way for a server to earn more by waiting.
// The rate limit (spec §6.2 cap 3) is what spaces requests out over time; this
// cap is what bounds them absolutely.
type runElicitationBudget struct {
	limit int // zero means unlimited

	mu    sync.Mutex
	spent int
}

// newRunElicitationBudget returns nil when limit <= 0, so an unlimited policy
// carries no counter and no lock at all.
func newRunElicitationBudget(limit int) *runElicitationBudget {
	if limit <= 0 {
		return nil
	}
	return &runElicitationBudget{limit: limit}
}

// Check consumes requests from the budget, or refuses. A pause that would
// overrun the budget consumes nothing: a partial spend would leave the run in a
// state where a smaller later pause could still succeed, which reads as the
// budget being negotiable.
func (b *runElicitationBudget) Check(_ context.Context, _ string, requests int) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.spent+requests > b.limit {
		elicitationBudgetExhausted.Inc()
		return fmt.Errorf("%w: %d of %d already used, this request needs %d",
			ErrElicitationBudgetExhausted, b.spent, b.limit, requests)
	}
	b.spent += requests
	return nil
}

// spentCount reports how much of the budget is consumed. Test-only accessor.
func (b *runElicitationBudget) spentCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// inputRequiredOptions builds the handler options implied by a policy. It is a
// function rather than inline construction so New() reads the same whether or
// not the policy sets a budget — a zero or absent limit yields no options at
// all, and the handler stays budget-free.
func inputRequiredOptions(policy *model.ParsedPolicy) []InputRequiredHandlerOption {
	if policy == nil {
		return nil
	}
	budget := newRunElicitationBudget(policy.Agent.Limits.MaxElicitationsPerRun)
	if budget == nil {
		return nil
	}
	return []InputRequiredHandlerOption{WithElicitationBudget(budget)}
}

// InputRoutingRequest is one MRTR pause: everything needed to persist the
// request durably and, later, to correlate the operator's answer with the
// original call.
type InputRoutingRequest struct {
	RunID    string
	ServerID string // mcp_servers row ID owning the original tools/call
	ToolName string // dot-name as the agent called it
	Input    map[string]any
	Result   *mcp.InputRequiredResult

	// Timeout is the human leg's deadline, resolved by the caller from the
	// policy. Zero or negative falls back to fallbackInputTimeout.
	Timeout time.Duration

	// ServerTaskTTL and RequestStateTTL are the server-side clocks that can
	// shorten the wait (spec §6.3). Zero means the clock does not apply, which
	// is the common case: neither is reachable until a wait is backed by a
	// durable task or a server declares a requestState TTL out-of-band.
	ServerTaskTTL   time.Time
	RequestStateTTL time.Time

	// Replay is what the operator was asked and answered immediately before
	// this request, set only when the server re-asked a DIFFERENT question
	// after its MRTR state expired (spec §6.5). Nil on a first ask.
	Replay *ReplayContext
}

// PersistedInputRequest is the wire shape of one entry in
// tool_input_requests.request_payload. It exists so the column has a stable,
// snake_case contract the API layer can decode, rather than whatever field
// names the mcp package's internal struct happens to marshal to today.
//
// Message is server-controlled text (spec §6.1). Everything that renders it —
// API response, UI, channel delivery — must treat it as untrusted content, not
// as markup and not as instructions.
type PersistedInputRequest struct {
	// ID is the server-assigned request id (ADR-061) this question was asked
	// under — the correlation key for the matching answer on the retry.
	ID              string          `json:"id"`
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requested_schema,omitempty"`
	ElicitationKind string          `json:"elicitation_kind,omitempty"`
}

// DecodeInputRequestPayload parses a tool_input_requests.request_payload blob.
// The API layer uses it to render what is being asked.
func DecodeInputRequestPayload(payload string) ([]PersistedInputRequest, error) {
	var requests []PersistedInputRequest
	if err := json.Unmarshal([]byte(payload), &requests); err != nil {
		return nil, fmt.Errorf("decoding tool input request payload: %w", err)
	}
	return requests, nil
}

// toPersistedRequests converts the decoded MRTR requests into the persisted
// shape.
func toPersistedRequests(requests []mcp.InputRequest) []PersistedInputRequest {
	out := make([]PersistedInputRequest, len(requests))
	for i, r := range requests {
		out[i] = PersistedInputRequest{
			ID:              r.ID,
			Message:         r.Message,
			RequestedSchema: r.RequestedSchema,
			ElicitationKind: r.ElicitationKind,
		}
	}
	return out
}

// inputAnswer is what a waiter's channel carries: the operator's answers,
// correlated by ID to the pause's InputRequests, plus who answered. The
// responder rides alongside the responses rather than only inside their
// _meta so Route can attach it to the decision record without re-parsing
// JSON it just built.
type inputAnswer struct {
	responses []mcp.InputResponse
	responder Responder
}

// inputWaiter is one registered wait. requests is the pause's own
// InputRequests, held here so Resolve/Decline can reject a mis-sized answer
// while the run is still safely paused rather than after it has resumed, and
// so each response can be stamped with the ID of the request it answers —
// MRTR correlates by ID (ADR-061), not by position. kind is the classified
// gate this pause required (permission or information), stamped into the
// responder _meta's "gate" field so the asking server can see which of its
// own two authorities Gleipnir actually enforced.
type inputWaiter struct {
	ch       chan inputAnswer
	requests []mcp.InputRequest
	kind     model.ElicitationKind
}

// InputRequiredHandler owns the tool-initiated pause lifecycle. It holds no
// BoundAgent reference so it can be constructed and tested independently, the
// same shape as ApprovalHandler and FeedbackHandler.
//
// Its waiter map is its own rather than the feedback path's inAppChannel: that
// channel carries freeform operator text, while this one carries a typed,
// position-correlated []mcp.InputResponse that has already been validated
// against the pause it answers. Sharing the map would mean pushing JSON
// through a string channel and re-parsing it on the far side, after the point
// where a bad payload could still be rejected harmlessly.
type InputRequiredHandler struct {
	audit          *AuditWriter
	sm             *RunStateMachine
	defaultTimeout time.Duration
	budget         ElicitationBudget

	mu      sync.Mutex
	waiters map[string]*inputWaiter
}

// InputRequiredHandlerOption is a functional option for NewInputRequiredHandler.
type InputRequiredHandlerOption func(*InputRequiredHandler)

// WithElicitationBudget attaches a per-run elicitation budget to the handler.
func WithElicitationBudget(b ElicitationBudget) InputRequiredHandlerOption {
	return func(h *InputRequiredHandler) { h.budget = b }
}

// NewInputRequiredHandler constructs an InputRequiredHandler. defaultTimeout is
// the system-wide feedback timeout, used when the policy names none.
func NewInputRequiredHandler(audit *AuditWriter, sm *RunStateMachine, defaultTimeout time.Duration, opts ...InputRequiredHandlerOption) *InputRequiredHandler {
	h := &InputRequiredHandler{
		audit:          audit,
		sm:             sm,
		defaultTimeout: defaultTimeout,
		waiters:        make(map[string]*inputWaiter),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// registerWaiter allocates the response channel for requestID. Registration
// happens BEFORE the run transitions to waiting_for_feedback, so an answer that
// arrives immediately after the transition's SSE event is never lost — the same
// invariant the feedback path maintains.
func (h *InputRequiredHandler) registerWaiter(requestID string, requests []mcp.InputRequest, kind model.ElicitationKind) <-chan inputAnswer {
	w := &inputWaiter{ch: make(chan inputAnswer, 1), requests: requests, kind: kind}
	h.mu.Lock()
	h.waiters[requestID] = w
	h.mu.Unlock()
	return w.ch
}

// unregisterWaiter drops the waiter for requestID. Safe when no entry exists.
func (h *InputRequiredHandler) unregisterWaiter(requestID string) {
	h.mu.Lock()
	delete(h.waiters, requestID)
	h.mu.Unlock()
}

// Resolve delivers an operator's answers to the waiter registered for
// requestID. body is a JSON array of {action, content} objects, one per
// InputRequest in the pause, correlated to the pause's InputRequests by
// position in the SUBMITTED body — the same order the API rendered the
// questions in — and re-keyed here onto each InputRequest's server-assigned
// ID (ADR-061) before it is handed to the wire. responder is the
// authenticated Gleipnir user who answered, asserted server-side in every
// response's _meta — the body itself carries only action and content, so
// neither the operator nor the model can set or alter this identity.
//
// Validation happens here rather than on the waiting side on purpose: a
// malformed answer is the caller's problem to fix and leaves the run paused and
// answerable, whereas the same check made after the handoff would fail a run
// over a bad payload the operator could simply have resubmitted.
func (h *InputRequiredHandler) Resolve(requestID, body string, responder Responder) error {
	h.mu.Lock()
	w, ok := h.waiters[requestID]
	h.mu.Unlock()
	if !ok {
		return ErrUnknownInputRequestID
	}

	responses, err := decodeInputResponses(body, len(w.requests))
	if err != nil {
		return err
	}

	meta, err := responderMeta(responder, w.kind)
	if err != nil {
		return fmt.Errorf("marshaling responder identity: %w", err)
	}
	for i := range responses {
		responses[i].ID = w.requests[i].ID
		// A cancel asserts no responder identity: nobody decided anything, per
		// the contract (security review finding 8) -- the operator abandoned
		// the exchange rather than accepting or declining it.
		if responses[i].Action != inputActionCancel {
			responses[i].Meta = meta
		}
	}
	return h.deliver(requestID, inputAnswer{responses: responses, responder: responder})
}

// Decline resolves the pause by declining every request in it — the deny path.
// The declines are still handed back to the server: MRTR treats a refusal as a
// legitimate answer, and the server decides whether that means an error result,
// a partial result, or a different question.
//
// It has no production caller today (the API's deny path goes through Resolve
// with action "decline" instead, so the responder identity is asserted the
// same way an accept is); kept for tests and as the shape a future
// host-initiated decline would use.
func (h *InputRequiredHandler) Decline(requestID string) error {
	h.mu.Lock()
	w, ok := h.waiters[requestID]
	h.mu.Unlock()
	if !ok {
		return ErrUnknownInputRequestID
	}

	responses := make([]mcp.InputResponse, len(w.requests))
	for i := range responses {
		responses[i] = mcp.InputResponse{ID: w.requests[i].ID, Action: inputActionDecline}
	}
	return h.deliver(requestID, inputAnswer{responses: responses})
}

// deliver hands a validated answer to the registered waiter and drops the
// registration so a second answer cannot arrive. The send is outside the lock:
// the channel is buffered (cap 1) with a single reader, so it never blocks, and
// holding the lock across it would serialize every Resolve call.
func (h *InputRequiredHandler) deliver(requestID string, answer inputAnswer) error {
	h.mu.Lock()
	w, ok := h.waiters[requestID]
	if ok {
		delete(h.waiters, requestID)
	}
	h.mu.Unlock()
	if !ok {
		return ErrUnknownInputRequestID
	}
	w.ch <- answer
	return nil
}

// Route pauses the run for one input_required result and returns the operator's
// answers, ready to be replayed on the retry tools/call.
//
// The pause is durable before it is observable: the tool_input_requests row and
// the waiting_for_feedback status change commit in one transaction (ADR-038), so
// a host that dies mid-wait leaves a pending row an operator answer can still be
// applied against. Full run resurrection is explicitly not claimed — the record
// surviving is.
func (h *InputRequiredHandler) Route(ctx context.Context, req InputRoutingRequest) ([]mcp.InputResponse, string, error) {
	requestID := model.NewULID()

	// The budget refusal is returned unwrapped, NOT inside an
	// InputRoutingError: nothing was routed, so it is not a routing failure.
	// The distinction is what lets the caller fail the call and keep the run
	// alive instead of treating it like an unanswered operator wait.
	if h.budget != nil {
		if err := h.budget.Check(ctx, req.RunID, len(req.Result.InputRequests)); err != nil {
			return nil, "", err
		}
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = h.defaultTimeout
	}
	if timeout <= 0 {
		timeout = fallbackInputTimeout
	}

	// The deadline that governs this wait is the minimum of every applicable
	// clock (spec §6.3), computed once here so no caller re-derives it.
	deadline, source := EffectiveDeadline(DeadlineInputs{
		Now:             timeNow(),
		PolicyTimeout:   timeout,
		ServerTaskTTL:   req.ServerTaskTTL,
		RequestStateTTL: req.RequestStateTTL,
	})
	expiresAt := deadline.UTC().Format(time.RFC3339Nano)

	// The in-process wait follows the EFFECTIVE deadline, not the policy
	// timeout: a server clock that expires first must end the wait first, or
	// the host would sit holding an answer slot the server has already
	// abandoned.
	waitFor := time.Until(deadline)
	if waitFor < 0 {
		waitFor = 0
	}

	if err := checkPersistedSize(req.Result, requestID); err != nil {
		return nil, "", err
	}

	callArgs, err := json.Marshal(req.Input)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling call args for %s: %w", req.ToolName, err)
	}
	requestPayload, err := json.Marshal(toPersistedRequests(req.Result.InputRequests))
	if err != nil {
		return nil, "", fmt.Errorf("marshaling input requests for %s: %w", req.ToolName, err)
	}
	var replayContext string
	if req.Replay != nil {
		encoded, err := json.Marshal(req.Replay)
		if err != nil {
			return nil, "", fmt.Errorf("marshaling replay context for %s: %w", req.ToolName, err)
		}
		replayContext = string(encoded)
	}

	kind := classifyElicitationKind(req.Result.InputRequests)

	// Register before the transition; release on every exit path.
	responses := h.registerWaiter(requestID, req.Result.InputRequests, kind)
	defer h.unregisterWaiter(requestID)

	if err := h.sm.Transition(ctx, model.RunStatusWaitingForFeedback, "", WithToolInputPayload(ToolInputPayload{
		RequestID:       requestID,
		ServerID:        req.ServerID,
		ToolName:        req.ToolName,
		CallArgs:        string(callArgs),
		RequestState:    string(req.Result.RequestState),
		RequestPayload:  string(requestPayload),
		ElicitationKind: string(kind),
		ExpiresAt:       expiresAt,
		DeadlineSource:  string(source),
		ReplayContext:   replayContext,
	})); err != nil {
		return nil, "", fmt.Errorf("transitioning run to waiting_for_feedback for tool input: %w", err)
	}

	// decisionBase carries the fields every settlement of THIS request shares,
	// so each branch below only has to fill in what differs (outcome, actor).
	decisionBase := decision.Record{
		RunID:             req.RunID,
		RequestID:         requestID,
		Kind:              kind,
		ToolName:          req.ToolName,
		ChannelEntryID:    audience.InAppEntryID,
		ChannelAssurance:  decision.AssuranceOf(mcp.ChannelAssuranceAuthenticated),
		EffectiveDeadline: deadline,
		DeadlineSource:    string(source),
	}

	timer := time.NewTimer(waitFor)
	defer timer.Stop()

	select {
	case ans := <-responses:
		// Finding 6 (security review): rows==0 means the timeout scanner
		// already claimed this row before the answer arrived. Send nothing
		// back to the server -- the answer was never genuinely applied -- and
		// do not record it as `answered`; the scanner's own OnTerminated hook
		// already recorded the timeout.
		rows := h.resolveRecord(ctx, req.RunID, requestID, ans.responses)
		if rows == 0 {
			logctx.Logger(ctx).WarnContext(ctx, "tool input: answer arrived after the scanner already claimed the row as timed out",
				"request_id", requestID, "run_id", req.RunID)
			return nil, "", &InputRoutingError{RequestID: requestID, Err: errors.New("request already resolved by the timeout scanner")}
		}

		rec := decisionBase
		rec.Outcome = outcomeFor(ans.responses)
		if rec.Outcome.HadActor() && ans.responder.UserID != "" {
			rec.ActorUserID = ans.responder.UserID
			rec.LinkMethod = decision.LinkSession
		} else {
			rec.LinkMethod = decision.LinkNone
		}
		// Recorded BEFORE the run-state transition, not after: a human
		// genuinely answered (rows==1), and that fact must survive even if
		// the transition below fails (finding 6).
		h.recordDecision(ctx, rec)

		if err := h.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
			return nil, "", fmt.Errorf("transitioning run back to running after tool input: %w", err)
		}
		return ans.responses, requestID, nil

	case <-timer.C:
		logctx.Logger(ctx).WarnContext(ctx, "tool input request timed out",
			"tool", req.ToolName, "request_id", requestID, "timeout", timeout.String())
		// Race the timeout scanner for the pending row, exactly as the approval
		// and feedback waits do: the conditional UPDATE arbitrates, and only the
		// winner writes the error step and the decision record. The scanner's
		// OWN win (this process restarted, or simply lost the race) is recorded
		// separately by its WithOnTerminated hook in main.go — every settlement
		// path gets a decision record, not only this live one.
		err := claimRequestTimeout(ctx, h.audit, timeoutClaim{
			name:      "tool input",
			runID:     req.RunID,
			requestID: requestID,
			claim: func(dbCtx context.Context, now string) (int64, error) {
				return h.sm.Queries().ExpireToolInputRequest(dbCtx, db.ExpireToolInputRequestParams{
					ResolvedAt: &now,
					ID:         requestID,
				})
			},
			errorCode:   model.ErrorCodeFeedbackTimeout,
			wonMessage:  timeoutMessage(req.ToolName, source, timeout),
			lostMessage: fmt.Sprintf("tool input timeout: already resolved by scanner for tool %s", req.ToolName),
			onWon: func() {
				rec := decisionBase
				rec.Outcome = decision.OutcomeTimeout
				rec.LinkMethod = decision.LinkNone
				h.recordDecision(ctx, rec)
			},
		})
		return nil, "", &InputRoutingError{RequestID: requestID, Err: err}

	case <-ctx.Done():
		// The run's context can be cancelled at almost the same instant an
		// operator's answer lands in the buffered `responses` channel above
		// (deliver() already returned success to the HTTP handler): Go's
		// select makes no promise about which ready case it picks. That race
		// is still safe here because nothing has touched the DB row yet — the
		// write to 'resolved' only happens in the answer branch above, which
		// cannot also run once this branch is chosen — so there is nothing
		// for CancelToolInputRequest to clobber in that scenario; the answer
		// is simply never applied, and labeling the row cancelled matches
		// what actually happened to the run.
		//
		// The real hazard the conditional UPDATE guards against is the
		// INDEPENDENT timeout scanner, racing on its own goroutine and ticker:
		// it can claim the very same row as expired at nearly the same moment
		// this run is cancelled. WHERE status='pending' is what makes the two
		// writers race safely — whichever CAS lands first wins, and the loser
		// (rows==0) records nothing rather than overwrite a decision that
		// already happened, so a row an operator or the scanner had already
		// settled is never relabelled cancelled underneath them.
		// A fresh, bounded context for every write below: ctx is the run's own
		// context and is already cancelled on this branch, so both the claim
		// and the decision record it may produce need their own deadline
		// rather than inheriting one that is already done.
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		rows, cancelErr := h.sm.Queries().CancelToolInputRequest(cancelCtx, db.CancelToolInputRequestParams{
			ResolvedAt: &now,
			ID:         requestID,
		})
		if cancelErr != nil {
			logctx.Logger(ctx).WarnContext(ctx, "tool input: CancelToolInputRequest failed",
				"request_id", requestID, "run_id", req.RunID, "err", cancelErr)
		} else if rows == 1 {
			rec := decisionBase
			rec.Outcome = decision.OutcomeCancelled
			rec.LinkMethod = decision.LinkNone
			h.recordDecision(cancelCtx, rec)
		}
		return nil, "", &InputRoutingError{RequestID: requestID, Err: fmt.Errorf("context cancelled waiting for tool input: %w", ctx.Err())}
	}
}

// outcomeFor classifies a settled answer, in this priority order:
//
//   - Any accept is a consent/value grant (OutcomeAnswered).
//   - Failing that, any decline is a refusal WITH an actor (OutcomeRejected)
//     — a person pressed Reject, and that is still true even if the bundle
//     mixes a decline with a cancel on another entry; a security-review
//     follow-up correction from the original "any cancel wins" rule, which
//     would have stripped a real human refusal of its actor whenever a
//     cancel rode alongside it.
//   - Only when EVERY response is a cancel is this OutcomeCancelled
//     (security review finding 8 — a cancel is not a decision, and must not
//     be recorded as one with an actor attached).
//
// Answered and rejected are both legitimate MRTR round trips handed back to
// the server, not host-side failures; a cancel is reachable through the API
// only (relay-646 §4).
func outcomeFor(responses []mcp.InputResponse) decision.Outcome {
	sawDecline := false
	for _, r := range responses {
		switch r.Action {
		case inputActionAccept:
			return decision.OutcomeAnswered
		case inputActionDecline:
			sawDecline = true
		}
	}
	if sawDecline {
		return decision.OutcomeRejected
	}
	return decision.OutcomeCancelled
}

// recordDecision writes one decision record for a settled tool-initiated
// request (ADR-055 §6.6). Best-effort and logged, never fatal to the run: the
// record is oversight evidence, and an audit-write hiccup must not fail a run
// whose human leg already settled successfully.
func (h *InputRequiredHandler) recordDecision(ctx context.Context, rec decision.Record) {
	if err := decision.NewRecorder(h.sm.Queries()).Record(ctx, rec); err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "tool input: writing decision record failed",
			"request_id", rec.RequestID, "run_id", rec.RunID, "outcome", string(rec.Outcome), "err", err)
	}
}

// recordReplay writes the §6.5 replay's own decision record. Its outcome is
// OutcomeReplayedAfterTTL rather than a second OutcomeAnswered: no human acted
// at this moment, and reading a replay as a fresh approval would count one
// consent twice (decision.OutcomeReplayedAfterTTL's own doc). Per that same
// invariant, the record itself carries NO actor — decision.Record.Validate
// rejects an actor on an outcome that HadActor() reports false for. The
// original responder is not asserted on this round's wire either (security
// review findings 1-3): rekeyReplayResponses strips it and substitutes
// io.gleipnir/replayed-from, and this record's ReplayOfRequestID names the
// SAME original request so an auditor can trace the replay back to the human
// decision it reused without either side claiming a human acted twice.
//
// newKind is the classification of the FRESH bundle the server just sent, not
// the prior one — recordReplay is only ever called once canReplay has already
// confirmed the two match, but the record should describe what was actually
// re-asked.
//
// There is no persisted tool_input_requests row backing this event — the
// whole point of a replay is that the host never asked anyone again — so
// RequestID is a fresh ID naming this settlement, distinct from the original
// request's own (already-recorded) decision.
func (h *InputRequiredHandler) recordReplay(ctx context.Context, runID, toolName string, prior *answeredQuestion, newKind model.ElicitationKind) {
	h.recordDecision(ctx, decision.Record{
		RunID:             runID,
		RequestID:         model.NewULID(),
		Kind:              newKind,
		ToolName:          toolName,
		ChannelEntryID:    audience.InAppEntryID,
		ChannelAssurance:  decision.AssuranceOf(mcp.ChannelAssuranceAuthenticated),
		LinkMethod:        decision.LinkNone,
		Outcome:           decision.OutcomeReplayedAfterTTL,
		ReplayOfRequestID: prior.requestID,
	})
}

// callToolWithInputRounds performs a tools/call and, for as long as the server
// answers with MRTR input_required, pauses the run to collect operator answers
// and re-issues the SAME call with those answers and the server's requestState
// attached. It returns the first result that is not input_required.
//
// The retry is the same call, not a new one: the tool_call step was written
// once, before dispatch, and the tool_result step is written once, after this
// returns. Everything between them is invisible to the agent.
//
// Answer replay (spec §6.5) lives in this loop. When a server discards its MRTR
// state while a human is thinking — a TTL expiring mid-wait — the answer that
// eventually arrives is spent on a retry the server no longer recognizes, and
// the server starts over by asking again. If it asks the IDENTICAL question,
// the stored answer is replayed against the new requestState without the
// operator ever seeing it. MRTR is stateless by design, so starting over is
// always available; making a human re-answer a question they already answered
// gains nothing and trains them to click through prompts.
//
// Only the MRTR shape is wired here. Spec §6 names a second shape — a durable
// task that enters input_required, answered with tasks/update — which is
// unreachable today because no agent path yet turns a tools/call into a task
// handle. It slots in at this loop: same Route call, a different way of
// delivering the answer.
func (a *BoundAgent) callToolWithInputRounds(ctx context.Context, runID string, entry resolvedToolEntry, toolName string, input map[string]any) (mcp.ToolResult, error) {
	opts := mcp.CallOptions{
		// entry.tool.Capabilities is data flowing inward from the ResolvedTool
		// the agent was constructed with — the agent never decides a capability
		// declaration itself. entry.tool.SchemaForHeaderParams() is the source
		// of SEP-2243 x-mcp-header annotations, canonical when available.
		Capabilities:      entry.tool.Capabilities,
		HeaderParamSchema: entry.tool.SchemaForHeaderParams(),
		// Attribution (issue #943) is set once, before the loop, and reused
		// across every MRTR round below — a fresh span-id is drawn inside
		// CallTool per round, since each round is its own HTTP request under
		// the same trace-id. Host-owned values only; input never flows here.
		Attribution: a.runAttribution(runID),
	}

	// The most recent answer, held for the replay path. Only the latest is
	// kept: replay recovers a hiccup on the question just answered, not an
	// arbitrary earlier one, and a growing history would be a growing supply
	// of answers a server could try to steer back onto a different question.
	var prior *answeredQuestion

	for round := 1; ; round++ {
		result, err := entry.tool.Client.CallTool(ctx, entry.tool.ToolName, input, opts)
		if err != nil {
			return mcp.ToolResult{}, err
		}
		if result.InputRequired == nil {
			return result, nil
		}
		if round >= maxInputRequiredRounds {
			return mcp.ToolResult{}, &InputCallAbandonedError{
				ToolName: toolName,
				Reason:   fmt.Sprintf("asked for operator input %d times on a single call", round),
			}
		}

		// Refuse before persisting or pausing, not after: a secret-collecting
		// form must never reach an operator's screen, and a request nobody
		// will ever be shown should not leave a pending row behind.
		if err := checkNoSecretFields(result.InputRequired.InputRequests); err != nil {
			return mcp.ToolResult{}, &InputCallAbandonedError{
				ToolName: toolName,
				Reason:   "requested a secret value in an elicitation form, which is never rendered",
				Err:      err,
			}
		}

		fingerprint := questionFingerprint(result.InputRequired.InputRequests)
		newKind := classifyElicitationKind(result.InputRequired.InputRequests)

		// §6.5 replay: the same question again, so the answer already in hand
		// still answers it. Spend it silently against the fresh requestState
		// -- but ONLY for an information ask (security review findings 1-3).
		//
		// A permission ask is a consent decision, and the host replaying one
		// on a human's behalf just because the bundle repeats byte-for-byte
		// is exactly the shape a forged approval takes: nothing distinguishes
		// "a legitimate MRTR retry" from "the server asking the same person
		// to approve the same action twice and being told the FIRST answer
		// still counts." Every permission re-ask reaches a human,
		// unconditionally, regardless of whether it matches. The kind must
		// also match on BOTH sides — prior.kind and newKind — so a server
		// cannot flip a bundle's classification via the elicitation-kind
		// _meta hint (content unchanged) and have that silently replayed
		// under the WRONG gate; canReplay requires both to be information.
		//
		// Once per question. A server that answers the replay with the same
		// question a THIRD time is not recovering from an expired state, it is
		// looping, and continuing to feed it an answer it demonstrably will not
		// accept just burns rounds — so the second identical re-ask falls
		// through to the human, who can see something is wrong.
		canReplay := prior.matches(fingerprint) && !prior.replayed &&
			prior.kind == elicitationKindInformation && newKind == elicitationKindInformation
		if canReplay {
			if replayed, ok := rekeyReplayResponses(result.InputRequired.InputRequests, prior.answers, prior.requestID); ok {
				logctx.Logger(ctx).InfoContext(ctx, "replaying operator answer after server re-asked the identical information question",
					"tool", toolName, "run_id", runID, "round", round, "original_request_id", prior.requestID)
				prior.replayed = true
				inputAnswerReplays.Inc()
				opts.InputResponses = replayed
				opts.RequestState = result.InputRequired.RequestState
				a.inputRequired.recordReplay(ctx, runID, toolName, prior, newKind)
				continue
			}
			// Defensive fallback (should be unreachable -- see
			// rekeyReplayResponses' own doc): fall through to an ordinary
			// human ask, which the "identical, not replayed" branch below
			// still recognizes as prior.matches(fingerprint).
			logctx.Logger(ctx).WarnContext(ctx, "replay eligible but the request/answer counts differ; asking a human instead",
				"tool", toolName, "run_id", runID, "round", round)
		}

		// Either a first ask, or the server re-asked and this round did NOT
		// silently replay: the content changed, the kind flipped, THIS
		// answer was already spent on one replay, or (a permission ask) it
		// is simply never eligible. `!prior.replayed` belongs only to
		// canReplay's "once per question" allowance above — it plays no part
		// in whether the operator gets to see the prior context, so it is
		// deliberately absent from both branches here.
		//
		// In EITHER re-ask case the operator gets the previous question and
		// answer alongside the new one:
		//   - identical content (a permission ask, or a kind flip under an
		//     unchanged fingerprint): the operator needs to know they
		//     answered this EXACT question moments ago, so a reflexive
		//     second approval is not the only signal they have to go on.
		//   - genuinely different content: the same context, for the
		//     ordinary reason a re-prompt should never look like an
		//     unexplained duplicate.
		var replay *ReplayContext
		if prior != nil {
			if prior.matches(fingerprint) {
				replay = newReplayContextWithReason(prior, reasonIdenticalReask)
			} else {
				inputAnswerReplayMismatches.Inc()
				replay = newReplayContext(prior)
			}
		}

		answers, respRequestID, err := a.inputRequired.Route(ctx, InputRoutingRequest{
			RunID:    runID,
			ServerID: entry.tool.ServerID,
			ToolName: toolName,
			Input:    input,
			Result:   result.InputRequired,
			Timeout:  resolveOperatorTimeout(ctx, a.policy.Capabilities.Feedback, 0),
			Replay:   replay,
		})
		if err != nil {
			// An exhausted budget fails the CALL and lets the run continue
			// (spec §6.2), unlike an unanswered pause, which fails the run.
			// Nobody was interrupted here — the host declined to interrupt
			// them — so there is no abandoned operator wait to account for.
			if errors.Is(err, ErrElicitationBudgetExhausted) {
				return mcp.ToolResult{}, &InputCallAbandonedError{
					ToolName: toolName,
					Reason:   "the run's elicitation budget is exhausted",
					Err:      err,
				}
			}
			return mcp.ToolResult{}, err
		}

		prior = &answeredQuestion{
			fingerprint: fingerprint,
			kind:        newKind,
			requestID:   respRequestID,
			requests:    result.InputRequired.InputRequests,
			answers:     answers,
		}
		opts.InputResponses = answers
		opts.RequestState = result.InputRequired.RequestState
	}
}

// replayedFromMetaKey names the ORIGINAL tool_input_requests row a replayed
// response's answer was spent on. It is the honest replacement for
// responderMetaKey on a replay: no human acted this round, so no identity is
// asserted (security review findings 1-3) — but a server or auditor reading
// the wire can still tell where the answer came from.
const replayedFromMetaKey = "io.gleipnir/replayed-from"

type replayedFromMetaValue struct {
	RequestID string `json:"request_id"`
}

// replayedFromMeta marshals the §6.5 replay provenance marker, or returns nil
// when originalRequestID is empty (defensive; Route always returns one on a
// successful answer).
func replayedFromMeta(originalRequestID string) json.RawMessage {
	if originalRequestID == "" {
		return nil
	}
	meta, err := json.Marshal(map[string]replayedFromMetaValue{
		replayedFromMetaKey: {RequestID: originalRequestID},
	})
	if err != nil {
		// replayedFromMetaValue is a fixed, JSON-safe struct; this cannot
		// fail in practice. Send nothing rather than a response the caller
		// cannot construct, matching responderMeta's own posture.
		return nil
	}
	return meta
}

// rekeyReplayResponses builds the retry's inputResponses from an answer
// already in hand, for the §6.5 replay path (information asks only, per
// canReplay above). Two things must NOT survive verbatim from the original
// answer:
//
//   - The ID. The wire correlates by id (ADR-061), and a server MAY
//     legitimately mint a fresh id for an identical re-ask; sending the OLD
//     id back would answer a question this round never asked. Re-keyed by
//     POSITION onto the NEW bundle's ids — the fingerprint match already
//     guarantees the two bundles have the same count and order.
//   - The responder _meta. No human acted this round — replaying is the host
//     spending an answer already in hand, not a human clicking again — so
//     asserting io.gleipnir/responder here would tell the server a person
//     decided something they did not (security review findings 1-3).
//     io.gleipnir/replayed-from names the ORIGINAL tool_input_requests row
//     instead, so a server or an auditor can still tell where the answer
//     came from without a false identity claim.
func rekeyReplayResponses(newRequests []mcp.InputRequest, priorAnswers []mcp.InputResponse, originalRequestID string) ([]mcp.InputResponse, bool) {
	if len(newRequests) != len(priorAnswers) {
		// Defensive: the fingerprint match that gates a replay already
		// length-prefixes the request count (questionFingerprint), so this
		// should be structurally impossible -- but a replay is not something
		// to risk on an invariant holding by construction alone. The caller
		// falls back to asking a human instead of guessing at a mapping.
		return nil, false
	}
	meta := replayedFromMeta(originalRequestID)
	out := make([]mcp.InputResponse, len(priorAnswers))
	for i, ans := range priorAnswers {
		out[i] = mcp.InputResponse{
			ID:      newRequests[i].ID,
			Action:  ans.Action,
			Content: ans.Content,
			Meta:    meta,
		}
	}
	return out, true
}

// toolResultError writes a tool_result error step and returns it in the
// (output, isError, err) shape handleToolCall hands back to the API loop — the
// "structural problem the agent can see and route around" rendering.
func (a *BoundAgent) toolResultError(ctx context.Context, runID, toolName, msg string) (string, bool, error) {
	if err := a.audit.Write(ctx, Step{
		RunID: runID,
		Type:  model.StepTypeToolResult,
		Content: map[string]any{
			"tool_name": toolName,
			"output":    msg,
			"is_error":  true,
		},
	}); err != nil {
		return "", false, fmt.Errorf("writing tool_result error step: %w", err)
	}
	return msg, true, nil
}

// maxPersistedRequestStateBytes and maxPersistedPayloadBytes bound what one
// pause may write to tool_input_requests. internal/mcp already enforces the
// spec §6.2 caps at decode time, so a result arriving through the MCP client
// is bounded before it gets here — these are the persistence-layer backstop
// the same section asks for, covering any future producer of an
// InputRequiredResult that does not come through that decode path.
//
// They are deliberately looser than the decode-time caps: this layer is
// guarding the database against an unbounded write, not re-litigating what a
// reasonable elicitation looks like.
const (
	maxPersistedRequestStateBytes = 64 << 10  // 64 KiB
	maxPersistedPayloadBytes      = 256 << 10 // 256 KiB
)

// checkPersistedSize rejects a pause whose durable record would be
// unreasonably large, before any row is written.
func checkPersistedSize(result *mcp.InputRequiredResult, requestID string) error {
	if n := len(result.RequestState); n > maxPersistedRequestStateBytes {
		return &InputRoutingError{
			RequestID: requestID,
			Err:       fmt.Errorf("requestState is %d bytes, exceeds the %d-byte persistence limit", n, maxPersistedRequestStateBytes),
		}
	}

	total := 0
	for _, r := range result.InputRequests {
		total += len(r.Message) + len(r.RequestedSchema) + len(r.ElicitationKind)
	}
	if total > maxPersistedPayloadBytes {
		return &InputRoutingError{
			RequestID: requestID,
			Err:       fmt.Errorf("elicitation payload is %d bytes, exceeds the %d-byte persistence limit", total, maxPersistedPayloadBytes),
		}
	}
	return nil
}

// resolveRecord marks the tool_input_requests row resolved and returns the
// rows affected, so the caller can tell a genuine CAS loss (rows==0: the
// timeout scanner got there first) from an infrastructure hiccup (marshal or
// DB error, treated as best-effort success — the answer is already in hand
// and the run must resume regardless of a DB hiccup). Route uses rows==0 to
// refuse sending a retry for an answer that was never genuinely applied
// (security review finding 6).
func (h *InputRequiredHandler) resolveRecord(ctx context.Context, runID, requestID string, answers []mcp.InputResponse) int64 {
	encoded, err := json.Marshal(answers)
	if err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "tool input: marshaling operator answers failed",
			"request_id", requestID, "run_id", runID, "err", err)
		return 1
	}
	response := string(encoded)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := h.sm.Queries().ResolveToolInputRequest(ctx, db.ResolveToolInputRequestParams{
		Response:   &response,
		ResolvedAt: &now,
		ID:         requestID,
	})
	if err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "tool input: ResolveToolInputRequest failed",
			"request_id", requestID, "run_id", runID, "err", err)
		return 1
	}
	if rows == 0 {
		logctx.Logger(ctx).WarnContext(ctx, "tool input already resolved by scanner",
			"request_id", requestID, "run_id", runID)
	}
	return rows
}

// decodeInputResponses parses an operator answer payload into per-request
// responses. The payload is a JSON array correlated to the pause's
// InputRequests by position, so a length mismatch is an error rather than
// something to pad or truncate — answering the wrong question is worse than
// answering none.
func decodeInputResponses(body string, expected int) ([]mcp.InputResponse, error) {
	var wire []struct {
		Action  string          `json:"action"`
		Content json.RawMessage `json:"content,omitempty"`
	}
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		return nil, fmt.Errorf("tool input response does not parse as a JSON array of {action, content}: %w", err)
	}
	if len(wire) != expected {
		return nil, fmt.Errorf("tool input response has %d entries, expected %d", len(wire), expected)
	}

	responses := make([]mcp.InputResponse, len(wire))
	for i, w := range wire {
		switch w.Action {
		case inputActionAccept:
			if len(w.Content) == 0 {
				return nil, fmt.Errorf("tool input response %d: action %q requires content", i, w.Action)
			}
		case inputActionDecline, inputActionCancel:
			if len(w.Content) > 0 {
				return nil, fmt.Errorf("tool input response %d: action %q must not carry content", i, w.Action)
			}
		default:
			return nil, fmt.Errorf("tool input response %d: unknown action %q (want %q, %q, or %q)",
				i, w.Action, inputActionAccept, inputActionDecline, inputActionCancel)
		}
		responses[i] = mcp.InputResponse{Action: w.Action, Content: w.Content}
	}
	return responses, nil
}

// classifyElicitationKind maps one input_required result onto the
// tool_input_requests.elicitation_kind vocabulary (spec §6.1). An explicit
// _meta io.gleipnir/elicitation-kind wins when it names a known kind for a
// given entry; otherwise the §6.1 convention decides — a requestedSchema
// asking for no fields is a consent-only ask, anything else needs a form.
//
// When one result bundles several requests, PERMISSION WINS: if any entry is
// permission-shaped (an explicit permission hint, or no valid hint and no
// fields), the WHOLE bundle is classified permission, requiring the approver
// role (security review finding 4). The earlier rule let "information win" —
// bundling one permission-shaped entry alongside one harmless information
// entry classified the whole ask as information, letting an operator (not an
// approver) consent to something that needed the stronger gate. Consent is
// the higher-privilege operation here, so ANY consent-shaped entry in a
// bundle must raise the WHOLE bundle to the role that may grant consent.
//
// A malformed _meta hint — a kind outside the vocabulary — falls through to
// the schema-shape convention for that entry rather than being honored or
// rejected: it is an optional hint from a server that got it wrong, and the
// schema shape is still readable.
//
// Manifest-declared per-tool kinds are a third source the manifest v2 work
// adds; the two here are what a server can express today.
func classifyElicitationKind(requests []mcp.InputRequest) model.ElicitationKind {
	for _, r := range requests {
		if entryIsPermission(r) {
			return elicitationKindPermission
		}
	}
	return elicitationKindInformation
}

// entryIsPermission reports whether one inputRequests entry is, by itself, a
// consent-only ask: an explicit permission hint, or no valid hint and a
// requestedSchema with no fields.
func entryIsPermission(r mcp.InputRequest) bool {
	declared := model.ElicitationKind(r.ElicitationKind)
	if declared.Valid() {
		return declared == elicitationKindPermission
	}
	return !requestsFields(r.RequestedSchema)
}

// secretSchemaKeys are the schema markers that say a field carries a secret.
// "x-gleipnir-secret" is this codebase's own marker (the same one plugin
// config schemas use); "password" and writeOnly are the JSON Schema / OpenAPI
// idioms a third-party server is most likely to reach for.
var secretSchemaKeys = []string{`"x-gleipnir-secret"`, `"format":"password"`, `"writeOnly":true`}

// ErrSecretElicitation reports that a server asked the operator to type a
// secret into a form. Spec §6.1 is explicit that form mode never carries
// secrets — URL mode exists for that, and does not exist yet — so the only
// honest handling is to refuse the elicitation rather than render the form.
//
// This is deliberately a refusal and not a redaction: a form that silently
// dropped the secret field would leave the operator answering a question they
// cannot see, and the server waiting on a value it will never get.
var ErrSecretElicitation = errors.New("elicitation requests a secret in form mode, which is not supported")

// checkNoSecretFields enforces the §6.1 no-secrets-in-form rule. The check is
// textual against the normalized schema rather than a structural walk: the
// markers may appear at any depth, under any nesting a server invents, and
// missing one because it sat inside an unexpected keyword would defeat the
// point. A false positive here costs a refused elicitation, which is the side
// to err on.
func checkNoSecretFields(requests []mcp.InputRequest) error {
	for _, r := range requests {
		if len(r.RequestedSchema) == 0 {
			continue
		}
		compact := removeJSONWhitespace(string(r.RequestedSchema))
		for _, marker := range secretSchemaKeys {
			if strings.Contains(compact, marker) {
				return fmt.Errorf("%w (marker %s)", ErrSecretElicitation, marker)
			}
		}
	}
	return nil
}

// removeJSONWhitespace strips whitespace that sits OUTSIDE string literals, so
// `"format" : "password"` matches the same marker as `"format":"password"`.
// Whitespace inside a string is preserved — collapsing it could make an
// innocent description read as a marker.
func removeJSONWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString, escaped := false, false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inString:
			escaped = true
		case r == '"':
			inString = !inString
		case !inString && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// requestsFields reports whether a requestedSchema asks the operator for any
// field at all. An unparseable schema counts as asking for fields: rendering an
// approve/reject button for a request whose real shape could not be read would
// misrepresent what the operator is consenting to.
func requestsFields(schema json.RawMessage) bool {
	if len(schema) == 0 {
		return false
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return true
	}
	return len(s.Properties) > 0
}
