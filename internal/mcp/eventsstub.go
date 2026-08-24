package mcp

import (
	"encoding/json"
	"net/http"
	"sync"
)

// FakeEventsServer is an in-process MCP server implementing the
// `io.gleipnir/events` extension, for testing the host client without a real
// plugin. Test-only seam; no production code reaches it — same posture as
// FakeChannelServer, and stdlib-only for the same reason: a production-
// imported package must never be coupled to *testing.T.
//
//	stub := mcp.NewFakeEventsServer()
//	srv := httptest.NewServer(stub); t.Cleanup(srv.Close)
//	client := mcp.NewClient(srv.URL, mcp.WithProtocolVersion(mcp.ProtocolVersion20260728), mcp.WithTrustTier(mcp.TrustTierManaged))
//
// This ships the server/discover + events/discover half only. Issue #900
// extends this same type with the events/listen stream; do not fork a
// second fake events server for that milestone.
type FakeEventsServer struct {
	mu sync.Mutex

	// Heartbeat and MaxBatch shape the capability declaration returned by
	// server/discover. Zero omits the corresponding wire field.
	HeartbeatMs int
	MaxBatch    int

	// DeclareExtension controls whether the capability entry appears at all.
	// False models a plain MCP server that does no events work.
	DeclareExtension bool

	// RawCapability, when non-nil, replaces the rendered capability entry
	// verbatim — the seam for testing a malformed declaration.
	RawCapability json.RawMessage

	// Kinds is what events/discover returns, verbatim and in this order.
	Kinds []EventKind

	// DiscoverCalls counts how many events/discover requests this server
	// received, so a test can assert the client called it at all.
	DiscoverCalls int
}

// NewFakeEventsServer returns a stub declaring the extension with no event
// kinds. Set Kinds before the client calls DiscoverEventKinds.
func NewFakeEventsServer() *FakeEventsServer {
	return &FakeEventsServer{DeclareExtension: true}
}

func (f *FakeEventsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case methodServerDiscover:
		// server/discover IS the modern handshake: the 2026-07-28 transport is
		// stateless, so this is the only place a modern server declares
		// anything, extensions included.
		f.writeResult(w, req.ID, f.discoverResult())
	case methodEventsDiscover:
		f.handleEventsDiscover(w, req.ID)
	default:
		f.writeError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// discoverResult renders the server/discover handshake response.
func (f *FakeEventsServer) discoverResult() map[string]any {
	return map[string]any{
		"supportedVersions": []string{ProtocolVersion20260728},
		"capabilities":      f.capabilities(),
		"_meta": map[string]any{
			"io.modelcontextprotocol/serverInfo": map[string]any{
				"name": "fake-events", "version": "0.0.1",
			},
		},
	}
}

// capabilities renders the extension declaration.
func (f *FakeEventsServer) capabilities() map[string]any {
	capabilities := map[string]any{}

	f.mu.Lock()
	declare := f.DeclareExtension
	raw := f.RawCapability
	heartbeatMs := f.HeartbeatMs
	maxBatch := f.MaxBatch
	f.mu.Unlock()

	if declare {
		var entry any
		if raw != nil {
			entry = raw
		} else {
			wire := map[string]any{"version": ExtensionEventsVersion}
			if heartbeatMs > 0 {
				wire["heartbeatMs"] = heartbeatMs
			}
			if maxBatch > 0 {
				wire["maxBatch"] = maxBatch
			}
			entry = wire
		}
		capabilities["extensions"] = map[string]any{ExtensionEvents: entry}
	}
	return capabilities
}

func (f *FakeEventsServer) handleEventsDiscover(w http.ResponseWriter, id json.RawMessage) {
	f.mu.Lock()
	f.DiscoverCalls++
	kinds := append([]EventKind(nil), f.Kinds...)
	f.mu.Unlock()

	wireKinds := make([]eventKindWire, len(kinds))
	for i, k := range kinds {
		wireKinds[i] = eventKindWire(k)
	}
	f.writeResult(w, id, eventsDiscoverResult{Kinds: wireKinds})
}

func (f *FakeEventsServer) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	body := map[string]any{"jsonrpc": "2.0", "result": result}
	if len(id) > 0 {
		body["id"] = id
	}
	json.NewEncoder(w).Encode(body) //nolint:errcheck // test seam
}

func (f *FakeEventsServer) writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	body := map[string]any{
		"jsonrpc": "2.0",
		"error":   map[string]any{"code": code, "message": message},
	}
	if len(id) > 0 {
		body["id"] = id
	}
	json.NewEncoder(w).Encode(body) //nolint:errcheck // test seam
}
