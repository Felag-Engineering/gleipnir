package events

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestBuffer_SinceFromBeginning(t *testing.T) {
	b := NewBuffer(10)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := b.Publish(ctx, Event{Type: "k", ID: fmt.Sprintf("e%d", i)}, "src"); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	got, err := b.Since(ctx, 0)
	if err != nil {
		t.Fatalf("Since(0): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Errorf("got[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestBuffer_SinceCaughtUp(t *testing.T) {
	b := NewBuffer(10)
	ctx := context.Background()
	seq, err := b.Publish(ctx, Event{Type: "k"}, "src")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got, err := b.Since(ctx, seq.Seq)
	if err != nil {
		t.Fatalf("Since(caught up): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events, want 0 (already caught up)", len(got))
	}
}

func TestBuffer_SinceUnissuedCursorIsUnknown(t *testing.T) {
	b := NewBuffer(10)
	ctx := context.Background()
	if _, err := b.Publish(ctx, Event{Type: "k"}, "src"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	_, err := b.Since(ctx, 999)
	if !errors.Is(err, ErrCursorUnknown) {
		t.Errorf("err = %v, want ErrCursorUnknown", err)
	}
}

// TestBuffer_RingBoundedEviction is the DoD's ring-bounded requirement:
// oldest events are evicted at capacity, and eviction is visible to a
// resumer as ErrCursorUnknown rather than a silent gap.
func TestBuffer_RingBoundedEviction(t *testing.T) {
	b := NewBuffer(3)
	ctx := context.Background()

	var seqs []uint64
	for i := 0; i < 6; i++ {
		seq, err := b.Publish(ctx, Event{Type: "k", ID: fmt.Sprintf("e%d", i)}, "src")
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		seqs = append(seqs, seq.Seq)
	}

	// Only the last 3 (seqs 4,5,6) remain; a cursor from before the
	// eviction boundary can no longer be satisfied gap-free.
	if _, err := b.Since(ctx, seqs[0]); !errors.Is(err, ErrCursorUnknown) {
		t.Errorf("Since(evicted cursor) err = %v, want ErrCursorUnknown", err)
	}
	if _, err := b.Since(ctx, seqs[1]); !errors.Is(err, ErrCursorUnknown) {
		t.Errorf("Since(evicted cursor) err = %v, want ErrCursorUnknown", err)
	}

	// The oldest still-present event's own seq minus one is the newest
	// satisfiable resume point.
	got, err := b.Since(ctx, seqs[2])
	if err != nil {
		t.Fatalf("Since(oldest present - 1): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want the 3 remaining", len(got))
	}

	got, err = b.Since(ctx, 0)
	if err != nil {
		t.Fatalf("Since(0): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ring holds %d events, want the bounded 3", len(got))
	}
	if got[0].Seq != seqs[3] {
		t.Errorf("oldest remaining seq = %d, want %d (the ring evicted the first 3)", got[0].Seq, seqs[3])
	}
}

// TestBuffer_ConcurrentPublishAndSubscribe exercises Publish and subscribe
// concurrently (the property -race checks) and confirms the buffer itself
// — the durable source of truth Since reads from — ends up with every
// event, contiguous and in order. It deliberately does NOT require the live
// subscriber channel to receive all n: Publish drops a live delivery to a
// full/slow subscriber by design (buffer.go's doc comment on
// subscriberBufferSize) precisely because the buffer is still authoritative
// and a resumer catches up via Since, so asserting on live-channel delivery
// count under concurrent load would be asserting on a race, not a contract.
func TestBuffer_ConcurrentPublishAndSubscribe(t *testing.T) {
	b := NewBuffer(1000)
	ch, cancel := b.subscribe()
	defer cancel()

	// Drain the live channel throughout the run so it never blocks a
	// Publish call, without asserting anything about how many it received.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range ch {
		}
	}()

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := b.Publish(context.Background(), Event{Type: "k", ID: fmt.Sprintf("e%d", i)}, "src"); err != nil {
				t.Errorf("Publish: %v", err)
			}
		}(i)
	}
	wg.Wait()
	cancel() // unregisters ch; Publish will no longer send to it
	close(ch)
	<-drainDone

	got, err := b.Since(context.Background(), 0)
	if err != nil {
		t.Fatalf("Since(0): %v", err)
	}
	if len(got) != n {
		t.Fatalf("buffer holds %d events, want all %d published", len(got), n)
	}
	seen := make(map[uint64]bool, n)
	for _, e := range got {
		if seen[e.Seq] {
			t.Fatalf("duplicate sequence number %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	for i := uint64(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("sequence %d missing from the buffer", i)
		}
	}
}

// fakeStore is a minimal in-memory Store implementation, used to prove the
// interface is real durability plug-in point (NewBufferWithStore) and not
// just a paper contract.
type fakeStore struct {
	mu     sync.Mutex
	events []StoredEvent
}

func (s *fakeStore) Append(_ context.Context, e StoredEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

func (s *fakeStore) Since(_ context.Context, after uint64, limit int) ([]StoredEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := s.latestSeqLocked()
	if after > latest {
		return nil, fmt.Errorf("fakeStore: %w", ErrCursorUnknown)
	}
	if after == latest {
		return nil, nil
	}
	if len(s.events) == 0 || s.events[0].Seq > after+1 {
		return nil, fmt.Errorf("fakeStore: %w", ErrCursorUnknown)
	}
	var out []StoredEvent
	for _, e := range s.events {
		if e.Seq > after {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *fakeStore) LatestSeq(_ context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latestSeqLocked(), nil
}

func (s *fakeStore) latestSeqLocked() uint64 {
	if len(s.events) == 0 {
		return 0
	}
	return s.events[len(s.events)-1].Seq
}

func TestNewBufferWithStore_ResumesSequenceFromStore(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	if err := store.Append(ctx, StoredEvent{Seq: 5, Type: "k"}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	b, err := NewBufferWithStore(ctx, store)
	if err != nil {
		t.Fatalf("NewBufferWithStore: %v", err)
	}

	seq, err := b.Publish(ctx, Event{Type: "k", ID: "next"}, "src")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if seq.Seq != 6 {
		t.Errorf("seq = %d, want 6 (continuing from the store's latest, 5)", seq.Seq)
	}

	got, err := b.Since(ctx, 5)
	if err != nil {
		t.Fatalf("Since(5): %v", err)
	}
	if len(got) != 1 || got[0].Seq != 6 {
		t.Errorf("got = %+v, want the one event with seq 6", got)
	}
}

func TestNewBufferWithStore_DelegatesCursorUnknown(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	b, err := NewBufferWithStore(ctx, store)
	if err != nil {
		t.Fatalf("NewBufferWithStore: %v", err)
	}
	if _, err := b.Publish(ctx, Event{Type: "k"}, "src"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	_, err = b.Since(ctx, 999)
	if !errors.Is(err, ErrCursorUnknown) {
		t.Errorf("err = %v, want it to wrap ErrCursorUnknown", err)
	}
}
