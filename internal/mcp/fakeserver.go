package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// FakeMCPServer is a shared, reusable fake MCP server for protocol-era
// testing. Test-only seam; no production code reaches it — mirrors
// registertest.go's "Test-only seam" precedent, except this one is an
// http.Handler rather than a DB-backed helper, so any package can wrap it in
// httptest.NewServer:
//
//	fake := mcp.NewFakeMCPServer(mcp.WithFakeMode(mcp.FakeModern))
//	srv := httptest.NewServer(fake); t.Cleanup(srv.Close)
//
// It deliberately imports only stdlib (encoding/json, io, net/http, fmt,
// sync) — never "testing" or "net/http/httptest" — so a production-imported
// package is never coupled to *testing.T and a handler goroutine can never
// be tempted to call t.Fatal (which is illegal outside the test goroutine).
//
// IMPORTANT compatibility note: by default the fake serves the legacy
// `initialize` (minting `Mcp-Session-Id: fake-session`, echoing
// `protocolVersion` when configured via WithFakeLegacyNegotiatedVersion),
// `notifications/initialized`, and a header-less `tools/list` (the
// configured tools) in EVERY mode — a pinned-modern server still receives
// legacy-shaped `initialize` + `tools/list` unless the test opts into
// WithFakeRejectLegacyHandshake(), which makes the fake behave like a real
// stateless 2026-07-28 server: the legacy handshake methods are rejected and
// tool traffic (tools/list, tools/call) must carry the standard transport
// headers. Every existing call site that does not pass that option keeps
// today's permissive behavior unchanged.
//
// Extension contract: a new era gets a new FakeServerMode constant plus a
// switch case in handleDiscover; new behavior gets a new WithFake… option
// with a zero-value-safe default. The variadic constructor means no
// existing call site changes when a new option is added. FakeRequest
// already captures Mcp-Name and the full header set, so tools/call
// assertions need no recorder change.
//
// Error codes emitted by this fake are the de-facto oracle for the
// transport-header rules; their values and spec citations now live in
// errorcodes.go, the package's single authoritative registry.
type FakeMCPServer struct {
	mu sync.Mutex

	mode                    FakeServerMode
	supportedVersions       []string
	tools                   []Tool
	legacyNegotiatedVersion string
	discoverStatusOverride  int // 0 = no override
	rejectLegacyHandshake   bool
	serverInfoName          string
	serverInfoVersion       string
	toolResultType          string
	toolInputRequests       any
	toolRequestState        any
	toolsListTTLMs          any
	toolsListCacheScope     any
	toolsListHintSet        bool

	requests   []FakeRequest
	violations []string
}

// FakeServerMode selects which era-shaped behavior the fake exposes on
// server/discover.
type FakeServerMode string

const (
	// FakeModern answers server/discover with a DiscoverResult advertising
	// the configured supported versions.
	//
	// DELIBERATE NON-COMPLIANCE, do not "fix": FakeModern returns a
	// DiscoverResult UNCONDITIONALLY — it does NOT cross-check the
	// requested MCP-Protocol-Version header against its configured
	// supportedVersions. A real compliant server that does not support the
	// requested version MUST answer -32022 instead (basic/versioning.md
	// §Protocol Version Negotiation); FakeVersionMismatch is the mode that
	// models that. Keeping FakeModern dumb is what lets a test drive the
	// "modern server advertises only versions we do not speak"
	// (non-compliant server) row.
	FakeModern FakeServerMode = "modern"
	// FakeLegacy answers server/discover the way a pre-2026 server does:
	// HTTP 404 with a plain-text body and no JSON-RPC envelope.
	FakeLegacy FakeServerMode = "legacy"
	// FakeVersionMismatch answers server/discover with HTTP 400 and the
	// -32022 UnsupportedProtocolVersion error carrying data.supported.
	FakeVersionMismatch FakeServerMode = "version_mismatch"
)

// FakeRequest records one request the fake received, for assertions.
type FakeRequest struct {
	Method         string          // JSON-RPC body "method"
	Params         json.RawMessage // raw body "params" (for _meta assertions)
	ProtocolHeader string          // MCP-Protocol-Version
	MethodHeader   string          // Mcp-Method
	NameHeader     string          // Mcp-Name (set on tools/call under a modern pin; "" on tools/list/server/discover)
	SessionHeader  string          // Mcp-Session-Id
	Header         http.Header     // full clone, for anything else
}

// FakeServerOption configures a FakeMCPServer.
type FakeServerOption func(*FakeMCPServer)

// WithFakeMode selects the era-shaped behavior. Default: FakeModern.
func WithFakeMode(m FakeServerMode) FakeServerOption {
	return func(f *FakeMCPServer) { f.mode = m }
}

// WithFakeSupportedVersions sets the versions advertised on server/discover
// (DiscoverResult.supportedVersions in FakeModern, or the -32022 error's
// data.supported in FakeVersionMismatch). Default: ["2026-07-28"].
func WithFakeSupportedVersions(v ...string) FakeServerOption {
	return func(f *FakeMCPServer) { f.supportedVersions = v }
}

// WithFakeTools sets the tools served by tools/list. Default: one tool
// named "tool-a".
func WithFakeTools(tools ...Tool) FakeServerOption {
	return func(f *FakeMCPServer) { f.tools = tools }
}

// WithFakeLegacyNegotiatedVersion sets the protocolVersion the fake reports
// in its legacy initialize result. "" (the default) omits the field
// entirely, matching a server that answers initialize without echoing a
// version.
func WithFakeLegacyNegotiatedVersion(v string) FakeServerOption {
	return func(f *FakeMCPServer) { f.legacyNegotiatedVersion = v }
}

// WithFakeDiscoverStatus forces server/discover to answer with the given
// HTTP status and an empty body, regardless of mode — used to drive the
// probe's "inconclusive status" (discoverUnclassified) path.
func WithFakeDiscoverStatus(code int) FakeServerOption {
	return func(f *FakeMCPServer) { f.discoverStatusOverride = code }
}

// WithFakeRejectLegacyHandshake makes the fake behave like a real stateless
// 2026-07-28 server rather than the deliberately permissive default:
// `initialize` / `notifications/initialized` are answered -32601 Method not
// found (they do not exist in this revision), and every non-discover method
// requires the standard transport headers (MCP-Protocol-Version, Mcp-Method,
// and Mcp-Name where the method names an entity). Default off, so every
// existing call site is unchanged and a pinned-modern fake still tolerates
// legacy-shaped tool traffic.
//
// NOTE: this mode now also enforces the _meta body fields (enforceMetaFields)
// on tool traffic, in addition to the standard transport headers — the
// client injects _meta on tools/list and tools/call under a modern pin.
// handleDiscover keeps enforcing both regimes as before.
func WithFakeRejectLegacyHandshake() FakeServerOption {
	return func(f *FakeMCPServer) { f.rejectLegacyHandshake = true }
}

// WithFakeServerInfo sets the name/version the fake reports in its server/discover
// result _meta. Both empty omits the _meta block entirely, modeling a compliant
// server that sends no serverInfo. Default: "fake-mcp-server" / "1.0.0".
func WithFakeServerInfo(name, version string) FakeServerOption {
	return func(f *FakeMCPServer) {
		f.serverInfoName = name
		f.serverInfoVersion = version
	}
}

// WithFakeToolResultType sets the resultType the fake reports on tools/call.
// "" (the default) OMITS the field entirely — the pre-2026 shape, and the
// fixture for spec §11's "absent ⇒ complete" rule. "complete" / "task" /
// "input_required" model a 2026-07-28 server. The fake deliberately does NOT
// validate the value (same "keep the fake dumb" discipline as FakeModern's
// documented no-cross-check note), so a test can drive an unrecognized value.
func WithFakeToolResultType(rt string) FakeServerOption {
	return func(f *FakeMCPServer) { f.toolResultType = rt }
}

// WithFakeInputRequired sets the inputRequests/requestState fixture the fake
// reports on tools/call, alongside whatever WithFakeToolResultType set — the
// two are independent so a test can drive resultType:"input_required" with
// no inputRequests/requestState at all, to fixture the "malformed"/"absent"
// decode cases. inputRequests and requestState are marshaled verbatim
// (same "keep the fake dumb" discipline as WithFakeToolResultType: no shape
// validation), so a test can drive an oversize or malformed fixture too.
// Both nil (the default) omits both keys entirely.
func WithFakeInputRequired(inputRequests, requestState any) FakeServerOption {
	return func(f *FakeMCPServer) {
		f.toolInputRequests = inputRequests
		f.toolRequestState = requestState
	}
}

// WithFakeToolsListCacheHint sets the ttlMs/cacheScope pair the fake reports
// on tools/list, next to "tools" in the result object. Default (option not
// applied): both keys omitted entirely — the pre-2026 shape, and the fixture
// for a legacy/unpinned server or a modern server that has not opted into
// caching.
//
// The fake does NOT validate either value — same "keep the fake dumb"
// discipline as WithFakeToolResultType (see its doc) — so a test can drive a
// non-compliant hint, e.g. ttlMs: "soon" or cacheScope: "shared", to prove
// the client absorbs it (parseCacheHint, cache.go) rather than failing the
// call.
func WithFakeToolsListCacheHint(ttlMs, cacheScope any) FakeServerOption {
	return func(f *FakeMCPServer) {
		f.toolsListTTLMs = ttlMs
		f.toolsListCacheScope = cacheScope
		f.toolsListHintSet = true
	}
}

// NewFakeMCPServer returns a ready FakeMCPServer. Wrap it in
// httptest.NewServer to use it as an MCP server target.
func NewFakeMCPServer(opts ...FakeServerOption) *FakeMCPServer {
	f := &FakeMCPServer{
		mode:              FakeModern,
		supportedVersions: []string{ProtocolVersion20260728},
		tools: []Tool{
			{Name: "tool-a", Description: "tool-a description", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		serverInfoName:    "fake-mcp-server",
		serverInfoVersion: "1.0.0",
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// Requests returns a mutex-guarded copy of every request the fake has
// received so far, in arrival order.
func (f *FakeMCPServer) Requests() []FakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// RequestsFor returns a copy of every recorded request whose JSON-RPC
// method equals method.
func (f *FakeMCPServer) RequestsFor(method string) []FakeRequest {
	var out []FakeRequest
	for _, r := range f.Requests() {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// Violations returns a mutex-guarded copy of every transport-header rule
// breach the fake has observed: server/discover enforcement in
// FakeModern/FakeVersionMismatch mode, plus tool-traffic (tools/list,
// tools/call) enforcement whenever WithFakeRejectLegacyHandshake is set.
func (f *FakeMCPServer) Violations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.violations))
	copy(out, f.violations)
	return out
}

func (f *FakeMCPServer) recordRequest(r FakeRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
}

func (f *FakeMCPServer) recordViolation(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.violations = append(f.violations, msg)
}

// ServeHTTP routes on the decoded JSON-RPC body's "method" field and
// records a FakeRequest for every request before responding.
func (f *FakeMCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	var decoded struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(bodyBytes, &decoded); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	req := FakeRequest{
		Method: decoded.Method,
		Params: decoded.Params,
		// r.Header.Get canonicalizes the lookup key, so "MCP-Protocol-Version"
		// matches Go's canonical "Mcp-Protocol-Version" storage form.
		ProtocolHeader: r.Header.Get("MCP-Protocol-Version"),
		MethodHeader:   r.Header.Get("Mcp-Method"),
		NameHeader:     r.Header.Get("Mcp-Name"),
		SessionHeader:  r.Header.Get("Mcp-Session-Id"),
		Header:         r.Header.Clone(),
	}
	f.recordRequest(req)

	switch decoded.Method {
	case methodServerDiscover:
		f.handleDiscover(w, req)
	case "initialize", "notifications/initialized":
		// A real stateless 2026-07-28 server has no session handshake at
		// all, so under WithFakeRejectLegacyHandshake these methods do not
		// exist — the same -32601 a compliant server would return for any
		// unrecognized method.
		if f.strictModern() {
			writeJSONRPCError(w, http.StatusNotFound, -32601, "Method not found", nil)
			return
		}
		if decoded.Method == "initialize" {
			f.handleInitialize(w)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	case methodToolsList:
		f.handleToolsList(w, req)
	case methodToolsCall:
		f.handleToolsCall(w, req)
	default:
		writeJSONRPCError(w, http.StatusNotFound, -32601, "Method not found", nil)
	}
}

// strictModern reports whether WithFakeRejectLegacyHandshake was configured.
func (f *FakeMCPServer) strictModern() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rejectLegacyHandshake
}

// errCodeHeaderMismatch and errCodeInvalidParams (errorcodes.go) are the two
// JSON-RPC error regimes server/discover validation can produce. They are
// DISTINCT and must not be conflated — see errorcodes.go for the full
// rationale. Both regimes apply to tool traffic under strict mode —
// tools/list and tools/call now carry _meta, so enforceMetaFields runs for
// them too, and the header-vs-body protocolVersion comparison in
// enforceStandardHeaders is consequently live there as well.

// enforceStandardHeaders validates the streamable-HTTP transport header
// rules (A4): MCP-Protocol-Version and Mcp-Method are always required, and
// Mcp-Name's rule depends on whether the method names an entity.
//   - expectedName == "" (server/discover, tools/list): today's behavior — a
//     present Mcp-Name is a superfluous header with no spec-defined
//     rejection, so it is recorded as a non-rejecting violation and
//     enforceStandardHeaders still returns true.
//   - expectedName != "" (tools/call): a missing Mcp-Name, or one that does
//     not equal expectedName, is a rejecting violation — errCodeHeaderMismatch
//     / HTTP 400 — same as MCP-Protocol-Version and Mcp-Method.
//
// On a rejecting violation it writes the compliant-server response AND
// records a human-readable Violations() entry, then returns false so the
// caller does not also write a success response.
func (f *FakeMCPServer) enforceStandardHeaders(w http.ResponseWriter, req FakeRequest, expectedName string) bool {
	meta := decodeMeta(req.Params)
	_, hasProtocolVersionField := meta[metaKeyProtocolVersion]
	bodyProtocolVersion := metaString(meta, metaKeyProtocolVersion)

	if req.ProtocolHeader == "" {
		f.recordViolation("Header mismatch: MCP-Protocol-Version header is missing")
		writeJSONRPCError(w, http.StatusBadRequest, errCodeHeaderMismatch, "Header mismatch: MCP-Protocol-Version header is missing", nil)
		return false
	}
	// A missing _meta.protocolVersion BODY field cannot be compared against
	// the header — that is enforceMetaFields's failure instead, so the
	// comparison only applies when the body field is actually present (inert
	// on tool traffic, which carries no _meta at all).
	if hasProtocolVersionField && req.ProtocolHeader != bodyProtocolVersion {
		msg := fmt.Sprintf("Header mismatch: MCP-Protocol-Version header value %q does not match body value %q",
			req.ProtocolHeader, bodyProtocolVersion)
		f.recordViolation(msg)
		writeJSONRPCError(w, http.StatusBadRequest, errCodeHeaderMismatch, msg, nil)
		return false
	}
	if req.MethodHeader == "" {
		f.recordViolation("Header mismatch: Mcp-Method header is missing")
		writeJSONRPCError(w, http.StatusBadRequest, errCodeHeaderMismatch, "Header mismatch: Mcp-Method header is missing", nil)
		return false
	}
	if req.MethodHeader != req.Method {
		msg := fmt.Sprintf("Header mismatch: Mcp-Method header value %q does not match body value %q", req.MethodHeader, req.Method)
		f.recordViolation(msg)
		writeJSONRPCError(w, http.StatusBadRequest, errCodeHeaderMismatch, msg, nil)
		return false
	}
	if expectedName == "" {
		if req.NameHeader != "" {
			// Mcp-Name does not apply to this method. No spec-defined
			// rejection exists for a superfluous header, so record it and
			// continue; the assertion lives in the caller.
			f.recordViolation(fmt.Sprintf("Mcp-Name header %q present on %s (not applicable)", req.NameHeader, req.Method))
		}
		return true
	}
	if req.NameHeader != expectedName {
		msg := fmt.Sprintf("Header mismatch: Mcp-Name header value %q does not match expected %q", req.NameHeader, expectedName)
		f.recordViolation(msg)
		writeJSONRPCError(w, http.StatusBadRequest, errCodeHeaderMismatch, msg, nil)
		return false
	}
	return true
}

// enforceMetaFields validates the _meta body-field rules (A1) for a
// server/discover request: the client's own request must carry both
// required _meta fields. Callers run enforceStandardHeaders first — a
// server cannot trust the body until the headers that mirror it validate.
func (f *FakeMCPServer) enforceMetaFields(w http.ResponseWriter, req FakeRequest) bool {
	meta := decodeMeta(req.Params)
	_, hasProtocolVersionField := meta[metaKeyProtocolVersion]
	_, hasClientCapsField := meta[metaKeyClientCapabilities]

	if !hasProtocolVersionField {
		msg := "Invalid params: missing required _meta field " + metaKeyProtocolVersion
		f.recordViolation(msg)
		writeJSONRPCError(w, http.StatusBadRequest, errCodeInvalidParams, msg, nil)
		return false
	}
	if !hasClientCapsField {
		msg := "Invalid params: missing required _meta field " + metaKeyClientCapabilities
		f.recordViolation(msg)
		writeJSONRPCError(w, http.StatusBadRequest, errCodeInvalidParams, msg, nil)
		return false
	}

	return true
}

// decodeMeta extracts the request body's params._meta as a raw-message map,
// so presence of a key can be distinguished from an empty/zero value. nil on
// any decode failure (an absent or malformed _meta is itself a validation
// finding the caller handles).
func decodeMeta(params json.RawMessage) map[string]json.RawMessage {
	var body struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return nil
	}
	return body.Meta
}

// metaString reads a string-valued _meta field, returning "" when the key
// is absent or is not a JSON string.
func metaString(meta map[string]json.RawMessage, key string) string {
	raw, ok := meta[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// handleDiscover implements the per-mode server/discover response shapes.
func (f *FakeMCPServer) handleDiscover(w http.ResponseWriter, req FakeRequest) {
	f.mu.Lock()
	override := f.discoverStatusOverride
	mode := f.mode
	supported := append([]string(nil), f.supportedVersions...)
	serverInfoName := f.serverInfoName
	serverInfoVersion := f.serverInfoVersion
	f.mu.Unlock()

	if override != 0 {
		w.WriteHeader(override)
		return
	}

	switch mode {
	case FakeLegacy:
		// A legacy server predates server/discover entirely: HTTP 404 with a
		// plain-text body and no JSON-RPC envelope. A4 enforcement is off —
		// a legacy server knows nothing of these headers.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("404 page not found")) //nolint:errcheck

	case FakeVersionMismatch:
		if !f.enforceStandardHeaders(w, req, "") {
			return
		}
		if !f.enforceMetaFields(w, req) {
			return
		}
		writeJSONRPCError(w, http.StatusBadRequest, errCodeUnsupportedProtocolVersion, "Unsupported protocol version",
			unsupportedVersionData{Supported: supported, Requested: ProtocolVersion20260728})

	default: // FakeModern
		if !f.enforceStandardHeaders(w, req, "") {
			return
		}
		if !f.enforceMetaFields(w, req) {
			return
		}
		result := map[string]any{
			"resultType":        "complete",
			"supportedVersions": supported,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
		}
		// Both empty models a compliant server that sends no serverInfo at
		// all — the _meta key is omitted entirely rather than sent with
		// empty strings.
		if serverInfoName != "" || serverInfoVersion != "" {
			result["_meta"] = map[string]any{
				metaKeyServerInfo: map[string]any{
					"name":    serverInfoName,
					"version": serverInfoVersion,
				},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      1,
			"result":  result,
		})
	}
}

// handleInitialize serves the legacy initialize handshake in every mode
// (see the file-level compatibility note).
func (f *FakeMCPServer) handleInitialize(w http.ResponseWriter) {
	f.mu.Lock()
	negotiated := f.legacyNegotiatedVersion
	f.mu.Unlock()

	w.Header().Set("Mcp-Session-Id", "fake-session")
	w.Header().Set("Content-Type", "application/json")
	result := map[string]any{}
	if negotiated != "" {
		result["protocolVersion"] = negotiated
	}
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      1,
		"result":  result,
	})
}

// handleToolsList serves the configured tools by default (see the
// file-level compatibility note); under WithFakeRejectLegacyHandshake it
// also enforces the standard transport headers, then the _meta body fields
// — tools/list names no entity, so expectedName is "".
func (f *FakeMCPServer) handleToolsList(w http.ResponseWriter, req FakeRequest) {
	if f.strictModern() {
		if !f.enforceStandardHeaders(w, req, "") {
			return
		}
		if !f.enforceMetaFields(w, req) {
			return
		}
	}

	f.mu.Lock()
	tools := make([]Tool, len(f.tools))
	copy(tools, f.tools)
	hintSet := f.toolsListHintSet
	ttlMs := f.toolsListTTLMs
	toolsListCacheScope := f.toolsListCacheScope
	f.mu.Unlock()

	wireTools := make([]map[string]any, len(tools))
	for i, t := range tools {
		wireTools[i] = map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		}
	}
	result := map[string]any{
		"tools": wireTools,
	}
	if hintSet {
		result["ttlMs"] = ttlMs
		result["cacheScope"] = toolsListCacheScope
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      1,
		"result":  result,
	})
}

// handleToolsCall serves a deliberately dumb tools/call response (same
// discipline as FakeModern's documented "do not cross-check" note): no
// unknown-tool rejection. Its resultType field is driven by
// WithFakeToolResultType, which omits the field by default. Under
// WithFakeRejectLegacyHandshake it enforces the standard transport headers
// first, with expectedName set to the tool name the request body names, then
// the _meta body fields.
func (f *FakeMCPServer) handleToolsCall(w http.ResponseWriter, req FakeRequest) {
	name := paramsName(req.Params)
	if f.strictModern() {
		if !f.enforceStandardHeaders(w, req, name) {
			return
		}
		if !f.enforceMetaFields(w, req) {
			return
		}
	}

	f.mu.Lock()
	resultType := f.toolResultType
	inputRequests := f.toolInputRequests
	requestState := f.toolRequestState
	f.mu.Unlock()

	result := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": "called " + name},
		},
		"isError": false,
	}
	if resultType != "" {
		result["resultType"] = resultType
	}
	if inputRequests != nil {
		result["inputRequests"] = inputRequests
	}
	if requestState != nil {
		result["requestState"] = requestState
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      1,
		"result":  result,
	})
}

// paramsName decodes a tools/call request body's params.name, returning ""
// on any decode failure. An empty result is indistinguishable from "this
// method names no entity", so handleToolsCall passes it to
// enforceStandardHeaders as expectedName "" — the same non-rejecting branch
// tools/list and server/discover take — meaning an absent or malformed
// params.name is silently absorbed, not validated.
func paramsName(params json.RawMessage) string {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return ""
	}
	return body.Name
}

// writeJSONRPCError writes a JSON-RPC error envelope with the given HTTP
// status. data is omitted from the envelope when nil.
func writeJSONRPCError(w http.ResponseWriter, status, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	errObj := map[string]any{
		"code":    code,
		"message": message,
	}
	if data != nil {
		errObj["data"] = data
	}
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      1,
		"error":   errObj,
	})
}
