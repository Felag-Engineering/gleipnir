package mcpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Request is the JSON-RPC request envelope handed to a mounted extension's
// MethodFunc: the request id (to echo back verbatim in the response) and the
// raw params object. Params is left undecoded — each extension owns its own
// params shape, and this package has no reason to know it.
type Request struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

// MethodFunc handles one JSON-RPC method other than this server's own
// server/discover, tools/list, and tools/call. Server checks
// MCP-Protocol-Version and Mcp-Method before calling it — the A4 rules every
// method on this transport shares; anything method-specific, such as an
// Mcp-Name check against a target named in Params, is the implementation's
// own job, since only it knows what "named entity" means for its own
// method. A MethodFunc is responsible for writing exactly one JSON-RPC
// response, via WriteResult or WriteError.
type MethodFunc func(w http.ResponseWriter, r *http.Request, req Request)

// Extension is the structural interface Mount accepts. It is intentionally
// not a named interface an extension package must import: plugin-sdk's
// events, channelext, and similar packages each implement this shape
// independently, so a leaf package never needs to import mcpserver just to
// satisfy it, and mcpserver never needs to import a leaf package to declare
// it.
type Extension interface {
	// ExtensionID is the reverse-DNS identifier declared in server/discover's
	// capabilities.extensions map (e.g. "io.gleipnir/events").
	ExtensionID() string

	// Declaration is marshaled as capabilities.extensions[ExtensionID()].
	Declaration() any

	// Methods are the JSON-RPC methods this extension serves, keyed by
	// method name (e.g. "channel/notify").
	Methods() map[string]MethodFunc
}

// Mount adds ext's declaration to server/discover and wires its methods into
// this server's dispatch table. Returns an error — rather than panicking —
// on an empty or duplicate extension ID, or a method name that collides
// with another mounted extension, a streaming mount, or this server's own
// reserved methods (server/discover, tools/list, tools/call): a plugin
// wiring two extensions together should see that fail at startup with a
// clear reason, not discover it later from whichever handler happened to
// run.
func (s *Server) Mount(ext Extension) error {
	id := ext.ExtensionID()
	if id == "" {
		return fmt.Errorf("mcpserver: extension has an empty ExtensionID")
	}
	if _, dup := s.extensionDecls[id]; dup {
		return fmt.Errorf("mcpserver: extension %q already mounted", id)
	}

	methods := ext.Methods()
	for name := range methods {
		if s.methodTaken(name) {
			return fmt.Errorf("mcpserver: method %q (extension %q) collides with an already-mounted method", name, id)
		}
	}

	s.extensionDecls[id] = ext.Declaration()
	for name, fn := range methods {
		s.methods[name] = fn
	}
	return nil
}
