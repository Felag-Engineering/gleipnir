package events

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrCursorUnknown is returned (or wrapped) when a requested resume point
// can no longer be satisfied — either it was evicted from a bounded ring,
// or the buffer never held it at all (e.g. a fresh process after a
// restart). Handler translates this into the doc's cursor-unknown
// events/listen error rather than silently starting the stream with a gap.
var ErrCursorUnknown = errors.New("cursor unknown, replaying from now")

// StoredEvent is one event as held by a Buffer or Store: the CloudEvents
// envelope fields Buffer.Publish resolves (Seq and Source, which an author
// never sets directly) plus the Event fields the author provided.
type StoredEvent struct {
	Seq    uint64
	Type   string
	ID     string
	Source string
	Time   time.Time
	Data   any
}

// Store is the durability backend a Buffer delegates to. The default Buffer
// (NewBuffer) needs no Store — it keeps a bounded in-memory ring instead,
// which is honest about restart: a ring holds nothing across a process
// restart, and NewBuffer's Since reports ErrCursorUnknown for any resume
// point it can no longer prove is gap-free, rather than silently starting
// the stream after a gap.
//
// Implement Store to back events/listen with real durability (a database,
// an append-only log, ...) across restarts. A Store implementation MUST
// preserve insertion order and MUST itself detect and report an
// unsatisfiable resume point via ErrCursorUnknown — Buffer does not
// re-derive gap detection for a Store-backed buffer beyond what Since
// reports.
type Store interface {
	// Append persists e. Called once per published event, in ascending Seq
	// order, while the Buffer's own publish lock is held — an
	// implementation that talks to a remote system should keep this fast,
	// since concurrent Publish calls serialize behind it.
	Append(ctx context.Context, e StoredEvent) error

	// Since returns every stored event with Seq > after, oldest first, up
	// to limit entries (0 means no limit). after == 0 means "from the
	// beginning". Returns an error wrapping ErrCursorUnknown if after does
	// not correspond to a point the store can resume from gap-free (e.g.
	// it was retention-evicted).
	Since(ctx context.Context, after uint64, limit int) ([]StoredEvent, error)

	// LatestSeq returns the highest Seq ever appended, or 0 if the store
	// holds nothing yet. NewBufferWithStore reads this once at
	// construction so a Store-backed Buffer resumes sequence assignment
	// where a prior process left off instead of restarting at 1.
	LatestSeq(ctx context.Context) (uint64, error)
}

// defaultBufferCapacity bounds the in-memory ring NewBuffer(0) constructs.
// Large enough to absorb a burst between an event source and the first
// listener connecting, small enough that an unbounded backlog can never
// accumulate silently in a long-running plugin process.
const defaultBufferCapacity = 1000

// subscriberBufferSize bounds each live listener's delivery channel. A full
// channel means Publish drops the live delivery rather than blocking the
// publisher on a slow reader — the dropped event is not lost, since it is
// still in the buffer/store and a reconnect with a resume cursor replays
// it; at-least-once delivery is the whole design (doc §7.3).
const subscriberBufferSize = 64

// Buffer assigns sequence numbers to published events, fans them out to
// live listeners, and answers resume requests. The zero value is not
// usable; construct with NewBuffer or NewBufferWithStore.
type Buffer struct {
	mu          sync.Mutex
	store       Store // nil ⇒ ring is authoritative
	ring        []StoredEvent
	capacity    int
	nextSeq     uint64
	subscribers map[chan StoredEvent]struct{}
}

// NewBuffer returns a Buffer backed by a bounded in-memory ring holding up
// to capacity events. capacity <= 0 uses defaultBufferCapacity.
func NewBuffer(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = defaultBufferCapacity
	}
	return &Buffer{capacity: capacity, subscribers: map[chan StoredEvent]struct{}{}}
}

// NewBufferWithStore returns a Buffer that delegates persistence and resume
// queries to store instead of keeping an in-memory ring. It reads store's
// current LatestSeq so sequence assignment continues where a prior process
// left off.
func NewBufferWithStore(ctx context.Context, store Store) (*Buffer, error) {
	latest, err := store.LatestSeq(ctx)
	if err != nil {
		return nil, fmt.Errorf("events: read store's latest sequence: %w", err)
	}
	return &Buffer{store: store, nextSeq: latest, subscribers: map[chan StoredEvent]struct{}{}}, nil
}

// Publish assigns e the next sequence number, persists it, and fans it out
// to every live listener subscribed at the time of the call. source is the
// CloudEvents "source" the Handler owns (an author never sets it directly —
// see Event's doc comment).
//
// A failed Store.Append still consumes the sequence number it was assigned:
// the alternative — reusing a number after a failure whose outcome is
// unknown (did the store actually persist it and only the RPC failed?)
// — risks two different events sharing one gleipnirseq, which would break
// every cursor built on top of it. A skipped number is honest; a reused one
// is not.
func (b *Buffer) Publish(ctx context.Context, e Event, source string) (StoredEvent, error) {
	b.mu.Lock()
	b.nextSeq++
	stored := StoredEvent{
		Seq:    b.nextSeq,
		Type:   e.Type,
		ID:     e.ID,
		Source: source,
		Time:   e.Time,
		Data:   e.Data,
	}
	if stored.Time.IsZero() {
		stored.Time = time.Now()
	}

	var appendErr error
	if b.store != nil {
		appendErr = b.store.Append(ctx, stored)
	} else {
		b.ring = append(b.ring, stored)
		if len(b.ring) > b.capacity {
			b.ring = b.ring[len(b.ring)-b.capacity:]
		}
	}

	subs := make([]chan StoredEvent, 0, len(b.subscribers))
	for ch := range b.subscribers {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	if appendErr != nil {
		return StoredEvent{}, fmt.Errorf("events: publish: %w", appendErr)
	}

	for _, ch := range subs {
		select {
		case ch <- stored:
		default: // slow listener: the buffer/store still has it for a resume
		}
	}
	return stored, nil
}

// Since returns every event with Seq > after, oldest first. after == 0
// means "everything currently buffered" (a first connection with no resume
// cursor never calls Since at all — see Handler). Returns an error wrapping
// ErrCursorUnknown when after cannot be satisfied gap-free.
func (b *Buffer) Since(ctx context.Context, after uint64) ([]StoredEvent, error) {
	b.mu.Lock()
	store := b.store
	if store == nil {
		defer b.mu.Unlock()
		return b.ringSince(after)
	}
	b.mu.Unlock()

	return store.Since(ctx, after, 0)
}

// ringSince implements Since for the in-memory ring. Caller must hold b.mu.
func (b *Buffer) ringSince(after uint64) ([]StoredEvent, error) {
	if after == 0 {
		return append([]StoredEvent(nil), b.ring...), nil
	}
	if after > b.nextSeq {
		// Refers to a sequence number this buffer never issued — a cursor
		// from a different process incarnation, or simply invalid.
		return nil, ErrCursorUnknown
	}
	if after == b.nextSeq {
		return nil, nil // caught up; nothing new yet
	}
	if len(b.ring) == 0 || b.ring[0].Seq > after+1 {
		// The event immediately after the acked point is missing: it was
		// evicted by the ring (or, after a restart, nothing was ever
		// re-buffered). Replaying from here would silently skip it.
		return nil, ErrCursorUnknown
	}

	out := make([]StoredEvent, 0, len(b.ring))
	for _, e := range b.ring {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out, nil
}

// subscribe registers a live-delivery channel and returns it along with a
// cancel func that must be called exactly once to unregister it.
func (b *Buffer) subscribe() (chan StoredEvent, func()) {
	ch := make(chan StoredEvent, subscriberBufferSize)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		delete(b.subscribers, ch)
		b.mu.Unlock()
	}
}
