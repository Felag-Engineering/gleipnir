package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
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

	// ListenEvents is what an events/listen stream replays, verbatim and in
	// this order, filtered by the request's cursor (see parseCursorSeq): an
	// event is replayed only when its Sequence is strictly greater than the
	// cursor's, mirroring doc §7.3's "resumes after the acked gleipnirseq".
	ListenEvents []CloudEvent

	// DelayBeforeEventsMs, when non-zero, sleeps that long after the stream
	// opens (headers sent and flushed) before writing anything — the seam a
	// test uses to exercise heartbeat-interval tolerance without a real
	// server race.
	DelayBeforeEventsMs int

	// InjectMalformedFrame, when true, writes one syntactically invalid
	// data: frame after replaying ListenEvents, instead of continuing
	// normally.
	InjectMalformedFrame bool

	// InjectOversizedFrame, when true, writes one data: frame larger than
	// the client's per-frame cap (maxSSEFrameBytes) after replaying
	// ListenEvents.
	InjectOversizedFrame bool

	// StallListen, when true, opens the stream and then sends nothing at
	// all — no events, no heartbeats — until the client disconnects. Models
	// a wedged connection for heartbeat-starvation tests.
	StallListen bool

	// CloseCleanly, when true, ends the stream with the doc §7.1 clean-close
	// response ({reason, cursor}) after replaying ListenEvents, rather than
	// heartbeating indefinitely.
	CloseCleanly bool
	CloseReason  string
	CloseCursor  string

	// BlockToolCall, when non-nil, makes tools/call block until the channel
	// is closed before replying with an empty success result.
	// FakeEventsServer has no tools of its own to actually call — this
	// exists solely to pin ListenEvents against the tools/call callGate
	// (issue #900's DoD: a full queue must not block a listen stream).
	BlockToolCall chan struct{}

	// ListenCalls counts how many events/listen requests this server
	// received.
	ListenCalls int
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
	case methodEventsListen:
		f.handleEventsListen(w, r, req.ID, req.Params)
	case methodToolsCall:
		// FakeEventsServer has no real tools; this exists only so a test can
		// saturate the client's per-server callGate (ListenEvents must not
		// be gated by it — see BlockToolCall's doc).
		f.handleToolsCallBlocking(w, req.ID)
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

func (f *FakeEventsServer) handleToolsCallBlocking(w http.ResponseWriter, id json.RawMessage) {
	f.mu.Lock()
	block := f.BlockToolCall
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.writeResult(w, id, map[string]any{
		"content":    []any{},
		"isError":    false,
		"resultType": ResultTypeComplete,
	})
}

// cloudEventOutWire is the wire shape one events/listen frame's params
// carries — CloudEvent, rendered back out onto the wire the way a real
// server would.
type cloudEventOutWire struct {
	SpecVersion string          `json:"specversion"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Time        string          `json:"time,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	GleipnirSeq uint64          `json:"gleipnirseq"`
}

// parseCursorSeq decodes an events/listen request's cursor into the
// gleipnirseq value to resume after. The real contract treats cursor as
// opaque (doc §7.2); this stub needs a concrete, deterministic convention to
// make the resume/replay tests exact, so it uses the decimal string form of
// a gleipnirseq value. An empty or unparseable cursor means "no cursor" —
// replay from the beginning.
func parseCursorSeq(cursor string) uint64 {
	if cursor == "" {
		return 0
	}
	n, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (f *FakeEventsServer) handleEventsListen(w http.ResponseWriter, r *http.Request, id, rawParams json.RawMessage) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	var params struct {
		Kinds  []string        `json:"kinds"`
		Scope  json.RawMessage `json:"scope"`
		Cursor string          `json:"cursor"`
	}
	json.Unmarshal(rawParams, &params) //nolint:errcheck // best-effort; a malformed request just replays everything

	f.mu.Lock()
	f.ListenCalls++
	events := append([]CloudEvent(nil), f.ListenEvents...)
	heartbeatMs := f.HeartbeatMs
	delayMs := f.DelayBeforeEventsMs
	malformed := f.InjectMalformedFrame
	oversized := f.InjectOversizedFrame
	stall := f.StallListen
	closeClean, closeReason, closeCursor := f.CloseCleanly, f.CloseReason, f.CloseCursor
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if delayMs > 0 {
		select {
		case <-time.After(time.Duration(delayMs) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}

	resumeAfter := parseCursorSeq(params.Cursor)
	for _, ev := range events {
		if ev.Sequence <= resumeAfter {
			continue // doc §7.3: replay resumes strictly after the acked cursor
		}
		if !f.writeEventFrame(w, flusher, ev) {
			return // client disconnected
		}
	}

	switch {
	case malformed:
		fmt.Fprint(w, "data: {not valid json\n\n") //nolint:errcheck // test seam
		flusher.Flush()
	case oversized:
		oversizedPayload := strings.Repeat("a", maxSSEFrameBytes+1024)
		fmt.Fprintf(w, "data: %s\n\n", oversizedPayload) //nolint:errcheck // test seam
		flusher.Flush()
	case stall:
		// Send nothing further at all — no events, no heartbeats — until
		// the client disconnects. Models a wedged connection for
		// heartbeat-starvation tests.
		<-r.Context().Done()
	case closeClean:
		f.writeCloseFrame(w, flusher, id, closeReason, closeCursor)
	default:
		// Nothing further queued: hold the connection open, heartbeating at
		// the declared cadence, until the client disconnects — mirrors a
		// real server with nothing further to say yet.
		heartbeat := time.Duration(heartbeatMs) * time.Millisecond
		if heartbeat <= 0 {
			heartbeat = 50 * time.Millisecond
		}
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

func (f *FakeEventsServer) writeEventFrame(w http.ResponseWriter, flusher http.Flusher, ev CloudEvent) bool {
	wire := cloudEventOutWire{
		SpecVersion: ev.SpecVersion,
		Source:      ev.Source,
		Type:        ev.Type,
		ID:          ev.ID,
		Data:        ev.Data,
		GleipnirSeq: ev.Sequence,
	}
	if wire.SpecVersion == "" {
		wire.SpecVersion = "1.0"
	}
	if !ev.Time.IsZero() {
		wire.Time = ev.Time.Format(time.RFC3339Nano)
	}
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  methodEventsEvent,
		"params":  wire,
	}
	payload, err := json.Marshal(notif)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func (f *FakeEventsServer) writeCloseFrame(w http.ResponseWriter, flusher http.Flusher, id json.RawMessage, reason, cursor string) {
	body := map[string]any{
		"jsonrpc": "2.0",
		"result":  map[string]any{"reason": reason, "cursor": cursor},
	}
	if len(id) > 0 {
		body["id"] = id
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", payload) //nolint:errcheck // test seam
	flusher.Flush()
}
