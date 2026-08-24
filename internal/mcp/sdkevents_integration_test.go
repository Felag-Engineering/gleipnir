// Cross-module contract test: the root module's events/listen CLIENT
// (internal/mcp, #900) driven against the plugin-sdk's events/listen SERVER
// helper (plugin-sdk/events, #904) — two independently written
// implementations of docs/developer/extension-io-gleipnir-events.md.
//
// This is the deferred DoD item from issue #904: the SDK's own tests drive
// its Handler with a hand-rolled HTTP client, and internal/mcp's tests drive
// ListenEvents against its own FakeEventsServer — each proves its half
// self-consistent, and neither proves the CONTRACT is implementable by
// someone who did not write the other side. Only this agreement does. It has
// already earned its place once: writing it exposed that the client fed a
// cursor-unknown JSON-RPC refusal (a plain application/json body, doc §7.2)
// to its SSE reader and reported a shapeless transport error; ListenEvents
// now checks the response Content-Type and surfaces ErrEventsCursorUnknown.
//
// Mirrors internal/plugin/hostendpoint/sdkclient_integration_test.go, the
// same-shaped cross-module pin for the host endpoint's contract (#882).
package mcp_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	sdkevents "github.com/felag-engineering/gleipnir/plugin-sdk/events"
)

// startSDKEventSource stands up the SDK Handler on an httptest server and
// returns a probed managed modern client pointed at it, plus the handler for
// Publish/Close. The kinds and heartbeat mirror the minimal-event-source
// example so this doubles as a pin that the example's shape negotiates.
func startSDKEventSource(t *testing.T) (*mcp.Client, *sdkevents.Handler) {
	t.Helper()

	handler := sdkevents.NewHandler(
		"cross-module-test",
		[]sdkevents.Kind{
			{Kind: "thing.created", Guidance: "fires when a thing is created"},
			{Kind: "thing.deleted"},
		},
		// 2s is contract-legal: the client clamps declared heartbeats to
		// [1s, 10m] (events.go), so a sub-second value here would come back
		// as the 1s floor rather than round-tripping.
		sdkevents.WithHeartbeat(2*time.Second),
		sdkevents.WithBuffer(sdkevents.NewBuffer(8)),
	)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := mcp.NewClient(srv.URL,
		mcp.WithProtocolVersion(mcp.ProtocolVersion20260728),
		mcp.WithTrustTier(mcp.TrustTierManaged),
	)
	probe, err := client.ProbeProtocolVersion(context.Background())
	if err != nil {
		t.Fatalf("ProbeProtocolVersion against the SDK handler: %v", err)
	}
	if !probe.EventsDeclared {
		t.Fatalf("SDK handler's server/discover did not declare %s to the client", mcp.ExtensionEvents)
	}
	if probe.Events.Heartbeat != 2*time.Second {
		t.Fatalf("declared heartbeat = %v, want 2s round-tripped through the capability entry", probe.Events.Heartbeat)
	}
	return client, handler
}

func TestCrossModule_DiscoverEventKinds(t *testing.T) {
	client, _ := startSDKEventSource(t)

	kinds, err := client.DiscoverEventKinds(context.Background())
	if err != nil {
		t.Fatalf("DiscoverEventKinds: %v", err)
	}
	if len(kinds) != 2 || kinds[0].Kind != "thing.created" || kinds[1].Kind != "thing.deleted" {
		t.Fatalf("kinds = %+v, want [thing.created thing.deleted] in server order", kinds)
	}
	if kinds[0].Guidance != "fires when a thing is created" {
		t.Fatalf("guidance did not round-trip: %q", kinds[0].Guidance)
	}
}

func TestCrossModule_StreamDeliversPublishedEvents(t *testing.T) {
	client, handler := startSDKEventSource(t)
	ctx := context.Background()

	stream, err := client.ListenEvents(ctx, mcp.EventsListenParams{Kinds: []string{"thing.created"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	defer stream.Close()

	// Publish two matching events and one the subscription filters out.
	if _, err := handler.Publish(ctx, sdkevents.Event{Type: "thing.created", ID: "a", Data: map[string]any{"n": 1}}); err != nil {
		t.Fatalf("Publish a: %v", err)
	}
	if _, err := handler.Publish(ctx, sdkevents.Event{Type: "thing.deleted", ID: "x"}); err != nil {
		t.Fatalf("Publish x: %v", err)
	}
	if _, err := handler.Publish(ctx, sdkevents.Event{Type: "thing.created", ID: "b"}); err != nil {
		t.Fatalf("Publish b: %v", err)
	}

	first, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("Next 1: %v", err)
	}
	second, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("Next 2: %v", err)
	}

	if first.ID != "a" || second.ID != "b" {
		t.Fatalf("delivered ids = %q, %q; want a, b (thing.deleted filtered by kinds)", first.ID, second.ID)
	}
	if first.Source != "cross-module-test" || first.SpecVersion != "1.0" {
		t.Fatalf("envelope fields the SDK owns did not survive: source=%q specversion=%q", first.Source, first.SpecVersion)
	}
	if second.Sequence <= first.Sequence {
		t.Fatalf("sequences not increasing: %d then %d", first.Sequence, second.Sequence)
	}
}

func TestCrossModule_CursorResumeAcrossReconnect(t *testing.T) {
	client, handler := startSDKEventSource(t)
	ctx := context.Background()

	for _, id := range []string{"e1", "e2", "e3"} {
		if _, err := handler.Publish(ctx, sdkevents.Event{Type: "thing.created", ID: id}); err != nil {
			t.Fatalf("Publish %s: %v", id, err)
		}
	}

	stream, err := client.ListenEvents(ctx, mcp.EventsListenParams{Kinds: []string{"thing.created"}, Cursor: ""})
	if err != nil {
		t.Fatalf("ListenEvents (first connect): %v", err)
	}
	// A fresh connection with no cursor gets the live feed only — the SDK
	// buffer replays exclusively on a resume cursor, so nothing published
	// before this connect is owed to it. Publish one more to have something
	// to ack.
	if _, err := handler.Publish(ctx, sdkevents.Event{Type: "thing.created", ID: "e4"}); err != nil {
		t.Fatalf("Publish e4: %v", err)
	}
	ev, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("Next on first connect: %v", err)
	}
	if ev.ID != "e4" {
		t.Fatalf("first connect delivered %q, want the post-connect e4 only", ev.ID)
	}
	stream.Close()

	// Publish two more while disconnected, then resume from e4's sequence:
	// the ack-on-reconnect model (doc §7.3) says exactly e5 and e6 replay.
	for _, id := range []string{"e5", "e6"} {
		if _, err := handler.Publish(ctx, sdkevents.Event{Type: "thing.created", ID: id}); err != nil {
			t.Fatalf("Publish %s: %v", id, err)
		}
	}
	resumed, err := client.ListenEvents(ctx, mcp.EventsListenParams{
		Kinds:  []string{"thing.created"},
		Cursor: mcp.FormatEventsCursor(ev.Sequence),
	})
	if err != nil {
		t.Fatalf("ListenEvents (resume): %v", err)
	}
	defer resumed.Close()

	got1, err := resumed.Next(ctx)
	if err != nil {
		t.Fatalf("Next after resume 1: %v", err)
	}
	got2, err := resumed.Next(ctx)
	if err != nil {
		t.Fatalf("Next after resume 2: %v", err)
	}
	if got1.ID != "e5" || got2.ID != "e6" {
		t.Fatalf("resume replayed %q, %q; want e5, e6 (after the acked sequence, not before)", got1.ID, got2.ID)
	}
	if got1.Sequence <= ev.Sequence {
		t.Fatalf("resume replayed at-or-before the acked sequence: %d <= %d", got1.Sequence, ev.Sequence)
	}
}

func TestCrossModule_UnknownCursorRefusedBeforeStreaming(t *testing.T) {
	client, _ := startSDKEventSource(t)

	// The buffer has seen nothing, so any nonzero resume point is a gap it
	// cannot bridge. The doc's §7.2 refusal is a plain JSON-RPC error, code
	// -32001 — the client must surface it as ErrEventsCursorUnknown, not
	// open a stream and fail shapelessly.
	_, err := client.ListenEvents(context.Background(), mcp.EventsListenParams{
		Kinds:  []string{"thing.created"},
		Cursor: "999",
	})
	if !errors.Is(err, mcp.ErrEventsCursorUnknown) {
		t.Fatalf("ListenEvents with unsatisfiable cursor: err = %v, want ErrEventsCursorUnknown", err)
	}

	// A malformed cursor is refused the same way (doc §7.2).
	_, err = client.ListenEvents(context.Background(), mcp.EventsListenParams{
		Kinds:  []string{"thing.created"},
		Cursor: "not-a-sequence",
	})
	if !errors.Is(err, mcp.ErrEventsCursorUnknown) {
		t.Fatalf("ListenEvents with malformed cursor: err = %v, want ErrEventsCursorUnknown", err)
	}
}

func TestCrossModule_ServerCloseIsCleanCloseWithCursor(t *testing.T) {
	client, handler := startSDKEventSource(t)
	ctx := context.Background()

	stream, err := client.ListenEvents(ctx, mcp.EventsListenParams{Kinds: []string{"thing.created"}})
	if err != nil {
		t.Fatalf("ListenEvents: %v", err)
	}
	defer stream.Close()

	if _, err := handler.Publish(ctx, sdkevents.Event{Type: "thing.created", ID: "last"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	ev, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	handler.Close("rolling restart")

	_, err = stream.Next(ctx)
	if !errors.Is(err, mcp.ErrEventsStreamClosed) {
		t.Fatalf("Next after server Close: err = %v, want ErrEventsStreamClosed", err)
	}
	var closed *mcp.EventsStreamClosed
	if !errors.As(err, &closed) {
		t.Fatalf("clean close did not carry EventsStreamClosed detail: %v", err)
	}
	if closed.Reason != "rolling restart" {
		t.Fatalf("close reason = %q, want the server's own wording", closed.Reason)
	}
	if closed.Cursor != mcp.FormatEventsCursor(ev.Sequence) {
		t.Fatalf("close cursor = %q, want the last delivered sequence %d", closed.Cursor, ev.Sequence)
	}
}
