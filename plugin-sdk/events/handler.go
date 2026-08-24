package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ExtensionEvents is the reverse-DNS identifier this package declares in
// server/discover's capabilities.extensions map (doc §4).
const ExtensionEvents = "io.gleipnir/events"

// ExtensionVersion is the contract version this package implements.
const ExtensionVersion = "1.0.0"

// ProtocolVersion is the MCP transport version both methods in this
// contract require (doc §4: "Both methods require the 2026-07-28
// transport.").
const ProtocolVersion = "2026-07-28"

// JSON-RPC method names this Handler serves. events/event is the
// notification method used for delivered events, not a method a client
// calls — named here because Handler renders it on the wire (doc §7.1).
const (
	methodServerDiscover = "server/discover"
	methodEventsDiscover = "events/discover"
	methodEventsListen   = "events/listen"
	methodEventsEvent    = "events/event"
)

// JSON-RPC error codes this Handler returns. CodeCursorUnknown is exported
// so a client-side test (or a future host client) can match on it directly
// rather than string-matching the message.
const (
	CodeCursorUnknown  = -32001 // reserved server-error range, JSON-RPC 2.0 §5.1
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// defaultHeartbeat is used when NewHandler is not given WithHeartbeat. 15s
// matches the worked example in doc §4.
const defaultHeartbeat = 15 * time.Second

// Handler is an http.Handler serving the full io.gleipnir/events server
// side: server/discover (extension negotiation), events/discover (kind
// discovery), and events/listen (SSE-framed delivery).
//
// The zero value is not usable; construct with NewHandler.
type Handler struct {
	source    string
	kinds     []Kind
	heartbeat time.Duration
	maxBatch  int
	buffer    *Buffer

	closeOnce   sync.Once
	closeCh     chan struct{}
	closeReason string

	// activeStreams counts open events/listen streams. Exposed only to this
	// package's own tests, which use it to prove a disconnected client's
	// stream loop actually exits rather than leaking.
	activeStreams atomic.Int64
}

// Option configures a Handler constructed by NewHandler.
type Option func(*Handler)

// WithHeartbeat overrides the SSE comment-frame interval declared in
// server/discover and actually emitted on every open events/listen stream.
// Defaults to 15s.
func WithHeartbeat(d time.Duration) Option {
	return func(h *Handler) { h.heartbeat = d }
}

// WithMaxBatch sets the maxBatch hint carried in server/discover (doc
// §4.1). Optional; omitted from the wire entirely when unset.
func WithMaxBatch(n int) Option {
	return func(h *Handler) { h.maxBatch = n }
}

// WithBuffer replaces the default in-memory ring Buffer with one the caller
// constructed — typically NewBufferWithStore, to back events/listen with
// real durability.
func WithBuffer(b *Buffer) Option {
	return func(h *Handler) { h.buffer = b }
}

// NewHandler returns a Handler declaring kinds and identifying itself as
// source on every event's CloudEvents "source" field.
//
// kinds is declared once here and is exactly what events/discover renders —
// see Kind's doc comment. Keeping a plugin's manifest event_kinds in
// agreement with kinds is the author's responsibility; this package has no
// visibility into the manifest of a separately-built plugin binary.
func NewHandler(source string, kinds []Kind, opts ...Option) *Handler {
	h := &Handler{
		source:    source,
		kinds:     append([]Kind(nil), kinds...),
		heartbeat: defaultHeartbeat,
		closeCh:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(h)
	}
	if h.buffer == nil {
		h.buffer = NewBuffer(0)
	}
	return h
}

// Publish assigns e a sequence number, persists it, and delivers it to
// every currently-open events/listen stream whose requested kinds include
// e.Type. Returns the assigned gleipnirseq.
func (h *Handler) Publish(ctx context.Context, e Event) (uint64, error) {
	stored, err := h.buffer.Publish(ctx, e, h.source)
	if err != nil {
		return 0, err
	}
	return stored.Seq, nil
}

// Close ends every open events/listen stream with a clean close: each
// stream sends the JSON-RPC response {reason, cursor} to its own request id
// (doc §7.1) before returning. reason is the doc's short, human-readable
// explanation (e.g. "shutting down for a redeploy"). Close is idempotent;
// only the first call's reason takes effect.
func (h *Handler) Close(reason string) {
	h.closeOnce.Do(func() {
		h.closeReason = reason
		close(h.closeCh)
	})
}

// ServeHTTP dispatches the three JSON-RPC methods this contract defines.
// Every request is a POST carrying one JSON-RPC message in the body — this
// is the streamable-HTTP shape the 2026-07-28 transport already uses (doc
// §7.1), not a bespoke framing.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req jsonrpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed JSON-RPC request", http.StatusBadRequest)
		return
	}

	switch req.Method {
	case methodServerDiscover:
		h.handleServerDiscover(w, req.ID)
	case methodEventsDiscover:
		h.handleEventsDiscover(w, req.ID)
	case methodEventsListen:
		h.handleEventsListen(w, r, req)
	default:
		writeJSONRPCError(w, req.ID, codeMethodNotFound, "method not found: "+req.Method)
	}
}

// handleServerDiscover renders the modern handshake result declaring this
// extension (doc §4).
func (h *Handler) handleServerDiscover(w http.ResponseWriter, id json.RawMessage) {
	decl := map[string]any{
		"version":     ExtensionVersion,
		"heartbeatMs": h.heartbeat.Milliseconds(),
	}
	if h.maxBatch > 0 {
		decl["maxBatch"] = h.maxBatch
	}

	writeJSONRPCResult(w, id, map[string]any{
		"supportedVersions": []string{ProtocolVersion},
		"capabilities": map[string]any{
			"extensions": map[string]any{ExtensionEvents: decl},
		},
	})
}

// handleEventsDiscover renders the kinds this Handler was constructed with.
func (h *Handler) handleEventsDiscover(w http.ResponseWriter, id json.RawMessage) {
	writeJSONRPCResult(w, id, discoverResult(h.kinds))
}

// listenParams is the events/listen request params (doc §7.2).
type listenParams struct {
	Kinds  []string        `json:"kinds,omitempty"`
	Scope  json.RawMessage `json:"scope,omitempty"` // reserved, opaque to this contract version
	Cursor string          `json:"cursor,omitempty"`
}

// handleEventsListen opens (or refuses) the long-lived SSE stream.
func (h *Handler) handleEventsListen(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	var params listenParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeJSONRPCError(w, req.ID, codeInvalidParams, "invalid events/listen params: "+err.Error())
			return
		}
	}

	seq, resume, err := parseCursor(params.Cursor)
	if err != nil {
		writeCursorUnknownError(w, req.ID)
		return
	}

	// Subscribe before computing the backlog, not after: any event
	// published in the window between the two would otherwise be missed
	// entirely. The resulting possible double-delivery (an event that lands
	// in both the backlog and the live channel) is resolved by lastSeqSent
	// below, not by ordering subscribe after Since.
	ch, cancel := h.buffer.subscribe()
	defer cancel()

	var backlog []StoredEvent
	if resume {
		backlog, err = h.buffer.Since(r.Context(), seq)
		if err != nil {
			if errors.Is(err, ErrCursorUnknown) {
				writeCursorUnknownError(w, req.ID)
			} else {
				writeJSONRPCError(w, req.ID, codeInternalError, "events/listen: "+err.Error())
			}
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	h.activeStreams.Add(1)
	defer h.activeStreams.Add(-1)

	lastSeqSent := seq
	for _, e := range backlog {
		if e.Seq <= lastSeqSent {
			continue // already delivered via the live channel race window
		}
		lastSeqSent = e.Seq
		if kindWanted(params.Kinds, e.Type) {
			writeEventNotification(w, e)
		}
	}
	flusher.Flush()

	heartbeat := time.NewTicker(h.heartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			// A dropped connection is not a clean close (doc §7.1); no
			// JSON-RPC response is owed, the stream just ends.
			return

		case <-h.closeCh:
			writeCloseResult(w, req.ID, h.closeReason, formatCursor(lastSeqSent))
			flusher.Flush()
			return

		case e, ok := <-ch:
			if !ok {
				return
			}
			if e.Seq <= lastSeqSent {
				continue
			}
			lastSeqSent = e.Seq
			if kindWanted(params.Kinds, e.Type) {
				writeEventNotification(w, e)
				flusher.Flush()
			}

		case <-heartbeat.C:
			writeHeartbeatComment(w)
			flusher.Flush()
		}
	}
}

// parseCursor decodes an events/listen cursor param. An absent cursor
// (empty string) means "no resume — start delivering new events only" and
// is always satisfiable. A non-empty cursor is a resume request; a value
// that does not parse as a gleipnirseq is treated the same as one the
// buffer cannot satisfy (err is used only to signal "malformed"; callers
// respond with the same cursor-unknown error either way).
func parseCursor(s string) (seq uint64, resume bool, err error) {
	if s == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, true, fmt.Errorf("malformed cursor %q: %w", s, err)
	}
	return n, true, nil
}

// formatCursor renders a gleipnirseq as the opaque cursor string the doc's
// wire shape expects.
func formatCursor(seq uint64) string {
	return strconv.FormatUint(seq, 10)
}

// kindWanted reports whether kind should be delivered to a listener that
// requested wanted. An empty wanted list means "no filter" — deliver
// everything this Handler declares, which is the safer default: a listener
// that forgot to filter should not silently receive nothing.
func kindWanted(wanted []string, kind string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, w := range wanted {
		if w == kind {
			return true
		}
	}
	return false
}
