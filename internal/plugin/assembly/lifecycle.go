package assembly

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	plugintrigger "github.com/felag-engineering/gleipnir/internal/plugin/trigger"
)

// loop is a long-lived component with a synchronous first pass: Start returns
// once that pass has run and the periodic loop is going. *reconciler.Reconciler,
// *caphealth.Prober and *resources.Collector all satisfy it.
type loop interface {
	Start(ctx context.Context) error
	Stop()
	Wait()
}

// listenerSet is the part of *hostendpoint.ListenerSet shutdown needs.
type listenerSet interface {
	Close(ctx context.Context) error
}

// eventStreams is the events supervisor's surface. Start and Stop are driven by
// the reconciler's observe pass as instances come, go and rotate; Restart is the
// admin surface reopening a stream under a changed scope; StopAll is what
// Quiesce joins before the run drain, after which Start is a no-op.
// *events.Supervisor satisfies it.
type eventStreams interface {
	Start(ctx context.Context, instanceID string)
	Restart(ctx context.Context, instanceID string)
	Stop(instanceID string)
	StopAll()
}

// taskPoller is the Tasks poll scheduler's lifecycle: *mcp.PollScheduler.
type taskPoller interface {
	Start(ctx context.Context, tickInterval time.Duration)
	Wait()
}

// gcPasser is the reconciler's cleanup pass.
type gcPasser interface {
	ReconcileGC(ctx context.Context) (reconciler.GCResult, error)
}

// listenerCloseTimeout bounds one graceful ListenerSet shutdown at exit, so an
// in-flight host call cannot hold the process up.
const listenerCloseTimeout = 5 * time.Second

// Assembly is the v2 plugin substrate. Every method is nil-safe: with plugins
// disabled (no container socket in auto mode) every component field is nil
// and the lifecycle methods do nothing.
type Assembly struct {
	// manifestSnap is real (non-nil whenever deps.Store is set), even though
	// no plugin ever populates it with rows today. *configvalidate.Snapshotter
	// panics on a nil receiver (it dereferences its DB querier field), and
	// ManifestSnap is handed to request-time handlers (the audience handler,
	// the binding-test handler, the subscribed-binding validator) that call
	// it — an admin exercising any of those endpoints against the tagged
	// build must get a normal "not found" style error, not a 500 crash.
	manifestSnap *configvalidate.Snapshotter

	// ctx is the assembly's own lifetime context, a child of the one New was
	// given; the loops started by OperatorListenerReady derive from it, and
	// Shutdown cancels it so those loops stop even when the parent never was.
	ctx    context.Context
	cancel context.CancelFunc

	// enabled is false when plugins are disabled; the component fields below
	// are then all nil.
	enabled bool

	health *caphealth.Registry

	// launcher is what LauncherDeps hands to run(): the task-backed approval
	// and feedback dispatchers and the managed tool classifier. Dispatcher,
	// Registrar, ToolResolver and Canceller stay nil on purpose — a managed
	// plugin's tools are MCP tools, so they go through the MCP registry and
	// the per-server gate, not a plugin dispatch pool.
	launcher LauncherDeps

	// admin is the container-substrate half of what AdminDeps hands to the
	// admin surface; AdminDeps adds the two fields that exist whether or not
	// plugins do.
	admin AdminDeps

	// watcher installs bundles dropped into the plugins directory. It is set up
	// and run by OperatorListenerReady, never earlier: an install writes rows
	// and, for a plugin that is already active, wakes the reconciler.
	watcher *loader.Watcher

	// kicker lets the admin surface wake the reconciler, which does not exist
	// until OperatorListenerReady.
	kicker *lateKicker

	// notifier fans the fixed "input requested" message out to notify-enabled
	// audience entries. It has no live call site yet: the HITL routing that
	// fires it after a request is routed is a later slice.
	notifier *hitl.Notifier

	// newReconciler constructs the reconciler. It takes the netguard proof as
	// an argument so the guard cannot be supplied any other way than through
	// OperatorListenerReady.
	newReconciler func(guard netguard.Guard, streams reconciler.EventStreams) (*reconciler.Reconciler, error)

	// bindEvents builds the events supervisor over the launcher's trigger
	// pipeline. It runs in BindLauncher because the pipeline's sink launches
	// runs and the launcher is built after the assembly.
	bindEvents func(launcher plugintrigger.RunLauncher) eventStreams

	mu    sync.Mutex
	ready bool

	reconciler    loop
	prober        loop
	collector     loop
	hostListeners listenerSet
	egressProxy   listenerSet
	events        eventStreams
	poller        taskPoller
	runtime       io.Closer

	// backgrounds are the composed scanners OperatorListenerReady starts on bg.
	backgrounds []backgroundLoop

	// bg joins background goroutines owned by the assembly: the OAuth, nonce
	// and dedup scanners, and the container GC schedule.
	bg sync.WaitGroup

	shutdownOnce sync.Once
}

// LauncherDeps returns the plugin-only inputs run() needs to finish building
// the RunLauncher. Zero while plugins are disabled.
func (a *Assembly) LauncherDeps() LauncherDeps {
	return a.launcher
}

// BindLauncher completes the two-phase wiring once the RunLauncher exists: it
// builds the events supervisor whose events flow, through the trigger
// dispatcher, into launcher. Nothing is streamed until OperatorListenerReady.
// A no-op while plugins are disabled.
func (a *Assembly) BindLauncher(_ context.Context, launcher *runpkg.RunLauncher) {
	if !a.enabled {
		return
	}
	if launcher == nil {
		slog.Error("plugin events are not bound: no run launcher was provided")
		return
	}
	a.bind(launcher)
}

func (a *Assembly) bind(launcher plugintrigger.RunLauncher) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = a.bindEvents(launcher)
}

// AdminDeps returns everything the admin plugin-instance surface needs.
// ManifestSnap is real (see the Assembly.manifestSnap doc) and the rest is the
// container-substrate implementation of each admin interface when plugins are
// enabled. While plugins are disabled every other field is nil.
//
// Installer is the OCI installer, which the upload route calls and the
// directory watcher drives. The OAuth and credentials handlers, the credential
// seeder and the public-URL hook are the v1 ones (they read manifests through
// manifest.Read, so they handle a v2 manifest) and are nil without an
// encryption key. OptionsHandler stays nil: it proxies a plugin's gRPC
// ConfigOptionsService, which a containerized MCP server does not serve, and
// the router then does not register the route, rather than serving a faked empty list.
func (a *Assembly) AdminDeps() AdminDeps {
	d := a.admin
	d.ManifestSnap = a.manifestSnap
	d.CapabilityHealth = a.health
	return d
}

// OperatorListenerReady is the security gate for self-attach. ln must be the
// operator API listener as it will be served, wrapped by netguard: until then
// a plugin container on a network Gleipnir has joined could reach the full
// operator API. Without that proof the reconciler is never constructed, so
// nothing is attached, and the caller must treat the error as fatal.
//
// With the proof it runs the boot sequence: one synchronous reconcile pass
// (core, rotation, attach, observe, in that order), then the tickers, then the
// consumers of what the pass converged — the task poller and the background
// scanners — so nothing reads an endpoint the boot pass has not yet registered
// or withdrawn. Events streams are not started here: the observe pass inside
// the boot pass (and every later one) starts a stream for each live instance
// and stops it when the instance goes. It starts nothing when plugins are
// disabled.
func (a *Assembly) OperatorListenerReady(ln net.Listener) error {
	guarded, ok := ln.(*netguard.Listener)
	if !ok || !netguard.Installed(ln) {
		return errors.New("operator API listener is not wrapped by netguard; refusing to enable plugin self-attach")
	}
	if !a.enabled {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ready {
		return errors.New("operator listener already reported ready")
	}

	// A nil a.events must reach the reconciler as a nil interface, not a typed
	// nil, so the observe pass skips event streams entirely.
	var streams reconciler.EventStreams
	if a.events != nil {
		streams = a.events
	}
	rec, err := a.newReconciler(guarded.Guard(), streams)
	if err != nil {
		return fmt.Errorf("building reconciler: %w", err)
	}
	a.reconciler = rec
	a.kicker.set(rec)
	a.ready = true

	if err := rec.Start(a.ctx); err != nil {
		return fmt.Errorf("starting reconciler: %w", err)
	}
	if err := a.prober.Start(a.ctx); err != nil {
		return fmt.Errorf("starting capability prober: %w", err)
	}
	if err := a.collector.Start(a.ctx); err != nil {
		return fmt.Errorf("starting resource collector: %w", err)
	}

	a.poller.Start(a.ctx, taskPollTick)
	a.scheduleGC(rec, containerGCInterval)
	a.startBackground()

	// Last, so a bundle already sitting in the directory is installed against a
	// substrate that has finished its boot pass.
	return a.startWatcher()
}

// scheduleGC runs the cleanup pass every interval. It is a separate, slower
// cadence than convergence, and a failed pass costs one interval: the next one
// finds the same unreferenced resources.
func (a *Assembly) scheduleGC(gc gcPasser, interval time.Duration) {
	a.goBackground(every(interval, func(ctx context.Context) {
		result, err := gc.ReconcileGC(ctx)
		if err != nil {
			slog.WarnContext(ctx, "container cleanup pass failed", "err", err)
			return
		}
		if result.ImagesReclaimed+result.SubnetsReleased+result.TokensPurged+result.Errors > 0 {
			slog.InfoContext(ctx, "container cleanup pass",
				"images_reclaimed", result.ImagesReclaimed,
				"subnets_released", result.SubnetsReleased,
				"tokens_purged", result.TokensPurged,
				"errors", result.Errors)
		}
	}))
}

// Quiesce stops every avenue by which a plugin trigger event can reach
// RunLauncher.Launch. It must run before the run drain: a stream still
// delivering events while runs drain would race RunManager.Wait.
//
// StopAll cancels each stream and joins its goroutine, so once Quiesce returns
// no event is in flight and none can start.
func (a *Assembly) Quiesce() {
	if a.events != nil {
		a.events.StopAll()
	}
}

// Shutdown stops the assembly's background goroutines. Containers are left
// running: desired state is durable and the next boot converges on it.
//
// The bundle watcher is joined with the other background goroutines, after the
// assembly's context is cancelled.
//
// Order: events streams (a backstop for a path that skipped Quiesce),
// capability prober, resource collector, reconciler (Stop+Wait), listener
// sets, then the task poller and background goroutines (the assembly's context
// is cancelled here so they return), then the runtime connection. The
// reconciler goes before the listener sets so a late pass cannot re-add a
// listener after it was closed. Log capture has no loop of its own: its
// followers belong to the reconciler's observe state and end with its Stop.
//
// It reads component fields without a.mu: it runs after OperatorListenerReady
// has returned (or never will), and main never calls the two concurrently.
func (a *Assembly) Shutdown() {
	a.shutdownOnce.Do(func() {
		a.Quiesce()

		stopLoop(a.prober)
		stopLoop(a.collector)
		stopLoop(a.reconciler)

		a.closeListeners()

		if a.cancel != nil {
			a.cancel()
		}
		if a.poller != nil {
			a.poller.Wait()
		}
		a.bg.Wait()

		if a.runtime != nil {
			if err := a.runtime.Close(); err != nil {
				slog.Warn("closing container runtime", "err", err)
			}
		}
	})
}

func stopLoop(l loop) {
	if l == nil {
		return
	}
	l.Stop()
	l.Wait()
}

func (a *Assembly) closeListeners() {
	ctx, cancel := context.WithTimeout(context.Background(), listenerCloseTimeout)
	defer cancel()
	if a.hostListeners != nil {
		if err := a.hostListeners.Close(ctx); err != nil {
			slog.Warn("closing host endpoint listeners", "err", err)
		}
	}
	if a.egressProxy != nil {
		if err := a.egressProxy.Close(ctx); err != nil {
			slog.Warn("closing egress proxy listeners", "err", err)
		}
	}
}
