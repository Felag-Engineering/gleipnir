package reconciler

import (
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

func desiredRow(instanceID, state string) *db.PluginContainer {
	return &db.PluginContainer{
		ID:               "c-" + instanceID,
		PluginInstanceID: instanceID,
		ImageRef:         "gleipnir/slack:1.0.0",
		ImageDigest:      "sha256:aaaa",
		ConfigHash:       "cfg-1",
		NetworkName:      "net-" + instanceID,
		DesiredState:     state,
	}
}

// observedNetwork is the instance's dedicated internal network as the runtime
// reports it.
func observedNetwork(instanceID string) container.NetworkInfo {
	return container.NetworkInfo{
		ID:       container.NetworkID("net-" + instanceID),
		Name:     "gleipnir-plugin-" + instanceID,
		Internal: true,
		Labels: map[string]string{
			LabelManaged:  ManagedValue,
			LabelInstance: instanceID,
		},
	}
}

func observedContainer(instanceID string, state container.ContainerState) *container.ContainerInfo {
	return &container.ContainerInfo{
		ID:    container.ContainerID("ctr-" + instanceID),
		Name:  containerName(instanceID),
		State: state,
		Labels: map[string]string{
			LabelManaged:     ManagedValue,
			LabelInstance:    instanceID,
			LabelConfigHash:  "cfg-1",
			LabelImageDigest: "sha256:aaaa",
		},
	}
}

// The convergence table: one step per (desired, observed) pair, and never more
// than one — a divergence needing several steps converges over several passes.
func TestPlanFor(t *testing.T) {
	tests := []struct {
		name      string
		desired   *db.PluginContainer
		observed  *container.ContainerInfo
		noNetwork bool // the instance's network does not exist yet
		want      ActionKind
	}{
		{
			name:    "desired running, nothing there, is created not started",
			desired: desiredRow("i1", DesiredRunning),
			want:    ActionCreate,
		},
		{
			name:    "desired stopped and absent is left alone",
			desired: desiredRow("i1", DesiredStopped),
			want:    ActionNone,
		},
		{
			name:     "created container is started",
			desired:  desiredRow("i1", DesiredRunning),
			observed: observedContainer("i1", container.ContainerStateCreated),
			want:     ActionStart,
		},
		{
			name:     "exited container is restarted",
			desired:  desiredRow("i1", DesiredRunning),
			observed: observedContainer("i1", container.ContainerStateExited),
			want:     ActionStart,
		},
		{
			name:     "dead container is restarted",
			desired:  desiredRow("i1", DesiredRunning),
			observed: observedContainer("i1", container.ContainerStateDead),
			want:     ActionStart,
		},
		{
			name:     "running container matching desired is converged",
			desired:  desiredRow("i1", DesiredRunning),
			observed: observedContainer("i1", container.ContainerStateRunning),
			want:     ActionNone,
		},
		{
			name:     "restarting container is left to settle",
			desired:  desiredRow("i1", DesiredRunning),
			observed: observedContainer("i1", container.ContainerStateRestarting),
			want:     ActionNone,
		},
		{
			name:     "removing container is left to settle",
			desired:  desiredRow("i1", DesiredRunning),
			observed: observedContainer("i1", container.ContainerStateRemoving),
			want:     ActionNone,
		},
		{
			name:     "running container desired stopped is stopped",
			desired:  desiredRow("i1", DesiredStopped),
			observed: observedContainer("i1", container.ContainerStateRunning),
			want:     ActionStop,
		},
		{
			name:     "paused container desired stopped is stopped",
			desired:  desiredRow("i1", DesiredStopped),
			observed: observedContainer("i1", container.ContainerStatePaused),
			want:     ActionStop,
		},
		{
			name:     "exited container desired stopped is converged",
			desired:  desiredRow("i1", DesiredStopped),
			observed: observedContainer("i1", container.ContainerStateExited),
			want:     ActionNone,
		},
		{
			name:     "running orphan is stopped first",
			observed: observedContainer("i1", container.ContainerStateRunning),
			want:     ActionStop,
		},
		{
			name:     "stopped orphan is removed",
			observed: observedContainer("i1", container.ContainerStateExited),
			want:     ActionRemove,
		},
		{
			name:      "neither side has anything",
			noNetwork: true,
			want:      ActionNone,
		},
		{
			// The network is the first step: a container cannot attach to one
			// that does not exist.
			name:      "desired running with no network yet creates the network first",
			desired:   desiredRow("i1", DesiredRunning),
			noNetwork: true,
			want:      ActionCreateNetwork,
		},
		{
			// Nothing to attach to a network for, so none is created.
			name:      "desired stopped with no network is left alone",
			desired:   desiredRow("i1", DesiredStopped),
			noNetwork: true,
			want:      ActionNone,
		},
		{
			// The second half of a teardown: the container is already gone.
			name: "a network with no desired row and no container is removed",
			want: ActionRemoveNetwork,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planFor(tc.desired, tc.observed, !tc.noNetwork)
			if got.Kind != tc.want {
				t.Fatalf("planFor = %q (%s), want %q", got.Kind, got.Reason, tc.want)
			}
		})
	}
}

// Drift is reported, never acted on: replacing a running container is a
// generation rotation, and a rotation is not a step this loop takes.
func TestPlanFor_Drift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*container.ContainerInfo)
		want   ActionKind
	}{
		{
			name:   "image digest drift",
			mutate: func(c *container.ContainerInfo) { c.Labels[LabelImageDigest] = "sha256:bbbb" },
			want:   ActionDriftDetected,
		},
		{
			name:   "config hash drift",
			mutate: func(c *container.ContainerInfo) { c.Labels[LabelConfigHash] = "cfg-2" },
			want:   ActionDriftDetected,
		},
		{
			name:   "no drift",
			mutate: func(c *container.ContainerInfo) {},
			want:   ActionNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			observed := observedContainer("i1", container.ContainerStateRunning)
			tc.mutate(observed)
			if got := planFor(desiredRow("i1", DesiredRunning), observed, true); got.Kind != tc.want {
				t.Fatalf("planFor = %q (%s), want %q", got.Kind, got.Reason, tc.want)
			}
		})
	}
}

// A drifted container that is also stopped still reports drift rather than
// being started: starting it would run the wrong image.
func TestPlanFor_DriftBeatsLifecycle(t *testing.T) {
	observed := observedContainer("i1", container.ContainerStateExited)
	observed.Labels[LabelImageDigest] = "sha256:bbbb"

	if got := planFor(desiredRow("i1", DesiredRunning), observed, true); got.Kind != ActionDriftDetected {
		t.Fatalf("planFor = %q, want drift_detected — a stopped container running the wrong image must not just be started", got.Kind)
	}
}

// A managed container carrying no instance label belongs to nothing, which is
// exactly what an orphan is. It must not be silently ignored: Gleipnir labelled
// it, so Gleipnir cleans it up.
func TestPlanPass_UnlabelledManagedContainerIsAnOrphan(t *testing.T) {
	observed := []container.ContainerInfo{{
		ID:     "ctr-mystery",
		State:  container.ContainerStateExited,
		Labels: map[string]string{LabelManaged: ManagedValue},
	}}

	actions := planPass(nil, observed, nil)
	if len(actions) != 1 || actions[0].Kind != ActionRemove {
		t.Fatalf("actions = %+v, want a single remove", actions)
	}
}

// One action per instance per pass, whatever the mix of divergences.
func TestPlanPass_OneActionPerInstance(t *testing.T) {
	desired := []db.PluginContainer{
		*desiredRow("i1", DesiredRunning), // absent → create
		*desiredRow("i2", DesiredRunning), // exited → start
		*desiredRow("i3", DesiredStopped), // running → stop
		*desiredRow("i4", DesiredRunning), // running → none
	}
	observed := []container.ContainerInfo{
		*observedContainer("i2", container.ContainerStateExited),
		*observedContainer("i3", container.ContainerStateRunning),
		*observedContainer("i4", container.ContainerStateRunning),
		*observedContainer("i5", container.ContainerStateRunning), // orphan → stop
	}

	networks := []container.NetworkInfo{
		observedNetwork("i1"), observedNetwork("i2"), observedNetwork("i3"), observedNetwork("i4"),
	}

	byInstance := map[string]ActionKind{}
	for _, a := range planPass(desired, observed, networks) {
		if prev, dup := byInstance[a.InstanceID]; dup {
			t.Fatalf("instance %s planned twice: %q then %q", a.InstanceID, prev, a.Kind)
		}
		byInstance[a.InstanceID] = a.Kind
	}

	want := map[string]ActionKind{
		"i1": ActionCreate,
		"i2": ActionStart,
		"i3": ActionStop,
		"i4": ActionNone,
		"i5": ActionStop,
	}
	for id, kind := range want {
		if byInstance[id] != kind {
			t.Errorf("instance %s planned %q, want %q", id, byInstance[id], kind)
		}
	}
}

func TestPinnedImage(t *testing.T) {
	tests := []struct {
		name   string
		ref    string
		digest string
		want   string
	}{
		{name: "digest appended", ref: "gleipnir/slack:1.0.0", digest: "sha256:aaaa", want: "gleipnir/slack:1.0.0@sha256:aaaa"},
		{name: "already pinned is left alone", ref: "gleipnir/slack@sha256:bbbb", digest: "sha256:aaaa", want: "gleipnir/slack@sha256:bbbb"},
		{name: "no digest falls back to the bare reference", ref: "gleipnir/slack:1.0.0", want: "gleipnir/slack:1.0.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinnedImage(tc.ref, tc.digest); got != tc.want {
				t.Errorf("pinnedImage(%q, %q) = %q, want %q", tc.ref, tc.digest, got, tc.want)
			}
		})
	}
}
