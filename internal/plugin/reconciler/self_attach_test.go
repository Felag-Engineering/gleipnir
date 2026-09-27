package reconciler

import (
	"context"
	"errors"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// failInspectRuntime fails Inspect for one specific container ID while Fail
// is true; every other call (and every other ID) delegates unchanged.
type failInspectRuntime struct {
	container.Runtime
	FailID container.ContainerID
	Fail   bool
}

func (f *failInspectRuntime) Inspect(ctx context.Context, id container.ContainerID) (container.ContainerInfo, error) {
	if f.Fail && id == f.FailID {
		return container.ContainerInfo{}, errors.New("simulated inspect failure")
	}
	return f.Runtime.Inspect(ctx, id)
}

// TestReconcile_SelfAttachBringUpAndTeardown exercises the ordering the DoD
// calls out end to end through ReconcileOnce, not just the pure planner:
// Gleipnir joins an instance's network right after it is created and before
// the plugin container is, and leaves it right after the plugin container is
// gone and before the network is removed.
func TestReconcile_SelfAttachBringUpAndTeardown(t *testing.T) {
	ctx := context.Background()
	fake := container.NewFake()
	selfID, err := fake.Create(ctx, container.CreateOptions{
		Name:    "gleipnir",
		Image:   "gleipnir/gleipnir:test",
		Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
	})
	if err != nil {
		t.Fatalf("creating self container: %v", err)
	}

	rt := &countingRuntime{Runtime: fake}
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))

	r, err := New(Config{
		Runtime:                 rt,
		Store:                   store,
		Subnets:                 testAllocator(t),
		Posture:                 container.PostureRootlessPodman,
		SelfContainerID:         selfID,
		OperatorAPIGuarded:      true,
		CheckForwardingDisabled: func() error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	wantUp := []ActionKind{ActionCreateNetwork, ActionAttachSelf, ActionCreate, ActionStart}
	for i, wantKind := range wantUp {
		result, err := r.ReconcileOnce(ctx)
		if err != nil {
			t.Fatalf("bring-up pass %d: %v", i+1, err)
		}
		if len(result.Actions) != 1 || result.Actions[0].Kind != wantKind {
			t.Fatalf("bring-up pass %d actions = %+v, want a single %s", i+1, result.Actions, wantKind)
		}
	}
	if rt.connects != 1 {
		t.Errorf("connects = %d, want exactly 1", rt.connects)
	}

	upFinal, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("bring-up final pass: %v", err)
	}
	if !upFinal.Converged {
		t.Fatalf("bring-up final pass actions = %+v, want convergence", upFinal.Actions)
	}

	// The instance is uninstalled: its desired row disappears.
	store.set()

	wantDown := []ActionKind{ActionStop, ActionRemove, ActionDetachSelf, ActionRemoveNetwork}
	for i, wantKind := range wantDown {
		result, err := r.ReconcileOnce(ctx)
		if err != nil {
			t.Fatalf("teardown pass %d: %v", i+1, err)
		}
		if len(result.Actions) != 1 || result.Actions[0].Kind != wantKind {
			t.Fatalf("teardown pass %d actions = %+v, want a single %s", i+1, result.Actions, wantKind)
		}
	}
	if rt.disconnects != 1 {
		t.Errorf("disconnects = %d, want exactly 1", rt.disconnects)
	}

	downFinal, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("teardown final pass: %v", err)
	}
	if !downFinal.Converged {
		t.Fatalf("teardown final pass actions = %+v, want convergence", downFinal.Actions)
	}
}

// #958 finding 2: self-attach must never activate on a host where IP
// forwarding is not confirmed disabled, even with SelfContainerID configured.
// The core loop still converges normally — the precondition failure degrades
// self-attach, not the whole reconciler.
func TestNew_RefusesSelfAttachWhenForwardingEnabled(t *testing.T) {
	fake := container.NewFake()
	ctx := context.Background()
	selfID, err := fake.Create(ctx, container.CreateOptions{
		Name: "gleipnir", Image: "gleipnir/gleipnir:test", Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
	})
	if err != nil {
		t.Fatalf("creating self container: %v", err)
	}

	rt := &countingRuntime{Runtime: fake}
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))

	r, err := New(Config{
		Runtime:            rt,
		Store:              store,
		Subnets:            testAllocator(t),
		Posture:            container.PostureRootlessPodman,
		SelfContainerID:    selfID,
		OperatorAPIGuarded: true,
		CheckForwardingDisabled: func() error {
			return &container.ForwardingEnabledError{Path: "/proc/sys/net/ipv4/ip_forward", Value: "1"}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	convergeFully(t, r)

	if rt.connects != 0 || rt.disconnects != 0 {
		t.Errorf("connects=%d disconnects=%d, want 0/0 when forwarding is not confirmed disabled", rt.connects, rt.disconnects)
	}
	// The core loop still converges the instance's own container despite
	// self-attach being refused.
	info, ok := fakeContainerFor(rt.Runtime.(*container.Fake), "i1")
	if !ok || info.State != container.ContainerStateRunning {
		t.Errorf("instance container = %+v (found=%v), want running despite self-attach being refused", info, ok)
	}
}

// #1021 review item 1: self-attach must never activate without the operator
// API guard confirmed, even when SelfContainerID and forwarding both check
// out. The core loop still converges normally.
func TestNew_RefusesSelfAttachWithoutOperatorAPIGuard(t *testing.T) {
	fake := container.NewFake()
	ctx := context.Background()
	selfID, err := fake.Create(ctx, container.CreateOptions{
		Name: "gleipnir", Image: "gleipnir/gleipnir:test", Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
	})
	if err != nil {
		t.Fatalf("creating self container: %v", err)
	}

	rt := &countingRuntime{Runtime: fake}
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))

	r, err := New(Config{
		Runtime:         rt,
		Store:           store,
		Subnets:         testAllocator(t),
		Posture:         container.PostureRootlessPodman,
		SelfContainerID: selfID,
		// OperatorAPIGuarded deliberately left unset (false, the fail-closed
		// default) — forwarding checks out fine, but that alone must not be
		// enough.
		CheckForwardingDisabled: func() error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	convergeFully(t, r)

	if rt.connects != 0 || rt.disconnects != 0 {
		t.Errorf("connects=%d disconnects=%d, want 0/0 without the operator API guard confirmed", rt.connects, rt.disconnects)
	}
	// The core loop still converges the instance's own container despite
	// self-attach being refused.
	info, ok := fakeContainerFor(rt.Runtime.(*container.Fake), "i1")
	if !ok || info.State != container.ContainerStateRunning {
		t.Errorf("instance container = %+v (found=%v), want running despite self-attach being refused", info, ok)
	}
}

func fakeContainerFor(f *container.Fake, instanceID string) (container.ContainerInfo, bool) {
	list, err := f.ListByLabel(context.Background(), LabelInstance, instanceID)
	if err != nil || len(list) == 0 {
		return container.ContainerInfo{}, false
	}
	return list[0], true
}

// #958 finding 7: a failed Inspect of Gleipnir's own container must not fail
// the whole pass — the core loop still converges everything else (here,
// creating the instance's network, which does not depend on self-attach
// status at all).
func TestReconcile_SelfInspectFailureDegradesGracefully(t *testing.T) {
	ctx := context.Background()
	fake := container.NewFake()
	selfID, err := fake.Create(ctx, container.CreateOptions{
		Name: "gleipnir", Image: "gleipnir/gleipnir:test", Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
	})
	if err != nil {
		t.Fatalf("creating self container: %v", err)
	}
	failing := &failInspectRuntime{Runtime: fake, FailID: selfID, Fail: true}

	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))

	r, err := New(Config{
		Runtime:                 failing,
		Store:                   store,
		Subnets:                 testAllocator(t),
		Posture:                 container.PostureRootlessPodman,
		SelfContainerID:         selfID,
		OperatorAPIGuarded:      true,
		CheckForwardingDisabled: func() error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v, want no error — an Inspect failure must degrade, not abort the pass", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != ActionCreateNetwork {
		t.Fatalf("actions = %+v, want a single create_network — the core loop must still converge", result.Actions)
	}
}

// #1021 review item 3b: once the network already exists, an Inspect failure
// must NOT fall through to creating the plugin container — that would risk a
// container coming up before Gleipnir can reach it, or (if Gleipnir turns out
// to already be attached) never finding out because the pass moved on.
// Instead the instance stays ActionNone and the same pass is retried once
// Inspect can confirm the real state.
func TestReconcile_SelfInspectFailureRefusesCreateOnceNetworkExists(t *testing.T) {
	ctx := context.Background()
	fake := container.NewFake()
	selfID, err := fake.Create(ctx, container.CreateOptions{
		Name: "gleipnir", Image: "gleipnir/gleipnir:test", Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
	})
	if err != nil {
		t.Fatalf("creating self container: %v", err)
	}
	failing := &failInspectRuntime{Runtime: fake, FailID: selfID}

	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))

	r, err := New(Config{
		Runtime:                 failing,
		Store:                   store,
		Subnets:                 testAllocator(t),
		Posture:                 container.PostureRootlessPodman,
		SelfContainerID:         selfID,
		OperatorAPIGuarded:      true,
		CheckForwardingDisabled: func() error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Pass 1: Inspect succeeds, network gets created (self is not yet
	// attached to it, so the next pass would ordinarily attach).
	result, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != ActionCreateNetwork {
		t.Fatalf("pass 1 actions = %+v, want a single create_network", result.Actions)
	}

	// Pass 2: Inspect now fails. The network exists, self is not confirmed
	// attached to it, and this is exactly the situation review item 3b
	// covers — the instance must stay untouched, not proceed to Create.
	failing.Fail = true
	result, err = r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if !result.Converged {
		t.Fatalf("pass 2 actions = %+v, want none (converged) — a failed Inspect must not fall through to create", result.Actions)
	}

	// Pass 3: Inspect succeeds again, and the instance proceeds normally —
	// nothing about the earlier failure left it permanently stuck.
	failing.Fail = false
	result, err = r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("pass 3: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != ActionAttachSelf {
		t.Fatalf("pass 3 actions = %+v, want a single attach_self once Inspect can confirm state again", result.Actions)
	}
}

// #958 finding 5: instanceNetwork refuses when more than one network is
// labelled for an instance, rather than silently acting on networks[0] — an
// ambiguous label state is worth surfacing loudly, since self-attach must
// know unambiguously which network it is joining or leaving.
func TestInstanceNetwork_RefusesMultipleMatches(t *testing.T) {
	fake := container.NewFake()
	ctx := context.Background()
	for _, name := range []string{"net-a", "net-b"} {
		if _, err := fake.CreateNetwork(ctx, container.NetworkOptions{
			Name: name, Internal: true,
			Labels: map[string]string{LabelManaged: ManagedValue, LabelInstance: "i1"},
		}); err != nil {
			t.Fatalf("CreateNetwork(%s): %v", name, err)
		}
	}

	r, err := New(Config{Runtime: fake, Store: &fakeStore{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := r.instanceNetwork(ctx, "i1"); err == nil {
		t.Fatal("instanceNetwork() with two labelled networks = nil error, want a refusal")
	}
}

// A self container ID that Config never set means the loop never attaches at
// all, even once a network exists to attach to — host-process mode with no
// plugins in play looks the same as it always has.
func TestReconcile_NoSelfContainerIDNeverAttaches(t *testing.T) {
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))
	r, rt, _ := newTestReconciler(t, store)

	convergeFully(t, r)

	if rt.connects != 0 || rt.disconnects != 0 {
		t.Errorf("connects=%d disconnects=%d, want 0/0 with no SelfContainerID configured", rt.connects, rt.disconnects)
	}
}
