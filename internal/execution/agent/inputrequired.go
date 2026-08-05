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
)

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
			Message:         r.Message,
			RequestedSchema: r.RequestedSchema,
			ElicitationKind: r.ElicitationKind,
		}
	}
	return out
}

// inputWaiter is one registered wait. expected is the number of InputRequests
// the pause is asking about, held here so Resolve can reject a mis-sized answer
// while the run is still safely paused rather than after it has resumed.
type inputWaiter struct {
	ch       chan []mcp.InputResponse
	expected int
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
func (h *InputRequiredHandler) registerWaiter(requestID string, expected int) <-chan []mcp.InputResponse {
	w := &inputWaiter{ch: make(chan []mcp.InputResponse, 1), expected: expected}
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
// InputRequest in the pause, correlated by position (MRTR carries no per-request
// id).
//
// Validation happens here rather than on the waiting side on purpose: a
// malformed answer is the caller's problem to fix and leaves the run paused and
// answerable, whereas the same check made after the handoff would fail a run
// over a bad payload the operator could simply have resubmitted.
func (h *InputRequiredHandler) Resolve(requestID, body string) error {
	h.mu.Lock()
	w, ok := h.waiters[requestID]
	h.mu.Unlock()
	if !ok {
		return ErrUnknownInputRequestID
	}

	responses, err := decodeInputResponses(body, w.expected)
	if err != nil {
		return err
	}
	return h.deliver(requestID, responses)
}

// Decline resolves the pause by declining every request in it — the deny path.
// The declines are still handed back to the server: MRTR treats a refusal as a
// legitimate answer, and the server decides whether that means an error result,
// a partial result, or a different question.
func (h *InputRequiredHandler) Decline(requestID string) error {
	h.mu.Lock()
	w, ok := h.waiters[requestID]
	h.mu.Unlock()
	if !ok {
		return ErrUnknownInputRequestID
	}

	responses := make([]mcp.InputResponse, w.expected)
	for i := range responses {
		responses[i] = mcp.InputResponse{Action: inputActionDecline}
	}
	return h.deliver(requestID, responses)
}

// deliver hands validated responses to the registered waiter and drops the
// registration so a second answer cannot arrive. The send is outside the lock:
// the channel is buffered (cap 1) with a single reader, so it never blocks, and
// holding the lock across it would serialize every Resolve call.
func (h *InputRequiredHandler) deliver(requestID string, responses []mcp.InputResponse) error {
	h.mu.Lock()
	w, ok := h.waiters[requestID]
	if ok {
		delete(h.waiters, requestID)
	}
	h.mu.Unlock()
	if !ok {
		return ErrUnknownInputRequestID
	}
	w.ch <- responses
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
func (h *InputRequiredHandler) Route(ctx context.Context, req InputRoutingRequest) ([]mcp.InputResponse, error) {
	requestID := model.NewULID()

	// The budget refusal is returned unwrapped, NOT inside an
	// InputRoutingError: nothing was routed, so it is not a routing failure.
	// The distinction is what lets the caller fail the call and keep the run
	// alive instead of treating it like an unanswered operator wait.
	if h.budget != nil {
		if err := h.budget.Check(ctx, req.RunID, len(req.Result.InputRequests)); err != nil {
			return nil, err
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
		return nil, err
	}

	callArgs, err := json.Marshal(req.Input)
	if err != nil {
		return nil, fmt.Errorf("marshaling call args for %s: %w", req.ToolName, err)
	}
	requestPayload, err := json.Marshal(toPersistedRequests(req.Result.InputRequests))
	if err != nil {
		return nil, fmt.Errorf("marshaling input requests for %s: %w", req.ToolName, err)
	}

	// Register before the transition; release on every exit path.
	responses := h.registerWaiter(requestID, len(req.Result.InputRequests))
	defer h.unregisterWaiter(requestID)

	if err := h.sm.Transition(ctx, model.RunStatusWaitingForFeedback, "", WithToolInputPayload(ToolInputPayload{
		RequestID:       requestID,
		ServerID:        req.ServerID,
		ToolName:        req.ToolName,
		CallArgs:        string(callArgs),
		RequestState:    string(req.Result.RequestState),
		RequestPayload:  string(requestPayload),
		ElicitationKind: string(classifyElicitationKind(req.Result.InputRequests)),
		ExpiresAt:       expiresAt,
		DeadlineSource:  string(source),
	})); err != nil {
		return nil, fmt.Errorf("transitioning run to waiting_for_feedback for tool input: %w", err)
	}

	timer := time.NewTimer(waitFor)
	defer timer.Stop()

	select {
	case answers := <-responses:
		h.resolveRecord(ctx, req.RunID, requestID, answers)
		if err := h.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
			return nil, fmt.Errorf("transitioning run back to running after tool input: %w", err)
		}
		return answers, nil

	case <-timer.C:
		logctx.Logger(ctx).WarnContext(ctx, "tool input request timed out",
			"tool", req.ToolName, "request_id", requestID, "timeout", timeout.String())
		// Race the timeout scanner for the pending row, exactly as the approval
		// and feedback waits do: the conditional UPDATE arbitrates, and only the
		// winner writes the error step.
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
		})
		return nil, &InputRoutingError{RequestID: requestID, Err: err}

	case <-ctx.Done():
		return nil, &InputRoutingError{RequestID: requestID, Err: fmt.Errorf("context cancelled waiting for tool input: %w", ctx.Err())}
	}
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
	}

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

		answers, err := a.inputRequired.Route(ctx, InputRoutingRequest{
			RunID:    runID,
			ServerID: entry.tool.ServerID,
			ToolName: toolName,
			Input:    input,
			Result:   result.InputRequired,
			Timeout:  resolveOperatorTimeout(ctx, a.policy.Capabilities.Feedback, 0),
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

		opts.InputResponses = answers
		opts.RequestState = result.InputRequired.RequestState
	}
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

// resolveRecord marks the tool_input_requests row resolved. Best-effort: the
// answer is already in hand and the run must resume regardless of a DB hiccup,
// and rows == 0 simply means the timeout scanner got there first — which the
// caller will discover on its next transition, not here.
func (h *InputRequiredHandler) resolveRecord(ctx context.Context, runID, requestID string, answers []mcp.InputResponse) {
	encoded, err := json.Marshal(answers)
	if err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "tool input: marshaling operator answers failed",
			"request_id", requestID, "run_id", runID, "err", err)
		return
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
		return
	}
	if rows == 0 {
		logctx.Logger(ctx).DebugContext(ctx, "tool input already resolved by scanner",
			"request_id", requestID, "run_id", runID)
	}
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
// _meta io.gleipnir/elicitation-kind wins when it names a known kind;
// otherwise the §6.1 convention decides — a requestedSchema asking for no
// fields is a consent-only ask, anything else needs a form. When one result
// bundles several requests, "information" wins: a single field anywhere means
// the operator must be shown a form rather than an approve/reject pair.
//
// A malformed _meta hint — a kind outside the vocabulary — falls through to
// the convention rather than being honored or rejected: it is an optional hint
// from a server that got it wrong, and the schema shape is still readable.
//
// Manifest-declared per-tool kinds are a third source the manifest v2 work
// adds; the two here are what a server can express today.
func classifyElicitationKind(requests []mcp.InputRequest) model.ElicitationKind {
	kind := elicitationKindPermission
	for _, r := range requests {
		declared := model.ElicitationKind(r.ElicitationKind)
		if declared.Valid() {
			if declared == elicitationKindInformation {
				return elicitationKindInformation
			}
			continue
		}
		if requestsFields(r.RequestedSchema) {
			kind = elicitationKindInformation
		}
	}
	return kind
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
