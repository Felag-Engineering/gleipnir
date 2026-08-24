// Package mcp — this file implements the host side of the `io.gleipnir/events`
// extension (ADR-054, mcp-realignment-spec.md §5).
//
// The extension exists for the same reason `io.gleipnir/channel` does: it is
// host-plane, not model-plane. The host decides to listen for events; a model
// never asks for it and must never be able to. The two extensions are
// siblings — `io.gleipnir/channel` carries messages OUT to a human,
// `io.gleipnir/events` carries signals IN to the host — and neither is ever
// reachable through `tools/call`.
//
// This file ships negotiation and `events/discover` only. `events/listen` —
// the long-lived streaming call — is issue #900's client; its wire contract
// is nonetheless frozen, in full, in the doc this file implements:
// docs/developer/extension-io-gleipnir-events.md.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ExtensionEvents is the reverse-DNS identifier a server declares in its
// server/discover `capabilities.extensions` map to advertise this extension.
const ExtensionEvents = "io.gleipnir/events"

// ExtensionEventsVersion is the contract version this client implements.
// SemVer from birth (spec §5's steward obligation): the moment a third party
// implements this extension, Gleipnir owes it the same deprecation
// discipline it expects of MCP itself.
const ExtensionEventsVersion = "1.0.0"

// methodEventsDiscover is the JSON-RPC method name for the discovery half of
// this extension. methodEventsListen and the events/event notification
// method are named in the doc but have no Go symbol yet — issue #900 adds
// the client that sends/receives them.
const methodEventsDiscover = "events/discover"

// EventsCapability is a server's declaration for this extension, decoded
// from the server/discover result's `capabilities.extensions["io.gleipnir/events"]`.
type EventsCapability struct {
	// Version is the contract version the server implements.
	Version string

	// Heartbeat is the interval at which the server commits to emitting an
	// SSE comment frame on an open events/listen stream (spec §5, doc §7).
	// Clamped to [minEventsHeartbeat, maxEventsHeartbeat]; zero means the
	// server sent no usable hint.
	Heartbeat time.Duration

	// MaxBatch is an optional hint for how many events the server may push
	// in one delivery. Carried but never consumed by this package today —
	// #900 decides whether/how to use it.
	MaxBatch int
}

// eventsCapabilityWire is the wire shape of the capability entry. Sub-fields
// decode as json.RawMessage so a malformed one (wrong type, out of range)
// cannot fail the whole eventsCapabilityWire unmarshal and flip a
// well-formed Version into the zero-value fallback — the same tolerance
// discipline decodeResultType and parseTaskDurationMs apply.
type eventsCapabilityWire struct {
	Version     string          `json:"version"`
	HeartbeatMs json.RawMessage `json:"heartbeatMs"`
	MaxBatch    json.RawMessage `json:"maxBatch"`
}

// minEventsHeartbeat and maxEventsHeartbeat bound a server's declared
// heartbeat cadence (doc §7: "mandatory heartbeat ... client treats 3x
// silence as a dead stream"). Sub-second is not a heartbeat, it is a
// firehose; longer than ten minutes makes the "3x silence" dead-stream
// signal too slow to be useful.
const (
	minEventsHeartbeat = 1 * time.Second
	maxEventsHeartbeat = 10 * time.Minute
)

// maxEventsMaxBatch bounds the currently-unconsumed maxBatch hint. Not
// policy — just a sanity ceiling so a hostile value cannot be carried
// forward unbounded into whatever future milestone starts reading it.
const maxEventsMaxBatch = 1000

// parseEventsCapability decodes an extensions-map entry. It is tolerant in
// the same way parseChannelCapability is — a malformed declaration yields a
// zero-value capability rather than failing the handshake — because a
// broken events declaration must not stop a server's TOOLS from working.
// The zero value discovers no event kinds (isModernProtocol/
// negotiatesGleipnirExtensions gate DiscoverEventKinds independently of
// this), so tolerance here cannot widen anything.
func parseEventsCapability(raw json.RawMessage) EventsCapability {
	var wire eventsCapabilityWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return EventsCapability{}
	}
	return EventsCapability{
		Version:   wire.Version,
		Heartbeat: parseTaskDurationMs(wire.HeartbeatMs, minEventsHeartbeat, maxEventsHeartbeat),
		MaxBatch:  parseEventsMaxBatch(wire.MaxBatch),
	}
}

// parseEventsMaxBatch decodes an optional maxBatch hint, clamping to
// [0, maxEventsMaxBatch]. Absent, null, non-numeric, or negative all decode
// to 0 — "no usable hint" — the same tolerant-decode posture
// parseTaskDurationMs applies to pollIntervalMs/ttlMs.
func parseEventsMaxBatch(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil || n < 0 {
		return 0
	}
	if n > maxEventsMaxBatch {
		n = maxEventsMaxBatch
	}
	return n
}

// EventKind is one event kind a server declared it may emit (doc §6:
// events/discover).
type EventKind struct {
	// Kind is the event-kind identifier, echoed as CloudEvents `type` on
	// every emitted event.
	Kind string

	// Guidance is server-authored prose shown to an operator binding a
	// policy to this kind. Untrusted, server-controlled text.
	Guidance string

	// BindingSchema is the JSON Schema for the typed binding filters a
	// policy may set on this kind (ADR-048). Held as raw bytes; never
	// interpreted by this package.
	BindingSchema json.RawMessage

	// Operators is the ADR-052 allowed-operator set per binding field: a map
	// from field name to the operator names a policy may use against it
	// (e.g. {"priority": ["eq", "in", "gt"]}). Decoded and stored, but
	// deliberately NEVER consumed here — ADR-052 decided operator
	// selectability but deferred implementation, and carrying the wire
	// shape now (rather than adding it later) keeps that adoption a
	// non-breaking minor version bump instead of a wire change.
	Operators map[string][]string
}

// eventKindWire is the wire shape of one events/discover result entry.
type eventKindWire struct {
	Kind          string              `json:"kind"`
	Guidance      string              `json:"guidance"`
	BindingSchema json.RawMessage     `json:"binding_schema,omitempty"`
	Operators     map[string][]string `json:"operators,omitempty"`
}

// eventsDiscoverParams is the params object for events/discover.
type eventsDiscoverParams struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

// eventsDiscoverResult is the wire shape of a successful events/discover
// response.
type eventsDiscoverResult struct {
	Kinds []eventKindWire `json:"kinds"`
}

// Bounds applied to every server-controlled string/collection an
// events/discover response carries. A plugin is external code; none of
// these are a realistic content size, they are a backstop against a
// hostile or buggy server (same posture as maxChannelResolutionFieldLen /
// ElicitationLimits).
const (
	// maxEventKindsPerResponse bounds how many kinds one response may list.
	// A legitimate manifest declares a handful of kinds; a much larger
	// number is not a realistic profile, it is a server padding a slice the
	// host has to walk on every discovery.
	maxEventKindsPerResponse = 64

	// maxEventKindNameLen bounds the kind identifier — it is echoed as
	// CloudEvents `type` on every subsequent event and reaches bindings,
	// logs, and audit records.
	maxEventKindNameLen = 128

	// maxEventGuidanceLen bounds the operator-facing prose. Longer than
	// server identity strings (maxServerInfoFieldLen) because guidance is
	// legitimately explanatory text, not an identifier.
	maxEventGuidanceLen = 2048

	// maxEventBindingSchemaBytes bounds one kind's raw binding schema.
	maxEventBindingSchemaBytes = 16 << 10 // 16 KiB

	// maxEventOperatorFields bounds how many binding fields one kind's
	// Operators map may declare allowed-operator sets for.
	maxEventOperatorFields = 32

	// maxEventOperatorsPerField bounds how many operator names one field
	// may list.
	maxEventOperatorsPerField = 16

	// maxEventOperatorNameLen bounds both a field name and an operator name
	// in the Operators map.
	maxEventOperatorNameLen = 64
)

// decodeEventKinds applies the bounds above to a raw events/discover
// result, preserving the server's own ordering — the only order this
// package has an opinion about is "no more than maxEventKindsPerResponse
// entries", never a reordering.
func decodeEventKinds(wire []eventKindWire) []EventKind {
	if len(wire) > maxEventKindsPerResponse {
		wire = wire[:maxEventKindsPerResponse]
	}
	out := make([]EventKind, 0, len(wire))
	for _, w := range wire {
		out = append(out, EventKind{
			Kind:          truncateForLog(w.Kind, maxEventKindNameLen),
			Guidance:      truncateForLog(w.Guidance, maxEventGuidanceLen),
			BindingSchema: boundEventBindingSchema(w.BindingSchema),
			Operators:     boundEventOperators(w.Operators),
		})
	}
	return out
}

// boundEventBindingSchema drops (rather than truncates) an oversize schema:
// truncating JSON produces invalid JSON, and a schema this package cannot
// use in full is not safely usable in part.
func boundEventBindingSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) > maxEventBindingSchemaBytes {
		return nil
	}
	return raw
}

// boundEventOperators bounds the Operators map's field count, per-field
// operator count, and every name's length. Map iteration order is
// unspecified by Go, but that is harmless here — Operators is never
// consumed today (see EventKind.Operators), so no ordering guarantee is
// owed to a caller yet.
func boundEventOperators(m map[string][]string) map[string][]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string][]string, min(len(m), maxEventOperatorFields))
	for field, ops := range m {
		if len(out) >= maxEventOperatorFields {
			break
		}
		bounded := make([]string, 0, min(len(ops), maxEventOperatorsPerField))
		for i, op := range ops {
			if i >= maxEventOperatorsPerField {
				break
			}
			bounded = append(bounded, truncateForLog(op, maxEventOperatorNameLen))
		}
		out[truncateForLog(field, maxEventOperatorNameLen)] = bounded
	}
	return out
}

// DiscoverEventKinds calls events/discover and returns the event kinds the
// server declared, in server order and with the bounds above applied.
//
// Refuses on two grounds, matching callTasksMethod's refusal shape: a
// legacy-pinned or never-probed client has no session that could possibly
// understand a 2026-07-28 extension, and a non-managed client must never
// negotiate an `io.gleipnir/*` method at all — the trust-tier drop in
// ProbeProtocolVersion already stops such a client from having a declared
// capability to call this against, but the method-call gate is repeated
// here as defense in depth, matching the discipline the doc's negotiation
// section requires.
func (c *Client) DiscoverEventKinds(ctx context.Context) ([]EventKind, error) {
	if !c.isModernProtocol() {
		return nil, fmt.Errorf("%s: requires the 2026-07-28 transport, server is pinned to %q",
			methodEventsDiscover, c.protocolVersion)
	}
	if !c.negotiatesGleipnirExtensions() {
		return nil, fmt.Errorf("%s: requires a managed plugin endpoint, this client is trust tier %q",
			methodEventsDiscover, c.TrustTier())
	}

	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      4,
		Method:  methodEventsDiscover,
		Params:  eventsDiscoverParams{Meta: c.requestMeta(ClientCapabilities{})},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", methodEventsDiscover, err)
	}

	// rpcName is "" — events/discover addresses no single named entity, the
	// same reasoning tools/list and server/discover apply (sendRPC's doc).
	resp, err := c.sendRPC(ctx, body, methodEventsDiscover, "", nil)
	if err != nil {
		return nil, fmt.Errorf("post %s: %w", methodEventsDiscover, err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := decodeResponse(resp, &envelope); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", methodEventsDiscover, err)
	}
	if envelope.Error != nil {
		return nil, envelope.Error
	}

	var result eventsDiscoverResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return nil, fmt.Errorf("unmarshal %s result: %w", methodEventsDiscover, err)
	}

	return decodeEventKinds(result.Kinds), nil
}

// EventsCapabilityOf returns the server's io.gleipnir/events declaration and
// whether it declared the extension at all.
//
// The two are distinct answers, same as ChannelCapabilityOf: a server that
// declared nothing is a server that emits no events — the "not declared"
// state is a first-class answer, not an error. A server that declared the
// extension with an unreadable body is a broken events plugin, and its
// zero-valued capability is the fail-closed direction.
//
// Requires a prior handshake; a Client that has not yet probed reports
// (zero, false).
func (c *Client) EventsCapabilityOf() (EventsCapability, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventsCap, c.eventsDeclared
}
