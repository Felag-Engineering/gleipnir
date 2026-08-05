// Package mcp — this file implements the host side of the `io.gleipnir/channel`
// extension (ADR-055 Amendment 1, mcp-realignment-spec.md §4 and §6.4).
//
// The extension exists because of one rule, stated in Amendment 1: *host-initiated
// ⇒ not a tool*. Delivering a message to a human is something the host decides to
// do, not something a model asks for, so it must not be reachable through
// `tools/call` — a grantable tool that posts to an operator's inbox is a tool an
// agent can be talked into using. Two methods, both host→plugin:
//
//   - `channel/notify` — fire-and-forget delivery.
//   - `channel/request` — ask a human something; returns a Tasks-extension task.
//
// The contract deliberately coins almost no vocabulary. `channel/request`'s
// payload is elicitation-shaped (message + requestedSchema + options) and the
// wait is a literal Tasks task, cancelled with `tasks/cancel` and answered by
// polling `tasks/get`. That is the design constraint, not an accident: if this
// extension ever grows its own request/response vocabulary, that is the signal
// it has drifted off the standard and should be re-derived from it.
//
// Full contract, versioning policy, and conformance checklist:
// docs/developer/extension-io-gleipnir-channel.md
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// ExtensionChannel is the reverse-DNS identifier a server declares in its
// initialize `capabilities.extensions` map to advertise this extension.
const ExtensionChannel = "io.gleipnir/channel"

// ExtensionChannelVersion is the contract version this client implements.
// SemVer from birth (spec §5's steward obligation, which §4 inherits): the
// moment a third party implements this, Gleipnir owes it the same deprecation
// discipline it expects of MCP itself.
const ExtensionChannelVersion = "1.0.0"

// Channel method names.
const (
	methodChannelNotify  = "channel/notify"
	methodChannelRequest = "channel/request"
)

// ChannelAssurance is how strongly a channel authenticates the human who acts
// on it (spec §4.1). It is declared by the server in its capability entry and
// is a claim about the transport, not about any individual message.
type ChannelAssurance string

const (
	// ChannelAssuranceAuthenticated means the channel identifies its actors:
	// the person who clicked is who the channel says they are.
	ChannelAssuranceAuthenticated ChannelAssurance = "authenticated"

	// ChannelAssuranceWeak means actor identity is forgeable or absent — an
	// email From: header being the canonical example.
	ChannelAssuranceWeak ChannelAssurance = "weak"
)

func (a ChannelAssurance) Valid() bool {
	switch a {
	case ChannelAssuranceAuthenticated, ChannelAssuranceWeak:
		return true
	}
	return false
}

// MayResolve reports whether a channel at this assurance level is allowed to
// settle an elicitation of the given kind (spec §4.1 default policy).
//
// The rule is the host's, not the plugin's, and it is asymmetric on purpose: a
// weak channel may supply *information*, because a wrong answer there is a
// wrong answer the agent then acts on visibly, but it may not grant
// *permission*, because a forged approval is indistinguishable from a real one
// after the fact. A weak channel does not fail the request — the dispatcher
// falls through to the next audience entry.
//
// An unrecognized assurance value resolves nothing. A server that declares a
// level this host does not know is a server whose guarantees this host cannot
// reason about, and guessing upward is the one direction that cannot be undone.
func (a ChannelAssurance) MayResolve(kind ElicitationKind) bool {
	switch a {
	case ChannelAssuranceAuthenticated:
		return true
	case ChannelAssuranceWeak:
		return kind == ElicitationKindInformation
	}
	return false
}

// ElicitationKind mirrors the spec §6.1 permission/information split for the
// purposes of the §4.1 assurance gate. It is a local alias of the vocabulary
// rather than an import of internal/model, because internal/mcp is a leaf of
// the protocol layer and must not depend on the domain model.
type ElicitationKind string

const (
	ElicitationKindPermission  ElicitationKind = "permission"
	ElicitationKindInformation ElicitationKind = "information"
)

// ChannelDelivery is where a message lands, in channel-neutral terms
// (spec §4.2: "DM" is a Slack-ism scheduled for removal — this vocabulary is
// what replaces it).
type ChannelDelivery string

const (
	// ChannelDeliveryDirect addresses one person privately.
	ChannelDeliveryDirect ChannelDelivery = "direct"

	// ChannelDeliveryShared addresses a space several people can see.
	ChannelDeliveryShared ChannelDelivery = "shared"
)

func (d ChannelDelivery) Valid() bool {
	switch d {
	case ChannelDeliveryDirect, ChannelDeliveryShared:
		return true
	}
	return false
}

// ChannelCapability is a server's declaration for this extension, decoded from
// the initialize result's `capabilities.extensions["io.gleipnir/channel"]`.
type ChannelCapability struct {
	// Version is the contract version the server implements.
	Version string

	// Assurance is the actor-authentication strength this channel claims.
	// Invalid or absent values decode to "" and resolve nothing (MayResolve).
	Assurance ChannelAssurance

	// Deliveries are the delivery targets the server supports. A server that
	// declares none supports none — the host does not assume `shared` as a
	// floor, because assuming a broadcast capability that is not there would
	// route a private question to nobody rather than failing loudly.
	Deliveries []ChannelDelivery
}

// Supports reports whether the server declared support for a delivery target.
func (c ChannelCapability) Supports(d ChannelDelivery) bool {
	for _, have := range c.Deliveries {
		if have == d {
			return true
		}
	}
	return false
}

// channelCapabilityWire is the wire shape of the capability entry.
type channelCapabilityWire struct {
	Version    string   `json:"version"`
	Assurance  string   `json:"assurance"`
	Deliveries []string `json:"deliveries"`
}

// maxChannelDeliveries bounds how many delivery targets one capability
// declaration may list. The vocabulary has two members; anything larger is a
// server padding a slice the host has to walk on every routing decision.
const maxChannelDeliveries = 8

// parseChannelCapability decodes an extensions-map entry. It is tolerant in the
// same way parseServerInfo is — a malformed declaration yields a zero-value
// capability rather than failing the handshake — because a broken channel
// declaration must not stop a server's TOOLS from working. The zero value
// resolves no elicitation kind and supports no delivery, so tolerance here
// cannot widen anything.
func parseChannelCapability(raw json.RawMessage) ChannelCapability {
	var wire channelCapabilityWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return ChannelCapability{}
	}

	cap := ChannelCapability{Version: wire.Version}
	if assurance := ChannelAssurance(wire.Assurance); assurance.Valid() {
		cap.Assurance = assurance
	}
	for i, d := range wire.Deliveries {
		if i >= maxChannelDeliveries {
			break
		}
		if delivery := ChannelDelivery(d); delivery.Valid() {
			cap.Deliveries = append(cap.Deliveries, delivery)
		}
	}
	return cap
}

// ChannelTarget addresses one delivery.
type ChannelTarget struct {
	// Delivery is direct or shared.
	Delivery ChannelDelivery

	// Address is the channel's own opaque identifier for the recipient or
	// space. The host never parses it: what a channel calls an address is the
	// channel's business, and a host that understood the format would be a
	// host with a per-channel special case.
	Address string
}

// ChannelNotification is one fire-and-forget delivery.
//
// Ordered fan-out across an audience's entries is the DISPATCHER's job, not the
// plugin's: a plugin that received a list and iterated it would be deciding
// routing policy, which is the host's. One notification, one target.
type ChannelNotification struct {
	Target ChannelTarget

	// Message is host-authored text. Unlike an elicitation message (which is
	// server-controlled and untrusted, spec §6.1), this direction is safe —
	// but a plugin must still render it as content, since the host may be
	// relaying an untrusted payload inside it.
	Message string
}

// ChannelOption is one answer a human may pick.
type ChannelOption struct {
	// ID is what comes back in the resolution. Opaque to the channel.
	ID string

	// Label is what the human reads.
	Label string
}

// ChannelRequestParams is one ask-a-human request. The payload is
// elicitation-shaped by design (§6.4): message, an optional requestedSchema for
// a form, and options for a pick-one.
type ChannelRequestParams struct {
	Target ChannelTarget

	// Message is what the human is being asked.
	Message string

	// RequestedSchema is the JSON Schema of a form, when the ask needs typed
	// values rather than a choice. Empty for a pick-one or a bare confirmation.
	RequestedSchema json.RawMessage

	// Options are the choices offered. Empty when RequestedSchema carries the
	// ask instead.
	Options []ChannelOption

	// Kind is the elicitation kind, sent so a channel can render a permission
	// prompt differently from an information form. It is NOT the enforcement
	// point — the §4.1 assurance gate runs host-side, before this request is
	// ever issued, because a rule enforced by the party it constrains is not a
	// rule.
	Kind ElicitationKind
}

// ChannelResolution is what a human actually did, decoded from the completed
// task's result payload.
type ChannelResolution struct {
	// OptionID is the chosen option's ID, for a pick-one ask.
	OptionID string

	// Content is the submitted form payload, for a schema ask.
	Content json.RawMessage

	// ActorExternalID is the channel's identifier for the person who acted.
	// Untrusted at this layer: it is the channel's claim, and how much that
	// claim is worth is exactly what ChannelAssurance measures. The audit
	// record stores both (spec §6.6) so an approval can be read as evidence
	// rather than as an assertion.
	ActorExternalID string
}

// channelResolutionWire is the wire shape of a completed request's result.
type channelResolutionWire struct {
	OptionID        string          `json:"optionId"`
	Content         json.RawMessage `json:"content,omitempty"`
	ActorExternalID string          `json:"actorExternalId"`
}

// maxChannelResolutionFieldLen bounds the identifier fields a channel returns.
// Same bounded-everything posture as maxServerInfoFieldLen: these strings reach
// audit records and logs, and a plugin is external.
const maxChannelResolutionFieldLen = 256

// DecodeChannelResolution parses a completed channel/request task's result.
//
// A terminal task with an unparseable result is an error rather than an empty
// resolution: the whole point of this call is to record what a human decided,
// and "we could not read the answer" must never be mistaken for "nobody
// answered" or, worse, for a specific answer.
func DecodeChannelResolution(result json.RawMessage) (ChannelResolution, error) {
	if len(result) == 0 {
		return ChannelResolution{}, fmt.Errorf("channel request completed with no result payload")
	}
	var wire channelResolutionWire
	if err := json.Unmarshal(result, &wire); err != nil {
		return ChannelResolution{}, fmt.Errorf("channel request result does not parse: %w", err)
	}
	if wire.OptionID == "" && len(wire.Content) == 0 {
		return ChannelResolution{}, fmt.Errorf("channel request result carries neither optionId nor content")
	}
	return ChannelResolution{
		OptionID:        truncateForLog(wire.OptionID, maxChannelResolutionFieldLen),
		Content:         wire.Content,
		ActorExternalID: truncateForLog(wire.ActorExternalID, maxChannelResolutionFieldLen),
	}, nil
}

// channelTargetWire / channelNotifyParams / channelRequestParamsWire are the
// on-the-wire shapes. They are separate from the exported structs so the Go
// field names can read naturally without pinning the JSON contract to them.
type channelTargetWire struct {
	Delivery string `json:"delivery"`
	Address  string `json:"address"`
}

type channelOptionWire struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type channelNotifyParams struct {
	Target  channelTargetWire `json:"target"`
	Message string            `json:"message"`
	Meta    map[string]any    `json:"_meta,omitempty"`
}

type channelRequestParamsWire struct {
	Target          channelTargetWire   `json:"target"`
	Message         string              `json:"message"`
	RequestedSchema json.RawMessage     `json:"requestedSchema,omitempty"`
	Options         []channelOptionWire `json:"options,omitempty"`
	Kind            string              `json:"kind,omitempty"`
	Meta            map[string]any      `json:"_meta,omitempty"`
}

// validateChannelTarget rejects a target the host should not have built.
// Checked client-side so a routing bug surfaces here rather than as a confusing
// plugin-side error, and so a malformed target never reaches the wire.
func validateChannelTarget(t ChannelTarget) error {
	if !t.Delivery.Valid() {
		return fmt.Errorf("channel target delivery %q is not %q or %q",
			t.Delivery, ChannelDeliveryDirect, ChannelDeliveryShared)
	}
	if t.Address == "" {
		return fmt.Errorf("channel target address is empty")
	}
	return nil
}

// ChannelNotify delivers a message and does not wait (spec §6.4).
//
// It returns an error only for a transport or protocol failure. Whether a human
// ever read the message is unknowable from here, and a channel that claimed
// otherwise would be claiming more than it can know — which is precisely why
// anything that needs an answer uses ChannelRequest instead.
func (c *Client) ChannelNotify(ctx context.Context, n ChannelNotification) error {
	if !c.isModernProtocol() {
		return fmt.Errorf("%s: requires the 2026-07-28 transport, server is pinned to %q",
			methodChannelNotify, c.protocolVersion)
	}
	if err := validateChannelTarget(n.Target); err != nil {
		return fmt.Errorf("%s: %w", methodChannelNotify, err)
	}

	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  methodChannelNotify,
		Params: channelNotifyParams{
			Target: channelTargetWire{
				Delivery: string(n.Target.Delivery),
				Address:  n.Target.Address,
			},
			Message: n.Message,
			Meta:    c.requestMeta(ClientCapabilities{}),
		},
	})
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", methodChannelNotify, err)
	}

	resp, err := c.sendRPC(ctx, body, methodChannelNotify, n.Target.Address, nil)
	if err != nil {
		return fmt.Errorf("post %s: %w", methodChannelNotify, err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := decodeResponse(resp, &envelope); err != nil {
		return fmt.Errorf("decode %s response: %w", methodChannelNotify, err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	return nil
}

// ChannelRequest asks a human something and returns the Tasks-extension task
// that represents the wait (spec §6.4: "the wait is a literal Tasks-extension
// task").
//
// The caller polls with GetTask, terminates with CancelTask, and reads the
// answer with DecodeChannelResolution once the task is terminal. Nothing about
// the wait is special to this extension, which is the point — durability,
// restart-resume, poll cadence, and TTL are all the Tasks extension's problem,
// already solved once.
func (c *Client) ChannelRequest(ctx context.Context, p ChannelRequestParams) (TaskStatus, error) {
	if !c.isModernProtocol() {
		return TaskStatus{}, fmt.Errorf("%s: requires the 2026-07-28 transport, server is pinned to %q",
			methodChannelRequest, c.protocolVersion)
	}
	if err := validateChannelTarget(p.Target); err != nil {
		return TaskStatus{}, fmt.Errorf("%s: %w", methodChannelRequest, err)
	}
	if p.Message == "" {
		return TaskStatus{}, fmt.Errorf("%s: message is empty; there is nothing to ask", methodChannelRequest)
	}
	if len(p.Options) == 0 && len(p.RequestedSchema) == 0 {
		// Neither a choice nor a form is not a question — it is a notification
		// that would leave a task open forever waiting for an answer the human
		// has no way to give.
		return TaskStatus{}, fmt.Errorf("%s: request carries neither options nor a requestedSchema; use %s for a message that needs no answer",
			methodChannelRequest, methodChannelNotify)
	}

	options := make([]channelOptionWire, len(p.Options))
	for i, o := range p.Options {
		options[i] = channelOptionWire(o)
	}

	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  methodChannelRequest,
		Params: channelRequestParamsWire{
			Target: channelTargetWire{
				Delivery: string(p.Target.Delivery),
				Address:  p.Target.Address,
			},
			Message:         p.Message,
			RequestedSchema: p.RequestedSchema,
			Options:         options,
			Kind:            string(p.Kind),
			Meta:            c.requestMeta(ClientCapabilities{}),
		},
	})
	if err != nil {
		return TaskStatus{}, fmt.Errorf("marshal %s request: %w", methodChannelRequest, err)
	}

	resp, err := c.sendRPC(ctx, body, methodChannelRequest, p.Target.Address, nil)
	if err != nil {
		return TaskStatus{}, fmt.Errorf("post %s: %w", methodChannelRequest, err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := decodeResponse(resp, &envelope); err != nil {
		return TaskStatus{}, fmt.Errorf("decode %s response: %w", methodChannelRequest, err)
	}
	if envelope.Error != nil {
		return TaskStatus{}, envelope.Error
	}

	var wire taskResultWire
	if err := json.Unmarshal(envelope.Result, &wire); err != nil {
		return TaskStatus{}, fmt.Errorf("unmarshal %s result: %w", methodChannelRequest, err)
	}
	if wire.TaskID == "" {
		// Without a task handle the wait is unaddressable: it cannot be
		// polled, cancelled, or resumed after a restart. That is a failed
		// request, not a request with a missing field.
		return TaskStatus{}, fmt.Errorf("%s: server returned no taskId; the wait would be unaddressable", methodChannelRequest)
	}
	status := decodeTaskStatusValue(wire.Status)
	if status == "" {
		return TaskStatus{}, fmt.Errorf("%s: server returned no usable task status", methodChannelRequest)
	}

	return TaskStatus{
		TaskID:        wire.TaskID,
		Status:        status,
		StatusMessage: decodeTaskStatusMessage(wire.StatusMessage),
		Result:        wire.Result,
		PollInterval:  parseTaskDurationMs(wire.PollIntervalMs, minTaskPollInterval, maxTaskPollInterval),
		TTL:           parseTaskDurationMs(wire.TtlMs, 0, maxTaskTTL),
	}, nil
}

// ChannelCapabilityOf returns the server's io.gleipnir/channel declaration and
// whether it declared the extension at all.
//
// The two are distinct answers. A server that declared nothing is a server that
// does not do channels — routing to it is a configuration error worth
// reporting. A server that declared the extension with an unreadable body is a
// broken channel plugin, and its zero-valued capability resolves nothing, which
// is the fail-closed direction.
//
// Requires a prior handshake; a Client that has not yet initialized reports
// (zero, false). Callers that need certainty should perform any call that
// establishes a session first.
func (c *Client) ChannelCapabilityOf() (ChannelCapability, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channelCap, c.channelDeclared
}
