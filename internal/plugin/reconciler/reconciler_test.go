package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// countingRuntime wraps a Runtime and counts the calls that WRITE to the
// socket. The idempotency claim ("a pass over a converged state performs zero
// socket writes") is about exactly these, so counting them is the assertion —
// list and inspect are reads a level-triggered loop makes every pass by design.
type countingRuntime struct {
	container.Runtime

	mu      sync.Mutex
	creates int
	starts  int
	stops   int
	removes int
}

func (c *countingRuntime) Create(ctx context.Context, opts container.CreateOptions) (container.ContainerID, error) {
	c.mu.Lock()
	c.creates++
	c.mu.Unlock()
	return c.Runtime.Create(ctx, opts)
}

func (c *countingRuntime) Start(ctx context.Context, id container.ContainerID) error {
	c.mu.Lock()
	c.starts++
	c.mu.Unlock()
	return c.Runtime.Start(ctx, id)
}

func (c *countingRuntime) Stop(ctx context.Context, id container.ContainerID, d time.Duration) error {
	c.mu.Lock()
	c.stops++
	c.mu.Unlock()
	return c.Runtime.Stop(ctx, id, d)
}

func (c *countingRuntime) Remove(ctx context.Context, id container.ContainerID, force bool) error {
	c.mu.Lock()
	c.removes++
	c.mu.Unlock()
	return c.Runtime.Remove(ctx, id, force)
}

func (c *countingRuntime) writes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creates + c.starts + c.stops + c.removes
}

// fakeStore is the desired-state side of the diff.
type fakeStore struct {
	mu   sync.Mutex
	rows []db.PluginContainer
	err  error
}

func (s *fakeStore) ListPluginContainers(context.Context) ([]db.PluginContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := make([]db.PluginContainer, len(s.rows))
	copy(out, s.rows)
	return out, nil
}

func (s *fakeStore) set(rows ...db.PluginContainer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = rows
}

// capturePublisher records published events so a test can synchronize on a
// completed pass instead of polling for its side effects.
type capturePublisher struct {
	mu     sync.Mutex
	events []string
}

func (p *capturePublisher) Publish(eventType string, _ json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, eventType)
}

func (p *capturePublisher) count(eventType string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.events {
		if e == eventType {
			n++
		}
	}
	return n
}

// waitForPasses blocks until at least n passes have been published.
func (p *capturePublisher) waitForPasses(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for p.count(EventPassCompleted) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d reconcile passes (saw %d)", n, p.count(EventPassCompleted))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newTestReconciler(t *testing.T, store *fakeStore) (*Reconciler, *countingRuntime, *capturePublisher) {
	t.Helper()
	rt := &countingRuntime{Runtime: container.NewFake()}
	pub := &capturePublisher{}
	r, err := New(Config{
		Runtime:   rt,
		Store:     store,
		Posture:   container.PostureRootlessPodman,
		Interval:  time.Hour, // kicks drive the tests; the ticker must not race them
		Publisher: pub,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, rt, pub
}

// A container that must be created and then started takes two passes, not one.
// This is the level-triggered contract: each pass takes one step and observes
// its result on the next, rather than driving a sequence to completion.
func TestReconcile_MultiStepConvergesOverPasses(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))
	r, rt, _ := newTestReconciler(t, store)

	first, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if len(first.Actions) != 1 || first.Actions[0].Kind != ActionCreate {
		t.Fatalf("pass 1 actions = %+v, want a single create", first.Actions)
	}
	if rt.starts != 0 {
		t.Error("pass 1 started the container; create and start must be separate steps")
	}

	second, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if len(second.Actions) != 1 || second.Actions[0].Kind != ActionStart {
		t.Fatalf("pass 2 actions = %+v, want a single start", second.Actions)
	}

	third, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("pass 3: %v", err)
	}
	if !third.Converged {
		t.Fatalf("pass 3 actions = %+v, want convergence", third.Actions)
	}
}

// Idempotency: once converged, a pass writes nothing at all.
func TestReconcile_ConvergedPassPerformsNoSocketWrites(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))
	r, rt, _ := newTestReconciler(t, store)

	for i := 0; i < 3; i++ {
		if _, err := r.ReconcileOnce(ctx); err != nil {
			t.Fatalf("converging pass %d: %v", i, err)
		}
	}
	writesAfterConvergence := rt.writes()

	for i := 0; i < 5; i++ {
		result, err := r.ReconcileOnce(ctx)
		if err != nil {
			t.Fatalf("steady-state pass %d: %v", i, err)
		}
		if !result.Converged {
			t.Fatalf("steady-state pass %d took actions %+v", i, result.Actions)
		}
	}
	if got := rt.writes(); got != writesAfterConvergence {
		t.Errorf("steady-state passes performed %d socket writes, want 0", got-writesAfterConvergence)
	}
}

// Crash-resume: the loop keeps no state between passes, so a process that dies
// mid-convergence and comes back with a brand-new Reconciler picks up exactly
// where the world is — identity comes from labels, not from anything in memory.
func TestReconcile_ResumesAfterRestart(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))

	fake := container.NewFake()
	first, err := New(Config{Runtime: fake, Store: store, Posture: container.PostureDocker, Interval: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := first.ReconcileOnce(ctx); err != nil { // creates, does not start
		t.Fatalf("pass before the crash: %v", err)
	}

	// The process dies here: a new Reconciler over the same runtime, sharing
	// nothing with the old one.
	second, err := New(Config{Runtime: fake, Store: store, Posture: container.PostureDocker, Interval: time.Hour})
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	result, err := second.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("pass after the restart: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != ActionStart {
		t.Fatalf("actions after restart = %+v, want the start the crashed process never reached", result.Actions)
	}

	final, err := second.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("final pass: %v", err)
	}
	if !final.Converged {
		t.Fatalf("final pass actions = %+v, want convergence", final.Actions)
	}
}

// An orphan is stopped, then removed — two passes, because a running container
// is never removed out from under itself.
func TestReconcile_OrphanIsStoppedThenRemoved(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))
	r, rt, _ := newTestReconciler(t, store)

	for i := 0; i < 3; i++ {
		if _, err := r.ReconcileOnce(ctx); err != nil {
			t.Fatalf("converging pass %d: %v", i, err)
		}
	}

	// The desired row goes away — an uninstall — leaving a running container
	// nothing claims.
	store.set()

	stopPass, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("orphan stop pass: %v", err)
	}
	if len(stopPass.Actions) != 1 || stopPass.Actions[0].Kind != ActionStop {
		t.Fatalf("orphan pass 1 actions = %+v, want a single stop", stopPass.Actions)
	}

	removePass, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("orphan remove pass: %v", err)
	}
	if len(removePass.Actions) != 1 || removePass.Actions[0].Kind != ActionRemove {
		t.Fatalf("orphan pass 2 actions = %+v, want a single remove", removePass.Actions)
	}

	final, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("post-cleanup pass: %v", err)
	}
	if !final.Converged {
		t.Fatalf("post-cleanup pass actions = %+v, want convergence", final.Actions)
	}
	if rt.removes != 1 {
		t.Errorf("removes = %d, want exactly 1", rt.removes)
	}
}

// Manual posture is a supported configuration, not a degraded one: the loop
// does not run at all. Enforcing it by not starting — rather than by hoping the
// diff comes out empty — is what keeps a level-triggered loop from reading an
// operator's own containers as orphans.
func TestStart_ManualPostureNeverTouchesTheSocket(t *testing.T) {
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))
	rt := &countingRuntime{Runtime: container.NewFake()}
	pub := &capturePublisher{}

	r, err := New(Config{
		Runtime:   rt,
		Store:     store,
		Posture:   container.PostureManual,
		Interval:  time.Millisecond,
		Publisher: pub,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)

	// Kicking an inert loop must also do nothing.
	r.Kick()
	r.Wait() // no goroutine was started, so this returns immediately

	if got := rt.writes(); got != 0 {
		t.Errorf("manual posture performed %d socket writes, want 0", got)
	}
	if got := pub.count(EventPassCompleted); got != 0 {
		t.Errorf("manual posture ran %d passes, want 0", got)
	}
}

// Start converges once before returning, so a caller can treat "Start
// returned" as "the substrate has been converged" and only then report ready.
func TestStart_ConvergesBeforeReturning(t *testing.T) {
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))
	r, rt, pub := newTestReconciler(t, store)

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)

	if rt.creates != 1 {
		t.Errorf("creates after Start = %d, want 1 — the boot pass must complete before Start returns", rt.creates)
	}
	if pub.count(EventPassCompleted) < 1 {
		t.Error("Start returned without publishing a pass")
	}
}

// A kick converges a change promptly instead of waiting out the interval. The
// test synchronizes on the published pass rather than on the side effect.
func TestKick_RunsAPass(t *testing.T) {
	store := &fakeStore{}
	r, rt, pub := newTestReconciler(t, store) // no desired rows yet

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)
	pub.waitForPasses(t, 1) // the boot pass

	store.set(*desiredRow("i1", DesiredRunning))
	r.Kick()
	pub.waitForPasses(t, 2)

	if rt.creates != 1 {
		t.Errorf("creates = %d, want 1 — the kick should have converged the new row", rt.creates)
	}
}

// Repeated kicks coalesce: the channel is buffered at 1 and the send is
// non-blocking, which costs nothing because every pass re-reads the whole
// desired set anyway.
func TestKick_IsNonBlockingAndCoalesces(t *testing.T) {
	store := &fakeStore{}
	r, _, _ := newTestReconciler(t, store)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			r.Kick() // no loop is running to drain it
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Kick blocked with no loop running")
	}
}

// A pass that cannot read either side of the diff fails as a whole: acting on
// half a picture is how a level-triggered loop deletes things it should not.
func TestReconcileOnce_UnreadableDesiredStateFailsThePass(t *testing.T) {
	store := &fakeStore{err: errors.New("database is locked")}
	r, rt, _ := newTestReconciler(t, store)

	if _, err := r.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("ReconcileOnce: want an error when the desired state cannot be read")
	}
	if got := rt.writes(); got != 0 {
		t.Errorf("%d socket writes on a failed pass, want 0", got)
	}
}

// One failing action does not abandon the pass or the loop: the others still
// run, and the next pass re-plans from whatever state the failure left.
func TestReconcileOnce_ActionFailureIsCountedNotFatal(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning), *desiredRow("i2", DesiredRunning))

	fake := container.NewFake()
	fake.CreateErr = errors.New("socket write failed")
	r, err := New(Config{Runtime: fake, Store: store, Posture: container.PostureDocker, Interval: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: a failed action must not fail the pass: %v", err)
	}
	if result.Errors != 2 {
		t.Errorf("Errors = %d, want 2", result.Errors)
	}

	// The socket recovers; the next pass simply plans the same step again.
	fake.CreateErr = nil
	retry, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("retry pass: %v", err)
	}
	if retry.Errors != 0 || len(retry.Actions) != 2 {
		t.Fatalf("retry pass = %+v, want both creates to succeed", retry)
	}
}

// The create request carries everything the self-constraint requires, and the
// resource caps translate from the desired row's units to the runtime's.
func TestCreateOptions(t *testing.T) {
	memory := int64(512 << 20)
	cpu := int64(1500) // 1.5 cores in millicores
	row := *desiredRow("i1", DesiredRunning)
	row.MemoryLimitBytes = &memory
	row.CpuLimitMillicores = &cpu

	r, _, _ := newTestReconciler(t, &fakeStore{})
	opts := r.createOptions(row)

	if opts.Network != "net-i1" {
		t.Errorf("Network = %q, want the row's network", opts.Network)
	}
	if opts.Privileged || len(opts.CapAdd) != 0 || len(opts.Mounts) != 0 {
		t.Error("create options request privileges, capabilities, or extra mounts")
	}
	if opts.Volume.Name == "" || opts.Volume.MountPath == "" {
		t.Error("create options omit the per-instance volume")
	}
	if opts.Image != "gleipnir/slack:1.0.0@sha256:aaaa" {
		t.Errorf("Image = %q, want the digest-pinned reference", opts.Image)
	}
	if opts.Labels[LabelManaged] != ManagedValue || opts.Labels[LabelInstance] != "i1" {
		t.Errorf("Labels = %v, want the discovery labels", opts.Labels)
	}
	if opts.Labels[LabelImageDigest] != "sha256:aaaa" || opts.Labels[LabelConfigHash] != "cfg-1" {
		t.Errorf("Labels = %v, want the drift-detection labels", opts.Labels)
	}
	if opts.Resources.MemoryBytes != memory {
		t.Errorf("MemoryBytes = %d, want %d", opts.Resources.MemoryBytes, memory)
	}
	if opts.Resources.NanoCPUs != 1_500_000_000 {
		t.Errorf("NanoCPUs = %d, want 1.5 cores", opts.Resources.NanoCPUs)
	}

	// The self-constraint is enforced by the runtime, not by this function —
	// prove the request it builds actually passes it.
	if err := container.ValidateCreate(opts); err != nil {
		t.Errorf("ValidateCreate rejected the reconciler's own create request: %v", err)
	}
}

// A row with no network name still produces a request that satisfies the
// "must attach to a network" constraint.
func TestCreateOptions_FallbackNetwork(t *testing.T) {
	row := *desiredRow("i1", DesiredRunning)
	row.NetworkName = ""

	r, _, _ := newTestReconciler(t, &fakeStore{})
	if err := container.ValidateCreate(r.createOptions(row)); err != nil {
		t.Errorf("ValidateCreate: %v", err)
	}
}

func TestNew_RequiresDependencies(t *testing.T) {
	if _, err := New(Config{Store: &fakeStore{}}); err == nil {
		t.Error("New: want an error when Runtime is nil")
	}
	if _, err := New(Config{Runtime: container.NewFake()}); err == nil {
		t.Error("New: want an error when Store is nil")
	}
}

// Stop drains the loop goroutine, which is what the shutdown machinery joins.
func TestStopDrainsTheLoop(t *testing.T) {
	store := &fakeStore{}
	r, _, pub := newTestReconciler(t, store)

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pub.waitForPasses(t, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Stop()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not drain the loop")
	}

	// Stop is safe to call twice, which matters when both an explicit
	// shutdown path and a deferred cleanup run.
	r.Stop()
}
