package mcpserver

import (
	"fmt"
	"net/http"
)

// MountStreaming registers h to handle every request whose JSON-RPC method
// equals method, bypassing this server's own request decoding entirely —
// the seam an extension needs when it must read the request body itself
// (an SSE stream's events/listen, whose params — kinds, scope, cursor — this
// server has no reason to know about) and hold the connection open past the
// point an ordinary MethodFunc response returns.
//
// h receives the original *http.Request with its body replayed from the
// top: this server peeks at the JSON-RPC envelope only far enough to read
// "method", then hands h a Body that reads the exact bytes the client sent,
// byte for byte. h is responsible for its own header validation and
// response framing — MountStreaming applies none of the A4 checks the
// ordinary method dispatch does, because a streaming response is not a
// single JSON-RPC response this server could write on the handler's behalf.
func (s *Server) MountStreaming(method string, h http.Handler) error {
	if method == "" {
		return fmt.Errorf("mcpserver: streaming method name is required")
	}
	if h == nil {
		return fmt.Errorf("mcpserver: streaming handler for %q is nil", method)
	}
	if s.methodTaken(method) {
		return fmt.Errorf("mcpserver: method %q collides with an already-mounted method", method)
	}
	s.streaming[method] = h
	return nil
}
