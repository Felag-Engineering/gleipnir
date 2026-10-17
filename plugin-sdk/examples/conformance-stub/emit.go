package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/events"
)

// publisher is the slice of *events.Handler that emission needs.
type publisher interface {
	Publish(ctx context.Context, e events.Event) (uint64, error)
}

// emitProgress is how far emission has got, kept in the data directory so a
// restarted container continues the sequence instead of starting it again.
//
// Without it a restart would re-emit every tick under fresh ids, and a host
// that resumed from its cursor would see those as new events: a restart would
// multiply the configured count rather than complete it.
type emitProgress struct {
	// RunID prefixes every event id. It is minted once per data directory, so
	// ids stay unique across instances while a restart reuses them.
	RunID string `json:"run_id"`
	// Emitted is the number of ticks already published.
	Emitted int `json:"emitted"`
}

// progressStore loads and saves emitProgress.
type progressStore interface {
	Load() (emitProgress, error)
	Save(emitProgress) error
}

// fileProgress is the progressStore in production: one JSON file.
type fileProgress struct {
	path string
}

func newFileProgress(dataDir string) fileProgress {
	return fileProgress{path: filepath.Join(dataDir, "emit-progress.json")}
}

// Load returns the stored progress, or a fresh run id with nothing emitted
// when there is none yet.
func (f fileProgress) Load() (emitProgress, error) {
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		runID, err := newRunID()
		if err != nil {
			return emitProgress{}, err
		}
		return emitProgress{RunID: runID}, nil
	}
	if err != nil {
		return emitProgress{}, fmt.Errorf("read %s: %w", f.path, err)
	}
	var p emitProgress
	if err := json.Unmarshal(raw, &p); err != nil {
		return emitProgress{}, fmt.Errorf("decode %s: %w", f.path, err)
	}
	if p.RunID == "" || p.Emitted < 0 {
		return emitProgress{}, fmt.Errorf("%s holds %+v, want a run id and a non-negative count", f.path, p)
	}
	return p, nil
}

// Save replaces the file atomically, so a crash mid-write leaves the previous
// progress rather than a torn file that would stop the next start.
func (f fileProgress) Save(p emitProgress) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode progress: %w", err)
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return fmt.Errorf("replace %s: %w", f.path, err)
	}
	return nil
}

func newRunID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mint run id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// emitTicks publishes conformance.tick events until count have been emitted
// over the life of the data directory, waiting interval before each, and stops
// early when ctx ends.
//
// start gates only the first tick the data directory ever emits. A restarted
// stub resumes without waiting for it, and publishes its next tick at once
// rather than after interval: main starts emission before the server listens,
// so that tick is in the durable event store before any host can be connected,
// and reaches the host only if the host resumes from its cursor and the store
// serves it. That redelivery is what a restart test exists to check, and
// waiting would let a quick reconnect hide it.
//
// Progress is saved after each publish. A crash between the two re-emits that
// one tick under the same id on the next start, which the host's dedup
// absorbs; saving first would instead lose the tick.
func emitTicks(ctx context.Context, pub publisher, count int, interval time.Duration, progress progressStore, start <-chan struct{}) {
	p, err := progress.Load()
	if err != nil {
		log.Printf("conformance-stub: not emitting: %v", err)
		return
	}
	if p.Emitted >= count {
		return
	}
	first := p.Emitted + 1
	resuming := p.Emitted > 0
	if !resuming {
		select {
		case <-start:
		case <-ctx.Done():
			return
		}
	}
	for i := first; i <= count; i++ {
		// Every tick waits the interval except the first after a restart,
		// which is published at once (see above).
		if !resuming || i != first {
			if err := sleepCtx(ctx, interval); err != nil {
				return
			}
		}
		label := fmt.Sprintf("tick-%d", i)
		_, err := pub.Publish(ctx, events.Event{
			Type: "conformance.tick",
			ID:   p.RunID + "-" + label,
			Data: map[string]any{"label": label},
		})
		if err != nil {
			log.Printf("conformance-stub: publish %s: %v", label, err)
			continue
		}
		p.Emitted = i
		if err := progress.Save(p); err != nil {
			log.Printf("conformance-stub: not emitting further: %v", err)
			return
		}
	}
}

// listenGate closes Started once the first events/listen stream is open.
//
// events/listen with no cursor delivers only events published after it
// subscribed, so ticks emitted before a host first connects reach nobody.
// Waiting for the stream makes the configured count the count a host sees.
type listenGate struct {
	once    sync.Once
	started chan struct{}
}

func newListenGate() *listenGate {
	return &listenGate{started: make(chan struct{})}
}

// Started is closed when the first stream opens.
func (g *listenGate) Started() <-chan struct{} { return g.started }

// Wrap returns next with stream starts reported to the gate.
func (g *listenGate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&streamStartWriter{ResponseWriter: w, onStream: g.open}, r)
	})
}

func (g *listenGate) open() {
	g.once.Do(func() { close(g.started) })
}

// streamStartWriter reports the response header of an event stream. The
// events handler subscribes to its buffer before it writes that header, so by
// then every later publish reaches the stream. A JSON-RPC error (a refused
// cursor, bad params) is written as a JSON body and never counts.
type streamStartWriter struct {
	http.ResponseWriter
	onStream func()
}

func (w *streamStartWriter) WriteHeader(code int) {
	if code == http.StatusOK && strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.onStream()
	}
	w.ResponseWriter.WriteHeader(code)
}

// Flush passes through: the events handler refuses a writer that cannot flush.
func (w *streamStartWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *streamStartWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
