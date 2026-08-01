package mcp

import (
	"encoding/json"

	"github.com/felag-engineering/gleipnir/internal/infra/version"
)

// _meta reserved keys used on every 2026-07-28 request (basic/index.md
// "Per-request protocol fields").
const (
	metaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaKeyClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaKeyServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// ClientCapabilities is the per-request capability declaration Gleipnir puts in
// _meta.clientCapabilities on every 2026-07-28 request (spec §11). The zero value
// — "declare nothing" — is the default on every call path in this repo today.
//
// There is deliberately NO Sampling field, and no option, setter, or config key
// that could produce one: sampling is deprecated upstream (SEP-2577) and ADR-026
// puts the LLM loop entirely in Gleipnir's hands, so a server must never be able
// to ask us to sample. A server cannot request what the client does not declare;
// that guarantee is only worth anything if the field cannot exist. Adding one is a
// hard-invariant violation, not a feature (TestClientCapabilities_HasNoSamplingKnob
// fails if a second field is ever added).
type ClientCapabilities struct {
	// Elicitation declares the MCP `elicitation` client capability. Set only where
	// the policy grants it; the HITL milestone (ADR-055) is what will flip it. The
	// form/url mode sub-fields of the capability object are deliberately not
	// modeled yet — see Key decisions.
	Elicitation bool
}

// wireObject returns the _meta.clientCapabilities object for cc. The
// returned map is always non-nil, even when nothing is declared: a nil map
// marshals to `null`, not `{}`, which would change the wire bytes of every
// request that declares no capability (including the server/discover probe).
func (cc ClientCapabilities) wireObject() map[string]any {
	obj := map[string]any{}
	if cc.Elicitation {
		obj["elicitation"] = map[string]any{}
	}
	return obj
}

// newRequestMeta builds the _meta object for one outbound 2026-07-28
// request. Constructs a fresh map per call — never a shared/global map (the
// #741 "no mutable aliases into a load-bearing global" precedent).
func newRequestMeta(protocolVersion string, caps ClientCapabilities) map[string]any {
	return map[string]any{
		metaKeyProtocolVersion: protocolVersion,
		metaKeyClientInfo: map[string]any{
			"name":    "gleipnir",
			"version": version.Version,
		},
		metaKeyClientCapabilities: caps.wireObject(),
	}
}

// requestMeta returns the _meta object for one outbound request, or nil when this
// client speaks the legacy transport.
//
// nil is the mechanism that keeps legacy request bytes IDENTICAL: every params
// struct tags its _meta field `omitempty`, so a nil map is omitted from the
// encoding entirely. The branch is isModernProtocol() — the exact same predicate
// sendRPC uses to pick the transport — so _meta and the modern transport headers
// can never disagree about which era a request belongs to.
//
// No lock, for the same reason isModernProtocol takes none: protocolVersion is
// written once by WithProtocolVersion during NewClient and never mutated.
func (c *Client) requestMeta(caps ClientCapabilities) map[string]any {
	if !c.isModernProtocol() {
		return nil
	}
	return newRequestMeta(c.protocolVersion, caps)
}

// ServerInfo is the server's self-reported identity from a 2026-07-28
// result's _meta["io.modelcontextprotocol/serverInfo"]. Diagnostic only.
//
// Both fields are UNTRUSTED, server-controlled strings. parseServerInfo bounds
// each to maxServerInfoFieldLen BEFORE this struct is ever populated, so no
// caller can accidentally log an unbounded value (same class as Findings 3/4
// of the #737 security review, which found an 8 MiB server string formatted
// into one slog record).
type ServerInfo struct {
	Name, Version string
}

const maxServerInfoFieldLen = 128

// parseServerInfo returns the ServerInfo carried in rawMeta, or the zero
// ServerInfo when rawMeta is absent/empty, is not a JSON object, the
// serverInfo key is missing, its value is not a JSON object, or any of the
// JSON is malformed — a server that omits or mangles _meta or serverInfo is
// still perfectly usable, so none of those cases is an error. rawMeta itself
// is untrusted and optional (discoverResult.Meta is a json.RawMessage for
// exactly this reason: it must decode for any valid JSON shape, so a
// malformed _meta can never affect protocol-era classification upstream of
// this function). Both fields are bounded through truncateForLog before this
// struct is ever populated.
func parseServerInfo(rawMeta json.RawMessage) ServerInfo {
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		return ServerInfo{}
	}
	raw, ok := meta[metaKeyServerInfo]
	if !ok {
		return ServerInfo{}
	}
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return ServerInfo{}
	}
	return ServerInfo{
		Name:    truncateForLog(info.Name, maxServerInfoFieldLen),
		Version: truncateForLog(info.Version, maxServerInfoFieldLen),
	}
}
