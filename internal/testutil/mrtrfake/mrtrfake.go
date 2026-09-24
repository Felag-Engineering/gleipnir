// Package mrtrfake is a reusable, Relay-shaped fake MCP server for
// end-to-end tests of Gleipnir's MRTR client (ADR-055, ADR-061). It speaks
// go-sdk v1.7.0's map-keyed `input_required`/`inputResponses` wire shape over
// plain HTTP, the same shape Felag-Engineering/gleipnir-relay#646 implements.
//
// This is a non-test, stdlib-only package (no "testing" import, no
// internal/* import) so it can be imported from any test binary — including
// a future cross-repository conformance suite (#931) — without pulling in
// Gleipnir's own test-only dependency graph. Every JSON-RPC shape below is
// therefore hand-rolled rather than reused from internal/mcp; keep the two in
// sync by hand if the wire contract changes.
//
// Usage:
//
//	fake := mrtrfake.New()
//	srv := httptest.NewServer(fake)
//	defer srv.Close()
//	// register srv.URL as an MCP server, grant its "run_operation" tool,
//	// trigger a run, then read fake.Notify() for each tools/call received.
//
// A fake pinned legacy (mrtrfake.New(mrtrfake.WithLegacyPin())) never offers
// MRTR at all: server/discover answers the pre-2026 opaque-404 shape, and
// tools/call always completes immediately with Relay's universal
// "pending_approval" text — the shape a client that never declared the
// `elicitation` capability is left with.
package mrtrfake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

// ToolCall records one tools/call this fake received, for test assertions.
// IsRetry distinguishes the original call (no requestState) from the MRTR
// retry that answers a prior input_required (requestState + inputResponses
// both present).
type ToolCall struct {
	IsRetry bool

	// Meta is the request's raw `_meta` object, or nil if absent -- absent is
	// itself a fact worth asserting on (a legacy-pinned client sends none).
	Meta json.RawMessage

	Arguments map[string]any

	// RequestState is the opaque blob this call carried back, verbatim, on a
	// retry. Empty on the original call.
	RequestState json.RawMessage

	// InputResponses is the retry's answer map, keyed by request id, decoded
	// only as far as {action, content, _meta} -- exactly what a real server
	// would see. nil on the original call.
	InputResponses map[string]InputResponseWire

	// IssuedRequestState is what THIS call's own response handed back as
	// requestState, when it paused on input_required -- nil when the call
	// completed instead. It is what a test compares the NEXT call's
	// RequestState against, to prove the retry echoed the fake's own blob
	// back byte for byte rather than a stale or fabricated one.
	IssuedRequestState json.RawMessage
}

// InputResponseWire is the wire shape of one inputResponses entry, as a real
// MRTR server receives it.
type InputResponseWire struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
	Meta    json.RawMessage `json:"_meta,omitempty"`
}

// ResponderMeta decodes InputResponseWire.Meta's `io.gleipnir/responder`
// entry, or returns the zero value when absent or unparseable -- the
// zero value IS the signal on a cancel, a timeout (no retry at all), or a
// replay (which asserts io.gleipnir/replayed-from instead).
type ResponderMeta struct {
	Username string `json:"username"`
	UserID   string `json:"user_id"`
	Gate     string `json:"gate"`
}

// Responder extracts r's `io.gleipnir/responder` assertion.
func (r InputResponseWire) Responder() ResponderMeta {
	if len(r.Meta) == 0 {
		return ResponderMeta{}
	}
	var wrapper map[string]ResponderMeta
	if err := json.Unmarshal(r.Meta, &wrapper); err != nil {
		return ResponderMeta{}
	}
	return wrapper["io.gleipnir/responder"]
}

// Option configures a Server.
type Option func(*Server)

// WithLegacyPin makes the fake answer server/discover the way a pre-2026
// server does, so Gleipnir's discovery probe pins it legacy: no `_meta`, no
// retry fields, and tools/call always completes immediately with the
// "pending_approval" text a client that never declared `elicitation` is left
// with (relay-646 §1).
func WithLegacyPin() Option {
	return func(s *Server) { s.legacyPin = true }
}

// WithToolName sets the tool this fake serves from tools/list and answers on
// tools/call. Default: "run_operation".
func WithToolName(name string) Option {
	return func(s *Server) { s.toolName = name }
}

// WithMessage sets the untrusted, Relay-authored elicitation message sent on
// the first input_required result. Default: a fixed placeholder string.
func WithMessage(message string) Option {
	return func(s *Server) { s.message = message }
}

// WithReAskOnce makes the fake re-issue the identical input_required ONE
// time after a valid retry answers it, instead of completing -- modelling a
// server whose MRTR state expired while a human was answering (spec §6.5).
// The re-ask is byte-for-byte the same message and schema as the original,
// so combined with the fake's default permission-shaped ask this is what
// lets an end-to-end test prove a permission ask is never silently replayed
// even through the full HTTP/launcher stack: the identical re-ask must
// still reach a second human, not the host's stored answer. The call after
// THAT retry completes normally.
func WithReAskOnce() Option {
	return func(s *Server) { s.reAskOnce = true }
}

// pendingCall is what the fake remembers between the original tools/call and
// its retry, so it can check the retry echoes both back unchanged.
type pendingCall struct {
	requestState string
	arguments    string // json.Marshal of the original arguments, for equality
}

// Server is a Relay-shaped fake MCP server. The zero value is not usable;
// construct with New.
type Server struct {
	legacyPin bool
	toolName  string
	message   string
	reAskOnce bool

	seq int64 // atomic: requestState/id generator

	mu      sync.Mutex
	calls   []ToolCall
	pending map[string]pendingCall // keyed by requestState
	reAsked bool                   // WithReAskOnce's one-shot latch

	notify chan ToolCall
}

// New returns a ready Server. Wrap it in httptest.NewServer to use it as an
// MCP server target.
func New(opts ...Option) *Server {
	s := &Server{
		toolName: "run_operation",
		message:  "Approve request apr_01J...: service.restart(unit=\"api-gateway\") [mutate] on 3 node(s)",
		pending:  make(map[string]pendingCall),
		// Buffered generously: a test that never drains Notify() must not
		// block the HTTP handler goroutine mid-response.
		notify: make(chan ToolCall, 64),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Calls returns a copy of every tools/call this fake has answered so far, in
// arrival order.
func (s *Server) Calls() []ToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ToolCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// Notify returns the channel one ToolCall is sent on per tools/call this fake
// answers, in arrival order -- the signal-don't-poll synchronization point
// for a test waiting on "did the retry arrive yet". Buffered at 64; a test
// driving more calls than that without reading Notify() will block the fake.
func (s *Server) Notify() <-chan ToolCall {
	return s.notify
}

// jsonrpcRequest is the minimal envelope this fake reads.
type jsonrpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req jsonrpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	switch req.Method {
	case "server/discover":
		s.handleDiscover(w, req)
	case "initialize":
		s.handleInitialize(w, req)
	case "notifications/initialized":
		w.WriteHeader(http.StatusOK)
	case "tools/list":
		s.handleToolsList(w, req)
	case "tools/call":
		s.handleToolsCall(w, req)
	default:
		writeError(w, req.ID, -32601, "method not found")
	}
}

// handleDiscover answers the 2026-07-28 discovery probe. A legacy-pinned
// fake answers the pre-2026 opaque-404 shape instead -- not a JSON-RPC
// envelope at all, which is what tells Gleipnir's classifier "legacy"
// (internal/mcp's classifyDiscoverResponse, kept in sync by hand here).
func (s *Server) handleDiscover(w http.ResponseWriter, req jsonrpcRequest) {
	if s.legacyPin {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "not found") //nolint:errcheck
		return
	}
	writeResult(w, req.ID, map[string]any{
		"resultType":        "complete",
		"supportedVersions": []string{"2026-07-28"},
	})
}

// handleInitialize is the legacy handshake, reachable only when this fake is
// pinned legacy (a modern-pinned client never calls it).
func (s *Server) handleInitialize(w http.ResponseWriter, req jsonrpcRequest) {
	w.Header().Set("Mcp-Session-Id", "mrtrfake-session")
	writeResult(w, req.ID, map[string]any{"protocolVersion": "2024-11-05"})
}

func (s *Server) handleToolsList(w http.ResponseWriter, req jsonrpcRequest) {
	writeResult(w, req.ID, map[string]any{
		"tools": []map[string]any{{
			"name":        s.toolName,
			"description": "runs a fleet Operation",
			"inputSchema": map[string]any{"type": "object"},
		}},
	})
}

// toolsCallBody is the subset of a tools/call request this fake reads.
type toolsCallBody struct {
	Arguments      map[string]any               `json:"arguments"`
	Meta           json.RawMessage              `json:"_meta,omitempty"`
	RequestState   json.RawMessage              `json:"requestState,omitempty"`
	InputResponses map[string]InputResponseWire `json:"inputResponses,omitempty"`
}

// handleToolsCall implements relay-646 §1-4: a legacy-pinned fake (or a
// modern one that somehow received no elicitation capability) always
// completes immediately with the universal pending_approval text; a
// modern-pinned fake parks on the first call and requires the retry to echo
// requestState and arguments back, carrying a responder assertion.
func (s *Server) handleToolsCall(w http.ResponseWriter, req jsonrpcRequest) {
	var body toolsCallBody
	if err := json.Unmarshal(req.Params, &body); err != nil {
		writeError(w, req.ID, -32602, "invalid params")
		return
	}

	isRetry := len(body.RequestState) > 0 && len(body.InputResponses) > 0
	call := ToolCall{
		IsRetry:        isRetry,
		Meta:           body.Meta,
		Arguments:      body.Arguments,
		RequestState:   body.RequestState,
		InputResponses: body.InputResponses,
	}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()
	s.notify <- call

	if !isRetry {
		s.handleOriginalCall(w, req, body)
		return
	}
	s.handleRetryCall(w, req, body)
}

// declaresElicitation reports whether the request's _meta advertises the
// `elicitation` client capability -- the signal relay-646 §1 keys its
// pending_approval fallback on. A legacy-pinned fake never even reaches this
// check: requestMeta on the real client returns nil for a legacy pin, so
// Meta is always empty there regardless.
func declaresElicitation(meta json.RawMessage) bool {
	if len(meta) == 0 {
		return false
	}
	var m struct {
		Capabilities struct {
			Elicitation json.RawMessage `json:"elicitation"`
		} `json:"io.modelcontextprotocol/clientCapabilities"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		return false
	}
	return m.Capabilities.Elicitation != nil
}

func (s *Server) handleOriginalCall(w http.ResponseWriter, req jsonrpcRequest, body toolsCallBody) {
	if s.legacyPin || !declaresElicitation(body.Meta) {
		writeResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "pending_approval: apr_01J..."}},
			"isError": false,
		})
		return
	}

	argsJSON, _ := json.Marshal(body.Arguments) //nolint:errcheck
	s.park(w, req, argsJSON)
}

// park writes an input_required result asking the fake's standard question,
// seals a fresh requestState against argsJSON so a matching retry can be
// verified, and stamps it onto the call this response answers (the LAST
// entry in s.calls) as IssuedRequestState -- what a test compares the next
// call's own RequestState against, to prove the retry echoed it back
// unchanged rather than a stale or fabricated blob.
func (s *Server) park(w http.ResponseWriter, req jsonrpcRequest, argsJSON []byte) {
	requestState := fmt.Sprintf("v1.sealed.%d", atomic.AddInt64(&s.seq, 1))
	requestStateJSON, _ := json.Marshal(requestState) //nolint:errcheck

	s.mu.Lock()
	s.pending[requestState] = pendingCall{requestState: requestState, arguments: string(argsJSON)}
	if n := len(s.calls); n > 0 {
		s.calls[n-1].IssuedRequestState = requestStateJSON
	}
	s.mu.Unlock()

	writeResult(w, req.ID, map[string]any{
		"resultType":   "input_required",
		"content":      nil,
		"requestState": requestState,
		"inputRequests": map[string]any{
			"approval": map[string]any{
				"method": "elicitation/create",
				"params": map[string]any{
					"mode":            "form",
					"message":         s.message,
					"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				},
			},
		},
	})
}

func (s *Server) handleRetryCall(w http.ResponseWriter, req jsonrpcRequest, body toolsCallBody) {
	var requestState string
	if err := json.Unmarshal(body.RequestState, &requestState); err != nil {
		writeError(w, req.ID, -32602, "malformed requestState")
		return
	}

	s.mu.Lock()
	pc, ok := s.pending[requestState]
	if ok {
		delete(s.pending, requestState) // one-shot, like a real sealed token
	}
	s.mu.Unlock()

	if !ok {
		writeError(w, req.ID, -32602, "unknown or already-consumed requestState")
		return
	}
	argsJSON, _ := json.Marshal(body.Arguments) //nolint:errcheck
	if string(argsJSON) != pc.arguments {
		// Dumb-fake discipline: report the mismatch as a refusal rather than
		// panicking, matching a real server's posture toward a caller that
		// changed its own arguments mid-retry.
		writeToolResult(w, req.ID, "arguments changed since the original call", true)
		return
	}

	entry, ok := body.InputResponses["approval"]
	if !ok {
		writeToolResult(w, req.ID, "missing the \"approval\" response", true)
		return
	}
	if entry.Responder().Username == "" {
		// relay-646 §5: a response with no asserted responder is
		// machine-originated (cancel, or a stale replay) and must not be
		// allowed to decide anything.
		writeToolResult(w, req.ID, "refused: no authenticated responder asserted", true)
		return
	}

	// WithReAskOnce: this valid answer arrived, but the fake's MRTR state has
	// "expired" once -- re-ask the identical question instead of completing.
	// A permission ask must reach a SECOND human for this, not have the
	// stored answer replayed onto it (security review findings 1-3); an
	// end-to-end test proves that by requiring a second POST here.
	if s.reAskOnce {
		s.mu.Lock()
		alreadyReAsked := s.reAsked
		s.reAsked = true
		s.mu.Unlock()
		if !alreadyReAsked {
			s.park(w, req, argsJSON)
			return
		}
	}

	if entry.Action == "accept" {
		writeToolResult(w, req.ID, "service.restart(unit=\"api-gateway\") completed on 3 node(s): gw-1, gw-2, gw-3", false)
		return
	}
	// decline: a legitimate answer, handed back as Relay's own denial result
	// -- NOT an error (relay-646 §4: "The model's tool_result is Relay's
	// denial. The run completes, not failed.").
	writeToolResult(w, req.ID, fmt.Sprintf("denied by %s", entry.Responder().Username), false)
}

func writeToolResult(w http.ResponseWriter, id json.RawMessage, text string, isError bool) {
	writeResult(w, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	})
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	})
}

func writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error":   map[string]any{"code": code, "message": message},
	})
}
