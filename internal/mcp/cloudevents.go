// Package mcp — CloudEvents 1.0 envelope decoding for the `io.gleipnir/events`
// extension (ADR-054, mcp-realignment-spec.md §5, doc §7.3).
//
// CloudEvents supplies the envelope so this package coins no new vocabulary
// for it (doc §2) — every field below except Sequence is adopted verbatim
// from the CloudEvents 1.0 spec, not invented here.
package mcp

import (
	"encoding/json"
	"fmt"
	"time"
)

// CloudEvent is a decoded CloudEvents 1.0 envelope, carried as the `params`
// of one `events/event` JSON-RPC notification (doc §7.1, §7.3).
type CloudEvent struct {
	// SpecVersion is always "1.0" — DecodeCloudEvent rejects anything else.
	SpecVersion string

	// Source is the emitting server's own identifier.
	Source string

	// Type is the event kind (EventKind.Kind, doc §6), echoed verbatim.
	Type string

	// ID is the dedup key — consumed downstream by internal/plugin/dedup,
	// unchanged by this extension. Never empty on a value DecodeCloudEvent
	// returns.
	ID string

	// Time is the event timestamp. Zero when the server sent an absent or
	// unparseable `time` field — DecodeCloudEvent tolerates that rather than
	// rejecting the event, and the host substitutes observation time
	// downstream (doc §7.3).
	Time time.Time

	// Data is the payload, bounded to maxCloudEventDataBytes. Host-captured;
	// doc §5 governs where (if anywhere) it may reach model context.
	Data json.RawMessage

	// Sequence is the value of the "gleipnirseq" CloudEvents extension
	// attribute — the ONE Gleipnir-coined name in this entire contract (doc
	// §2). CloudEvents extension-attribute names must be lowercase
	// alphanumeric and at most 20 characters; "gleipnirseq" (11 characters,
	// all lowercase ASCII letters) satisfies that rule by construction, so
	// this contract's one coined field is itself spec-compliant CloudEvents,
	// not an exception to it (doc §7.3). The cursor a reconnecting client
	// sends echoes this value.
	Sequence uint64
}

// cloudEventWire is the on-the-wire shape of a CloudEvents 1.0 JSON envelope.
// Time and GleipnirSeq decode as raw/string first so a malformed value can be
// classified precisely (tolerant for Time, strict for GleipnirSeq) rather
// than failing the whole json.Unmarshal — the same tolerant-decode discipline
// eventsCapabilityWire and taskResultWire apply elsewhere in this package.
type cloudEventWire struct {
	SpecVersion string          `json:"specversion"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Time        json.RawMessage `json:"time"`
	Data        json.RawMessage `json:"data,omitempty"`
	GleipnirSeq json.RawMessage `json:"gleipnirseq"`
}

// maxCloudEventDataBytes bounds a CloudEvents envelope's `data` field.
//
// This duplicates internal/plugin/hostsvc.maxPayloadJSONBytes's figure
// (64 KiB — the cap already applied to the EmitEvent payload_json field)
// rather than importing that package, which would pull the gRPC-era plugin
// substrate into internal/mcp — a leaf of the protocol layer that must not
// depend on internal/plugin (ADR-001 package boundary). Keep the two
// constants numerically in sync if either changes.
const maxCloudEventDataBytes = 64 * 1024

// maxCloudEventSourceLen and maxCloudEventIDLen bound the corresponding
// untrusted, server-controlled envelope strings before they reach logs,
// audit records, or the downstream dedup store — the same bounded-
// untrusted-string discipline maxServerInfoFieldLen (meta.go) and
// maxChannelResolutionFieldLen (channel.go) apply. Type reuses
// maxEventKindNameLen (events.go) rather than a third constant: it IS the
// event kind, echoed verbatim (doc §7.3's "type: The event kind
// (EventKind.Kind from §6)").
const (
	maxCloudEventSourceLen = 256
	maxCloudEventIDLen     = 256
)

// DecodeCloudEvent decodes one CloudEvents 1.0 envelope from an
// `events/event` notification's `params`.
//
// Strict where load-bearing, tolerant elsewhere (doc §7.3): specversion,
// id, type, source, and gleipnirseq must all be present and well-formed —
// id is the dedup key, and an event this package could not identify or
// order is not a value worth returning half-decoded. time is tolerated:
// a missing or unparseable timestamp decodes to the zero value rather than
// failing the event, because the host substitutes its own observation time
// for that case downstream. data is bounded to maxCloudEventDataBytes; an
// oversize payload is REJECTED rather than truncated, matching
// boundEventBindingSchema's reasoning (events.go) — truncated JSON is not
// valid JSON, and a payload this package cannot carry in full is not
// safely usable in part.
func DecodeCloudEvent(raw json.RawMessage) (CloudEvent, error) {
	var wire cloudEventWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return CloudEvent{}, fmt.Errorf("decode cloudevents envelope: %w", err)
	}
	if wire.SpecVersion != "1.0" {
		return CloudEvent{}, fmt.Errorf("cloudevents envelope: specversion %q is not \"1.0\"", wire.SpecVersion)
	}
	if wire.ID == "" {
		return CloudEvent{}, fmt.Errorf("cloudevents envelope: id is empty; it is the dedup key")
	}
	if wire.Type == "" {
		return CloudEvent{}, fmt.Errorf("cloudevents envelope: type is empty")
	}
	if wire.Source == "" {
		return CloudEvent{}, fmt.Errorf("cloudevents envelope: source is empty")
	}
	if len(wire.Data) > maxCloudEventDataBytes {
		return CloudEvent{}, fmt.Errorf("cloudevents envelope: data is %d bytes, exceeds the %d byte cap",
			len(wire.Data), maxCloudEventDataBytes)
	}
	seq, err := decodeGleipnirSeq(wire.GleipnirSeq)
	if err != nil {
		return CloudEvent{}, fmt.Errorf("cloudevents envelope: %w", err)
	}

	return CloudEvent{
		SpecVersion: wire.SpecVersion,
		Source:      truncateForLog(wire.Source, maxCloudEventSourceLen),
		Type:        truncateForLog(wire.Type, maxEventKindNameLen),
		ID:          truncateForLog(wire.ID, maxCloudEventIDLen),
		Time:        parseCloudEventTime(wire.Time),
		Data:        wire.Data,
		Sequence:    seq,
	}, nil
}

// parseCloudEventTime tolerates an absent or malformed `time` field —
// including one that is not even a JSON string, e.g. a server sending
// {"time":12345} — returning the zero time.Time rather than an error. See
// DecodeCloudEvent's doc: this is the one field this package deliberately
// does not fail the envelope over.
func parseCloudEventTime(raw json.RawMessage) time.Time {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// decodeGleipnirSeq strictly decodes the gleipnirseq extension attribute: it
// is the cursor value a reconnecting client echoes back (doc §7.3), so
// "absent" and "present but not a usable uint64" are both rejections rather
// than a silent zero — a wrong sequence, unlike a wrong timestamp, would
// corrupt resume semantics rather than merely lose precision.
func decodeGleipnirSeq(raw json.RawMessage) (uint64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, fmt.Errorf("gleipnirseq is absent")
	}
	var seq uint64
	if err := json.Unmarshal(raw, &seq); err != nil {
		return 0, fmt.Errorf("gleipnirseq is not a usable uint64: %w", err)
	}
	return seq, nil
}
