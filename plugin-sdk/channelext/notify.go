package channelext

import (
	"context"
	"encoding/json"
	"fmt"
)

// methodChannelNotify is the JSON-RPC method name channel/notify serves
// (contract §7).
const methodChannelNotify = "channel/notify"

// Target addresses one delivery (contract §6).
type Target struct {
	// Delivery is DeliveryDirect or DeliveryShared.
	Delivery string

	// Address is the channel's own opaque identifier for the recipient or
	// space. This package never interprets it — what a channel calls an
	// address is the channel's own business.
	Address string
}

// Notification is one fire-and-forget delivery (contract §7).
//
// Ordered fan-out across an audience is the host dispatcher's job, not this
// package's or the plugin's: one call, one target.
type Notification struct {
	Target Target

	// Message is host-authored text. It is safe in the sense that the host,
	// not the model, wrote it, but a plugin's own NotifyFunc must still
	// render it as content rather than as markup or instructions — the host
	// may be relaying an untrusted payload inside it.
	Message string
}

// NotifyFunc delivers one Notification (contract §7). Returning an error
// fails the channel/notify call with a JSON-RPC error rather than a silent
// success — the checklist's "rejects an unknown address" case is exactly
// this: only the plugin author's handler knows what its own addresses are,
// so an unrecognized one is the handler's error to return, not this
// package's to guess at.
type NotifyFunc func(ctx context.Context, n Notification) error

// targetWire is the wire shape of a channel/notify target (contract §6).
type targetWire struct {
	Delivery string `json:"delivery"`
	Address  string `json:"address"`
}

// notifyParamsWire is the wire shape of channel/notify's params (contract
// §7). _meta is decoded and ignored: nothing this package does depends on
// it, and an unrecognized field must not fail a decode that otherwise
// succeeds.
type notifyParamsWire struct {
	Target  targetWire      `json:"target"`
	Message string          `json:"message"`
	Meta    json.RawMessage `json:"_meta,omitempty"`
}

// decodeTarget validates a wire target strictly against the delivery
// vocabulary this contract defines. Unlike the host client's tolerant
// decoding of a CAPABILITY declaration (parseChannelCapability in
// internal/mcp/channel.go), a per-call target is not a handshake to survive
// — an unrecognized delivery or a missing address is a caller compliance
// bug, and the checklist requires refusing it with a JSON-RPC error rather
// than guessing.
func decodeTarget(w targetWire) (Target, error) {
	if !validDelivery(w.Delivery) {
		return Target{}, fmt.Errorf("channel/notify: target.delivery %q is not %q or %q",
			w.Delivery, DeliveryDirect, DeliveryShared)
	}
	if w.Address == "" {
		return Target{}, fmt.Errorf("channel/notify: target.address is empty")
	}
	return Target{Delivery: w.Delivery, Address: w.Address}, nil
}

// decodeNotification decodes and validates a channel/notify params object.
func decodeNotification(raw json.RawMessage) (Notification, error) {
	var wire notifyParamsWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Notification{}, fmt.Errorf("channel/notify: invalid params: %w", err)
	}
	target, err := decodeTarget(wire.Target)
	if err != nil {
		return Notification{}, err
	}
	return Notification{Target: target, Message: wire.Message}, nil
}
