// Package hostendpoint is the host-side MCP server that managed plugins call
// for server→host callbacks (mcp-realignment-spec.md §8, ADR-057 as amended:
// gRPC and protobuf leave the system; both directions speak MCP).
//
// It is the replacement for the gRPC host-RPC plane in internal/plugin/hostsvc,
// which stays live until the cutover removes it (#883). This package currently
// delivers the skeleton (#875): the server/discover surface, the per-instance
// listener set, and the host-plane assertion. Method handlers, token-auth
// middleware, and the SDK client land in the milestone's later issues
// (#876–#882).
//
// # The host-plane invariant (normative, spec §8)
//
// The host endpoint is host-plane only: its tools are never registered in
// internal/toolregistry, never appear in any discovery a policy can reach, and
// are never grantable. The listener binds exclusively to the per-instance
// internal networks (#811), as a separate listener from the operator API.
//
// Under gRPC that separation was automatic — the protocols differed. With both
// directions speaking MCP it is a topology-and-registration rule that nothing
// enforces structurally, and a host tool leaking into the registry would be
// grantable to an agent: an ADR-001 capability-enforcement break. So the rule
// is asserted, not assumed: AssertHostPlane runs at startup and refuses to
// boot a process whose tool registry holds a host-endpoint tool name, and
// ListenerSet refuses wildcard bind addresses so "internal networks only"
// cannot be lost to a caller passing 0.0.0.0. The conformance-suite pin lands
// in milestone #20.
//
// # Modern-only
//
// The endpoint speaks the 2026-07-28 transport exclusively. There is no
// legacy `initialize` and no session: every plugin that can reach this
// listener ships against the same realignment contract, so bilingualism —
// which internal/mcp's CLIENT needs for arbitrary external servers — would
// here only mask a broken caller. A legacy handshake is answered with
// JSON-RPC method-not-found.
package hostendpoint
