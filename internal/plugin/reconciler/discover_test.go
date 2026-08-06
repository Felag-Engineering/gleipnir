package reconciler

import (
	"context"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

func labelled(name, instanceID string, extra map[string]string) container.ContainerInfo {
	labels := map[string]string{LabelManaged: ManagedValue, LabelInstance: instanceID}
	for k, v := range extra {
		labels[k] = v
	}
	return container.ContainerInfo{
		ID:     container.ContainerID("c-" + name),
		Name:   name,
		Labels: labels,
		State:  container.ContainerStateRunning,
		Health: "healthy",
	}
}

func desiredRowFor(instanceID string) db.PluginContainer {
	return db.PluginContainer{
		PluginInstanceID: instanceID,
		ImageRef:         "img",
		ImageDigest:      "sha256:abc",
		DesiredState:     "running",
	}
}

// The discovery table. Every one of these failure modes is silent by nature —
// a container absent, or present under the wrong label — so each gets a named
// state rather than an instance that is quietly never routed to.
func TestDiscover_StateTable(t *testing.T) {
	tests := []struct {
		name     string
		desired  []db.PluginContainer
		observed []container.ContainerInfo
		want     map[string]DiscoveryState
	}{
		{
			name:     "matched",
			desired:  []db.PluginContainer{desiredRowFor("inst-1")},
			observed: []container.ContainerInfo{labelled("acme", "inst-1", nil)},
			want:     map[string]DiscoveryState{"inst-1": DiscoveryMatched},
		},
		{
			name:    "declared but not found",
			desired: []db.PluginContainer{desiredRowFor("inst-1")},
			want:    map[string]DiscoveryState{"inst-1": DiscoveryNotFound},
		},
		{
			name:     "found but not installed",
			observed: []container.ContainerInfo{labelled("stale", "inst-gone", nil)},
			want:     map[string]DiscoveryState{"inst-gone": DiscoveryNotInstalled},
		},
		{
			name: "wrong generation",
			desired: func() []db.PluginContainer {
				row := desiredRowFor("inst-1")
				row.Version = 4
				return []db.PluginContainer{row}
			}(),
			observed: []container.ContainerInfo{labelled("acme", "inst-1", map[string]string{LabelGeneration: "3"})},
			want:     map[string]DiscoveryState{"inst-1": DiscoveryWrongGeneration},
		},
		{
			name: "matching generation",
			desired: func() []db.PluginContainer {
				row := desiredRowFor("inst-1")
				row.Version = 4
				return []db.PluginContainer{row}
			}(),
			observed: []container.ContainerInfo{labelled("acme", "inst-1", map[string]string{LabelGeneration: "4"})},
			want:     map[string]DiscoveryState{"inst-1": DiscoveryMatched},
		},
		{
			// Picking one would be a guess, and a wrong guess is invisible
			// until something goes wrong.
			name:    "two containers claiming one instance",
			desired: []db.PluginContainer{desiredRowFor("inst-1")},
			observed: []container.ContainerInfo{
				labelled("acme-a", "inst-1", nil),
				labelled("acme-b", "inst-1", nil),
			},
			want: map[string]DiscoveryState{"inst-1": DiscoveryAmbiguous},
		},
		{
			name:    "several instances at once",
			desired: []db.PluginContainer{desiredRowFor("inst-1"), desiredRowFor("inst-2")},
			observed: []container.ContainerInfo{
				labelled("acme", "inst-1", nil),
				labelled("orphan", "inst-gone", nil),
			},
			want: map[string]DiscoveryState{
				"inst-1":    DiscoveryMatched,
				"inst-2":    DiscoveryNotFound,
				"inst-gone": DiscoveryNotInstalled,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Discover(tc.desired, tc.observed)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d conclusions, want %d: %+v", len(got), len(tc.want), got)
			}
			for _, d := range got {
				want, ok := tc.want[d.InstanceID]
				if !ok {
					t.Errorf("unexpected conclusion for %s: %+v", d.InstanceID, d)
					continue
				}
				if d.State != want {
					t.Errorf("%s state = %q, want %q (detail: %s)", d.InstanceID, d.State, want, d.Detail)
				}
				// Every state carries an operator-facing explanation. A state
				// with no detail is a chip that says "unhealthy" and nothing
				// actionable.
				if d.Detail == "" {
					t.Errorf("%s has no detail", d.InstanceID)
				}
			}
		})
	}
}

// A row that does not track a generation cannot be contradicted by one: manual
// operators who never rotate have nothing to disagree with.
func TestDiscover_NoGenerationTrackedMeansNoMismatch(t *testing.T) {
	row := desiredRowFor("inst-1") // Version stays 0
	got := Discover([]db.PluginContainer{row},
		[]container.ContainerInfo{labelled("acme", "inst-1", map[string]string{LabelGeneration: "99"})})

	if len(got) != 1 || got[0].State != DiscoveryMatched {
		t.Fatalf("got %+v, want a match", got)
	}
}

// Anything without an instance label is somebody else's container. The label
// selector already scoped the list; ignoring the remainder silently is right.
func TestDiscover_UnlabelledContainersAreIgnored(t *testing.T) {
	got := Discover(nil, []container.ContainerInfo{
		{ID: "c-x", Name: "unrelated", Labels: map[string]string{LabelManaged: ManagedValue}},
	})
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing for an unlabelled container", got)
	}
}

// An operator comparing two passes should see a diff only where something
// changed.
func TestDiscover_OrderIsStable(t *testing.T) {
	desired := []db.PluginContainer{desiredRowFor("inst-b"), desiredRowFor("inst-a")}
	observed := []container.ContainerInfo{labelled("z", "inst-z", nil), labelled("a", "inst-a", nil)}

	first := Discover(desired, observed)
	second := Discover(desired, observed)

	if len(first) != len(second) {
		t.Fatalf("passes disagree on length")
	}
	for i := range first {
		if first[i].InstanceID != second[i].InstanceID {
			t.Fatalf("order is unstable at %d: %q vs %q", i, first[i].InstanceID, second[i].InstanceID)
		}
	}
	if first[0].InstanceID != "inst-a" {
		t.Errorf("first = %q, want the lowest instance ID", first[0].InstanceID)
	}
}

// The headline manual-mode guarantee: a full pass performs ZERO writes against
// the socket. Enforced by the read-only runtime wrapper rather than by this
// code remembering to behave — a posture guaranteed by caller discipline is one
// refactor away from being untrue.
func TestDiscoverPass_PerformsNoWrites(t *testing.T) {
	fake := container.NewFake()
	counting := &countingRuntime{Runtime: container.NewReadOnlyRuntime(fake)}

	store := &fakeStore{}
	store.set(desiredRowFor("inst-1"))

	r, err := New(Config{
		Runtime: counting,
		Store:   store,
		Posture: container.PostureManual,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := r.DiscoverPass(context.Background()); err != nil {
		t.Fatalf("DiscoverPass: %v", err)
	}

	if writes := counting.writes(); writes != 0 {
		t.Errorf("manual-mode pass performed %d socket writes, want zero", writes)
	}
}

// And the wrapper refuses even if something did ask — the enforcement is
// structural, not a convention this package upholds.
func TestManualPosture_WriteIsRefusedByTheWrapper(t *testing.T) {
	readonly := container.NewReadOnlyRuntime(container.NewFake())

	_, err := readonly.Create(context.Background(), container.CreateOptions{
		Name: "c", Image: "img@sha256:abc", Network: "net",
	})
	if err == nil {
		t.Fatal("the read-only runtime accepted a create")
	}
	if !strings.Contains(err.Error(), "manual") {
		t.Errorf("error = %v, want it to name the posture", err)
	}
}
