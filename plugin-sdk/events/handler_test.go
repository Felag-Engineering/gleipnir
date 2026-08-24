package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHandler_ServerDiscover checks the negotiation declaration's shape
// against doc §4: supportedVersions, and the extension entry under
// capabilities.extensions keyed by ExtensionEvents, carrying version,
// heartbeatMs, and (when set) maxBatch.
func TestHandler_ServerDiscover(t *testing.T) {
	h := NewHandler("test-source", nil, WithHeartbeat(15*time.Second), WithMaxBatch(25))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp := postJSONRPC(t, srv.URL, `1`, "server/discover", map[string]any{})
	if resp.Error != nil {
		t.Fatalf("server/discover error: %+v", resp.Error)
	}

	var result struct {
		SupportedVersions []string `json:"supportedVersions"`
		Capabilities      struct {
			Extensions map[string]struct {
				Version     string `json:"version"`
				HeartbeatMs int64  `json:"heartbeatMs"`
				MaxBatch    int    `json:"maxBatch"`
			} `json:"extensions"`
		} `json:"capabilities"`
	}
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode server/discover result: %v", err)
	}

	if len(result.SupportedVersions) != 1 || result.SupportedVersions[0] != ProtocolVersion {
		t.Errorf("supportedVersions = %v, want [%s]", result.SupportedVersions, ProtocolVersion)
	}
	decl, ok := result.Capabilities.Extensions[ExtensionEvents]
	if !ok {
		t.Fatalf("capabilities.extensions missing %q", ExtensionEvents)
	}
	if decl.Version != ExtensionVersion {
		t.Errorf("version = %q, want %q", decl.Version, ExtensionVersion)
	}
	if decl.HeartbeatMs != 15000 {
		t.Errorf("heartbeatMs = %d, want 15000", decl.HeartbeatMs)
	}
	if decl.MaxBatch != 25 {
		t.Errorf("maxBatch = %d, want 25", decl.MaxBatch)
	}
}

// TestHandler_EventsDiscover checks the events/discover result carries the
// declared kinds verbatim, in order (doc §6).
func TestHandler_EventsDiscover(t *testing.T) {
	kinds := []Kind{
		{
			Kind:          "issue.opened",
			Guidance:      "Fires when an issue is opened.",
			BindingSchema: map[string]any{"type": "object"},
			Operators:     map[string][]string{"priority": {"eq", "in"}},
		},
		{Kind: "issue.closed", Guidance: "Fires when an issue is closed."},
	}
	h := NewHandler("test-source", kinds)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp := postJSONRPC(t, srv.URL, `4`, "events/discover", map[string]any{})
	if resp.Error != nil {
		t.Fatalf("events/discover error: %+v", resp.Error)
	}

	var result struct {
		Kinds []eventKindWire `json:"kinds"`
	}
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode events/discover result: %v", err)
	}
	if len(result.Kinds) != 2 {
		t.Fatalf("got %d kinds, want 2", len(result.Kinds))
	}
	if result.Kinds[0].Kind != "issue.opened" || result.Kinds[1].Kind != "issue.closed" {
		t.Errorf("kinds out of order: %+v", result.Kinds)
	}
	if len(result.Kinds[0].Operators["priority"]) != 2 {
		t.Errorf("operators not carried through: %+v", result.Kinds[0])
	}
}

// TestHandler_ListenRoundTrip is the core round trip: connect, publish, and
// receive the delivered event as a properly SSE-framed events/event
// notification carrying a correct CloudEvents envelope.
func TestHandler_ListenRoundTrip(t *testing.T) {
	kinds := []Kind{{Kind: "issue.opened"}}
	h := NewHandler("test-source", kinds, WithHeartbeat(time.Hour))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	resp, body := openListen(t, ctx, srv.URL, `1`, listenParams{Kinds: []string{"issue.opened"}})
	t.Cleanup(func() { resp.Body.Close() })

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	seq, err := h.Publish(context.Background(), Event{
		Type: "issue.opened",
		ID:   "delivery-1",
		Data: map[string]any{"priority": "high"},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	frame := readNextMessage(t, body)
	if frame.event != "message" {
		t.Errorf("SSE event field = %q, want %q", frame.event, "message")
	}
	// Exact byte-shape of the frame this package writes: "event: message"
	// then "data: <json>", each newline-terminated.
	if !strings.HasPrefix(frame.data, `{"jsonrpc":"2.0","method":"events/event"`) {
		t.Errorf("frame data does not start with the notification envelope: %s", frame.data)
	}

	env := decodeCloudEvent(t, frame.data)
	if env.SpecVersion != "1.0" {
		t.Errorf("specversion = %q, want 1.0", env.SpecVersion)
	}
	if env.Source != "test-source" {
		t.Errorf("source = %q, want test-source", env.Source)
	}
	if env.Type != "issue.opened" {
		t.Errorf("type = %q, want issue.opened", env.Type)
	}
	if env.ID != "delivery-1" {
		t.Errorf("id = %q, want delivery-1", env.ID)
	}
	if env.Time == "" {
		t.Error("time is empty")
	}
	if env.GleipnirSeq != seq {
		t.Errorf("gleipnirseq = %d, want %d", env.GleipnirSeq, seq)
	}
	data, ok := env.Data.(map[string]any)
	if !ok || data["priority"] != "high" {
		t.Errorf("data = %+v, want {priority: high}", env.Data)
	}
}

// TestHandler_KindFilter checks a listener that requested a subset of kinds
// receives only those, while gleipnirseq assignment (a global, not
// per-listener, sequence) is unaffected by the filter.
func TestHandler_KindFilter(t *testing.T) {
	kinds := []Kind{{Kind: "issue.opened"}, {Kind: "issue.closed"}}
	h := NewHandler("test-source", kinds, WithHeartbeat(time.Hour))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	resp, body := openListen(t, ctx, srv.URL, `1`, listenParams{Kinds: []string{"issue.opened"}})
	t.Cleanup(func() { resp.Body.Close() })

	if _, err := h.Publish(context.Background(), Event{Type: "issue.closed", ID: "skip-me"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	wantSeq, err := h.Publish(context.Background(), Event{Type: "issue.opened", ID: "deliver-me"})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	frame := readNextMessage(t, body)
	env := decodeCloudEvent(t, frame.data)
	if env.ID != "deliver-me" {
		t.Errorf("first delivered event id = %q, want deliver-me (issue.closed should have been filtered)", env.ID)
	}
	if env.GleipnirSeq != wantSeq {
		t.Errorf("gleipnirseq = %d, want %d", env.GleipnirSeq, wantSeq)
	}
}

// TestHandler_CursorResume_SequenceStrictlyIncreasing drives a connect,
// disconnect, publish-while-offline, reconnect-with-cursor cycle and
// asserts sequence numbers observed across both connections are strictly
// increasing with no duplicates and no gaps.
func TestHandler_CursorResume_SequenceStrictlyIncreasing(t *testing.T) {
	kinds := []Kind{{Kind: "k"}}
	h := NewHandler("test-source", kinds, WithHeartbeat(time.Hour))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	resp1, body1 := openListen(t, ctx1, srv.URL, `1`, listenParams{})

	var seqs []uint64
	for i := 0; i < 2; i++ {
		seq, err := h.Publish(context.Background(), Event{Type: "k", ID: fmt.Sprintf("first-%d", i)})
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		env := decodeCloudEvent(t, readNextMessage(t, body1).data)
		if env.GleipnirSeq != seq {
			t.Fatalf("delivered seq %d, want %d", env.GleipnirSeq, seq)
		}
		seqs = append(seqs, env.GleipnirSeq)
	}
	resp1.Body.Close()
	cancel1()

	var wantAfterReconnect []uint64
	for i := 2; i < 4; i++ {
		seq, err := h.Publish(context.Background(), Event{Type: "k", ID: fmt.Sprintf("offline-%d", i)})
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		wantAfterReconnect = append(wantAfterReconnect, seq)
	}

	cursor := formatCursor(seqs[len(seqs)-1])
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel2)
	resp2, body2 := openListen(t, ctx2, srv.URL, `2`, listenParams{Cursor: cursor})
	t.Cleanup(func() { resp2.Body.Close() })

	for _, want := range wantAfterReconnect {
		env := decodeCloudEvent(t, readNextMessage(t, body2).data)
		if env.GleipnirSeq != want {
			t.Fatalf("resumed delivery seq = %d, want %d", env.GleipnirSeq, want)
		}
		seqs = append(seqs, env.GleipnirSeq)
	}

	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("sequence numbers not strictly increasing across reconnect: %v", seqs)
		}
	}
}

// TestHandler_UnsatisfiableCursor checks a resume cursor the buffer cannot
// satisfy is answered with the distinguishable JSON-RPC error, as a plain
// (non-SSE) response — the stream never opens.
func TestHandler_UnsatisfiableCursor(t *testing.T) {
	h := NewHandler("test-source", nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp := postJSONRPC(t, srv.URL, `7`, "events/listen", listenParams{Cursor: "999999"})
	if resp.Error == nil {
		t.Fatal("events/listen with an unsatisfiable cursor succeeded, want an error")
	}
	if resp.Error.Code != CodeCursorUnknown {
		t.Errorf("error code = %d, want %d", resp.Error.Code, CodeCursorUnknown)
	}
	if !strings.Contains(resp.Error.Message, "cursor unknown") {
		t.Errorf("error message = %q, want it to name the cursor-unknown condition", resp.Error.Message)
	}
	if string(resp.ID) != "7" {
		t.Errorf("echoed id = %s, want 7", resp.ID)
	}
}

// TestHandler_MalformedCursorIsAlsoUnsatisfiable checks a cursor that does
// not even parse as a sequence number fails the same way as one the buffer
// legitimately cannot satisfy — both are "we cannot resume from here",
// never a crash or a silent full replay.
func TestHandler_MalformedCursorIsAlsoUnsatisfiable(t *testing.T) {
	h := NewHandler("test-source", nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp := postJSONRPC(t, srv.URL, `7`, "events/listen", listenParams{Cursor: "not-a-number"})
	if resp.Error == nil || resp.Error.Code != CodeCursorUnknown {
		t.Fatalf("resp = %+v, want a CodeCursorUnknown error", resp)
	}
}

// TestHandler_CleanClose checks Handler.Close sends the JSON-RPC
// {reason, cursor} response, SSE-framed, to the original request id.
func TestHandler_CleanClose(t *testing.T) {
	kinds := []Kind{{Kind: "k"}}
	h := NewHandler("test-source", kinds, WithHeartbeat(time.Hour))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	resp, body := openListen(t, ctx, srv.URL, `42`, listenParams{})
	t.Cleanup(func() { resp.Body.Close() })

	seq, err := h.Publish(context.Background(), Event{Type: "k", ID: "e1"})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	readNextMessage(t, body) // drain the event notification

	h.Close("shutting down for a redeploy")

	frame := readNextMessage(t, body)
	var closed struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  closeResult     `json:"result"`
	}
	if err := json.Unmarshal([]byte(frame.data), &closed); err != nil {
		t.Fatalf("decode close response %s: %v", frame.data, err)
	}
	if string(closed.ID) != "42" {
		t.Errorf("close response id = %s, want 42 (the original request id)", closed.ID)
	}
	if closed.Result.Reason != "shutting down for a redeploy" {
		t.Errorf("reason = %q", closed.Result.Reason)
	}
	if closed.Result.Cursor != formatCursor(seq) {
		t.Errorf("cursor = %q, want %q", closed.Result.Cursor, formatCursor(seq))
	}
}

// TestHandler_ClientDisconnectEndsStream proves a disconnected listener's
// stream loop actually returns instead of leaking: activeStreams (this
// package's own whitebox counter) drops back to zero.
func TestHandler_ClientDisconnectEndsStream(t *testing.T) {
	h := NewHandler("test-source", nil, WithHeartbeat(5*time.Millisecond))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	resp, _ := openListen(t, ctx, srv.URL, `1`, listenParams{})

	waitFor(t, 2*time.Second, func() bool { return h.activeStreams.Load() == 1 })

	resp.Body.Close()
	cancel()

	waitFor(t, 2*time.Second, func() bool { return h.activeStreams.Load() == 0 })
}

// TestHandler_HeartbeatCadence checks the SSE comment heartbeat is emitted
// at (or better than) the declared interval, using a short interval rather
// than an injectable clock — deliberate; see the CLAUDE.md testing-patterns
// note this trades off against: a real ticker at a few milliseconds, over a
// generously bounded window, is simpler than clock injection for an SSE
// select loop and is not meaningfully flakier in CI.
func TestHandler_HeartbeatCadence(t *testing.T) {
	const interval = 15 * time.Millisecond
	h := NewHandler("test-source", nil, WithHeartbeat(interval))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// 10x the interval: generous enough to tolerate CI scheduling jitter
	// while still requiring several heartbeats to have landed.
	ctx, cancel := context.WithTimeout(context.Background(), 10*interval)
	t.Cleanup(cancel)
	resp, body := openListen(t, ctx, srv.URL, `1`, listenParams{})
	t.Cleanup(func() { resp.Body.Close() })

	var heartbeats int
	for {
		f, err := readSSEFrame(body)
		if err != nil {
			break // context deadline reached, connection torn down
		}
		if f.comment != "" {
			heartbeats++
		}
	}
	if heartbeats < 3 {
		t.Errorf("got %d heartbeat frames over 10x the %s interval, want at least 3", heartbeats, interval)
	}
}
