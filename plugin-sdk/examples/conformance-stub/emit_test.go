package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/events"
)

type recordingPublisher struct {
	published []events.Event
}

func (r *recordingPublisher) Publish(_ context.Context, e events.Event) (uint64, error) {
	r.published = append(r.published, e)
	return uint64(len(r.published)), nil
}

// memProgress is an in-memory progressStore that records every save.
type memProgress struct {
	p       emitProgress
	saves   []emitProgress
	loadErr error
}

func (m *memProgress) Load() (emitProgress, error) { return m.p, m.loadErr }

func (m *memProgress) Save(p emitProgress) error {
	m.p = p
	m.saves = append(m.saves, p)
	return nil
}

func closedChan() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func labelsOf(published []events.Event) []string {
	var out []string
	for _, e := range published {
		data, _ := e.Data.(map[string]any)
		label, _ := data["label"].(string)
		out = append(out, label)
	}
	return out
}

func TestEmitTicks(t *testing.T) {
	tests := []struct {
		name       string
		start      emitProgress
		count      int
		wantLabels []string
	}{
		{"fresh data directory emits every tick", emitProgress{RunID: "run"}, 3, []string{"tick-1", "tick-2", "tick-3"}},
		{"a restart resumes after the last emitted tick", emitProgress{RunID: "run", Emitted: 2}, 5, []string{"tick-3", "tick-4", "tick-5"}},
		{"nothing left to emit", emitProgress{RunID: "run", Emitted: 3}, 3, nil},
		{"zero count emits nothing", emitProgress{RunID: "run"}, 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pub := &recordingPublisher{}
			progress := &memProgress{p: tc.start}
			emitTicks(t.Context(), pub, tc.count, 0, progress, closedChan())

			if got := labelsOf(pub.published); strings.Join(got, ",") != strings.Join(tc.wantLabels, ",") {
				t.Fatalf("labels = %v, want %v", got, tc.wantLabels)
			}
			for _, e := range pub.published {
				data, _ := e.Data.(map[string]any)
				if e.Type != "conformance.tick" || e.ID != "run-"+data["label"].(string) {
					t.Errorf("event = %+v, want a conformance.tick with id run-<label>", e)
				}
			}
			// Progress is saved after every publish, so a restart at any point
			// resumes at the next tick.
			if len(progress.saves) != len(tc.wantLabels) {
				t.Fatalf("saved progress %d times, want %d", len(progress.saves), len(tc.wantLabels))
			}
			for i, s := range progress.saves {
				if want := tc.start.Emitted + i + 1; s.Emitted != want || s.RunID != "run" {
					t.Errorf("save %d = %+v, want run id %q and %d emitted", i, s, "run", want)
				}
			}
		})
	}
}

func TestEmitTicksStopsWithoutEmitting(t *testing.T) {
	tests := []struct {
		name     string
		progress *memProgress
		start    func() <-chan struct{}
		interval time.Duration
		cancel   bool
	}{
		{
			name:     "cancelled before the first interval elapses",
			progress: &memProgress{p: emitProgress{RunID: "run"}},
			start:    closedChan,
			interval: time.Hour,
			cancel:   true,
		},
		{
			name:     "first tick waits for start",
			progress: &memProgress{p: emitProgress{RunID: "run"}},
			start:    func() <-chan struct{} { return make(chan struct{}) },
			cancel:   true,
		},
		{
			name:     "unreadable progress",
			progress: &memProgress{loadErr: errors.New("torn file")},
			start:    closedChan,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if tc.cancel {
				cancel()
			} else {
				defer cancel()
			}
			pub := &recordingPublisher{}
			emitTicks(ctx, pub, 3, tc.interval, tc.progress, tc.start())
			if len(pub.published) != 0 {
				t.Errorf("published %d events, want 0", len(pub.published))
			}
		})
	}
}

// A restarted stub publishes its next tick at once: no new listener, no
// interval. That tick reaches a host only through the durable store.
func TestEmitTicksResumesAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pub := &cancelAfterPublisher{after: 1, cancel: cancel}
	never := make(chan struct{})
	emitTicks(ctx, pub, 5, time.Hour, &memProgress{p: emitProgress{RunID: "run", Emitted: 3}}, never)
	if got := labelsOf(pub.published); len(got) != 1 || got[0] != "tick-4" {
		t.Errorf("labels = %v, want [tick-4] published without waiting for start or the interval", got)
	}
}

// cancelAfterPublisher cancels emission once it has seen after events, so a
// test can stop a loop whose next step would be a long interval.
type cancelAfterPublisher struct {
	recordingPublisher
	after  int
	cancel context.CancelFunc
}

func (c *cancelAfterPublisher) Publish(ctx context.Context, e events.Event) (uint64, error) {
	seq, err := c.recordingPublisher.Publish(ctx, e)
	if len(c.published) >= c.after {
		c.cancel()
	}
	return seq, err
}

func TestFileProgress(t *testing.T) {
	t.Run("missing file starts fresh with a run id", func(t *testing.T) {
		p, err := newFileProgress(t.TempDir()).Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if p.RunID == "" || p.Emitted != 0 {
			t.Errorf("Load = %+v, want a run id and nothing emitted", p)
		}
	})
	t.Run("save then load round trips", func(t *testing.T) {
		store := newFileProgress(t.TempDir())
		want := emitProgress{RunID: "abc", Emitted: 4}
		if err := store.Save(want); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got != want {
			t.Errorf("Load = %+v, want %+v", got, want)
		}
	})
	for name, content := range map[string]string{
		"corrupt file":     "{not json",
		"missing run id":   `{"emitted":1}`,
		"negative emitted": `{"run_id":"abc","emitted":-1}`,
	} {
		t.Run(name+" is an error, not a fresh start", func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "emit-progress.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if p, err := newFileProgress(dir).Load(); err == nil {
				t.Errorf("Load = %+v, want an error: starting over would re-emit every tick", p)
			}
		})
	}
}

func TestListenGate(t *testing.T) {
	h := events.NewHandler("test", eventKinds)
	gate := newListenGate()
	srv := httptest.NewServer(gate.Wrap(h.ListenHandler()))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { h.Close("test over") })

	listen := func(params string) *http.Response {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"events/listen","params":` + params + `}`
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("events/listen: %v", err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	// A refused cursor is a JSON-RPC error, not a stream.
	if resp := listen(`{"cursor":"999"}`); resp.Header.Get("Content-Type") == "text/event-stream" {
		t.Fatal("an unsatisfiable cursor opened a stream; the control below would be meaningless")
	}
	select {
	case <-gate.Started():
		t.Fatal("gate opened on a refused events/listen")
	default:
	}

	resp := listen(`{}`)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("events/listen Content-Type = %q, want an event stream", ct)
	}
	select {
	case <-gate.Started():
	default:
		t.Fatal("gate still closed after a stream opened")
	}
}
