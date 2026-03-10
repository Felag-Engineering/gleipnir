// Package sse implements the in-process event broadcaster for the SSE endpoint.
// It fans out published events to all connected subscribers using buffered channels.
// Slow clients whose buffers fill up receive dropped events rather than blocking delivery
// to other subscribers. A fixed ring buffer enables Last-Event-ID reconnection replay.
//
// The EventBroadcaster interface is the seam for swapping in a distributed backend
// (Redis Pub/Sub, NATS) when horizontal scaling is needed — callers depend on the
// interface, not the concrete Broadcaster.
package sse

import (
	"encoding/json"
	"sync"
)

// Event is a single SSE event published through the broadcaster.
type Event struct {
	ID   uint64
	Type string
	Data json.RawMessage
}

// EventBroadcaster is the interface callers depend on. Keeping the interface
// narrow makes it straightforward to swap in a distributed backend later.
type EventBroadcaster interface {
	Subscribe() *Subscription
	Unsubscribe(*Subscription)
	Publish(eventType string, data json.RawMessage)
	Replay(sinceID uint64) []Event
}

// Subscription represents a single connected SSE client. C is the read end of
// the event channel. When the subscription is unsubscribed, C is closed so
// that a range loop over C terminates naturally.
type Subscription struct {
	id uint64       // used by Unsubscribe to locate the entry in the subs map
	ch chan Event   // write end, held by the broadcaster
	C  <-chan Event // read end, exposed to the HTTP handler
}

// Option configures a Broadcaster at construction time.
type Option func(*Broadcaster)

// WithChannelSize sets the per-subscription channel depth.
// The default is 64. Events published to a full channel are dropped silently
// rather than blocking delivery to other subscribers.
func WithChannelSize(n int) Option {
	return func(b *Broadcaster) {
		b.channelSize = n
	}
}

// WithRingSize sets the capacity of the replay ring buffer.
// The default is 512. Once full, the oldest event is overwritten.
func WithRingSize(n int) Option {
	return func(b *Broadcaster) {
		b.ringSize = n
	}
}

// Broadcaster fans events out to all registered subscriptions and maintains a
// ring buffer for Last-Event-ID reconnection replay.
type Broadcaster struct {
	mu          sync.RWMutex
	nextSubID   uint64
	nextEventID uint64
	subs        map[uint64]*Subscription
	ring        []Event
	ringHead    int // next write position; wraps via modulo
	ringCount   int // populated slots, capped at ringSize
	channelSize int
	ringSize    int
}

// compile-time check that *Broadcaster satisfies EventBroadcaster.
var _ EventBroadcaster = (*Broadcaster)(nil)

// NewBroadcaster creates a Broadcaster with the given options applied.
func NewBroadcaster(opts ...Option) *Broadcaster {
	b := &Broadcaster{
		subs:        make(map[uint64]*Subscription),
		channelSize: 64,
		ringSize:    512,
	}
	for _, opt := range opts {
		opt(b)
	}
	b.ring = make([]Event, b.ringSize)
	return b
}

// Subscribe registers a new subscriber and returns its Subscription.
// The caller is responsible for calling Unsubscribe when done.
func (b *Broadcaster) Subscribe() *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextSubID++
	ch := make(chan Event, b.channelSize)
	sub := &Subscription{
		id: b.nextSubID,
		ch: ch,
		C:  ch,
	}
	b.subs[sub.id] = sub
	return sub
}

// Unsubscribe removes the subscription and closes its channel, which causes
// any range loop over sub.C to terminate. Closing is safe here because Publish
// and Unsubscribe both hold the write lock, so they are mutually exclusive —
// Publish cannot send to a channel that Unsubscribe has already closed.
func (b *Broadcaster) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.subs, sub.id)
	close(sub.ch)
}

// Publish assigns an event ID, writes the event into the ring buffer, and
// delivers it to all active subscriptions. Subscribers whose channels are full
// miss the event rather than stalling delivery to others.
//
// Event IDs are assigned inside the write lock (not with sync/atomic) to
// guarantee the ring buffer reflects the same monotonic ordering that
// subscribers observe.
func (b *Broadcaster) Publish(eventType string, data json.RawMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextEventID++
	ev := Event{
		ID:   b.nextEventID,
		Type: eventType,
		Data: data,
	}

	// Write into the ring, overwriting the oldest slot when full.
	b.ring[b.ringHead%b.ringSize] = ev
	b.ringHead++
	if b.ringCount < b.ringSize {
		b.ringCount++
	}

	for _, sub := range b.subs {
		select {
		case sub.ch <- ev:
		default:
			// Drop the event for this slow subscriber rather than blocking.
		}
	}
}

// Replay returns all events in the ring with ID > sinceID, in order from
// oldest to newest. Pass sinceID=0 to receive all buffered events.
// This supports the Last-Event-ID reconnection pattern defined in the SSE spec.
func (b *Broadcaster) Replay(sinceID uint64) []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.ringCount == 0 {
		return nil
	}

	count := b.ringCount
	// oldest is the ring index of the earliest event still in the buffer.
	oldest := ((b.ringHead-count)%b.ringSize + b.ringSize) % b.ringSize

	var out []Event
	for i := range count {
		slot := (oldest + i) % b.ringSize
		ev := b.ring[slot]
		if ev.ID > sinceID {
			out = append(out, ev)
		}
	}
	return out
}
