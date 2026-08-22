// Package hostendpoint is the host-side MCP server that managed plugins call
// for server→host callbacks (mcp-realignment-spec.md §8, ADR-057 as amended:
// gRPC and protobuf leave the system; both directions speak MCP).
//
// It is the replacement for the gRPC host-RPC plane in internal/plugin/hostsvc,
// which stays live until the cutover removes it (#883). Delivered so far: the
// skeleton (#875 — server/discover, the per-instance listener set, the
// host-plane assertion) and the request middleware (#876 — the three gRPC
// interceptors ported with semantics unchanged: bearer-token auth via a
// TokenResolver, generation refcounting via generation.Controller, and
// Gleipnir-Call-Id correlation; Chain composes them in the canonical
// token → generation → call-id order); the six kept Tier-1 methods as tools
// behind that chain (#877 — tier1.go; EmitMetric's ADR-047 guard is shared
// with the gRPC plane via internal/plugin/pluginmetrics, and SetHealthState
// is per-capability via internal/plugin/caphealth); the Tier-2 methods with
// their manifest capability gate, policy-grant scoping, and
// unauthorized_tier2_call audit intact (#878 — tier2.go); AuthorizeActor
// with the ADR-058 verified-identity constraint behind ActorDirectory and
// the spec §6.4 poll-now hint (#879 — authorize.go); and SubmitIdentityProof
// plus GetUserConfig as thin entry points over the two #18 seams (#881 —
// userlink.go). With that, every method in the §8 inventory that this
// milestone keeps is served. Remaining: the SDK client (#882), gRPC removal
// (#883), and the contract doc (#884).
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
