package events_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
)

// staleCursorStore serves a fixed stale cursor until Reset is called, then
// serves the empty cursor — the smallest store that lets a test drive the
// doc §7.2 cursor-unknown recovery loop end to end.
type staleCursorStore struct {
	mu       sync.Mutex
	resets   int
	resetCh  chan struct{}
	advanced []uint64
}

func newStaleCursorStore() *staleCursorStore {
	return &staleCursorStore{resetCh: make(chan struct{}, 8)}
}

func (s *staleCursorStore) Load(context.Context, string, string) (string, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resets > 0 {
		return "", 0, nil
	}
	return "42", 42, nil
}

func (s *staleCursorStore) Advance(_ context.Context, _, _, _ string, seq uint64) error {
	s.mu.Lock()
	s.advanced = append(s.advanced, seq)
	s.mu.Unlock()
	return nil
}

func (s *staleCursorStore) Reset(context.Context, string) error {
	s.mu.Lock()
	s.resets++
	s.mu.Unlock()
	select {
	case s.resetCh <- struct{}{}:
	default:
	}
	return nil
}

// cursorRefusingOpener refuses any connect that carries a cursor with the
// wrapped mcp.ErrEventsCursorUnknown (the shape ListenEvents returns since
// #916), and accepts a cursorless connect with a one-event stream.
type cursorRefusingOpener struct {
	inner *fakeOpener

	mu       sync.Mutex
	refusals int
}

func (o *cursorRefusingOpener) ListenEvents(ctx context.Context, p mcp.EventsListenParams) (events.EventStream, error) {
	if p.Cursor != "" {
		o.mu.Lock()
		o.refusals++
		o.mu.Unlock()
		return nil, fmt.Errorf("post events/listen: %w: cursor unknown, replaying from now", mcp.ErrEventsCursorUnknown)
	}
	return o.inner.ListenEvents(ctx, p)
}

// TestSupervisor_CursorUnknown_ResetsAndReconnectsFromEmpty pins the doc §7.2
// recovery: a server refusing the stored cursor (mcp.ErrEventsCursorUnknown,
// surfaced by ListenEvents since #916) causes the supervisor to Reset the
// stored cursor and reconnect with none — WITHOUT the refusal counting
// toward UnhealthyAfter, because the plugin is healthy; the host's cursor
// was stale. UnhealthyAfter is 1 here so any wrongly-counted failure would
// mark unhealthy immediately and fail the test.
func TestSupervisor_CursorUnknown_ResetsAndReconnectsFromEmpty(t *testing.T) {
	t.Parallel()

	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-cu", PluginID: "plug-cu", SubscriptionScopeJson: `{}`})
	q.setPlugin(db.Plugin{ID: "plug-cu", ManifestSnapshot: buildEventManifestYAML(t, "message")})

	inner := newFakeOpener(func(int) *fakeStream {
		return &fakeStream{events: []mcp.CloudEvent{{
			SpecVersion: "1.0", Source: "inst-cu", Type: "message", ID: "ev-1", Sequence: 1,
		}}}
	})
	opener := &cursorRefusingOpener{inner: inner}
	store := newStaleCursorStore()
	sink := newFakeSink()

	var healthMu sync.Mutex
	var unhealthyMarks []string
	sup := events.NewSupervisor(events.Config{
		Querier:            q,
		TestStreamResolver: &fakeResolver{opener: opener},
		Capability:         &fakeCapability{serves: true},
		Cursor:             store,
		Sink:               sink,
		HealthSetter: func(_ context.Context, instanceID string, state model.PluginHealthState, detail string) {
			if state == model.PluginHealthStateUnhealthy {
				healthMu.Lock()
				unhealthyMarks = append(unhealthyMarks, instanceID+": "+detail)
				healthMu.Unlock()
			}
		},
		BackoffInitial: time.Microsecond,
		BackoffMax:     5 * time.Millisecond,
		UnhealthyAfter: 1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-cu")
	t.Cleanup(sup.StopAll)

	// The reset is the recovery's first observable step.
	select {
	case <-store.resetCh:
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor never Reset the stored cursor after a cursor-unknown refusal")
	}

	// The reconnect-from-empty then delivers the event end to end.
	select {
	case e := <-sink.entered:
		if e.EventID != "ev-1" {
			t.Fatalf("delivered event id = %q, want ev-1", e.EventID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no event delivered after the cursor reset")
	}

	opener.mu.Lock()
	refusals := opener.refusals
	opener.mu.Unlock()
	if refusals != 1 {
		t.Fatalf("cursor-bearing connects refused = %d, want exactly 1 (the reset must not retry the same cursor)", refusals)
	}
	if got := inner.paramsAt(0).Cursor; got != "" {
		t.Fatalf("post-reset connect carried cursor %q, want empty", got)
	}

	healthMu.Lock()
	defer healthMu.Unlock()
	if len(unhealthyMarks) != 0 {
		t.Fatalf("cursor-unknown recovery marked the instance unhealthy (UnhealthyAfter=1): %v", unhealthyMarks)
	}
}
