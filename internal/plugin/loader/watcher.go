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

	// ctx and fire are set during Run and used by schedule/AfterFunc callbacks.
	// They are only written once (at the start of Run) before any goroutines that
	// read them are spawned, so no additional synchronisation is needed.
	ctx  context.Context
	fire chan string
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

// Setup creates the plugins directory if it does not exist, opens the fsnotify
// watcher, and registers the directory with it. The returned *fsnotify.Watcher
// must be passed to Run. Separating Setup from Run lets callers (StartWatcher)
// surface pre-flight errors synchronously before spawning any goroutines.
func (w *Watcher) Setup() (*fsnotify.Watcher, error) {
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure plugins dir %q: %w", w.dir, err)
	}

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	if err := fw.Add(w.dir); err != nil {
		fw.Close()
		return nil, fmt.Errorf("watch plugins dir %q: %w", w.dir, err)
	}

	return fw, nil
}

// Run blocks until ctx is cancelled, dispatching settled tarballs to Install.
// It takes the fsnotify.Watcher created by Setup, spawns the dispatch goroutine,
// performs an initial sweep of any existing tarballs, then enters the event loop.
//
// Initial sweep: for each unique plugin name found in the drop directory, only
// the highest-version tarball (by semver comparison) is scheduled. Unreadable
// tarballs and lower-version duplicates are logged at Warn so operators can see
// what the sweep chose and why. This makes the sweep deterministic regardless of
// filename or os.ReadDir order.
//
// Run must not be called more than once on the same Watcher: it writes w.ctx and
// w.fire at startup, and a second call would stomp those fields while the first
// call's AfterFunc callbacks are still reading them.
func (w *Watcher) Run(ctx context.Context, fw *fsnotify.Watcher) error {
	defer func() {
		if cerr := fw.Close(); cerr != nil {
			w.logger.Debug("watcher: fsnotify close error", "dir", w.dir, "err", cerr)
		}
	}()

	// fire receives absolute paths of tarballs that have settled (debounce expired).
	// Buffer of 16 absorbs an initial sweep burst without blocking schedule callbacks.
	fire := make(chan string, 16)

	// Store ctx and fire so schedule()'s AfterFunc callbacks can reach them without
	// capturing the loop variables from Run (which would require passing them through
	// every call site).
	w.ctx = ctx
	w.fire = fire

	// Sequential dispatch goroutine. One Install at a time, mirroring ADR-003.
	// We select on both fire and ctx.Done() rather than ranging over fire so the
	// goroutine exits when ctx is cancelled without requiring close(fire). Closing
	// fire after cancelAllTimers() would race: a timer whose AfterFunc callback
	// already fired but hasn't sent yet would panic writing to a closed channel.
	// The channel is GC'd once both goroutines drop their references.
	//
	// The dispatch goroutine is started AFTER fsnotify is set up so that if
	// fsnotify setup fails we don't leak a goroutine waiting for a ctx cancel.
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

	// Initial sweep: for each plugin name, select only the highest-version
	// tarball so a cold start is deterministic regardless of filename order.
	// The dispatch goroutine is already running so schedule() sends are drained.
	if err := w.sweepDir(); err != nil {
		return err
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
				w.schedule(abs)
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

// schedule arms the debounce timer for the tarball at abs. If a timer is
// already pending it is stopped and replaced with a fresh one to avoid the
// Reset-after-fire-already-queued race under Go 1.23+ AfterFunc semantics.
func (w *Watcher) schedule(abs string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if existing, ok := w.pending[abs]; ok {
		existing.Stop()
		delete(w.pending, abs)
	}

	w.pending[abs] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.pending, abs)
		w.mu.Unlock()

		select {
		case w.fire <- abs:
		case <-w.ctx.Done():
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

// sweepCandidate holds the winning tarball path and version for one plugin name.
type sweepCandidate struct {
	path    string
	version string
}

// sweepDir reads the drop directory, selects the highest-version tarball per
// plugin name, and schedules only the winners. Unreadable tarballs are logged
// at Warn and skipped; lower-version duplicates are also logged at Warn so
// operators can see what was ignored and why.
func (w *Watcher) sweepDir() error {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return fmt.Errorf("read plugins dir %q: %w", w.dir, err)
	}

	// winners maps plugin name → the current best candidate.
	winners := make(map[string]sweepCandidate)

	for _, e := range entries {
		if e.IsDir() || !isTarball(e.Name()) {
			continue
		}
		abs := filepath.Join(w.dir, e.Name())

		m, peekErr := ReadManifestFromTarball(abs)
		if peekErr != nil {
			w.logger.Warn("plugin sweep: skipping unreadable tarball",
				"path", abs, "err", peekErr)
			continue
		}

		existing, seen := winners[m.Name]
		if !seen {
			winners[m.Name] = sweepCandidate{path: abs, version: m.Version}
			continue
		}

		cmp := compareVersions(m.Version, existing.version)
		switch {
		case cmp > 0:
			// Incoming is higher — demote the previous winner.
			w.logger.Warn("plugin sweep: ignoring lower-version tarball",
				"name", m.Name,
				"version", existing.version,
				"chosen_version", m.Version,
				"path", existing.path,
			)
			winners[m.Name] = sweepCandidate{path: abs, version: m.Version}
		case cmp == 0:
			// Same version from a different file — keep the lexically-first path
			// for stable determinism and log the duplicate.
			if abs < existing.path {
				w.logger.Warn("plugin sweep: ignoring duplicate-version tarball",
					"name", m.Name,
					"version", m.Version,
					"chosen_path", abs,
					"ignored_path", existing.path,
				)
				winners[m.Name] = sweepCandidate{path: abs, version: m.Version}
			} else {
				w.logger.Warn("plugin sweep: ignoring duplicate-version tarball",
					"name", m.Name,
					"version", m.Version,
					"chosen_path", existing.path,
					"ignored_path", abs,
				)
			}
		default:
			// Incoming is lower — keep current winner.
			w.logger.Warn("plugin sweep: ignoring lower-version tarball",
				"name", m.Name,
				"version", m.Version,
				"chosen_version", existing.version,
				"path", abs,
			)
		}
	}

	for _, c := range winners {
		w.schedule(c.path)
	}
	return nil
}
