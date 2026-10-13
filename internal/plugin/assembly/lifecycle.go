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

// eventStreams is the events supervisor's quiesce surface. Nothing sets it in
// this slice; the slice that builds events.Supervisor assigns it so Quiesce
// joins the streams before the run drain.
type eventStreams interface {
	StopAll()
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

	// ctx is the process-lifetime context New was given; the loops started by
	// OperatorListenerReady derive from it.
	ctx context.Context

	// enabled is false when plugins are disabled; the component fields below
	// are then all nil.
	enabled bool

	health *caphealth.Registry

	// newReconciler constructs the reconciler. It takes the netguard proof as
	// an argument so the guard cannot be supplied any other way than through
	// OperatorListenerReady.
	newReconciler func(guard netguard.Guard) (loop, error)

	mu    sync.Mutex
	ready bool

	reconciler    loop
	prober        loop
	collector     loop
	hostListeners listenerSet
	egressProxy   listenerSet
	events        eventStreams
	runtime       io.Closer

	// bg joins background goroutines owned by the assembly (the OAuth, nonce
	// and dedup scanners will live here). None exist yet.
	bg sync.WaitGroup

	shutdownOnce sync.Once
}

// LauncherDeps returns the plugin-only inputs run() needs to finish building
// the RunLauncher. Every field is nil in this slice: the dispatchers, tool
// classifier and canceller arrive with the slices that build them.
func (a *Assembly) LauncherDeps() LauncherDeps {
	return LauncherDeps{}
}

// BindLauncher completes the two-phase wiring once the RunLauncher exists.
// A no-op in this slice.
func (a *Assembly) BindLauncher(ctx context.Context, launcher *runpkg.RunLauncher) {}

// AdminDeps returns everything the admin plugin-instance surface needs.
// ManifestSnap is real (see the Assembly.manifestSnap doc) and CapabilityHealth
// is the live registry when plugins are enabled; every other field is nil in
// this slice.
func (a *Assembly) AdminDeps() AdminDeps {
	return AdminDeps{ManifestSnap: a.manifestSnap, CapabilityHealth: a.health}
}

// OperatorListenerReady is the security gate for self-attach. ln must be the
// operator API listener as it will be served, wrapped by netguard: until then
// a plugin container on a network Gleipnir has joined could reach the full
// operator API. Without that proof the reconciler is never constructed, so
// nothing is attached, and the caller must treat the error as fatal.
//
// With the proof it runs the boot sequence: one synchronous reconcile pass
// (core, rotation, attach, observe, in that order), then the tickers. It
// starts nothing when plugins are disabled.
func (a *Assembly) OperatorListenerReady(ln net.Listener) error {
	if !netguard.Installed(ln) {
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

	// Installed has just established the concrete type.
	rec, err := a.newReconciler(ln.(*netguard.Listener).Guard())
	if err != nil {
		return fmt.Errorf("building reconciler: %w", err)
	}
	a.reconciler = rec
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
	return nil
}

// Quiesce stops every avenue by which a plugin trigger event can reach
// RunLauncher.Launch. It must run before the run drain: a stream still
// delivering events while runs drain would race RunManager.Wait.
func (a *Assembly) Quiesce() {
	if a.events != nil {
		a.events.StopAll()
	}
}

// Shutdown stops the assembly's background goroutines. Containers are left
// running: desired state is durable and the next boot converges on it.
//
// Order: capability prober, resource collector, host-endpoint listeners,
// egress-proxy listeners, reconciler (Stop+Wait), background goroutines, then
// the runtime connection. Log capture has no loop of its own: its followers
// belong to the reconciler's observe state and end with the reconciler's Stop.
//
// The reconciler outlives the listener sets by one step, so a pass racing the
// first Close could re-add a listener. Both sets are closed again once the
// reconciler is joined to catch exactly that.
func (a *Assembly) Shutdown() {
	a.shutdownOnce.Do(func() {
		stopLoop(a.prober)
		stopLoop(a.collector)

		a.closeListeners()
		if a.reconciler != nil {
			a.reconciler.Stop()
			a.reconciler.Wait()
		}
		a.closeListeners()

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
