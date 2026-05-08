// Package generation provides per-plugin-generation refcounting and drain
// primitives for hot-reload. On hot-reload the old generation must drain its
// in-flight Host RPCs before the new generation begins accepting traffic.
//
// This package is a leaf: it imports only stdlib + log/slog. No internal
// packages are imported — same boundary discipline as internal/plugin/identity
// and internal/toolregistry (CLAUDE.md package-boundary constraint).
package generation

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// forceCancelGrace is the additional window given after the drain grace period
// to let force-cancelled calls observe ctx.Done() and return before we declare
// the drain complete regardless. This mirrors the 5s deadline used by
// dispatch.Pool.CancelRun (spec §13.8).
const forceCancelGrace = 5 * time.Second

// GenerationKey identifies a specific generation of a plugin instance.
//
// NOTE: the host-RPC generation counter here is independent of
// internal/plugin/tools.Registrar.Generation (which is int64 and is minted at
// registrar time for tool-name reservations). These live in different epochs
// and are intentionally not correlated — do not attempt to unify them.
type GenerationKey struct {
	InstanceID string
	Generation uint64
}

// genState is the per-instance mutable state managed by the Controller.
type genState struct {
	mu sync.Mutex

	// current is the generation that new Acquire calls are assigned to.
	current uint64

	// refs tracks the number of in-flight Host RPCs per generation.
	refs map[uint64]int

	// drainCh is closed when refs[g] reaches zero AND g is no longer current.
	// A nil entry means no drain channel has been allocated yet (or it already
	// fired and was cleaned up).
	drainCh map[uint64]chan struct{}

	// cancels holds cancel entries for every in-flight Host RPC, keyed by
	// generation. BeginDrain invokes all of these after the grace period elapses
	// (force-cancel path). Entries are removed by release() when a call finishes
	// naturally so BeginDrain does not double-invoke them.
	cancels map[uint64][]*cancelEntry

	// nextCancelID is a monotonically increasing counter used to give each
	// cancelEntry a stable identity for removal without comparing func pointers.
	nextCancelID uint64

	// paused is true while BeginDrain is in progress. New Acquire callers block
	// on pausedCh until paused is cleared (a fresh pausedCh is closed).
	paused   bool
	pausedCh chan struct{}

	// draining prevents concurrent BeginDrain calls from racing on the same
	// instance. The second caller gets an explicit error; callers are expected
	// to serialise their own reload requests.
	draining bool

	// unregistered is set by UnregisterInstance. Any subsequent Acquire call
	// returns an error immediately rather than blocking.
	unregistered   bool
	unregisteredCh chan struct{}
}

// newGenState returns an initialised genState for a brand-new instance,
// starting at generation 1.
func newGenState() *genState {
	s := &genState{
		current:        1,
		refs:           make(map[uint64]int),
		drainCh:        make(map[uint64]chan struct{}),
		cancels:        make(map[uint64][]*cancelEntry),
		pausedCh:       make(chan struct{}),
		unregisteredCh: make(chan struct{}),
	}
	// Close pausedCh immediately so initial Acquire callers are not blocked.
	close(s.pausedCh)
	return s
}

// Controller owns per-instance generation state for all running plugin
// instances. It is used by the hostsvc interceptor to gate Host RPCs and by
// process.Manager to coordinate hot-reload drains.
//
// All public methods are safe for concurrent use.
type Controller struct {
	mu         sync.Mutex
	byInstance map[string]*genState
}

// New returns a Controller with no registered instances.
func New() *Controller {
	return &Controller{
		byInstance: make(map[string]*genState),
	}
}

// RegisterInstance ensures an instance entry exists and returns the current
// generation. For a brand-new instance, the first generation is 1. For an
// already-registered instance, the current generation is returned unchanged —
// including after BeginDrain has bumped the counter. RegisterInstance is a
// soft ensure-exists; only New() and the per-instance generation rotation in
// BeginDrain mutate the counter.
//
// Callers (process.Manager.Start) may call this unconditionally on both cold
// start and post-reload restart paths — it is idempotent and safe.
//
// See issue #294.
func (c *Controller) RegisterInstance(instanceID string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	if s, ok := c.byInstance[instanceID]; ok {
		s.mu.Lock()
		gen := s.current
		s.mu.Unlock()
		return gen
	}

	s := newGenState()
	c.byInstance[instanceID] = s
	return s.current
}

// Acquire increments the refcount for the current generation of instanceID and
// returns a wrapped context, a release function, and the generation number.
//
// If the instance is paused (a hot-reload drain is in progress), Acquire
// blocks until the new generation becomes current, until the instance is
// unregistered, or until ctx is cancelled. This means:
//   - In-flight RPCs that already acquired before the drain started continue
//     under a cancellable ctx and are force-cancelled only after the grace
//     period elapses.
//   - New RPCs arriving during the pause window block in Acquire; if their own
//     request ctx expires before the drain completes, they return Unavailable.
//
// The caller MUST call release() exactly once when the RPC completes.
//
// See issue #294, spec §8.4.
func (c *Controller) Acquire(ctx context.Context, instanceID string) (wrappedCtx context.Context, release func(), gen uint64, err error) {
	for {
		c.mu.Lock()
		s, ok := c.byInstance[instanceID]
		c.mu.Unlock()

		if !ok {
			return nil, nil, 0, errors.New("generation: instance not registered")
		}

		s.mu.Lock()
		if s.unregistered {
			s.mu.Unlock()
			return nil, nil, 0, errors.New("generation: instance unregistered")
		}

		if !s.paused {
			// Not paused: assign to the current generation immediately.
			g := s.current
			s.refs[g]++
			wrappedCtx, cancel := context.WithCancel(ctx)

			// Assign a stable ID to this cancel entry so the release closure can
			// remove it without comparing function pointers.
			s.nextCancelID++
			entry := &cancelEntry{id: s.nextCancelID, cancel: cancel}
			s.cancels[g] = append(s.cancels[g], entry)

			// Build the release closure while holding the lock so it captures a
			// stable reference to s, g, and entry.
			rel := buildRelease(s, g, entry)
			s.mu.Unlock()
			return wrappedCtx, rel, g, nil
		}

		// Paused: snapshot the channels we need to wait on before releasing the lock.
		pausedCh := s.pausedCh
		unregisteredCh := s.unregisteredCh
		s.mu.Unlock()

		// Block until the pause clears, the instance is unregistered, or ctx expires.
		select {
		case <-pausedCh:
			// Pause was lifted; loop back to try again under the new generation.
			continue
		case <-unregisteredCh:
			return nil, nil, 0, errors.New("generation: instance unregistered")
		case <-ctx.Done():
			return nil, nil, 0, ctx.Err()
		}
	}
}

// cancelEntry pairs a cancel func with a stable index so the release closure
// can remove its own entry without comparing function pointers (which Go does
// not support directly).
type cancelEntry struct {
	id     uint64 // unique within the genState, monotonically increasing
	cancel context.CancelFunc
}

// buildRelease constructs the release closure for a single Acquire. It is
// extracted here so the closure captures only s, g, and entry — not the
// entire Acquire stack frame.
func buildRelease(s *genState, g uint64, entry *cancelEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			// Remove this cancel entry first so BeginDrain does not double-invoke
			// it after the call has already completed naturally.
			s.mu.Lock()
			s.cancels[g] = removeCancelEntry(s.cancels[g], entry.id)
			s.refs[g]--

			if s.refs[g] == 0 {
				// If a drain channel exists for this generation, close it to
				// unblock BeginDrain. A drainCh is allocated only when BeginDrain
				// is actively waiting on this generation, so it is safe to close
				// here regardless of whether g is still current.
				if ch, ok := s.drainCh[g]; ok {
					close(ch)
					delete(s.drainCh, g)
				}
				// Only clean up the refs/cancels maps if g is no longer current;
				// otherwise the current generation's zero-ref state is expected
				// (no calls in flight yet is normal, not a stale entry).
				if g != s.current {
					delete(s.refs, g)
					delete(s.cancels, g)
				}
			}
			s.mu.Unlock()
		})
	}
}

// removeCancelEntry removes the entry with the given id from the slice.
func removeCancelEntry(entries []*cancelEntry, id uint64) []*cancelEntry {
	for i, e := range entries {
		if e.id == id {
			return append(entries[:i], entries[i+1:]...)
		}
	}
	return entries
}

// BeginDrain pauses new Host RPC traffic for instanceID, waits up to grace for
// all in-flight RPCs of the current (old) generation to complete, then rotates
// to a fresh generation. After the grace period, any remaining in-flight calls
// are force-cancelled via their context.CancelFunc (mirroring the 5s
// cancel-then-disconnect pattern of dispatch.Pool.CancelRun, spec §13.8).
//
// If another drain is already in progress for this instance, BeginDrain returns
// an explicit error. Callers (the reload controller) are expected to serialise
// their own reload requests; coalescing concurrent drains would add complexity
// without a meaningful benefit.
//
// Returns the new generation number and a boolean indicating whether all
// in-flight RPCs drained naturally before the grace period elapsed.
//
// Note: force-cancel here uses context.CancelFunc rather than conn.Close().
// Host RPCs are ordinary unary handlers that observe ctx.Done() cleanly; ctx
// cancellation is sufficient and avoids the process-wide blast radius that
// dispatch.Pool accepts for streaming tool calls.
//
// See issue #294, spec §8.4 + §13.8.
func (c *Controller) BeginDrain(ctx context.Context, instanceID string, grace time.Duration) (newGeneration uint64, drained bool, err error) {
	c.mu.Lock()
	s, ok := c.byInstance[instanceID]
	c.mu.Unlock()

	if !ok {
		return 0, false, errors.New("generation: instance not registered")
	}

	// --- Step 1: enter pause state under the instance lock ---
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return 0, false, errors.New("generation: drain already in progress")
	}
	s.draining = true
	s.paused = true
	s.pausedCh = make(chan struct{}) // fresh channel; will be closed on rotation

	oldGen := s.current

	// Allocate a drain channel if there are in-flight calls for oldGen. If refs
	// is already zero the generation has no active callers, so we skip waiting.
	var waitCh chan struct{}
	if s.refs[oldGen] > 0 {
		ch := make(chan struct{})
		s.drainCh[oldGen] = ch
		waitCh = ch
	}
	s.mu.Unlock()

	// --- Step 2: wait up to grace for in-flight calls to drain ---
	drainedNaturally := false
	if waitCh != nil {
		timer := time.NewTimer(grace)
		defer timer.Stop()

		select {
		case <-waitCh:
			drainedNaturally = true
		case <-timer.C:
			// Grace period elapsed; proceed to force-cancel below.
		case <-ctx.Done():
			// Caller cancelled; still rotate so the instance is not left paused.
		}
	} else {
		drainedNaturally = true
	}

	// --- Step 3: force-cancel remaining in-flight calls if needed ---
	if !drainedNaturally {
		s.mu.Lock()
		// Snapshot and clear the cancel entries so we do not double-invoke them
		// if calls complete concurrently while we are iterating.
		toCancel := s.cancels[oldGen]
		s.cancels[oldGen] = nil
		s.mu.Unlock()

		for _, entry := range toCancel {
			entry.cancel()
		}

		// Wait up to forceCancelGrace for the refcount to drain to zero. We log a
		// warning if any calls are still in-flight after that window — the process
		// continues regardless (same envelope as dispatch.Pool per spec §13.8).
		if waitCh != nil {
			forceTimer := time.NewTimer(forceCancelGrace)
			defer forceTimer.Stop()
			select {
			case <-waitCh:
				// All calls have released.
			case <-forceTimer.C:
				s.mu.Lock()
				remaining := s.refs[oldGen]
				s.mu.Unlock()
				slog.Warn("plugin generation: force-cancel grace elapsed with in-flight calls remaining",
					"instance_id", instanceID,
					"generation", oldGen,
					"remaining", remaining,
				)
			}
		}
	}

	// --- Step 4: rotate to the new generation and wake blocked Acquire callers ---
	s.mu.Lock()
	newGen := oldGen + 1
	s.current = newGen
	s.refs[newGen] = 0
	oldPausedCh := s.pausedCh
	s.paused = false
	s.pausedCh = make(chan struct{})
	close(s.pausedCh) // immediately unblocked for subsequent Acquire callers
	s.draining = false
	s.mu.Unlock()

	// Close the old pausedCh to wake any goroutines that were blocked in
	// Acquire before the rotation. They will loop back and pick up newGen.
	close(oldPausedCh)

	return newGen, drainedNaturally, nil
}

// UnregisterInstance cleans up all state for instanceID. It closes the
// unregisteredCh and pausedCh so any goroutines blocked in Acquire return an
// error rather than deadlocking. In-flight calls are NOT force-cancelled here;
// UnregisterInstance is called after the subprocess has been stopped by the
// Manager (the gRPC stream tear-down races their completion anyway).
//
// See issue #294.
func (c *Controller) UnregisterInstance(instanceID string) {
	c.mu.Lock()
	s, ok := c.byInstance[instanceID]
	if ok {
		delete(c.byInstance, instanceID)
	}
	c.mu.Unlock()

	if !ok {
		return
	}

	s.mu.Lock()
	if !s.unregistered {
		s.unregistered = true
		close(s.unregisteredCh)
		// Also close pausedCh in case callers are blocked there waiting for a
		// drain that will never complete (concurrent uninstall + drain race).
		if s.paused {
			s.paused = false
			close(s.pausedCh)
		}
	}
	s.mu.Unlock()
}
