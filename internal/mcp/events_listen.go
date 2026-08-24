// Package mcp — `events/listen`, the long-lived streaming half of the
// `io.gleipnir/events` extension (ADR-054, mcp-realignment-spec.md §5, doc
// §7). events.go ships negotiation and `events/discover`; this file is the
// client issue #900 adds.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// methodEventsListen is the JSON-RPC method name for the streaming half of
// this extension. methodEventsEvent is the notification method every
// delivered event rides (doc §7.1) — the host never sends it, only decodes
// it.
const (
	methodEventsListen = "events/listen"
	methodEventsEvent  = "events/event"
)

// eventsListenRequestID is the fixed JSON-RPC id of the events/listen
// request that opens a stream. One stream carries exactly one such request
// for its whole lifetime — unlike tools/call, there is nothing to
// distinguish by id on this connection, so a fixed value (matching the
// package's other single-purpose method ids: ChannelNotify/ChannelRequest
// use 3, DiscoverEventKinds uses 4) is sufficient.
const eventsListenRequestID = 5

// FormatEventsCursor renders a delivered CloudEvent's Sequence as the
// opaque cursor string the events/listen `cursor` param carries (doc §7.2:
// "the sequence value to resume after, echoing a prior gleipnirseq"). The
// decimal rendering is the contract's wire shape, but callers should treat
// the result as opaque — store it, echo it, never do arithmetic on it.
func FormatEventsCursor(seq uint64) string {
	return strconv.FormatUint(seq, 10)
}

// errCodeEventsCursorUnknown is the JSON-RPC error code a server answers an
// events/listen request with when the resume cursor cannot be satisfied
// gap-free (doc §7.2). It sits in JSON-RPC 2.0 §5.1's reserved server-error
// range. Deliberately a local constant rather than an entry in
// errorcodes.go: that registry holds the core 2026-07-28 transport codes,
// and this one belongs to the io.gleipnir/events extension contract.
const errCodeEventsCursorUnknown = -32001

// EventsListenParams is the events/listen request body (doc §7.2).
type EventsListenParams struct {
	// Kinds is the event kinds this listener wants.
	Kinds []string

	// Scope is reserved for future narrowing (doc §7.2); opaque to this
	// contract version and passed through verbatim.
	Scope json.RawMessage

	// Cursor is the gleipnirseq value to resume after, echoing a value a
	// prior CloudEvent on this same listener carried. Empty on a first
	// connection.
	Cursor string
}

// eventsListenParamsWire is the wire shape of EventsListenParams.
type eventsListenParamsWire struct {
	Kinds  []string        `json:"kinds"`
	Scope  json.RawMessage `json:"scope,omitempty"`
	Cursor string          `json:"cursor,omitempty"`
	Meta   map[string]any  `json:"_meta,omitempty"`
}

// defaultEventsHeartbeat is the fallback interval this client uses to judge
// heartbeat liveness when the server's declared heartbeatMs decodes to zero
// (doc §4.1: a malformed or absent capability entry carries no usable
// heartbeat). A package-level var, not a const, so a test can shorten it
// rather than waiting out a realistic real-world interval — the injectable-
// value seam docs/developer/testing-patterns.md asks for time.Now(), applied
// here to a fixed duration instead.
var defaultEventsHeartbeat = 30 * time.Second

// eventsResponseHeaderTimeout bounds how long ListenEvents waits for the
// server to send response HEADERS — i.e. to begin the SSE stream — before
// giving up. It deliberately does NOT bound anything after that: a server
// that answers headers and then goes quiet is the heartbeat's problem
// (doc §7.4), not this timeout's.
const eventsResponseHeaderTimeout = 30 * time.Second

// maxSSEFrameBytes bounds one accumulated SSE `data:` frame — every
// continuation line joined, per the SSE multi-line grammar — before
// rejecting it. Larger than maxCloudEventDataBytes to leave headroom for the
// JSON-RPC/CloudEvents envelope wrapping the (already-capped) data field. An
// over-cap frame FAILS THE STREAM outright rather than being skipped: a
// skipped frame is a silently dropped event, which is worse than a dead
// stream the caller (#902's supervisor) can simply reconnect.
const maxSSEFrameBytes = 128 << 10 // 128 KiB

// ErrEventsHeartbeatTimeout, ErrEventsStreamClosed, and ErrEventsTransportError
// are the three terminal sentinels EventStream.Next can return, matching doc
// §7.1/§7.4's three distinguishable ways a stream ends. A caller (the
// supervisor, #902) branches on all three: heartbeat starvation and a clean
// close both call for a reconnect (with, for a clean close, the carried
// cursor); a transport error is the same "reconnect" outcome for most
// causes, but is kept distinct because it is also the bucket for a
// programming/protocol fault (a malformed or over-cap frame) rather than a
// timing signal.
var (
	// ErrEventsHeartbeatTimeout reports that the server missed three
	// consecutive declared heartbeats (doc §7.4) — a wedged connection whose
	// socket never signalled close.
	ErrEventsHeartbeatTimeout = errors.New("mcp: events/listen stream missed 3 consecutive heartbeats")

	// ErrEventsStreamClosed reports a clean server close: doc §7.1's
	// JSON-RPC response to the original events/listen id, carrying
	// {reason, cursor}. Use errors.As to recover the *EventsStreamClosed
	// wrapping this sentinel, which carries the reason and the resume
	// cursor.
	ErrEventsStreamClosed = errors.New("mcp: events/listen stream closed cleanly by the server")

	// ErrEventsCursorUnknown reports that the server refused to OPEN the
	// stream because the supplied resume cursor is one its buffer cannot
	// satisfy gap-free (doc §7.2: JSON-RPC error code -32001, sent instead
	// of a stream — a server that opened the stream anyway and replayed
	// "from now" would be hiding exactly the gap the cursor exists to
	// close). Unlike the three terminal sentinels below, this one is
	// returned by ListenEvents itself: no stream ever existed. The caller
	// (the supervisor, #902) treats it as "reset the stored cursor and
	// reconnect from empty, accepting the redelivery dedup absorbs".
	ErrEventsCursorUnknown = errors.New("mcp: events/listen resume cursor unknown to the server")

	// ErrEventsTransportError reports that the stream ended for a reason
	// other than a clean close or heartbeat starvation: a read error, a bare
	// connection drop (doc §7.1: not a clean close, "handled as a dead
	// stream"), a malformed frame, or an over-cap frame.
	ErrEventsTransportError = errors.New("mcp: events/listen stream ended with a transport error")
)

// EventsStreamClosed carries the {reason, cursor} payload of a clean server
// close (doc §7.1). errors.Is(err, ErrEventsStreamClosed) reports true for a
// value of this type via Unwrap.
type EventsStreamClosed struct {
	// Reason is the server's short, human-readable explanation.
	Reason string
	// Cursor is the ack point to resume from on reconnect.
	Cursor string
}

func (e *EventsStreamClosed) Error() string {
	return fmt.Sprintf("mcp: events/listen stream closed by server (reason=%q cursor=%q)", e.Reason, e.Cursor)
}

func (e *EventsStreamClosed) Unwrap() error { return ErrEventsStreamClosed }

// maxEventsCloseFieldLen bounds the server-controlled reason/cursor strings
// carried on a clean close — same bounded-untrusted-string discipline as
// maxChannelResolutionFieldLen.
const maxEventsCloseFieldLen = 256

// ListenEvents opens the long-lived events/listen stream (doc §7): a
// JSON-RPC POST whose response is held open and SSE-framed. The returned
// EventStream delivers events in order via Next until a terminal sentinel
// (see above) ends it; callers must Close it when done.
//
// Refuses on the same two grounds as DiscoverEventKinds, before any request
// is built or sent: a legacy-pinned or never-probed client has no session
// that could understand a 2026-07-28 extension, and a non-managed client
// must never negotiate an io.gleipnir/* method at all.
//
// Deliberately does NOT go through c.callGate: events/listen is a host-plane
// subscription opened once per server, not a tools/call invocation, and must
// never compete with — or be blocked by — the bounded tools/call queue
// (gate.go). A server saturated on tool calls has no bearing on whether the
// host may open its event stream.
func (c *Client) ListenEvents(ctx context.Context, p EventsListenParams) (*EventStream, error) {
	if !c.isModernProtocol() {
		return nil, fmt.Errorf("%s: requires the 2026-07-28 transport, server is pinned to %q",
			methodEventsListen, c.protocolVersion)
	}
	if !c.negotiatesGleipnirExtensions() {
		return nil, fmt.Errorf("%s: requires a managed plugin endpoint, this client is trust tier %q",
			methodEventsListen, c.TrustTier())
	}

	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      eventsListenRequestID,
		Method:  methodEventsListen,
		Params: eventsListenParamsWire{
			Kinds:  p.Kinds,
			Scope:  p.Scope,
			Cursor: p.Cursor,
			Meta:   c.requestMeta(ClientCapabilities{}),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", methodEventsListen, err)
	}

	req, err := c.newStreamRequest(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", methodEventsListen, err)
	}

	resp, err := c.streamHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("post %s: %w", methodEventsListen, err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		drainResponseBody(resp.Body)
		return nil, fmt.Errorf("post %s: %w", methodEventsListen,
			&HTTPStatusError{StatusCode: resp.StatusCode, Body: errBody})
	}

	// A server that refuses to open the stream answers with an ordinary
	// JSON-RPC error body (application/json), not an event stream (doc
	// §7.2's cursor-unknown case is the one the contract defines). Without
	// this check the refusal would be fed to the SSE reader and surface as
	// a shapeless transport error — the cross-module contract test
	// (sdkevents_integration_test.go) is what caught that.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		defer drainResponseBody(resp.Body)
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		var env struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &env); err == nil && env.Error != nil {
			if env.Error.Code == errCodeEventsCursorUnknown {
				return nil, fmt.Errorf("%w: %s", ErrEventsCursorUnknown, env.Error.Message)
			}
			return nil, fmt.Errorf("post %s: server refused the stream: jsonrpc error %d: %s",
				methodEventsListen, env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("post %s: expected text/event-stream response, got %q",
			methodEventsListen, ct)
	}

	return newEventStream(resp, c.eventsHeartbeatInterval()), nil
}

// eventsHeartbeatInterval returns the heartbeat the server declared in its
// server/discover capability, or defaultEventsHeartbeat when it declared
// none (doc §4.1's "malformed ⇒ zero value ⇒ no usable heartbeat" already
// resolves to 0 by the time this reads eventsCap).
func (c *Client) eventsHeartbeatInterval() time.Duration {
	c.mu.Lock()
	hb := c.eventsCap.Heartbeat
	c.mu.Unlock()
	if hb <= 0 {
		return defaultEventsHeartbeat
	}
	return hb
}

// newStreamRequest builds the events/listen HTTP request. events/listen
// addresses no single named entity (like tools/list, server/discover, and
// events/discover), so — matching sendRPC's Mcp-Name convention — no
// Mcp-Name header is set. Modern-only: this is reached only after
// ListenEvents' isModernProtocol gate, so there is no legacy session to
// attach.
func (c *Client) newStreamRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for _, h := range c.authHeaders {
		req.Header.Set(h.Name, h.Value)
	}
	req.Header.Set("MCP-Protocol-Version", c.protocolVersion)
	req.Header.Set("Mcp-Method", methodEventsListen)
	return req, nil
}

// streamHTTPClient returns a dedicated *http.Client for events/listen,
// cloned from c.httpClient with Timeout: 0.
//
// THE TRAP: NewClient configures c.httpClient with Timeout: 30 * time.Second
// (client.go), and net/http.Client.Timeout covers the ENTIRE round trip
// including reading the response body — exactly right for an ordinary
// tools/call, but events/listen's response body is a connection meant to
// stay open for as long as the listener wants events. Reusing c.httpClient
// here would silently kill every stream at 30 seconds. Timeout: 0 removes
// that ceiling; ResponseHeaderTimeout on the cloned Transport replaces it
// with the ceiling that actually applies to a stream — how long to wait for
// the server to START responding, not how long the response may last.
func (c *Client) streamHTTPClient() *http.Client {
	base := *c.httpClient
	base.Timeout = 0

	switch t := base.Transport.(type) {
	case nil:
		clone := http.DefaultTransport.(*http.Transport).Clone()
		clone.ResponseHeaderTimeout = eventsResponseHeaderTimeout
		base.Transport = clone
	case *http.Transport:
		clone := t.Clone()
		clone.ResponseHeaderTimeout = eventsResponseHeaderTimeout
		base.Transport = clone
	default:
		// A caller-supplied non-*http.Transport RoundTripper (WithHTTPClient)
		// is used as-is: this client has no way to set ResponseHeaderTimeout
		// on an implementation it does not own. That header-arrival ceiling
		// is defense in depth here, not the load-bearing guarantee — the
		// heartbeat (doc §7.4) is what actually detects a dead stream once
		// headers have arrived.
	}
	return &base
}

// frameKind classifies one parsed SSE frame handed from readLoop to Next.
type frameKind int

const (
	frameHeartbeat frameKind = iota
	frameEvent
	frameClose
	frameError
)

// parsedFrame is one unit readLoop sends on EventStream.frames.
type parsedFrame struct {
	kind   frameKind
	event  CloudEvent
	closed *EventsStreamClosed
	err    error
}

// EventStream is one open events/listen connection. Obtained from
// ListenEvents; Next delivers events (and internally absorbs heartbeats)
// until a terminal sentinel ends it, and Close releases the connection.
//
// Safe for concurrent Close and Next — see the doc on those methods for what
// "safe" means here.
type EventStream struct {
	resp      *http.Response
	reader    *bufio.Reader
	heartbeat time.Duration
	frames    chan parsedFrame

	mu          sync.Mutex
	terminalErr error

	closeOnce sync.Once
	closeErr  error
}

// errEventsStreamClosedByCaller is what a Next call returns after Close was
// called explicitly rather than the stream ending on its own. It is bucketed
// under ErrEventsTransportError — the caller already knows why it ended, so
// this only matters if Next is called again anyway.
var errEventsStreamClosedByCaller = fmt.Errorf("%w: closed by caller", ErrEventsTransportError)

func newEventStream(resp *http.Response, heartbeat time.Duration) *EventStream {
	s := &EventStream{
		resp:      resp,
		reader:    bufio.NewReader(resp.Body),
		heartbeat: heartbeat,
		frames:    make(chan parsedFrame, 1),
	}
	go s.readLoop()
	return s
}

// setTerminal records err as the stream's terminal error, first write wins —
// once a real reason (heartbeat timeout, clean close, transport error, or an
// explicit Close) is recorded, a later call must not overwrite it, so every
// subsequent Next observes the SAME reason the stream actually ended for.
func (s *EventStream) setTerminal(err error) {
	s.mu.Lock()
	if s.terminalErr == nil {
		s.terminalErr = err
	}
	s.mu.Unlock()
}

func (s *EventStream) terminal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalErr
}

// Next blocks until the next event, or one of the three terminal sentinels
// (ErrEventsHeartbeatTimeout, ErrEventsStreamClosed, ErrEventsTransportError)
// ends the stream, or ctx is done. Once the stream has ended, every
// subsequent Next call returns the same terminal error immediately.
//
// Concurrent Next calls are not a supported usage pattern — this method is
// meant to be called in a single reader loop — but they are race-free: no
// call ever mutates state another concurrent call reads without a lock.
func (s *EventStream) Next(ctx context.Context) (CloudEvent, error) {
	if err := s.terminal(); err != nil {
		return CloudEvent{}, err
	}

	missed := 0
	for {
		timer := time.NewTimer(s.heartbeat)
		select {
		case <-ctx.Done():
			timer.Stop()
			return CloudEvent{}, ctx.Err()

		case frame, ok := <-s.frames:
			timer.Stop()
			if !ok {
				// The reader goroutine exited without ever sending a
				// terminal frame — e.g. Close ran between two frames. The
				// stored terminal error (set by Close or an earlier Next
				// call) is authoritative; fall back to a transport error
				// only if nothing else recorded one.
				err := s.terminal()
				if err == nil {
					err = fmt.Errorf("%w: reader stopped without a reason", ErrEventsTransportError)
					s.setTerminal(err)
				}
				return CloudEvent{}, err
			}
			switch frame.kind {
			case frameHeartbeat:
				missed = 0
				continue
			case frameEvent:
				return frame.event, nil
			case frameClose:
				s.setTerminal(frame.closed)
				return CloudEvent{}, frame.closed
			case frameError:
				err := fmt.Errorf("%w: %v", ErrEventsTransportError, frame.err)
				s.setTerminal(err)
				return CloudEvent{}, err
			}

		case <-timer.C:
			missed++
			if missed >= 3 {
				s.setTerminal(ErrEventsHeartbeatTimeout)
				return CloudEvent{}, ErrEventsHeartbeatTimeout
			}
		}
	}
}

// Close releases the stream's connection. Idempotent — a second Close is a
// no-op returning the first call's result — and safe to call concurrently
// with Next, which observes ErrEventsTransportError (wrapping
// errEventsStreamClosedByCaller) on any subsequent call.
func (s *EventStream) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.resp.Body.Close()
		s.setTerminal(errEventsStreamClosedByCaller)
		// The reader goroutine may be blocked trying to send a frame nobody
		// will ever call Next() again to receive (a caller that Closes and
		// walks away). Drain it so that goroutine can observe the now-closed
		// body, finish, and close(s.frames) — without this, Close alone
		// could leak it.
		go func() {
			for range s.frames { //nolint:revive // drain-to-completion, no body needed
			}
		}()
	})
	return s.closeErr
}

// readLoop parses SSE frames from the connection and sends one parsedFrame
// per dispatchable unit (a comment/heartbeat line, or a fully-accumulated
// data: event) on s.frames, terminating on the first close/error frame.
//
// Reads via readSSELine, a bufio.Reader-based line reader — deliberately NOT
// bufio.Scanner, whose default 64 KiB token cap is smaller than a
// legitimate CloudEvents payload can be and would turn a large event into a
// parse error rather than a value (see readSSELine's doc).
func (s *EventStream) readLoop() {
	defer close(s.frames)

	var dataLines []string
	var dataLen int
	resetData := func() {
		dataLines = nil
		dataLen = 0
	}

	for {
		line, err := readSSELine(s.reader, maxSSEFrameBytes)
		if err != nil {
			s.frames <- parsedFrame{kind: frameError, err: err}
			return
		}

		switch {
		case line == "":
			if len(dataLines) == 0 {
				continue // a bare blank line with nothing accumulated is a no-op keep-alive
			}
			frame, dispatchErr := s.dispatchPayload(strings.Join(dataLines, "\n"))
			resetData()
			if dispatchErr != nil {
				s.frames <- parsedFrame{kind: frameError, err: dispatchErr}
				return
			}
			s.frames <- frame
			if frame.kind == frameClose {
				return
			}

		case strings.HasPrefix(line, ":"):
			// A comment frame (SSE spec: a line beginning ":") IS the
			// mandatory heartbeat (doc §7.4) — content-free, invisible to
			// events/event consumers.
			s.frames <- parsedFrame{kind: frameHeartbeat}

		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			dataLen += len(chunk)
			if dataLen > maxSSEFrameBytes {
				s.frames <- parsedFrame{kind: frameError, err: fmt.Errorf(
					"events/listen: sse frame exceeds %d byte cap", maxSSEFrameBytes)}
				return
			}
			dataLines = append(dataLines, chunk)

		case isIgnoredSSEField(line):
			// id:/event:/retry: are part of the SSE grammar but not read by
			// this client: SSE id: is never read (resume travels in
			// events/listen params only, doc §7.3), and event:/retry: carry
			// nothing this contract needs.
			continue

		default:
			// An unrecognized field name is ignored, not fatal — a future
			// minor version of the wire format may add fields this client
			// does not yet know about (doc §3's minor-version discipline).
			continue
		}
	}
}

// eventsFrameWire is the generic shape of one SSE data: frame's JSON
// payload: either an events/event notification (Method set, no ID) or the
// clean-close response to the original events/listen request (Result set,
// ID echoed).
type eventsFrameWire struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// eventsCloseResultWire is the {reason, cursor} shape of a clean close's
// result (doc §7.1).
type eventsCloseResultWire struct {
	Reason string `json:"reason"`
	Cursor string `json:"cursor"`
}

// dispatchPayload decodes one fully-accumulated SSE data: frame's JSON
// payload and classifies it as an event or a clean close. Any other shape —
// malformed JSON, a JSON-RPC error response, or a payload that is neither —
// is a protocol violation this stream cannot recover from and ends it.
func (s *EventStream) dispatchPayload(payload string) (parsedFrame, error) {
	var wire eventsFrameWire
	if err := json.Unmarshal([]byte(payload), &wire); err != nil {
		return parsedFrame{}, fmt.Errorf("events/listen: malformed sse frame: %w", err)
	}

	switch {
	case wire.Method == methodEventsEvent:
		ce, err := DecodeCloudEvent(wire.Params)
		if err != nil {
			return parsedFrame{}, fmt.Errorf("events/listen: %w", err)
		}
		return parsedFrame{kind: frameEvent, event: ce}, nil

	case wire.Error != nil:
		return parsedFrame{}, fmt.Errorf("events/listen: %w", wire.Error)

	case len(wire.Result) > 0:
		var result eventsCloseResultWire
		if err := json.Unmarshal(wire.Result, &result); err != nil {
			return parsedFrame{}, fmt.Errorf("events/listen: malformed clean-close result: %w", err)
		}
		return parsedFrame{kind: frameClose, closed: &EventsStreamClosed{
			Reason: truncateForLog(result.Reason, maxEventsCloseFieldLen),
			Cursor: truncateForLog(result.Cursor, maxEventsCloseFieldLen),
		}}, nil

	default:
		return parsedFrame{}, fmt.Errorf("events/listen: sse frame is neither an %s notification nor a clean-close response",
			methodEventsEvent)
	}
}

// readSSELine reads one line — up to and including the terminating '\n',
// trimmed of the trailing "\r\n"/"\n" — from r, refusing to buffer more than
// maxBytes.
//
// bufio.Reader.ReadString/ReadBytes has NO such bound on its own: unlike
// bufio.Scanner's fixed token cap (which this package deliberately avoids
// because the default 64 KiB is smaller than a legitimate CloudEvents
// payload can legally be, per maxCloudEventDataBytes), ReadString will
// happily accumulate an unbounded amount of data if the server never sends
// '\n'. ReadSlice returns bufio.ErrBufferFull when its own internal buffer
// fills before finding the delimiter — this loop treats that as "keep
// accumulating," checking OUR cap on every chunk, so a hostile or buggy
// server cannot exhaust memory by simply omitting a newline.
func readSSELine(r *bufio.Reader, maxBytes int) (string, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxBytes {
			return "", fmt.Errorf("events/listen: sse frame exceeds %d byte cap", maxBytes)
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return "", err
	}
	return strings.TrimRight(string(buf), "\r\n"), nil
}

// sseIgnoredFieldPrefixes are the SSE fields readLoop recognizes but does
// not act on — see isIgnoredSSEField's doc.
var sseIgnoredFieldPrefixes = []string{"id:", "event:", "retry:"}

// isIgnoredSSEField reports whether line is one of the SSE grammar's id:,
// event:, or retry: fields — recognized so readLoop's default case does not
// have to (a truly unrecognized field name falls through there instead, per
// doc §3's minor-version discipline), but never acted on.
func isIgnoredSSEField(line string) bool {
	for _, prefix := range sseIgnoredFieldPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
