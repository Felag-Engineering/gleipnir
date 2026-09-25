package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// DefaultMaxRequestBytes bounds a request body this server will read before
// refusing it, mirroring internal/mcp's maxJSONRPCPayloadBytes backstop on
// the client side of this same transport: generous enough for a legitimate
// tools/call argument payload, bounded so a crafted request cannot force
// unbounded buffering. Override with WithMaxRequestBytes.
const DefaultMaxRequestBytes = 32 << 20 // 32 MiB

// Server composes tools/list, tools/call, and any number of mounted
// extensions behind one server/discover — see the package doc for why a
// managed plugin needs exactly one of these rather than one per profile.
//
// The zero value is not usable; construct with NewServer. RegisterTool,
// Mount, and MountStreaming must all run before the server is handed to a
// listener: none of them takes a lock, matching
// internal/plugin/hostendpoint.Server's discipline, because a tool or
// extension registered after traffic starts is one server/discover already
// answered without.
type Server struct {
	name    string
	version string

	maxRequestBytes int64

	tools map[string]Tool

	extensionDecls map[string]any
	methods        map[string]MethodFunc
	streaming      map[string]http.Handler
}

// Option configures a Server constructed by NewServer.
type Option func(*Server)

// WithMaxRequestBytes overrides DefaultMaxRequestBytes, the cap on how much
// of an incoming request body this server will read before refusing it.
func WithMaxRequestBytes(n int64) Option {
	return func(s *Server) { s.maxRequestBytes = n }
}

// NewServer returns a Server that identifies itself as name/version in
// server/discover's _meta serverInfo (the ADR-042 per-service version, in
// place of a proto package version). Register tools and mount extensions
// before handing it to a listener.
func NewServer(name, version string, opts ...Option) *Server {
	s := &Server{
		name:            name,
		version:         version,
		maxRequestBytes: DefaultMaxRequestBytes,
		tools:           make(map[string]Tool),
		extensionDecls:  make(map[string]any),
		methods:         make(map[string]MethodFunc),
		streaming:       make(map[string]http.Handler),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ServeHTTP dispatches every JSON-RPC method this server knows about. It
// answers on every path — the host dials http://<ip>:<port>/ with no further
// routing (package doc) — so the request's URL is never consulted.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// The transport is POST-only; anything else is not MCP traffic.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// http.MaxBytesReader makes an oversize body a plain read error below —
	// a JSON-RPC invalid-request response, not an unbounded buffer, a hang,
	// or a panic.
	body := http.MaxBytesReader(w, r.Body, s.maxRequestBytes)
	raw, err := io.ReadAll(body)
	if err != nil {
		WriteError(w, nil, http.StatusBadRequest, ErrCodeInvalidParams, "invalid JSON-RPC request body", nil)
		return
	}

	var req jsonrpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		WriteError(w, nil, http.StatusBadRequest, ErrCodeInvalidParams, "invalid JSON-RPC request body", nil)
		return
	}

	if h, ok := s.streaming[req.Method]; ok {
		// This server has already read the body once, to learn the method;
		// replay it byte for byte so the streaming handler can do its own
		// decode from the top, including request fields (kinds, scope,
		// cursor, ...) this server has no reason to know about.
		r.Body = io.NopCloser(bytes.NewReader(raw))
		h.ServeHTTP(w, r)
		return
	}

	switch req.Method {
	case methodServerDiscover:
		s.handleDiscover(w, r, req)
	case methodToolsList:
		s.handleToolsList(w, r, req)
	case methodToolsCall:
		s.handleToolsCall(w, r, req)
	default:
		fn, ok := s.methods[req.Method]
		if !ok {
			// Includes legacy `initialize`: this server is modern-only by
			// construction, so the legacy handshake is an unimplemented
			// method here, not a dialect to negotiate down to.
			WriteError(w, req.ID, http.StatusNotFound, ErrCodeMethodNotFound,
				"method not found: "+req.Method, nil)
			return
		}
		if !s.enforceMethodHeaders(w, r, req, "") {
			return
		}
		fn(w, r, Request{ID: req.ID, Method: req.Method, Params: req.Params})
	}
}

// enforceMethodHeaders applies the A4 header rules every method on this
// transport shares: MCP-Protocol-Version and Mcp-Method are required, and
// Mcp-Method must match the request body's method. When expectedName is
// non-empty (tools/call), Mcp-Name must also equal it — the one case this
// server itself knows what "named entity" means for a method, since the
// tool name is this server's own dispatch key. A mounted extension method
// that needs its own Mcp-Name check (e.g. against a target named in its
// params) does that itself; it has the raw *http.Request to do so.
func (s *Server) enforceMethodHeaders(w http.ResponseWriter, r *http.Request, req jsonrpcRequest, expectedName string) bool {
	if r.Header.Get("MCP-Protocol-Version") == "" {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header is missing", nil)
		return false
	}
	methodHeader := r.Header.Get("Mcp-Method")
	if methodHeader == "" {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header is missing", nil)
		return false
	}
	if methodHeader != req.Method {
		WriteError(w, req.ID, http.StatusBadRequest, ErrCodeHeaderMismatch,
			"Header mismatch: Mcp-Method header value does not match body method", nil)
		return false
	}
	if expectedName != "" {
		if name := r.Header.Get("Mcp-Name"); name != expectedName {
			WriteError(w, req.ID, http.StatusBadRequest, ErrCodeHeaderMismatch,
				"Header mismatch: Mcp-Name header value does not match called tool", nil)
			return false
		}
	}
	return true
}

// methodTaken reports whether name is already claimed by this server's own
// reserved methods, a previously mounted extension, or a streaming mount.
// Shared by Mount and MountStreaming so the two collide with each other, not
// just with themselves.
func (s *Server) methodTaken(name string) bool {
	switch name {
	case methodServerDiscover, methodToolsList, methodToolsCall:
		return true
	}
	if _, ok := s.methods[name]; ok {
		return true
	}
	if _, ok := s.streaming[name]; ok {
		return true
	}
	return false
}
