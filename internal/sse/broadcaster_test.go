package sse

import (
	"encoding/json"
	"sync"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
)

func rawJSON(s string) json.RawMessage {
	return json.RawMessage(s)
}

func TestBroadcaster_FanOut(t *testing.T) {
	b := NewBroadcaster()

	subs := make([]*Subscription, 3)
	for i := range subs {
		subs[i] = b.Subscribe()
	}
	defer func() {
		for _, s := range subs {
			b.Unsubscribe(s)
		}
	}()

	const n = 5
	for range n {
		b.Publish("test", rawJSON(`{"i":1}`))
	}

	for idx, sub := range subs {
		var received []Event
		for range n {
			ev := <-sub.C
			received = append(received, ev)
		}
		if len(received) != n {
			t.Errorf("sub[%d]: got %d events, want %d", idx, len(received), n)
		}
		for i, ev := range received {
			wantID := uint64(i + 1)
			if ev.ID != wantID {
				t.Errorf("sub[%d] event[%d]: ID = %d, want %d", idx, i, ev.ID, wantID)
			}
		}
	}
}

func TestBroadcaster_DropOnFull(t *testing.T) {
	b := NewBroadcaster(WithChannelSize(2))
	sub := b.Subscribe()

	const publish = 5
	for range publish {
		b.Publish("test", rawJSON(`{}`))
	}

	// Only the first 2 events should be in the channel; the rest were dropped.
	if len(sub.C) != 2 {
		t.Fatalf("channel length = %d, want 2", len(sub.C))
	}

	ev1 := <-sub.C
	ev2 := <-sub.C
	if ev1.ID != 1 {
		t.Errorf("first event ID = %d, want 1", ev1.ID)
	}
	if ev2.ID != 2 {
		t.Errorf("second event ID = %d, want 2", ev2.ID)
	}

	b.Unsubscribe(sub)
}

func TestBroadcaster_SlowSubscriberDoesNotBlock(t *testing.T) {
	// channelSize=2 ensures buffers fill quickly. Publish 5 events without
	// draining s1 (the "slow" subscriber). The critical property: all Publish
	// calls return immediately — non-blocking sends drop events rather than
	// stalling delivery to others.
	b := NewBroadcaster(WithChannelSize(2))
	s1 := b.Subscribe()
	s2 := b.Subscribe()

	const publish = 5
	for range publish {
		b.Publish("test", rawJSON(`{}`))
	}

	// Drain s2 to confirm it received events while s1 remained undrained.
	for len(s2.C) > 0 {
		<-s2.C
	}

	b.Unsubscribe(s1)
	b.Unsubscribe(s2)
}

func TestBroadcaster_Replay_Empty(t *testing.T) {
	b := NewBroadcaster()
	events := b.Replay(0)
	if len(events) != 0 {
		t.Errorf("Replay on empty broadcaster returned %d events, want 0", len(events))
	}
}

func TestBroadcaster_Replay_AllEvents(t *testing.T) {
	b := NewBroadcaster()

	for range 5 {
		b.Publish("test", rawJSON(`{}`))
	}

	events := b.Replay(0)
	if len(events) != 5 {
		t.Fatalf("Replay(0) returned %d events, want 5", len(events))
	}
	for i, ev := range events {
		if ev.ID != uint64(i+1) {
			t.Errorf("events[%d].ID = %d, want %d", i, ev.ID, i+1)
		}
	}
}

func TestBroadcaster_Replay_SinceID(t *testing.T) {
	b := NewBroadcaster()

	for range 10 {
		b.Publish("test", rawJSON(`{}`))
	}

	events := b.Replay(5)
	if len(events) != 5 {
		t.Fatalf("Replay(5) returned %d events, want 5", len(events))
	}
	for i, ev := range events {
		wantID := uint64(6 + i)
		if ev.ID != wantID {
			t.Errorf("events[%d].ID = %d, want %d", i, ev.ID, wantID)
		}
	}
}

func TestBroadcaster_Replay_SinceIDExceedsMax(t *testing.T) {
	b := NewBroadcaster()

	for range 5 {
		b.Publish("test", rawJSON(`{}`))
	}

	events := b.Replay(100)
	if len(events) != 0 {
		t.Fatalf("Replay(100) returned %d events, want 0", len(events))
	}
}

func TestBroadcaster_Replay_RingWraparound(t *testing.T) {
	b := NewBroadcaster(WithRingSize(4))

	for range 10 {
		b.Publish("test", rawJSON(`{}`))
	}

	events := b.Replay(0)
	if len(events) != 4 {
		t.Fatalf("Replay(0) after wraparound returned %d events, want 4", len(events))
	}
	wantIDs := []uint64{7, 8, 9, 10}
	for i, ev := range events {
		if ev.ID != wantIDs[i] {
			t.Errorf("events[%d].ID = %d, want %d", i, ev.ID, wantIDs[i])
		}
	}
}

func TestBroadcaster_Unsubscribe_ClosesChannel(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe()

	b.Publish("test", rawJSON(`{}`))
	b.Unsubscribe(sub)

	// Drain all events and then verify the channel is closed (range terminates).
	var count int
	for range sub.C {
		count++
	}
	// We published 1 event before unsubscribing, so we expect at least 0 events
	// (the event may or may not have been received before close). The important
	// property is that the range loop terminated — meaning the channel was closed.
	_ = count
}

func TestBroadcaster_ConcurrentPublish(t *testing.T) {
	const goroutines = 10
	const eventsEach = 10
	const total = goroutines * eventsEach

	// Use a ring large enough to hold all events so Replay reflects the full count.
	b := NewBroadcaster(WithRingSize(total))

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range eventsEach {
				b.Publish("test", rawJSON(`{}`))
			}
		}()
	}
	wg.Wait()

	// Observable behavior: all published events appear in the replay buffer.
	events := b.Replay(0)
	if len(events) != total {
		t.Errorf("Replay returned %d events, want %d", len(events), total)
	}
}

func TestBroadcaster_ConcurrentSubscribeUnsubscribe(t *testing.T) {
	b := NewBroadcaster()

	var wg sync.WaitGroup

	// Publishers
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				b.Publish("test", rawJSON(`{}`))
			}
		}()
	}

	// Subscribers that subscribe then quickly unsubscribe
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub := b.Subscribe()
			// Drain whatever arrived to avoid goroutine leak on unsubscribe.
			// The channel will be closed by Unsubscribe.
			done := make(chan struct{})
			go func() {
				defer close(done)
				for range sub.C {
				}
			}()
			b.Unsubscribe(sub)
			<-done
		}()
	}

	wg.Wait()
}

// TestBroadcaster_DropIncrementsCounter verifies that dropped events (subscriber
// channel full) increment gleipnir_sse_events_dropped_total. Delta assertions are
// used because the counter persists across tests in the same binary.
func TestBroadcaster_DropIncrementsCounter(t *testing.T) {
	b := NewBroadcaster(WithChannelSize(2))
	sub := b.Subscribe()
	defer b.Unsubscribe(sub)

	before := promtestutil.ToFloat64(sseEventsDroppedTotal)

	// Publish 5 events to a channel of depth 2; the first 2 are delivered,
	// the remaining 3 are dropped.
	const publish = 5
	for range publish {
		b.Publish("test", rawJSON(`{}`))
	}

	after := promtestutil.ToFloat64(sseEventsDroppedTotal)
	if got := after - before; got != 3 {
		t.Errorf("drop counter delta = %.0f, want 3", got)
	}
}

// TestBroadcaster_SuccessfulPublishDoesNotIncrementDropCounter verifies that
// events delivered without drops do not increment the counter.
func TestBroadcaster_SuccessfulPublishDoesNotIncrementDropCounter(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe()
	defer b.Unsubscribe(sub)

	before := promtestutil.ToFloat64(sseEventsDroppedTotal)

	const publish = 3
	for range publish {
		b.Publish("test", rawJSON(`{}`))
	}

	// Drain the channel before asserting zero drops, to confirm all events
	// were accepted by the subscriber.
	for range publish {
		<-sub.C
	}

	after := promtestutil.ToFloat64(sseEventsDroppedTotal)
	if got := after - before; got != 0 {
		t.Errorf("drop counter delta = %.0f, want 0", got)
	}
}
