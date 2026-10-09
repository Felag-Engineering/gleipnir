package reconciler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// #1033 d: a persistent self-Inspect failure holds instance creation, so the
// pass result must say how long the hold has lasted and reset once Inspect
// recovers.
func TestReconcile_SelfInspectFailedPassesCounter(t *testing.T) {
	ctx := context.Background()
	fake := container.NewFake()
	selfID, err := fake.Create(ctx, container.CreateOptions{
		Name: "gleipnir", Image: "gleipnir/gleipnir:test", Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Sysctls: container.RequiredSysctls(),
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
		OperatorAPIGuard:        guardedOperatorAPI(t),
		CheckForwardingDisabled: func() error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	steps := []struct {
		name string
		fail bool
		want int
	}{
		{"first failure", true, 1},
		{"second consecutive failure", true, 2},
		{"third consecutive failure", true, 3},
		{"recovery resets the count", false, 0},
		{"a later failure starts over", true, 1},
	}
	for _, step := range steps {
		failing.Fail = step.fail
		result, err := r.ReconcileOnce(ctx)
		if err != nil {
			t.Fatalf("%s: ReconcileOnce: %v", step.name, err)
		}
		if result.SelfInspectFailedPasses != step.want {
			t.Errorf("%s: SelfInspectFailedPasses = %d, want %d", step.name, result.SelfInspectFailedPasses, step.want)
		}
	}

	data, err := json.Marshal(PassResult{SelfInspectFailedPasses: 2})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["self_inspect_failed_passes"] != float64(2) {
		t.Errorf("event payload = %s, want self_inspect_failed_passes=2", data)
	}
}

func TestReconcile_SelfInspectFailedPassesZeroWithoutSelfAttach(t *testing.T) {
	store := &fakeStore{}
	store.set(*desiredRow("i1", DesiredRunning))
	r, err := New(Config{
		Runtime: container.NewFake(),
		Store:   store,
		Subnets: testAllocator(t),
		Posture: container.PostureRootlessPodman,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := r.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if result.SelfInspectFailedPasses != 0 {
		t.Errorf("SelfInspectFailedPasses = %d, want 0 when self-attach is not configured", result.SelfInspectFailedPasses)
	}
}
