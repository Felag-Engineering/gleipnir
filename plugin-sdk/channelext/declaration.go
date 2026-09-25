package channelext

import (
	"fmt"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// ExtensionChannel is the reverse-DNS identifier this package declares in
// server/discover's capabilities.extensions map, matching the host client's
// own ExtensionChannel constant (internal/mcp/channel.go) byte for byte —
// the two sides implement the same wire contract independently.
const ExtensionChannel = "io.gleipnir/channel"

// ExtensionVersion is the contract version this package implements.
const ExtensionVersion = "1.0.0"

// Delivery vocabulary (contract §6): channel-neutral, replacing vendor terms
// like "DM" (spec §4.2 lists that as a scheduled-for-removal vendor-ism).
const (
	// DeliveryDirect addresses one person privately.
	DeliveryDirect = "direct"

	// DeliveryShared addresses a space several people can see.
	DeliveryShared = "shared"
)

func validDelivery(d string) bool {
	switch d {
	case DeliveryDirect, DeliveryShared:
		return true
	}
	return false
}

// Declaration is this server's io.gleipnir/channel capability entry
// (contract §4.1), rendered as server/discover's
// capabilities.extensions["io.gleipnir/channel"].
type Declaration struct {
	// Version is the contract version this server implements. Ordinarily
	// ExtensionVersion.
	Version string

	// Assurance is how strongly this channel authenticates the human who
	// acts on it: manifestv2.AssuranceAuthenticated or
	// manifestv2.AssuranceWeak. Reusing the manifest's own vocabulary here
	// rather than coining a parallel one — the manifest's human_channel
	// profile and this declaration describe the same fact.
	Assurance string

	// Deliveries are the delivery targets this server supports: DeliveryDirect
	// and/or DeliveryShared. A server that declares none supports none — the
	// host does not assume shared as a floor — so an empty list is refused by
	// Validate rather than accepted and silently routed to nobody.
	Deliveries []string
}

// Validate reports whether d is a declaration Declare should serve. New calls
// it before constructing an Extension: a broken declaration is a startup bug
// to fail loudly on, not a malformed handshake to tolerate — that tolerance
// belongs to the CLIENT side of this contract (parseChannelCapability in
// internal/mcp/channel.go), which must survive a broken plugin's declaration
// without losing its own tools.
func (d Declaration) Validate() error {
	if d.Version == "" {
		return fmt.Errorf("channelext: declaration version is required")
	}
	switch d.Assurance {
	case manifestv2.AssuranceAuthenticated, manifestv2.AssuranceWeak:
	default:
		return fmt.Errorf("channelext: declaration assurance %q is not %q or %q",
			d.Assurance, manifestv2.AssuranceAuthenticated, manifestv2.AssuranceWeak)
	}
	if len(d.Deliveries) == 0 {
		return fmt.Errorf("channelext: declaration deliveries is empty; a server declaring none supports none, so an empty list is refused rather than routed to nobody")
	}
	for _, delivery := range d.Deliveries {
		if !validDelivery(delivery) {
			return fmt.Errorf("channelext: declaration delivery %q is not %q or %q",
				delivery, DeliveryDirect, DeliveryShared)
		}
	}
	return nil
}

// declarationWire is the wire shape of the capability entry (contract §4.1).
type declarationWire struct {
	Version    string   `json:"version"`
	Assurance  string   `json:"assurance"`
	Deliveries []string `json:"deliveries"`
}

func (d Declaration) wire() declarationWire {
	return declarationWire{
		Version:    d.Version,
		Assurance:  d.Assurance,
		Deliveries: append([]string(nil), d.Deliveries...),
	}
}
