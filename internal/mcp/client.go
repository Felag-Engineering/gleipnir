// Package mcp implements the MCP HTTP transport client and tool registry.
// This package must not import internal/execution/agent (package boundary, ADR-001).
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/infra/version"
)

const (
	// maxRedirects is the maximum number of HTTP redirects the MCP client will follow.
	maxRedirects = 10

	// ProtocolVersionLegacy is the version Gleipnir sends in the legacy
	// initialize handshake and the pin used for servers that do not answer
	// server/discover.
	ProtocolVersionLegacy = "2024-11-05"
	// ProtocolVersion20260728 is the modern MCP revision Gleipnir speaks.
	ProtocolVersion20260728 = "2026-07-28"
)

// Tool is a tool discovered from an MCP server.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage // JSON Schema object
}

// ToolResult is the response from a tool invocation.
type ToolResult struct {
	Output  json.RawMessage
	IsError bool

	// ResultType is the tool call's resultType (spec §11), verbatim from the
	// server (bounded to maxResultTypeLen) except an absent, empty, or
	// non-string value is normalised to ResultTypeComplete — never empty on
	// a value returned by CallTool.
	//
	// A value other than ResultTypeComplete is DATA, not an error: CallTool
	// still returns a nil error, nothing in this package branches on it, and
	// it is deliberately not written into the run's LLM-visible audit trail.
	// Interpreting it belongs to the milestone that consumes it.
	ResultType string

	// InputRequired is non-nil exactly when ResultType == ResultTypeInputRequired
	// (inputrequired.go): the decoded MRTR elicitation batch plus the opaque
	// requestState the retry call must echo back. nil for every other
	// ResultType.
	InputRequired *InputRequiredResult
}

// ResultTypeComplete is the tools/call resultType every pre-2026-07-28
// server implies by omitting the field, and the value CallTool normalises
// an absent or empty resultType to (spec §11: "absent ⇒ complete for older
// servers").
//
// The other 2026-07-28 value, "task", is deliberately NOT declared here:
// this package must not branch on it, and naming a value nothing compares
// against would imply it does. "input_required" WAS in that category before
// #792 — it is now ResultTypeInputRequired (inputrequired.go), the milestone
// that declares and interprets it.
const ResultTypeComplete = "complete"

// maxResultTypeLen bounds a server-controlled resultType string before it is
// returned in a ToolResult, matching the maxServerInfoFieldLen /
// legacyVersionMaxLen / maxModernErrMessageLen bounded-untrusted-string
// discipline elsewhere in this package. Every spec-defined value ("complete",
// "task", "input_required") is well under 14 bytes, so this is headroom, not
// a realistic limit — it is a length bound only, not semantic validation: an
// unrecognized value of sane length still passes through verbatim.
const maxResultTypeLen = 64

// normalizeResultType is the ONLY place the "absent ⇒ complete" rule lives;
// every ToolResult this package returns passes its resultType through here.
//
// It is deliberately NOT gated on isModernProtocol: a legacy server never
// sends the field at all, so the gate would buy nothing on the compliant
// path and would break precisely the "for older servers" case the rule
// exists for. A non-empty value is passed through verbatim (bounded to
// maxResultTypeLen), including an unrecognized one, because this package
// does not interpret the field — validating it here would put policy in the
// transport.
func normalizeResultType(raw string) string {
	if raw == "" {
		return ResultTypeComplete
	}
	return truncateForLog(raw, maxResultTypeLen)
}

// decodeResultType extracts the resultType field's string value out of a
// tools/call result, tolerating a non-compliant server. raw is nil when the
// field was absent (toolsCallResult.ResultType is the json.RawMessage zero
// value); it is treated identically to absent — "" — when the field is
// present but is not a JSON string, e.g. a server sending
// {"resultType":1} or {"resultType":{}}.
//
// That tolerance matters: before ToolResult had a ResultType field at all,
// an unrecognized "resultType" key was just an unknown key that
// json.Unmarshal silently ignored, and the call succeeded. Decoding straight
// into a string-typed struct field would turn a non-string value into an
// UnmarshalTypeError that fails the whole CallTool — a behavior regression
// against a non-compliant server that this package's "don't change behavior
// for older/non-modern servers" premise forbids. The caller feeds the result
// through normalizeResultType, so a malformed value ends up
// ResultTypeComplete exactly like a genuinely absent one.
func decodeResultType(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// JSON-RPC 2.0 wire types used for MCP HTTP transport.

type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC error response from an MCP server.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// maxErrorBodyBytes caps how much of a non-2xx response body HTTPStatusError
// retains. Era detection (see discover.go) needs to read 400/404 bodies to
// distinguish a modern server's JSON-RPC error from a legacy server's opaque
// 4xx; this bound keeps a misbehaving server from ballooning memory.
const maxErrorBodyBytes = 64 << 10

// HTTPStatusError represents a non-OK HTTP status code from an MCP server.
type HTTPStatusError struct {
	StatusCode int
	// Body is up to maxErrorBodyBytes of the response body. Retained because
	// era detection must distinguish a modern server's JSON-RPC error
	// 400/404 from a legacy server's opaque 4xx.
	//
	// WARNING: Body is untrusted, attacker-controlled content from the
	// remote server and may contain secrets or arbitrary HTML/text. Error()
	// deliberately does not include it. Do not add a %#v verb — or any other
	// formatting path that would print every exported field — anywhere this
	// type is logged; Go's default %#v struct formatting would dump Body.
	Body []byte
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("mcp server returned status %d", e.StatusCode)
}

// toolsListParams is the params object for tools/list. The zero value marshals to
// exactly `{}` — the same bytes the previous `struct{}{}` produced — so a legacy
// request is unchanged.
type toolsListParams struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

// toolsCallParams is the params object for tools/call. Name and Arguments are
// declared first and _meta last with omitempty, so a legacy request marshals to
// exactly `{"name":...,"arguments":...}` — byte-for-byte what the previous
// anonymous struct produced.
//
// InputResponses and RequestState are the MRTR retry fields (inputrequired.go,
// spec §6.4): both omitempty, both nil on every call that is not answering a
// prior input_required result, so an ordinary tools/call is unaffected.
type toolsCallParams struct {
	Name           string              `json:"name"`
	Arguments      map[string]any      `json:"arguments"`
	Meta           map[string]any      `json:"_meta,omitempty"`
	InputResponses []inputResponseWire `json:"inputResponses,omitempty"`
	RequestState   json.RawMessage     `json:"requestState,omitempty"`
}

type toolsListResult struct {
	Tools []toolWire `json:"tools"`

	// TTLMs and CacheScope are the 2026-07-28 CacheableResult cache hint
	// (spec §11): "ttlMs" (minimum 0, "0 ⇒ immediately stale") and
	// "cacheScope" ("private"/"public"), both required by the schema on a
	// result that actually implements caching. json.RawMessage rather than
	// typed fields for the same reason toolsCallResult.ResultType is (see
	// decodeResultType's doc below): a non-compliant value must not fail the
	// whole tools/list unmarshal. parseCacheHint (cache.go) does the
	// tolerant decode and gates on isModernProtocol().
	TTLMs      json.RawMessage `json:"ttlMs"`
	CacheScope json.RawMessage `json:"cacheScope"`
}

type toolWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// toolsCallResult is decode-only — its sole uses are the json.Unmarshal in
// CallTool and one test unmarshal in fakeserver_test.go — so field order is
// cosmetic and no request bytes are affected by it.
//
// ResultType is json.RawMessage rather than string so a non-compliant
// server sending a non-string resultType cannot fail the whole
// json.Unmarshal call — see decodeResultType, which extracts the string
// value and tolerates exactly that case. Absent ⇒ ResultTypeComplete
// (spec §11).
//
// Deliberately no TTLMs/CacheScope fields here: CallToolResult carries NO
// cache hint in the 2026-07-28 schema. CacheableResult (which toolsListResult
// above embeds the fields of) requires ["cacheScope","resultType","ttlMs"]
// and covers tools/list, prompts/list, resources/list,
// resources/templates/list, and resources/read — CallToolResult's required
// fields are only ["content","resultType"]. There is nothing to parse for a
// cache hint on a tool call result; do not add one here to "complete the
// symmetry" with toolsListResult.
//
// InputRequests and RequestState carry the MRTR input_required payload
// (inputrequired.go, spec §6): both json.RawMessage so a malformed or
// oversize payload can be classified precisely by
// decodeInputRequiredResult rather than failing this json.Unmarshal outright
// — CallTool only reaches that decode when ResultType normalises to
// ResultTypeInputRequired, so both fields are ignored (and may be absent)
// on every other result.
type toolsCallResult struct {
	Content       []contentItem   `json:"content"`
	IsError       bool            `json:"isError"`
	ResultType    json.RawMessage `json:"resultType"`
	InputRequests json.RawMessage `json:"inputRequests,omitempty"`
	RequestState  json.RawMessage `json:"requestState,omitempty"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Client calls a single MCP server over HTTP transport.
type Client struct {
	serverURL   string
	serverName  string       // Prometheus label; set by registry.newClient, empty for direct NewClient callers
	authHeaders []AuthHeader // static headers injected on every outbound request
	httpClient  *http.Client
	mu          sync.Mutex
	sessionID   string

	protocolVersion   string // pinned per-server version; "" = unpinned/legacy
	negotiatedVersion string // legacy initialize result; guarded by mu

	// channelCap is the server's io.gleipnir/channel declaration from the
	// initialize handshake (spec §4, Amendment 1). Guarded by mu; zero-valued
	// until initialize runs and for every server that does not declare the
	// extension — which is most of them, and is not an error.
	channelCap      ChannelCapability
	channelDeclared bool

	// elicitationLimits and elicitationRate bound what one server can push at
	// an operator through MRTR input_required (spec §6.2). Both are host
	// self-protection with safe defaults, so a Client built without them
	// behaves exactly as it did before the caps existed. elicitationRate is
	// nil until first use and built lazily under mu.
	elicitationLimits ElicitationLimits
	elicitationRate   *elicitationLimiter
	elicitationRateHz float64
	elicitationBurst  int

	// trustTier decides whether this server may participate in the
	// `io.gleipnir/*` extensions (spec §3/§5, #819). The zero value is
	// external, which is the fail-closed direction: a Client built without an
	// opinion negotiates nothing private.
	trustTier TrustTier

	// callGate bounds concurrent tools/call requests to this server and the
	// queue waiting for a slot. nil means unbounded, which is what a Client
	// constructed directly (tests, probes) gets.
	callGate *serverGate
}

// ClientOption configures a Client. Options are applied sequentially after
// the default Client is constructed, so order matters when combining options
// (e.g. WithHTTPClient followed by WithTimeout sets the timeout on the
// supplied client, not the default one).
type ClientOption func(*Client)

// WithHTTPClient replaces the Client's HTTP client entirely. This replaces
// the default CheckRedirect policy as well; the caller is responsible for
// their own redirect policy when using this option.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(cl *Client) {
		cl.httpClient = c
	}
}

// WithTimeout sets the Timeout on whatever httpClient exists at the time this
// option is applied. When combined with WithHTTPClient, place WithTimeout
// after WithHTTPClient so the timeout is set on the supplied client.
func WithTimeout(d time.Duration) ClientOption {
	return func(cl *Client) {
		cl.httpClient.Timeout = d
	}
}

// WithAuthHeaders configures static headers to be injected on every outbound
// request. Headers are applied before Mcp-Session-Id so the client-managed
// session header always takes precedence.
func WithAuthHeaders(hs []AuthHeader) ClientOption {
	return func(cl *Client) {
		cl.authHeaders = hs
	}
}

// WithProtocolVersion pins the MCP protocol version negotiated for this
// server. "" (the default) means "not yet probed" and keeps the legacy
// request shaping; a pin matching a version in supportedProtocolVersions
// switches every request through sendRPC to the stateless 2026-07-28
// transport instead (see isModernProtocol).
func WithProtocolVersion(v string) ClientOption {
	return func(cl *Client) {
		cl.protocolVersion = v
	}
}

// WithElicitationLimits sets the per-result size caps applied when decoding an
// MRTR input_required result (spec §6.2 cap 2). Unset fields fall back to the
// package defaults.
func WithElicitationLimits(l ElicitationLimits) ClientOption {
	return func(cl *Client) { cl.elicitationLimits = l }
}

// WithElicitationRateLimit sets this server's input_required token bucket
// (spec §6.2 cap 3). Non-positive values fall back to the package defaults.
func WithElicitationRateLimit(ratePerSec float64, burst int) ClientOption {
	return func(cl *Client) {
		cl.elicitationRateHz = ratePerSec
		cl.elicitationBurst = burst
	}
}

// allowElicitation reports whether one more input_required result from this
// server is within its rate limit, building the bucket on first use.
func (c *Client) allowElicitation() bool {
	c.mu.Lock()
	if c.elicitationRate == nil {
		c.elicitationRate = newElicitationLimiter(c.elicitationRateHz, c.elicitationBurst)
	}
	limiter := c.elicitationRate
	c.mu.Unlock()
	return limiter.allow()
}

// NewClient returns a Client targeting serverURL. Optional ClientOptions are
// applied in order after the default Client is constructed.
func NewClient(serverURL string, opts ...ClientOption) *Client {
	c := &Client{
		serverURL: serverURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if err := checkRedirectTarget(req.URL); err != nil {
					return err
				}
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// initializeResult is the subset of the legacy initialize response body we
// read. The server's negotiated protocolVersion is recorded as the legacy
// pin (see negotiatedLegacyVersion).
type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// initialize performs the MCP handshake and returns the session ID assigned
// by the server, plus the protocolVersion the server negotiated (empty if
// the result could not be parsed). Callers must include the session ID as
// "Mcp-Session-Id" on all subsequent requests to the same server.
//
// The streamable-HTTP transport requires:
//  1. POST initialize → server replies with Mcp-Session-Id header
//  2. POST notifications/initialized (no response body expected)
//
// Only after that will the server accept method calls like tools/list.
func (c *Client) initialize(ctx context.Context) (sessionID, negotiated string, err error) {
	initBody, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": ProtocolVersionLegacy,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "gleipnir",
				"version": version.Version,
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("marshal initialize: %w", err)
	}

	resp, err := c.postRaw(ctx, initBody, "")
	if err != nil {
		return "", "", fmt.Errorf("initialize: %w", err)
	}
	defer resp.Body.Close()

	sessionID = resp.Header.Get("Mcp-Session-Id")

	var initEnvelope jsonrpcResponse
	if err := decodeResponse(resp, &initEnvelope); err != nil {
		return "", "", fmt.Errorf("decode initialize response: %w", err)
	}
	if initEnvelope.Error != nil {
		return "", "", fmt.Errorf("initialize error: %w", initEnvelope.Error)
	}
	if len(initEnvelope.Result) > 0 {
		var result initializeResult
		if err := json.Unmarshal(initEnvelope.Result, &result); err != nil {
			// A server that answers initialize with an odd result shape is
			// still usable — the session ID is what matters for tool
			// traffic. Log and continue with an empty negotiated version.
			slog.Debug("failed to parse initialize result; continuing without negotiated version",
				"server_name", c.serverName, "err", err)
		} else {
			negotiated = result.ProtocolVersion
		}
	}
	// Notify the server that initialisation is complete (fire-and-forget; the
	// server sends no response to notifications).
	notifyBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	})
	if err != nil {
		return "", "", fmt.Errorf("marshal notifications/initialized: %w", err)
	}
	nresp, err := c.postRaw(ctx, notifyBody, sessionID)
	if err != nil {
		return "", "", fmt.Errorf("notifications/initialized: %w", err)
	}
	drainResponseBody(nresp.Body)

	return sessionID, negotiated, nil
}

// ensureSession returns the cached session ID, initializing if necessary.
// Uses double-checked locking so that the HTTP round-trip to initialize is not
// done under the mutex — concurrent callers unblock immediately once any one
// of them stores a valid session.
func (c *Client) ensureSession(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.sessionID != "" {
		sid := c.sessionID
		c.mu.Unlock()
		return sid, nil
	}
	c.mu.Unlock()

	sid, negotiated, err := c.initialize(ctx)
	if err != nil {
		return "", fmt.Errorf("ensure session: %w", err)
	}

	c.mu.Lock()
	if c.sessionID != "" {
		// Another goroutine stored a valid session while we were initializing.
		sid = c.sessionID
	} else {
		c.sessionID = sid
		c.negotiatedVersion = negotiated
	}
	c.mu.Unlock()
	return sid, nil
}

// resetSession clears the cached session ID so the next call re-initializes.
func (c *Client) resetSession() {
	c.mu.Lock()
	c.sessionID = ""
	c.mu.Unlock()
}

// negotiatedLegacyVersion returns the protocolVersion the server reported in
// the most recent legacy initialize handshake, or "".
func (c *Client) negotiatedLegacyVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.negotiatedVersion
}

// isModernProtocol reports whether this client speaks the 2026-07-28
// stateless transport. Only an explicit pin of a version in
// supportedProtocolVersions counts: "" (never probed) and every legacy
// pin keep the legacy session shaping, so a server that has not been
// probed can never be silently re-shaped.
//
// No lock: protocolVersion is written once by WithProtocolVersion during
// NewClient and never mutated afterwards (unlike sessionID /
// negotiatedVersion, which mu guards).
//
// A legacy server cannot reach this branch by lying about its version:
// sanitizedLegacyVersion (discover.go) only ever emits an allowlisted
// pre-2026 token, and knownLegacyProtocolVersions is disjoint from
// supportedProtocolVersions by construction
// (TestLegacyAllowlistDisjointFromModernVersions).
func (c *Client) isModernProtocol() bool {
	for _, v := range supportedProtocolVersions {
		if c.protocolVersion == v {
			return true
		}
	}
	return false
}

// callWithSession sends body to the server, automatically handling session
// initialization and a single re-init retry on HTTP 401.
func (c *Client) callWithSession(ctx context.Context, body []byte) (*http.Response, error) {
	sid, err := c.ensureSession(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := c.postRaw(ctx, body, sid)
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusUnauthorized {
			// Session expired — re-initialize once and retry.
			c.resetSession()
			sid, err = c.ensureSession(ctx)
			if err != nil {
				return nil, err
			}
			return c.postRaw(ctx, body, sid)
		}
		return nil, err
	}
	return resp, nil
}

// sendRPC dispatches one JSON-RPC request under whichever transport the
// pinned protocol version calls for.
//
// Modern (2026-07-28): stateless. MCP-Protocol-Version, Mcp-Method and (when
// the method targets a named entity) Mcp-Name are bound to the request;
// there is no session and therefore no 401 re-init retry — a 401 on a
// stateless transport is an auth failure, not a stale session, and retrying
// it would double every auth failure.
//
// Legacy: unchanged, byte for byte — callWithSession keeps owning the
// handshake and the single 401 re-init retry. headerParams is deliberately
// not forwarded on the legacy branch — callWithSession/postRaw carry no
// tool-parameter headers, the same era-gating discipline requestMeta
// (meta.go) applies to _meta.
//
// rpcName is "" for methods that address no named entity (tools/list,
// server/discover); see the Mcp-Name decision in Key decisions.
func (c *Client) sendRPC(ctx context.Context, body []byte, method, rpcName string, headerParams []headerParam) (*http.Response, error) {
	if !c.isModernProtocol() {
		return c.callWithSession(ctx, body)
	}
	return c.post(ctx, body, postOptions{
		protocolVersion: c.protocolVersion,
		rpcMethod:       method,
		rpcName:         rpcName,
		headerParams:    headerParams,
	})
}

// methodToolsList / methodToolsCall are written once so the JSON-RPC body's
// "method" and the Mcp-Method header cannot drift (same single-variable rule
// ProbeProtocolVersion applies to requestedVersion, discover.go).
const (
	methodToolsList = "tools/list"
	methodToolsCall = "tools/call"
)

// DiscoverTools calls the MCP server's tool list endpoint and returns all
// available tools. Used during server registration to populate mcp_tools.
//
// Thin wrapper over discoverToolsWithHint that discards the parsed cache
// hint, so every existing production and test call site keeps this exact
// two-return-value signature; the hint is only useful to
// Registry.discoverToolsCached (cache.go), the sole caller of
// discoverToolsWithHint.
func (c *Client) DiscoverTools(ctx context.Context) ([]Tool, error) {
	tools, _, err := c.discoverToolsWithHint(ctx)
	return tools, err
}

// discoverToolsWithHint is DiscoverTools plus the tools/list result's
// ttlMs/cacheScope cache hint (spec §11), parsed by parseCacheHint
// (cache.go). Unexported: its only caller is Registry.discoverToolsCached in
// this same package.
func (c *Client) discoverToolsWithHint(ctx context.Context) ([]Tool, cacheHint, error) {
	// tools/list invokes nothing, so it never declares a capability; the empty
	// clientCapabilities object is still required on the modern path.
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  methodToolsList,
		Params:  toolsListParams{Meta: c.requestMeta(ClientCapabilities{})},
	})
	if err != nil {
		return nil, cacheHint{}, fmt.Errorf("marshal tools/list request: %w", err)
	}

	resp, err := c.sendRPC(ctx, body, methodToolsList, "", nil)
	if err != nil {
		return nil, cacheHint{}, fmt.Errorf("post tools/list: %w", err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := decodeResponse(resp, &envelope); err != nil {
		return nil, cacheHint{}, fmt.Errorf("decode tools/list response: %w", err)
	}
	if envelope.Error != nil {
		return nil, cacheHint{}, envelope.Error
	}

	var result toolsListResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return nil, cacheHint{}, fmt.Errorf("unmarshal tools/list result: %w", err)
	}

	tools := make([]Tool, len(result.Tools))
	for i, tw := range result.Tools {
		tools[i] = Tool(tw)
	}
	hint := parseCacheHint(c.isModernProtocol(), result.TTLMs, result.CacheScope)
	return tools, hint, nil
}

// CallOptions carries the per-call inputs to CallTool that are not part of
// the JSON-RPC arguments object. The zero value is today's behavior: declare
// nothing, send no tool-parameter headers.
type CallOptions struct {
	// Capabilities is the per-request client capability declaration (spec
	// §11). It is honored only on the 2026-07-28 transport; on the legacy
	// transport it is structurally inert because requestMeta returns nil for
	// a non-modern client, so a granted capability can never leak into a
	// legacy request body.
	Capabilities ClientCapabilities

	// HeaderParamSchema is the tool's input schema, used ONLY as the source
	// of SEP-2243 x-mcp-header annotations — never for argument validation
	// (that is the agent's pre-dispatch ArgValidator, #744). Empty means "no
	// annotations to honor".
	HeaderParamSchema json.RawMessage

	// InputResponses and RequestState make this call an MRTR retry (spec
	// §6.4): the caller answers each InputRequest from a prior
	// InputRequiredResult and attaches RequestState verbatim so the server
	// can resume the round trip. Both are the zero value on an ordinary
	// call; honored only on the 2026-07-28 transport, matching every other
	// _meta/header extension in this file — a legacy-pinned or never-probed
	// server never returns input_required in the first place, so there is
	// nothing to retry there.
	InputResponses []InputResponse
	RequestState   json.RawMessage
}

// CallTool invokes a named tool on the MCP server with the given input.
// The input must be a JSON-serialisable value matching the tool's inputSchema.
//
// A property in input that opts.HeaderParamSchema annotates with
// x-mcp-header is ALSO still sent in the JSON-RPC arguments object below —
// it is not stripped. The property is declared in the server's own
// inputSchema (and may be in its "required" list), the tool_call audit step
// records input before dispatch, and the pre-dispatch ArgValidator validates
// the full argument set — stripping the property afterwards would disagree
// with all three.
func (c *Client) CallTool(ctx context.Context, name string, input map[string]any, opts CallOptions) (res ToolResult, err error) {
	start := time.Now()
	defer func() {
		mcpCallDurationSeconds.
			WithLabelValues(c.serverName, name).
			Observe(time.Since(start).Seconds())
		if err != nil {
			mcpErrorsTotal.
				WithLabelValues(c.serverName, ClassifyMCPErrorType(err)).
				Inc()
		}
	}()

	// The per-server gate is claimed before anything else this call does — no
	// header resolution, no request build, and above all no socket. A ceiling
	// enforced after the work is a ceiling on nothing; and a call rejected with
	// ErrQueueFull has to be cheap, because "the server is saturated" is
	// exactly the moment when doing avoidable work per rejected call is worst.
	release, gateErr := c.callGate.acquire(ctx)
	if gateErr != nil {
		err = fmt.Errorf("calling tool %q: %w", name, gateErr)
		return
	}
	defer release()

	// x-mcp-header is a 2026-07-28 feature (spec §11). A legacy-pinned or
	// never-probed server never negotiated it, so the annotation is not even
	// read there: legacy request shaping stays byte-identical and a legacy
	// server's schema can never introduce a new failure mode for a tool that
	// works today.
	var headerParams []headerParam
	if c.isModernProtocol() {
		headerParams, err = extractHeaderParams(opts.HeaderParamSchema, input, c.authHeaders)
		if err != nil {
			err = fmt.Errorf("resolving x-mcp-header parameters for tool %q: %w", name, err)
			return
		}
	}

	params := toolsCallParams{Name: name, Arguments: input, Meta: c.requestMeta(opts.Capabilities)}
	if c.isModernProtocol() && len(opts.InputResponses) > 0 {
		wireResponses := make([]inputResponseWire, len(opts.InputResponses))
		for i, r := range opts.InputResponses {
			wireResponses[i] = inputResponseWire(r)
		}
		params.InputResponses = wireResponses
		params.RequestState = opts.RequestState
	}

	var body []byte
	body, err = json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  methodToolsCall,
		Params:  params,
	})
	if err != nil {
		err = fmt.Errorf("marshal tools/call request: %w", err)
		return
	}

	var resp *http.Response
	resp, err = c.sendRPC(ctx, body, methodToolsCall, name, headerParams)
	if err != nil {
		err = fmt.Errorf("post tools/call: %w", err)
		return
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if decErr := decodeResponse(resp, &envelope); decErr != nil {
		err = fmt.Errorf("decode tools/call response: %w", decErr)
		return
	}
	if envelope.Error != nil {
		err = envelope.Error
		return
	}

	var result toolsCallResult
	if umErr := json.Unmarshal(envelope.Result, &result); umErr != nil {
		err = fmt.Errorf("unmarshal tools/call result: %w", umErr)
		return
	}

	var output []byte
	output, err = json.Marshal(result.Content)
	if err != nil {
		err = fmt.Errorf("marshal content array: %w", err)
		return
	}

	res = ToolResult{
		Output:     output,
		IsError:    result.IsError,
		ResultType: normalizeResultType(decodeResultType(result.ResultType)),
	}

	if res.ResultType == ResultTypeInputRequired {
		// Rate-limit before decoding, not after: the cheapest possible
		// rejection for a server flooding the operator, and it keeps an
		// over-limit result from ever becoming a routable request.
		if !c.allowElicitation() {
			res = ToolResult{}
			err = &ElicitationRateLimitError{ServerName: c.serverName}
			return
		}
		var inputRequired InputRequiredResult
		inputRequired, err = decodeInputRequiredResult(result, c.elicitationLimits)
		if err != nil {
			res = ToolResult{}
			err = fmt.Errorf("tool %q returned input_required: %w", name, err)
			return
		}
		res.InputRequired = &inputRequired
	}
	return
}

// maxJSONRPCPayloadBytes bounds how much of a 2xx response body
// readJSONRPCPayload will ever buffer. Legitimate tools/list and
// server/discover responses are expected to be well under this — the plan
// deliberately left this path uncapped for #737 because tool catalogs are
// legitimately large, but Finding 4 (security review, #737 cycle 2)
// confirmed an uncapped read buffered 33,554,476 bytes from a single crafted
// 2xx response. This cap is a memory-exhaustion backstop, not a realistic
// content-size limit — it is far larger than maxErrorBodyBytes because error
// bodies are expected to be small diagnostic text while a tool catalog is
// not.
const maxJSONRPCPayloadBytes = 32 << 20 // 32 MiB

// readJSONRPCPayload returns the raw JSON-RPC payload bytes from resp,
// transparently unwrapping the first "data: " line when the server replies
// with an SSE stream.
func readJSONRPCPayload(resp *http.Response) ([]byte, error) {
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				return []byte(strings.TrimPrefix(line, "data: ")), nil
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read SSE stream: %w", err)
		}
		return nil, fmt.Errorf("no data line found in SSE response")
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxJSONRPCPayloadBytes))
}

// decodeResponse decodes a JSON-RPC response from resp.Body into dst.
// The MCP streamable-HTTP transport may return either plain JSON or an SSE
// stream (Content-Type: text/event-stream). In SSE mode each response is a
// "data: <json>" line; we extract the first such line and decode it.
func decodeResponse(resp *http.Response, dst any) error {
	payload, err := readJSONRPCPayload(resp)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, dst)
}

// postOptions carries the per-request client-managed transport headers.
// The zero value is today's legacy shaping.
type postOptions struct {
	sessionID       string        // Mcp-Session-Id      (legacy transport)
	protocolVersion string        // MCP-Protocol-Version (2026-07-28 transport)
	rpcMethod       string        // Mcp-Method           (2026-07-28 transport)
	rpcName         string        // Mcp-Name             (2026-07-28 transport; "" when the method targets no named entity)
	headerParams    []headerParam // x-mcp-header tool-parameter headers (2026-07-28 transport)
}

// post sends a JSON-RPC request body to c.serverURL and returns the HTTP
// response. It returns an error for non-2xx status codes.
//
// Header injection order:
//  1. o.headerParams — x-mcp-header tool-parameter headers (spec §11),
//     agent-supplied. This is the WEAKEST layer: set first so every other
//     layer below overrides it on a same-name collision, including
//     Content-Type/Accept. Ordering alone is NOT the whole invariant, though:
//     a header whose semantics act on OTHER headers (Connection, Upgrade,
//     TE, ...) is never "overridden" by a later layer just because that
//     layer is set afterward, so extractHeaderParams rejects those names
//     outright via a dedicated denylist (headerparams.go) before
//     o.headerParams is ever populated — and rejects a name colliding with a
//     configured ADR-039 auth header the same way, rather than leaving that
//     collision to this ordering to resolve silently. A byte-different name
//     can also collide with another layer's header downstream of this
//     client without ever colliding here: http.CanonicalHeaderKey only
//     capitalizes the letter after a "-", so it treats every other RFC 7230
//     token character (including "_", ".", "^", "`", "|", "~") as an
//     ordinary letter rather than a word separator, and e.g. "X-Api-Key",
//     "X-Api_Key", and "X.Api.Key" are three distinct headers to every check
//     in this package even though many CGI/WSGI backends fold all three onto
//     the same env var — extractHeaderParams closes that gap by rejecting
//     any x-mcp-header name containing a byte outside "[A-Za-z0-9-]"
//     outright, rather than trying to widen every name/collision check to
//     treat each of those separators as equivalent to "-". The invariant "a
//     tool parameter can never displace, or substitute for, a transport or
//     operator header" therefore depends on that denylist, the ADR-039
//     collision check, and the name allowlist together with this ordering,
//     not on ordering alone: ordering is what makes a same-name VALUE
//     collision resolve correctly; it cannot make an out-of-band or
//     byte-different header name harmless.
//  2. Content-Type and Accept (transport requirements)
//  3. c.authHeaders (ADR-039 operator-configured, applied in registration
//     order) — overrides 1.
//  4. all client-managed headers (session, protocol version, method, name),
//     each only when non-empty — set last so they always win, even if an
//     operator configures a colliding auth header.
//
// req.Header.Set canonicalizes the header name (textproto.CanonicalMIMEHeaderKey),
// so a case-differing collision between layers still overwrites rather than
// appending — this is what makes "the admin value wins" true regardless of
// the casing a layer's source (e.g. a remote server's schema) used.
func (c *Client) post(ctx context.Context, body []byte, o postOptions) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for _, h := range o.headerParams {
		req.Header.Set(h.Name, h.Value)
	}
	req.Header.Set("Content-Type", "application/json")
	// MCP streamable-HTTP transport requires the client to accept both JSON
	// (for single-response calls) and SSE (for streaming responses).
	req.Header.Set("Accept", "application/json, text/event-stream")
	// Inject operator-configured auth headers before the client-managed
	// headers so that the client-managed values always take precedence if an
	// operator mistakenly configures a header with a colliding name.
	for _, h := range c.authHeaders {
		req.Header.Set(h.Name, h.Value)
	}
	if o.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", o.sessionID)
	}
	if o.protocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", o.protocolVersion)
	}
	if o.rpcMethod != "" {
		req.Header.Set("Mcp-Method", o.rpcMethod)
	}
	if o.rpcName != "" {
		// o.rpcName is a tool name that ultimately originates from the remote
		// MCP server (DiscoverTools takes it verbatim) and is not
		// charset-constrained upstream — a hostile server could hand us a
		// name containing CRLF. We do not sanitize it here: net/http's
		// outbound Transport.roundTrip runs httpguts.ValidHeaderFieldValue on
		// every header value and refuses to send a request carrying CTL
		// bytes, failing the call closed rather than smuggling a header.
		req.Header.Set("Mcp-Name", o.rpcName)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		// Capture a bounded prefix of the body before draining, so era
		// detection (discover.go) can inspect a 400/404 body, then still
		// drain fully so the connection can be reused.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		drainResponseBody(resp.Body)
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Body: errBody}
	}

	return resp, nil
}

// postRaw is a backward-compatible wrapper over post for the legacy
// session-based transport. sessionID is included as "Mcp-Session-Id" when
// non-empty. It never carries headerParams: x-mcp-header is a 2026-07-28
// transport feature (see sendRPC), and postRaw only ever serves the legacy
// path.
func (c *Client) postRaw(ctx context.Context, body []byte, sessionID string) (*http.Response, error) {
	return c.post(ctx, body, postOptions{sessionID: sessionID})
}

// drainResponseBody reads any remaining data from rc and closes it.
// This ensures the underlying TCP connection can be reused by the HTTP
// transport's connection pool.
func drainResponseBody(rc io.ReadCloser) {
	io.Copy(io.Discard, rc) //nolint:errcheck
	rc.Close()
}
