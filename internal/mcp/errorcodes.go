package mcp

// This file is the single authoritative registry for every JSON-RPC / MCP
// error code this package knows about. Production classification
// (discover.go), test fixtures (fakeserver.go), and Prometheus labeling
// (metrics.go) all cite the constants declared here, rather than each owning
// its own copy.
//
// 2026-07-28 allocation policy (basic/index.md "Error codes"):
//   - -32700..-32600 — the standard JSON-RPC 2.0 base range (parse error,
//     invalid request, method not found, invalid params, internal error).
//   - -32000..-32019 — legacy / implementation-defined. New implementations
//     SHOULD NOT allocate new codes here; receivers MUST NOT assume any
//     meaning for a code in this range, apart from -32002 (see
//     errCodeResourceNotFoundLegacy below).
//   - -32020..-32099 — reserved for the MCP spec itself. Implementations
//     MUST NOT emit a code in this sub-range that the spec does not define.
//     That MUST NOT is exactly what makes range membership proof of a modern
//     peer — classifyDiscoverResponse (discover.go) already depends on it.

// errCodeMCPReservedMax and errCodeMCPReservedMin bound the MCP-reserved
// sub-range (-32020..-32099, numerically "Max" is the end closer to zero).
// A code inside this range can only have been emitted by a server that
// speaks the MCP spec's error-code allocation, which is what lets
// classifyDiscoverResponse treat range membership as proof of a modern peer.
const (
	errCodeMCPReservedMax = -32020
	errCodeMCPReservedMin = -32099
)

// errCodeHeaderMismatch (-32020, HeaderMismatch) covers every required
// standard transport header failure per streamable-http.md §Server
// Validation: a required standard header (MCP-Protocol-Version, Mcp-Method,
// Mcp-Name) is missing, or a header value does not match the corresponding
// request body value.
const errCodeHeaderMismatch = -32020

// errCodeMissingRequiredClientCapability (-32021,
// MissingRequiredClientCapability) is returned when processing a request
// requires a capability the client did not declare in
// `_meta["io.modelcontextprotocol/clientCapabilities"]`. Its `data.requiredCapabilities`
// carries a ClientCapabilities OBJECT, not a string array — declaring
// `elicitation` per policy is a later milestone's job, so nothing in this
// package decodes that payload today; the code is named and classified and
// deliberately left undecoded.
const errCodeMissingRequiredClientCapability = -32021

// errCodeUnsupportedProtocolVersion (-32022, UnsupportedProtocolVersion) is
// returned when a server shares no protocol version with the client's
// request. Its `data.supported` lists the server's versions; decoded by
// classifyDiscoverResponse into unsupportedVersionData.
const errCodeUnsupportedProtocolVersion = -32022

// errCodeInvalidParams (-32602, the standard JSON-RPC Invalid Params code)
// covers ONLY a missing required `_meta` BODY field, per basic/index.md
// "Per-request protocol fields". It is OUTSIDE the MCP-reserved range, so
// classifyDiscoverResponse reads a -32602 responder as legacy — conflating
// it with errCodeHeaderMismatch would silently misread a client compliance
// bug (a forgotten `_meta` field) as "this server is old".
const errCodeInvalidParams = -32602

// errCodeResourceNotFoundLegacy (-32002) is the pre-2026-07-28 code for a
// `resources/read` "resource not found" error. The 2026-07-28 changelog
// renumbers it to errCodeInvalidParams (-32602, Invalid Params), but the
// change is additive, not a replacement: server/resources.md §Error handling
// says clients SHOULD still accept -32002 from older peers, so a bilingual
// client accepts BOTH spellings.
//
// This constant is documentation-only and drives no code path: this package
// issues exactly initialize, tools/list, tools/call, and server/discover
// (methodToolsList / methodToolsCall / methodServerDiscover) and never
// `resources/read`, so the renumbering is inert here. It is pinned to the
// same `protocol` metric label as every other code in this file by
// errorcodes_test.go, so the accept-both rule is written down for whoever
// adds a `resources/*` client — until then, do not invent a mapping between
// -32002 and -32602.
const errCodeResourceNotFoundLegacy = -32002
