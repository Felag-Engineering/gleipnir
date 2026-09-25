package hostclienttest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
)

// ServerName and Version are the fake's server/discover identity. They match
// internal/plugin/hostendpoint's ServerName and Version constants exactly —
// duplicated, not imported (doc.go) — so a test asserting the discovered
// identity is exercising the real value, not a fake-only stand-in. A future
// change to the real constants that is not mirrored here is exactly the kind
// of drift the root-module contract test (internal/plugin/hostendpoint's
// TestHostClientTest_MatchesRealServer) exists to catch.
const (
	ServerName = "gleipnir-host-endpoint"
	Version    = "0.1.0"
)

// defaultToken is the bearer token New() issues when no WithToken option is
// given. This fake models exactly one plugin instance, so one fixed token is
// all a test needs — the real endpoint's per-generation token model (§3.1)
// has no equivalent here.
const defaultToken = "fake-instance-token"

// Server is a stdlib http.Handler that faithfully mirrors the wire contract
// of internal/plugin/hostendpoint.Server (docs/developer/host-endpoint-contract.md
// §6): the same JSON-RPC method set, the same §2.1 header/_meta validation,
// the same §2.2 error-vs-isError split, and the same bearer auth (§3). Build
// one with New (a bare handler) or NewServer (also wraps it in an
// httptest.Server and wires the environment hostclient.New reads).
//
// Server holds no per-instance identity concept beyond a single fixed token:
// a plugin author testing their own tool/channel code has exactly one
// instance to be, unlike the real endpoint's per-generation multi-tenant
// model.
type Server struct {
	mu sync.Mutex

	token string

	instanceConfigJSON string
	credentialsJSON    string
	runContexts        map[string]RunContext
	userConfigs        map[string]string
	runHistory         []RunSummary
	userDirectory      []UserEntry
	healthState        map[string]string // "profile\x00capability" -> last reported state

	authorizedActors map[string]string // actor_external_id -> user_id
	authorizePolicy  ActorAuthorizer
	binder           IdentityBinder

	calls   []Call
	logs    []LogEntry
	metrics []MetricEntry
	health  []HealthEntry

	waiters map[string][]chan struct{}

	handlers map[string]toolHandler

	// httpServer is set by NewServer only; nil for a bare Server built with
	// New. Backs the URL method.
	httpServer *httptest.Server
}

// ActorAuthorizer decides whether actorExternalID may settle the pending ask
// named by requestID, mirroring host/authorize_actor's role-gate decision.
// The default policy (no WithAuthorizePolicy option) checks the set
// configured via WithAuthorizedActor.
type ActorAuthorizer func(requestID, actorExternalID string) (authorized bool, userID string)

// IdentityBinder decides the outcome of a host/submit_identity_proof call.
// The default (no WithIdentityBinder option) rejects every proof with
// ReasonNoPendingLink, modeling a host with no identity-link flow
// configured — the same outcome the real endpoint reports when its
// PendingLinkBinder seam is nil.
type IdentityBinder func(externalUserID, code string) (accepted bool, reason string)

// ReasonNoPendingLink mirrors hostendpoint.ReasonNoPendingLink exactly: the
// rejection reason when no identity-link flow is configured, or the
// configured IdentityBinder found no pending link to bind.
const ReasonNoPendingLink = "no_pending_link"

// RunContext is the canned result WithRunContext associates with one call
// id, served by host/get_run_context.
type RunContext struct {
	RunID     string
	PolicyID  string
	StartedAt string
	StepIndex int64
}

// RunSummary mirrors host/run_history_read's per-run response shape.
type RunSummary struct {
	RunID      string `json:"run_id"`
	PolicyID   string `json:"policy_id"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

// UserEntry mirrors host/user_directory_read's per-user response shape.
type UserEntry struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// BindResult is host/submit_identity_proof's entire result vocabulary,
// mirroring hostendpoint.BindResult exactly: accepted, plus a reason on
// rejection — nothing else (ADR-058: no user_id, no role, no echoed id).
type BindResult struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// New constructs a fake host endpoint as a bare http.Handler. Most tests want
// NewServer instead, which additionally wraps this in an httptest.Server and
// sets the environment variables hostclient.New reads by default.
func New(opts ...Option) *Server {
	s := &Server{
		token:              defaultToken,
		instanceConfigJSON: "{}",
		credentialsJSON:    "",
		runContexts:        map[string]RunContext{},
		userConfigs:        map[string]string{},
		healthState:        map[string]string{},
		authorizedActors:   map[string]string{},
		waiters:            map[string][]chan struct{}{},
	}
	for _, opt := range opts {
		opt(s)
	}
	s.handlers = map[string]toolHandler{
		toolGetInstanceConfig:   s.getInstanceConfig,
		toolGetCredentials:      s.getCredentials,
		toolGetRunContext:       s.getRunContext,
		toolEmitMetric:          s.emitMetric,
		toolLog:                 s.log,
		toolSetHealthState:      s.setHealthState,
		toolRunHistoryRead:      s.runHistoryRead,
		toolUserDirectoryRead:   s.userDirectoryRead,
		toolAuthorizeActor:      s.authorizeActorCall,
		toolSubmitIdentityProof: s.submitIdentityProof,
		toolGetUserConfig:       s.getUserConfig,
	}
	return s
}

// Token returns the bearer token a client must present — either the default
// or whatever WithToken configured.
func (s *Server) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// ServeHTTP implements the host endpoint's wire contract: POST-only,
// bearer auth before anything else (§3), then the same JSON-RPC method
// routing and §2.1 header/_meta validation the real server applies.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Bearer auth runs before the body is even decoded, mirroring
	// RequireInstanceToken's position as the outermost link in the real
	// Chain (middleware.go): an unauthenticated caller never reaches a
	// handler, and the two rejection messages stay distinct for the same
	// reason the real middleware keeps them distinct — "missing" and
	// "unknown/bad" are different bugs for a plugin author to chase.
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "missing instance token", http.StatusUnauthorized)
		return
	}
	if token != s.Token() {
		http.Error(w, "unknown or revoked instance token", http.StatusUnauthorized)
		return
	}

	// A missing OR ambiguous (multi-valued) header means no call id, mirroring
	// hostendpoint.WithCallID exactly: any call-scope enforcement belongs to
	// the handler that needs it, not to a guess between multiple values.
	if vals := r.Header.Values(callIDHeader); len(vals) == 1 && vals[0] != "" {
		r = r.WithContext(withCallID(r.Context(), vals[0]))
	}

	var req jsonrpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, nil, http.StatusBadRequest, errCodeInvalidParams, "invalid JSON-RPC request body", nil)
		return
	}

	switch req.Method {
	case "server/discover":
		s.handleDiscover(w, r, req)
	case "tools/list":
		s.handleToolsList(w, r, req)
	case "tools/call":
		s.handleToolsCall(w, r, req)
	default:
		// Includes a legacy `initialize`: the host endpoint is modern-only,
		// so any method outside the three above is unrecognized rather than
		// a dialect to negotiate down to (contract §2).
		writeError(w, req.ID, http.StatusNotFound, errCodeMethodNotFound,
			"method not found: "+req.Method, nil)
	}
}

// handleDiscover serves server/discover, enforcing the same A4 header / A1
// _meta regimes the real endpoint enforces (contract §2.1).
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	meta := decodeMeta(req.Params)

	headerVersion := r.Header.Get("MCP-Protocol-Version")
	if headerVersion == "" {
		writeError(w, req.ID, http.StatusBadRequest, errCodeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header is missing", nil)
		return
	}
	if methodHeader := r.Header.Get("Mcp-Method"); methodHeader == "" {
		writeError(w, req.ID, http.StatusBadRequest, errCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header is missing", nil)
		return
	} else if methodHeader != req.Method {
		writeError(w, req.ID, http.StatusBadRequest, errCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header value does not match body method", nil)
		return
	}

	bodyVersion, hasVersionField := metaString(meta, metaKeyProtocolVersion)
	if !hasVersionField {
		writeError(w, req.ID, http.StatusBadRequest, errCodeInvalidParams,
			"Invalid params: missing required _meta field "+metaKeyProtocolVersion, nil)
		return
	}
	if _, ok := meta[metaKeyClientCapabilities]; !ok {
		writeError(w, req.ID, http.StatusBadRequest, errCodeInvalidParams,
			"Invalid params: missing required _meta field "+metaKeyClientCapabilities, nil)
		return
	}
	if headerVersion != bodyVersion {
		writeError(w, req.ID, http.StatusBadRequest, errCodeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header value does not match body value", nil)
		return
	}
	if bodyVersion != protocolVersion {
		writeError(w, req.ID, http.StatusBadRequest, errCodeUnsupportedProtocolVersion,
			"Unsupported protocol version", map[string]any{
				"supported": []string{protocolVersion},
				"requested": bodyVersion,
			})
		return
	}

	s.recordCall(Call{Method: "server/discover"})
	writeResult(w, req.ID, map[string]any{
		"resultType":        "complete",
		"supportedVersions": []string{protocolVersion},
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"_meta": map[string]any{
			metaKeyServerInfo: map[string]any{
				"name":    ServerName,
				"version": Version,
			},
		},
	})
}

// handleToolsList serves the fixed eleven-tool inventory this fake always
// registers — unlike the real endpoint, tool mounting is not configurable
// here, since every §6 method must be served (DoD).
func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	if !s.enforceToolTransport(w, r, req, "") {
		return
	}
	s.recordCall(Call{Method: "tools/list"})
	tools := make([]map[string]any, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, map[string]any{
			"name":        name,
			"description": "",
			"inputSchema": map[string]any{"type": "object"},
		})
	}
	writeResult(w, req.ID, map[string]any{"tools": tools})
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// handleToolsCall dispatches one tool invocation, splitting transport faults
// (JSON-RPC errors) from handler refusals (isError results) exactly as the
// real endpoint does (contract §2.2).
func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeError(w, req.ID, http.StatusBadRequest, errCodeInvalidParams, "tools/call requires params.name", nil)
		return
	}
	if !s.enforceToolTransport(w, r, req, params.Name) {
		return
	}
	handler, ok := s.handlers[params.Name]
	if !ok {
		writeError(w, req.ID, http.StatusBadRequest, errCodeInvalidParams,
			"unknown tool: "+params.Name, nil)
		return
	}

	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	s.recordCall(Call{Method: params.Name, Arguments: args})

	result, err := handler(r, args)
	if err != nil {
		var te *toolError
		if !errors.As(err, &te) {
			te = &toolError{Code: "internal", Message: err.Error()}
		}
		writeResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": te.Error()}},
			"isError": true,
		})
		return
	}

	payload, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		writeResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "internal: marshal tool result: " + marshalErr.Error()}},
			"isError": true,
		})
		return
	}
	writeResult(w, req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(payload)}},
		"isError": false,
	})
}

// enforceToolTransport applies the A4 header rules to tool traffic:
// MCP-Protocol-Version and Mcp-Method always; Mcp-Name must equal the called
// tool's name on tools/call (expectedName != "").
func (s *Server) enforceToolTransport(w http.ResponseWriter, r *http.Request, req jsonrpcRequest, expectedName string) bool {
	if r.Header.Get("MCP-Protocol-Version") == "" {
		writeError(w, req.ID, http.StatusBadRequest, errCodeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header is missing", nil)
		return false
	}
	methodHeader := r.Header.Get("Mcp-Method")
	if methodHeader == "" {
		writeError(w, req.ID, http.StatusBadRequest, errCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header is missing", nil)
		return false
	}
	if methodHeader != req.Method {
		writeError(w, req.ID, http.StatusBadRequest, errCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header value does not match body method", nil)
		return false
	}
	if expectedName != "" {
		if name := r.Header.Get("Mcp-Name"); name != expectedName {
			writeError(w, req.ID, http.StatusBadRequest, errCodeHeaderMismatch,
				"Header mismatch: Mcp-Name header value does not match called tool", nil)
			return false
		}
	}
	return true
}

// callIDCtxKey is this fake's own request-scoped call-id key — deliberately
// not hostclient's (that package's WithCallID/CallIDFromContext are for the
// CLIENT side, attaching the outgoing header; this is the SERVER side,
// reading it back off the incoming request).
type callIDCtxKey struct{}

func withCallID(ctx context.Context, callID string) context.Context {
	return context.WithValue(ctx, callIDCtxKey{}, callID)
}

func callIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(callIDCtxKey{}).(string)
	return v, ok && v != ""
}
