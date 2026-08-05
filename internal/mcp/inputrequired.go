package mcp

import (
	"encoding/json"
	"fmt"
)

// metaKeyElicitationKind is the _meta key a managed plugin sets on one
// inputRequests entry to make the spec §6.1 permission-vs-information
// convention explicit rather than inferred from an empty requestedSchema.
const metaKeyElicitationKind = "io.gleipnir/elicitation-kind"

// ResultTypeInputRequired is the 2026-07-28 tools/call resultType meaning the
// server needs one or more elicitations answered before the call can
// complete (spec §6, SEP-2322 "Multi Round-Trip Requests"). Unlike
// ResultTypeComplete, this package DOES branch on this value: CallTool
// decodes the accompanying inputRequests/requestState onto
// ToolResult.InputRequired and enforces the spec §6.2 abuse-control caps at
// decode time. It still does not pause a run, route to an audience, or touch
// any run state — that is the caller's job, deferred to a later milestone.
const ResultTypeInputRequired = "input_required"

// ElicitationLimits bounds what one input_required result may carry (spec
// §6.2 cap 2). The zero value means "use the defaults below", so a Client
// constructed without limits behaves exactly as it did before the caps became
// configurable.
//
// These are host self-protection, not policy: they are configured once at
// startup and applied to every server, because the thing being bounded is what
// a possibly-hostile server can push at the host and its operators.
type ElicitationLimits struct {
	MaxRequestStateBytes int
	MaxRequests          int
	MaxRequestsBytes     int
}

// resolve fills any unset (zero or negative) field with its default. A
// misconfigured value can disable a cap only by being explicitly larger, never
// by being absent.
func (l ElicitationLimits) resolve() ElicitationLimits {
	if l.MaxRequestStateBytes <= 0 {
		l.MaxRequestStateBytes = defaultMaxRequestStateBytes
	}
	if l.MaxRequests <= 0 {
		l.MaxRequests = defaultMaxInputRequests
	}
	if l.MaxRequestsBytes <= 0 {
		l.MaxRequestsBytes = defaultMaxInputRequestsBytes
	}
	return l
}

// defaultMaxRequestStateBytes bounds the opaque requestState blob a server may
// return alongside an input_required result (spec §6.2: "size caps on
// persisted requestState (bytes) and inputRequests (count + bytes); oversize
// is rejected as a structural error"). requestState is never interpreted by
// this package -- it is stored and replayed verbatim on the MRTR retry
// (CallOptions.RequestState) -- so this is purely a memory/persistence
// backstop against a hostile or buggy server, not a realistic content-size
// limit.
const defaultMaxRequestStateBytes = 16 << 10 // 16 KiB

// defaultMaxInputRequests bounds how many elicitations one input_required result
// may bundle (spec §6.2 "inputRequests (count ...)"). A legitimate MRTR round
// trip asks for a small, human-answerable batch; a larger number is not a
// realistic ask, it is an attempt to flood the operator or the audience
// routing this feeds (spec §6.2's rationale: "repetition fatigue-trains
// approvers").
const defaultMaxInputRequests = 8

// defaultMaxInputRequestsBytes bounds the serialized size of the inputRequests
// array (spec §6.2 "inputRequests (... bytes)"), independent of
// maxInputRequests -- a small number of entries can still be individually
// enormous (e.g. a pathological requestedSchema).
const defaultMaxInputRequestsBytes = 64 << 10 // 64 KiB

// maxInputRequiredReasonLen bounds InputRequiredError.Reason. Reason is
// built from this package's own fixed strings plus small integers (counts,
// byte lengths); the one exception is a json.Unmarshal error's own message
// wrapped verbatim (see decodeInputRequiredResult), which can in principle
// re-embed a fragment of the server's payload. Same bounded-everything
// posture as maxHeaderParamReasonLen in headerparams.go.
const maxInputRequiredReasonLen = 256

// InputRequiredError reports that an input_required tools/call result failed
// to decode -- either it does not parse or it exceeds one of the spec §6.2
// caps. CallTool returns it wrapped: the call fails and nothing is
// persisted. Exported so a caller can recover it with errors.As.
type InputRequiredError struct {
	Reason string // bounded to maxInputRequiredReasonLen; see newInputRequiredError
}

func (e *InputRequiredError) Error() string {
	return fmt.Sprintf("mcp input_required result rejected: %s", e.Reason)
}

func newInputRequiredError(reason string) *InputRequiredError {
	return &InputRequiredError{Reason: truncateForLog(reason, maxInputRequiredReasonLen)}
}

// inputRequestWire is the wire shape of one inputRequests entry (SEP-2322 /
// ElicitRequest-shaped: message + requestedSchema). Meta is json.RawMessage,
// decoded tolerantly by parseElicitationKind, for the same reason
// ServerInfo's _meta is (meta.go): a server that omits or mangles it is
// still a usable input_required result, just without an explicit
// elicitation-kind.
type inputRequestWire struct {
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema"`
	Meta            json.RawMessage `json:"_meta,omitempty"`
}

// InputRequest is one elicitation the server is asking the operator to
// answer before a tools/call can complete (spec §6, §6.1).
type InputRequest struct {
	// Message is server-controlled text describing what is being asked.
	// Untrusted: render as content, never as markup or instructions (spec
	// §6.1: "Elicitation messages are server-controlled text rendered as
	// untrusted content everywhere").
	Message string

	// RequestedSchema is the JSON Schema of the fields being requested. An
	// object with no properties is the spec §6.1 convention for a
	// permission-only ask (approve/reject); a non-empty properties object
	// makes this an information request (form rendering). This package does
	// not distinguish the two cases itself -- that convention belongs to
	// whoever renders the request.
	RequestedSchema json.RawMessage

	// ElicitationKind is _meta["io.gleipnir/elicitation-kind"] when a managed
	// plugin declares it explicitly (spec §6.1); "" when the server omitted
	// it or it was not a JSON string.
	ElicitationKind string
}

// InputRequiredResult is the decoded MRTR input_required result (spec §6):
// a tools/call returned resultType "input_required" instead of completing.
// ToolResult.InputRequired is non-nil exactly when ToolResult.ResultType ==
// ResultTypeInputRequired.
type InputRequiredResult struct {
	InputRequests []InputRequest

	// RequestState is the opaque blob the server expects back, byte-identical,
	// on the retry tools/call that answers these InputRequests (see
	// CallOptions.RequestState). This package never interprets it.
	RequestState json.RawMessage
}

// InputResponse is the operator's answer to one InputRequest from a prior
// input_required result. MRTR carries no per-request id, so a response is
// correlated to InputRequiredResult.InputRequests by array position (spec
// §6): InputResponses[i] answers InputRequests[i].
type InputResponse struct {
	// Action is the elicitation outcome: "accept", "decline", or "cancel".
	// This package does not validate Action against that vocabulary -- it is
	// round-tripped to the server, which owns it.
	Action string

	// Content is the operator-supplied answer payload, present only when
	// Action is "accept". nil for "decline"/"cancel".
	Content json.RawMessage
}

// inputResponseWire is the wire shape of one inputResponses entry sent on an
// MRTR retry tools/call.
type inputResponseWire struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
}

// parseElicitationKind returns the _meta["io.gleipnir/elicitation-kind"]
// string carried in rawMeta, or "" when rawMeta is absent/empty, is not a
// JSON object, the key is missing, its value is not a JSON string, or the
// JSON is malformed. Same tolerant-decode posture as parseServerInfo
// (meta.go): a server that omits or mangles this optional hint still
// produces a usable InputRequest.
func parseElicitationKind(rawMeta json.RawMessage) string {
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		return ""
	}
	raw, ok := meta[metaKeyElicitationKind]
	if !ok {
		return ""
	}
	var kind string
	if err := json.Unmarshal(raw, &kind); err != nil {
		return ""
	}
	return kind
}

// decodeInputRequiredResult decodes and validates result's inputRequests and
// requestState per the spec §6.2 caps. Called by CallTool only when the
// result's resultType normalizes to ResultTypeInputRequired -- an absent
// resultType (the legacy-pinned and pre-#792 cases) never reaches this
// function, so a legacy server's tools/call behavior is unchanged.
//
// Every rejection here is a structural error: the caller's CallTool fails
// the whole call rather than returning a partially-decoded
// InputRequiredResult. Unlike decodeResultType's tolerance for a
// non-compliant resultType (a value this package already accepted before
// #792 existed), a server that claims input_required but sends an
// unparseable or oversize payload is making a new, deliberately-interpreted
// claim it must back up.
func decodeInputRequiredResult(result toolsCallResult, limits ElicitationLimits) (InputRequiredResult, error) {
	limits = limits.resolve()

	if len(result.RequestState) == 0 {
		return InputRequiredResult{}, newInputRequiredError("missing requestState")
	}
	if len(result.RequestState) > limits.MaxRequestStateBytes {
		return InputRequiredResult{}, newInputRequiredError(fmt.Sprintf(
			"requestState is %d bytes, exceeds the %d-byte limit", len(result.RequestState), limits.MaxRequestStateBytes))
	}
	if len(result.InputRequests) > limits.MaxRequestsBytes {
		return InputRequiredResult{}, newInputRequiredError(fmt.Sprintf(
			"inputRequests is %d bytes, exceeds the %d-byte limit", len(result.InputRequests), limits.MaxRequestsBytes))
	}

	var wireRequests []inputRequestWire
	if err := json.Unmarshal(result.InputRequests, &wireRequests); err != nil {
		return InputRequiredResult{}, newInputRequiredError(fmt.Sprintf("inputRequests does not parse: %s", err))
	}
	if len(wireRequests) == 0 {
		return InputRequiredResult{}, newInputRequiredError("inputRequests is empty")
	}
	if len(wireRequests) > limits.MaxRequests {
		return InputRequiredResult{}, newInputRequiredError(fmt.Sprintf(
			"inputRequests has %d entries, exceeds the limit of %d", len(wireRequests), limits.MaxRequests))
	}

	requests := make([]InputRequest, len(wireRequests))
	for i, wr := range wireRequests {
		requests[i] = InputRequest{
			Message:         wr.Message,
			RequestedSchema: wr.RequestedSchema,
			ElicitationKind: parseElicitationKind(wr.Meta),
		}
	}

	return InputRequiredResult{InputRequests: requests, RequestState: result.RequestState}, nil
}
