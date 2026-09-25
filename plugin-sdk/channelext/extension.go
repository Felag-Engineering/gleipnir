package channelext

import (
	"fmt"
	"net/http"

	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

// errCodeNotifyFailed is returned when a plugin's own NotifyFunc fails.
// -32603 is the standard JSON-RPC 2.0 "Internal error" code (reserved base
// range, JSON-RPC 2.0 §5.1); channel/notify has no delivery-specific error
// code of its own, per the contract's "coin no vocabulary" discipline (§2).
const errCodeNotifyFailed = -32603

// Extension is the mcpserver.Extension implementation of io.gleipnir/channel.
// The zero value is not usable; construct with New.
type Extension struct {
	decl   Declaration
	notify NotifyFunc
}

// New returns an Extension declaring decl and serving channel/notify with
// notify.
//
// decl is validated up front (Declaration.Validate): a broken declaration is
// a startup bug to fail loudly on, the same discipline
// mcpserver.RegisterTool and Server.Mount already apply. notify must not be
// nil — an extension with nothing to deliver notifications to has nothing to
// mount.
//
// channel/request is deliberately absent until a request handler exists
// (#967): Methods() serves channel/notify only, so the composed Server's own
// dispatch answers channel/request with method-not-found. That is the
// contract's worked example C (a notify-only channel with no input surface),
// not a gap in this package.
func New(decl Declaration, notify NotifyFunc) (*Extension, error) {
	if err := decl.Validate(); err != nil {
		return nil, err
	}
	if notify == nil {
		return nil, fmt.Errorf("channelext: notify handler is required")
	}
	return &Extension{decl: decl, notify: notify}, nil
}

// ExtensionID satisfies mcpserver.Extension.
func (e *Extension) ExtensionID() string { return ExtensionChannel }

// Declaration satisfies mcpserver.Extension: it is marshaled verbatim as
// server/discover's capabilities.extensions["io.gleipnir/channel"].
func (e *Extension) Declaration() any { return e.decl.wire() }

// Methods satisfies mcpserver.Extension.
func (e *Extension) Methods() map[string]mcpserver.MethodFunc {
	return map[string]mcpserver.MethodFunc{
		methodChannelNotify: e.handleNotify,
	}
}

// handleNotify serves channel/notify (contract §7). Server has already
// enforced the A4 header rules (MCP-Protocol-Version, Mcp-Method) shared by
// every method on this transport before calling this MethodFunc; everything
// here is specific to this method's own params shape.
func (e *Extension) handleNotify(w http.ResponseWriter, r *http.Request, req mcpserver.Request) {
	n, err := decodeNotification(req.Params)
	if err != nil {
		mcpserver.WriteError(w, req.ID, http.StatusBadRequest, mcpserver.ErrCodeInvalidParams, err.Error(), nil)
		return
	}

	if err := e.notify(r.Context(), n); err != nil {
		// The plugin's own handler failed — an unrecognized address, most
		// commonly (checklist: "rejects an unknown address with a JSON-RPC
		// error, not a silent success"). Reported as a call failure, never as
		// the {} success result a healthy delivery gets.
		mcpserver.WriteError(w, req.ID, http.StatusInternalServerError, errCodeNotifyFailed,
			"channel/notify: "+err.Error(), nil)
		return
	}

	// Fire-and-forget still answers the RPC: the result says the channel
	// accepted the message, not that anyone read it (contract §7).
	mcpserver.WriteResult(w, req.ID, map[string]any{})
}
