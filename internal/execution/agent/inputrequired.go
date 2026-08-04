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
// §6.1). The column has a CHECK constraint on exactly these two values.
const (
	elicitationKindPermission  = "permission"
	elicitationKindInformation = "information"
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

// InputRoundLimitError reports that one tools/call exceeded
// maxInputRequiredRounds pauses. Unlike InputRoutingError this is not fatal to
// the run: the caller renders it as a tool_result error so the agent can try
// something else.
type InputRoundLimitError struct {
	ToolName string
	Rounds   int
}

func (e *InputRoundLimitError) Error() string {
	return fmt.Sprintf("tool %s asked for operator input %d times on a single call; the call was abandoned",
		e.ToolName, e.Rounds)
}

// ElicitationBudget is the per-run elicitation budget check (spec §6.2 cap 1).
// The handler calls Check before routing each pause so the enforcement point
// already exists at its final call site; the budget itself — policy-configurable,
// fail-closed, a sibling of max_steps — lands with the abuse-controls work.
// A nil budget means no accounting, which is today's behavior.
type ElicitationBudget interface {
	// Check reports whether runID may raise requests more elicitations. A
	// non-nil error abandons the pause and fails the call.
	Check(ctx context.Context, runID string, requests int) error
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
	// policy. Zero or negative falls back to fallbackInputTimeout. Full
	// precedence against server-side TTLs (spec §6.3) is a later milestone;
	// today the policy clock is the only clock consulted.
	Timeout time.Duration
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

	if h.budget != nil {
		if err := h.budget.Check(ctx, req.RunID, len(req.Result.InputRequests)); err != nil {
			return nil, &InputRoutingError{RequestID: requestID, Err: fmt.Errorf("elicitation budget: %w", err)}
		}
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = h.defaultTimeout
	}
	if timeout <= 0 {
		timeout = fallbackInputTimeout
	}
	expiresAt := time.Now().UTC().Add(timeout).Format(time.RFC3339Nano)

	callArgs, err := json.Marshal(req.Input)
	if err != nil {
		return nil, fmt.Errorf("marshaling call args for %s: %w", req.ToolName, err)
	}
	requestPayload, err := json.Marshal(req.Result.InputRequests)
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
		ElicitationKind: classifyElicitationKind(req.Result.InputRequests),
		ExpiresAt:       expiresAt,
	})); err != nil {
		return nil, fmt.Errorf("transitioning run to waiting_for_feedback for tool input: %w", err)
	}

	timer := time.NewTimer(timeout)
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
			errorCode: model.ErrorCodeFeedbackTimeout,
			wonMessage: fmt.Sprintf("tool input timeout: operator did not answer %s within %s",
				req.ToolName, timeout),
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
			return mcp.ToolResult{}, &InputRoundLimitError{ToolName: toolName, Rounds: round}
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
// This is the persistence-side classification only. Which roles may resolve
// which kind, and manifest-declared per-tool kinds, are a separate concern.
func classifyElicitationKind(requests []mcp.InputRequest) string {
	kind := elicitationKindPermission
	for _, r := range requests {
		switch r.ElicitationKind {
		case elicitationKindInformation:
			return elicitationKindInformation
		case elicitationKindPermission:
			continue
		}
		if requestsFields(r.RequestedSchema) {
			kind = elicitationKindInformation
		}
	}
	return kind
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
