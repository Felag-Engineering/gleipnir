package loader

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const defaultDebounce = 250 * time.Millisecond

// installFunc is the install signature accepted by Watcher. Using a function
// type rather than the concrete Installer keeps watcher_test.go stub-friendly.
type installFunc func(ctx context.Context, tarPath string) error

// Watcher watches a directory for plugin tarballs using fsnotify and dispatches
// each settled file through an install function.
//
// Design notes:
//   - Debounce: tarball writes generate many Create/Write events as the file
//     lands on disk. The watcher waits for a quiet period (default 250ms) before
//     firing Install, so only one call is made per tarball drop.
//   - Sequential dispatch: a single goroutine drains the fire channel and runs
//     Install one at a time, mirroring the ADR-003 audit-write serialization pattern.
//   - Initial sweep: on Run the watcher enqueues any *.tar.gz / *.tgz files
//     already present so a server restart picks up tarballs that arrived while down.
//   - inotify on bind-mounted /plugins is fine on Linux (the default deployment).
type Watcher struct {
	dir      string
	debounce time.Duration
	install  installFunc
	logger   *slog.Logger

	mu      sync.Mutex
	pending map[string]*time.Timer
}

// WatcherOption configures a Watcher.
type WatcherOption func(*Watcher)

// WithDebounce overrides the debounce window. Intended for tests where 250ms
// would make the suite too slow.
func WithDebounce(d time.Duration) WatcherOption {
	return func(w *Watcher) { w.debounce = d }
}

// NewWatcher creates a Watcher for dir. The install function is called once per
// settled tarball. opts may include WithDebounce.
func NewWatcher(dir string, install installFunc, opts ...WatcherOption) *Watcher {
	w := &Watcher{
		dir:      dir,
		debounce: defaultDebounce,
		install:  install,
		logger:   slog.Default(),
		pending:  make(map[string]*time.Timer),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Run blocks until ctx is cancelled, dispatching settled tarballs to Install.
// It creates the watch directory if it does not exist, performs an initial sweep
// of any existing tarballs, then enters the fsnotify event loop.
func (w *Watcher) Run(ctx context.Context) error {
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("ensure plugins dir %q: %w", w.dir, err)
	}

	// fire receives absolute paths of tarballs that have settled (debounce expired).
	fire := make(chan string, 16)

	// Sequential dispatch goroutine. One Install at a time, mirroring ADR-003.
	// We select on both fire and ctx.Done() rather than ranging over fire so the
	// goroutine exits when ctx is cancelled without requiring close(fire). Closing
	// fire after cancelAllTimers() would race: a timer whose AfterFunc callback
	// already fired but hasn't sent yet would panic writing to a closed channel.
	// The channel is GC'd once both goroutines drop their references.
	go func() {
		for {
			select {
			case path := <-fire:
				if err := w.install(ctx, path); err != nil {
					w.logger.Warn("plugin install failed", "path", path, "err", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Initial sweep: enqueue tarballs already present before the watcher starts.
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return fmt.Errorf("read plugins dir %q: %w", w.dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() && isTarball(e.Name()) {
			abs := filepath.Join(w.dir, e.Name())
			w.schedule(ctx, abs, fire)
		}
	}

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create fsnotify watcher: %w", err)
	}
	defer fw.Close()

	if err := fw.Add(w.dir); err != nil {
		return fmt.Errorf("watch plugins dir %q: %w", w.dir, err)
	}

	for {
		select {
		case <-ctx.Done():
			w.cancelAllTimers()
			return ctx.Err()

		case event, ok := <-fw.Events:
			if !ok {
				return nil
			}
			abs := filepath.Clean(event.Name)
			if !isTarball(abs) {
				continue
			}
			switch {
			case event.Has(fsnotify.Create) || event.Has(fsnotify.Write) || event.Has(fsnotify.Rename):
				w.schedule(ctx, abs, fire)
			case event.Has(fsnotify.Remove):
				w.cancelTimer(abs)
			}

		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			w.logger.Warn("fsnotify error", "dir", w.dir, "err", err)
		}
	}
}

// schedule arms (or resets) the debounce timer for the tarball at abs. When
// the timer fires it posts abs on the fire channel.
func (w *Watcher) schedule(ctx context.Context, abs string, fire chan<- string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if t, ok := w.pending[abs]; ok {
		t.Reset(w.debounce)
		return
	}

	w.pending[abs] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.pending, abs)
		w.mu.Unlock()

		select {
		case fire <- abs:
		case <-ctx.Done():
		}
	})
}

// cancelTimer stops and removes the pending timer for abs, if any.
func (w *Watcher) cancelTimer(abs string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.pending[abs]; ok {
		t.Stop()
		delete(w.pending, abs)
	}
}

// cancelAllTimers stops all pending debounce timers. Called on ctx cancellation.
func (w *Watcher) cancelAllTimers() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for abs, t := range w.pending {
		t.Stop()
		delete(w.pending, abs)
	}
}

// isTarball returns true for filenames ending in .tar.gz or .tgz.
func isTarball(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz")
}
