package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// probedEventsClient wires a client to a stub events server at the managed
// trust tier and runs the handshake, mirroring newEventsClient
// (events_test.go) plus the ProbeProtocolVersion every events/listen test
// needs before the client has a recorded eventsCap/trust classification.
func probedEventsClient(t *testing.T, stub *FakeEventsServer) *Client {
	t.Helper()
	client := newEventsClient(t, stub)
	if _, err := client.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}
	return client
}

func TestListenEvents_RequiresModernTransport(t *testing.T) {
	stub := NewFakeEventsServer()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, WithTrustTier(TrustTierManaged)) // unpinned ⇒ legacy

	_, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err == nil {
		t.Fatal("ListenEvents ran on a legacy-pinned client")
	}
	if !strings.Contains(err.Error(), "2026-07-28") {
		t.Errorf("error %q does not name the required transport", err.Error())
	}
	if stub.ListenCalls != 0 {
		t.Errorf("events/listen reached the server %d times, want 0 — the refusal must happen client-side", stub.ListenCalls)
	}
}

func TestListenEvents_RequiresManagedTier(t *testing.T) {
	stub := NewFakeEventsServer()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728)) // external by default

	_, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err == nil {
		t.Fatal("ListenEvents ran on an external-tier client")
	}
	if stub.ListenCalls != 0 {
		t.Errorf("events/listen reached the server %d times, want 0 — the refusal must happen client-side", stub.ListenCalls)
	}
}

func TestListenEvents_StreamClientHasZeroTimeout(t *testing.T) {
	client := NewClient("http://example.invalid",
		WithProtocolVersion(ProtocolVersion20260728), WithTrustTier(TrustTierManaged))

	sc := client.streamHTTPClient()
	if sc.Timeout != 0 {
		t.Errorf("stream client Timeout = %v, want 0 (c.httpClient's ordinary 30s Timeout "+
			"covers the whole body read and would kill a long-lived stream)", sc.Timeout)
	}
}

func TestListenEvents_RoundTrip_MonotonicOrder(t *testing.T) {
	stub := NewFakeEventsServer()
	seqs := []uint64{1, 2, 3, 4}
	for _, seq := range seqs {
		stub.ListenEvents = append(stub.ListenEvents, CloudEvent{
			SpecVersion: "1.0", Source: "src", Type: "issue.opened",
			ID: "e" + strconv.FormatUint(seq, 10), Sequence: seq,
		})
	}
	stub.CloseCleanly = true
	stub.CloseReason = "test done"
	client := probedEventsClient(t, stub)

	stream, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	t.Cleanup(func() { stream.Close() })

	var last uint64
	for range seqs {
		ev, err := stream.Next(context.Background())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if ev.Sequence <= last {
			t.Errorf("sequence not monotonically increasing: got %d after %d", ev.Sequence, last)
		}
		last = ev.Sequence
	}
}

func TestListenEvents_ResumesAfterCursor(t *testing.T) {
	stub := NewFakeEventsServer()
	stub.ListenEvents = []CloudEvent{
		{SpecVersion: "1.0", Source: "s", Type: "issue.opened", ID: "e1", Sequence: 1},
		{SpecVersion: "1.0", Source: "s", Type: "issue.opened", ID: "e2", Sequence: 2},
		{SpecVersion: "1.0", Source: "s", Type: "issue.opened", ID: "e3", Sequence: 3},
	}
	stub.CloseCleanly = true
	client := probedEventsClient(t, stub)

	stream, err := client.ListenEvents(context.Background(),
		EventsListenParams{Kinds: []string{"issue.opened"}, Cursor: "1"})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	t.Cleanup(func() { stream.Close() })

	ev, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if ev.ID != "e2" {
		t.Errorf("first delivered id = %q, want e2 (cursor=1 resumes strictly after seq 1)", ev.ID)
	}

	ev, err = stream.Next(context.Background())
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if ev.ID != "e3" {
		t.Errorf("second delivered id = %q, want e3", ev.ID)
	}
}

func TestListenEvents_CleanCloseReturnsReasonAndCursor(t *testing.T) {
	stub := NewFakeEventsServer()
	stub.ListenEvents = []CloudEvent{{SpecVersion: "1.0", Source: "s", Type: "issue.opened", ID: "e1", Sequence: 1}}
	stub.CloseCleanly = true
	stub.CloseReason = "rolling restart"
	stub.CloseCursor = "1"
	client := probedEventsClient(t, stub)

	stream, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	t.Cleanup(func() { stream.Close() })

	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("first Next: %v", err)
	}

	_, err = stream.Next(context.Background())
	var closed *EventsStreamClosed
	if !errors.As(err, &closed) {
		t.Fatalf("second Next err = %v, want *EventsStreamClosed", err)
	}
	if closed.Reason != "rolling restart" || closed.Cursor != "1" {
		t.Errorf("closed = %+v, want reason=%q cursor=%q", closed, "rolling restart", "1")
	}
	if !errors.Is(err, ErrEventsStreamClosed) {
		t.Error("errors.Is(err, ErrEventsStreamClosed) = false")
	}

	// The terminal error is sticky: a further Next must return the same
	// answer, not block or fail differently.
	_, err = stream.Next(context.Background())
	if !errors.Is(err, ErrEventsStreamClosed) {
		t.Errorf("third Next err = %v, want the same sticky ErrEventsStreamClosed", err)
	}
}

func TestListenEvents_HeartbeatStarvationReturnsSentinel(t *testing.T) {
	restoreHeartbeat := shortenDefaultEventsHeartbeat(t, 10*time.Millisecond)
	defer restoreHeartbeat()

	stub := NewFakeEventsServer()
	stub.StallListen = true // opens the stream, then sends nothing at all
	client := probedEventsClient(t, stub)

	stream, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	t.Cleanup(func() { stream.Close() })

	_, err = stream.Next(context.Background())
	if !errors.Is(err, ErrEventsHeartbeatTimeout) {
		t.Fatalf("Next = %v, want ErrEventsHeartbeatTimeout", err)
	}
}

// Waiting past ONE heartbeat interval (but well under the 3x dead-stream
// threshold) must not kill the stream — a delivery that simply took a while
// still arrives. defaultEventsHeartbeat is shortened so this proves the
// tolerance quickly rather than waiting out a realistic production interval.
func TestListenEvents_DeliversPastShortenedHeartbeatDefault(t *testing.T) {
	restoreHeartbeat := shortenDefaultEventsHeartbeat(t, 20*time.Millisecond)
	defer restoreHeartbeat()

	stub := NewFakeEventsServer()
	// HeartbeatMs left at 0: the server declares no usable heartbeat hint,
	// so the client falls back to the (shortened) defaultEventsHeartbeat.
	stub.DelayBeforeEventsMs = 45 // ~2x the shortened default: past one interval, comfortably under the 3x threshold
	stub.ListenEvents = []CloudEvent{{SpecVersion: "1.0", Source: "s", Type: "issue.opened", ID: "e1", Sequence: 1}}
	client := probedEventsClient(t, stub)

	stream, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	t.Cleanup(func() { stream.Close() })

	ev, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v (the stream must tolerate one long-ish wait, not just instant delivery)", err)
	}
	if ev.ID != "e1" {
		t.Errorf("event id = %q, want e1", ev.ID)
	}
}

func TestListenEvents_MalformedFrameFailsTheStream(t *testing.T) {
	stub := NewFakeEventsServer()
	stub.InjectMalformedFrame = true
	client := probedEventsClient(t, stub)

	stream, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	t.Cleanup(func() { stream.Close() })

	_, err = stream.Next(context.Background())
	if !errors.Is(err, ErrEventsTransportError) {
		t.Fatalf("Next = %v, want ErrEventsTransportError", err)
	}
}

func TestListenEvents_OversizedFrameNamesTheCap(t *testing.T) {
	stub := NewFakeEventsServer()
	stub.InjectOversizedFrame = true
	client := probedEventsClient(t, stub)

	stream, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	t.Cleanup(func() { stream.Close() })

	_, err = stream.Next(context.Background())
	if !errors.Is(err, ErrEventsTransportError) {
		t.Fatalf("Next = %v, want ErrEventsTransportError", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(maxSSEFrameBytes)) {
		t.Errorf("error %q does not name the %d byte cap", err.Error(), maxSSEFrameBytes)
	}
}

func TestListenEvents_CloseIsIdempotent(t *testing.T) {
	stub := NewFakeEventsServer()
	client := probedEventsClient(t, stub)

	stream, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// -race must find nothing wrong with Next and Close running concurrently.
func TestListenEvents_ConcurrentNextAndClose(t *testing.T) {
	stub := NewFakeEventsServer()
	client := probedEventsClient(t, stub)

	stream, err := client.ListenEvents(context.Background(), EventsListenParams{Kinds: []string{"issue.opened"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		stream.Next(context.Background()) //nolint:errcheck // -race target, not an assertion
	}()
	go func() {
		defer wg.Done()
		stream.Close() //nolint:errcheck // -race target, not an assertion
	}()
	wg.Wait()
}

// ListenEvents must never be gated by the tools/call callGate: a queue
// already saturated by CallTool traffic must not block opening a stream.
func TestListenEvents_DoesNotConsumeCallGate(t *testing.T) {
	stub := NewFakeEventsServer()
	stub.BlockToolCall = make(chan struct{})
	client := probedEventsClientWithLimits(t, stub, ServerLimits{MaxConcurrent: 1, MaxQueueDepth: 1})

	claimed := make(chan struct{})
	testHookQueueSlotClaimed = func() { close(claimed) }
	t.Cleanup(func() { testHookQueueSlotClaimed = nil })

	ctx := context.Background()
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		client.CallTool(ctx, "whatever", nil, CallOptions{}) //nolint:errcheck // occupies the gate; result unused
	}()
	<-claimed // call #1 now holds the queue's only slot (MaxQueueDepth: 1)

	if _, err := client.CallTool(ctx, "whatever", nil, CallOptions{}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second CallTool = %v, want ErrQueueFull (the gate must actually be "+
			"saturated for this test to mean anything)", err)
	}

	stream, err := client.ListenEvents(ctx, EventsListenParams{Kinds: []string{"issue.opened"}})
	if err != nil {
		t.Fatalf("ListenEvents with a full callGate: %v", err)
	}
	stream.Close()

	close(stub.BlockToolCall)
	<-callDone
}

// shortenDefaultEventsHeartbeat overrides the package-level
// defaultEventsHeartbeat for the duration of one test and returns a restore
// func. Mutates package state, so a caller must not run t.Parallel — same
// rule testHookQueueSlotClaimed callers already follow.
func shortenDefaultEventsHeartbeat(t *testing.T, d time.Duration) func() {
	t.Helper()
	orig := defaultEventsHeartbeat
	defaultEventsHeartbeat = d
	return func() { defaultEventsHeartbeat = orig }
}

// probedEventsClientWithLimits is probedEventsClient plus a bounded
// callGate, for the one test that needs to saturate it.
func probedEventsClientWithLimits(t *testing.T, stub *FakeEventsServer, limits ServerLimits) *Client {
	t.Helper()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL,
		WithProtocolVersion(ProtocolVersion20260728),
		WithTrustTier(TrustTierManaged),
		WithServerLimits(limits),
	)
	if _, err := client.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}
	return client
}
