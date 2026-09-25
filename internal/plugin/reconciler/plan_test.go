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

// generationContainer is a per-generation container as ReconcileRotations
// creates it (LabelGeneration set), for exercising the core loop's
// never-adopt-a-generation-container rule.
func generationContainer(instanceID string, generation int64, state container.ContainerState) *container.ContainerInfo {
	c := observedContainer(instanceID, state)
	c.Name = generationContainerName(instanceID, generation)
	c.Labels[LabelGeneration] = itoa64(generation)
	return c
}

// The convergence table: one step per (desired, observed) pair, and never more
// than one — a divergence needing several steps converges over several passes.
func TestPlanFor(t *testing.T) {
	tests := []struct {
		name      string
		desired   *db.PluginContainer
		observed  *container.ContainerInfo
		noNetwork bool // the instance's network does not exist yet
		gen       GenerationState
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
		{
			// Generation tracking enabled, nothing minted yet: the core loop
			// mints generation 1 before it ever tries to create a container.
			name:    "generation tracking enabled with no live generation mints the first one",
			desired: desiredRow("i1", DesiredRunning),
			gen:     GenerationMissing,
			want:    ActionBeginFirstGeneration,
		},
		{
			// A live generation already owns this instance's container
			// lifecycle. The core loop backs off entirely -- even though
			// nothing is observed and the desired row asks for a running
			// container, ReconcileRotations is the one creating it.
			name:    "generation tracking enabled with a live generation defers to rotation",
			desired: desiredRow("i1", DesiredRunning),
			gen:     GenerationLive,
			want:    ActionNone,
		},
		{
			// The defer-to-rotation rule holds even when the core loop can
			// see a container: two containers can carry the same instance
			// label mid-rotation, and the core loop cannot tell which is
			// which, so it must not act on either.
			name:     "a live generation is left alone even with a container observed",
			desired:  desiredRow("i1", DesiredRunning),
			observed: observedContainer("i1", container.ContainerStateExited),
			gen:      GenerationLive,
			want:     ActionNone,
		},
		{
			// Security review (#955 finding 1): a generation-labelled
			// container with NO live generation is a leftover from a failed
			// attempt (lost token, health-gate abort before the container
			// was claimed), never something to adopt. Running, it is stopped
			// first.
			name:     "a running generation-labelled container with no live generation is an orphan",
			desired:  desiredRow("i1", DesiredRunning),
			observed: generationContainer("i1", 1, container.ContainerStateRunning),
			gen:      GenerationMissing,
			want:     ActionStop,
		},
		{
			// Stopped, it is removed outright -- never started, which is what
			// planForExisting would otherwise do with a plain created/exited
			// container.
			name:     "a stopped generation-labelled container with no live generation is removed",
			desired:  desiredRow("i1", DesiredRunning),
			observed: generationContainer("i1", 1, container.ContainerStateExited),
			gen:      GenerationMissing,
			want:     ActionRemove,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planFor(tc.desired, tc.observed, !tc.noNetwork, tc.gen, selfAttachState{})
			if got.Kind != tc.want {
				t.Fatalf("planFor = %q (%s), want %q", got.Kind, got.Reason, tc.want)
			}
		})
	}
}

// Self-attach sits between network creation and container creation on the
// way up, and between container removal and network removal on the way down
// — the ordering the DoD calls out explicitly. Every case here configures a
// network so self-attach is even reachable; a create/teardown with no network
// yet is covered by TestPlanFor's own "creates the network first" cases,
// which self.Enabled must not short-circuit.
func TestPlanFor_SelfAttach(t *testing.T) {
	tests := []struct {
		name     string
		desired  *db.PluginContainer
		observed *container.ContainerInfo
		self     selfAttachState
		want     ActionKind
	}{
		{
			name:    "bring-up: self not yet attached joins before the container is created",
			desired: desiredRow("i1", DesiredRunning),
			self:    selfAttachState{Enabled: true, Attached: false},
			want:    ActionAttachSelf,
		},
		{
			name:    "bring-up: self already attached proceeds to create the container",
			desired: desiredRow("i1", DesiredRunning),
			self:    selfAttachState{Enabled: true, Attached: true},
			want:    ActionCreate,
		},
		{
			name:    "bring-up: self-attach disabled proceeds to create the container",
			desired: desiredRow("i1", DesiredRunning),
			self:    selfAttachState{},
			want:    ActionCreate,
		},
		{
			name:     "teardown: self still attached leaves the network before it is removed",
			observed: nil,
			self:     selfAttachState{Enabled: true, Attached: true},
			want:     ActionDetachSelf,
		},
		{
			name: "teardown: self already detached removes the network",
			self: selfAttachState{Enabled: true, Attached: false},
			want: ActionRemoveNetwork,
		},
		{
			name: "teardown: self-attach disabled removes the network",
			self: selfAttachState{},
			want: ActionRemoveNetwork,
		},
		{
			// #958 finding 7: self-attach is independent of the container's
			// OWN lifecycle. A stopped instance whose container is already
			// gone (observed == nil, DesiredStopped) with self still attached
			// leaves the network — Gleipnir has no reason to stay attached to
			// an instance nobody wants running.
			name:     "stopped instance with no container and self attached detaches",
			desired:  desiredRow("i1", DesiredStopped),
			observed: nil,
			self:     selfAttachState{Enabled: true, Attached: true},
			want:     ActionDetachSelf,
		},
		{
			name:     "stopped instance with no container and self already detached is left alone",
			desired:  desiredRow("i1", DesiredStopped),
			observed: nil,
			self:     selfAttachState{Enabled: true, Attached: false},
			want:     ActionNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planFor(tc.desired, tc.observed, true, GenerationTrackingDisabled, tc.self)
			if got.Kind != tc.want {
				t.Fatalf("planFor = %q (%s), want %q", got.Kind, got.Reason, tc.want)
			}
		})
	}
}

// #958 finding 7: self-attach/detach is planned independently of the
// container's own lifecycle state — a recreated Gleipnir (a fresh container
// ID, so Attached starts false again) must rejoin a network whose plugin is
// ALREADY running and fully converged, and an instance the operator stopped
// is one Gleipnir has no reason to stay attached to, even while its container
// still exists (stopped, not yet removed).
func TestPlanFor_SelfAttachIndependentOfLifecycle(t *testing.T) {
	tests := []struct {
		name     string
		desired  *db.PluginContainer
		observed *container.ContainerInfo
		self     selfAttachState
		want     ActionKind
	}{
		{
			name:     "a recreated gleipnir rejoins a network whose plugin is already running",
			desired:  desiredRow("i1", DesiredRunning),
			observed: observedContainer("i1", container.ContainerStateRunning),
			self:     selfAttachState{Enabled: true, Attached: false},
			want:     ActionAttachSelf,
		},
		{
			name:     "a stopped instance whose container still exists is detached from",
			desired:  desiredRow("i1", DesiredStopped),
			observed: observedContainer("i1", container.ContainerStateExited),
			self:     selfAttachState{Enabled: true, Attached: true},
			want:     ActionDetachSelf,
		},
		{
			name:     "self-attach disabled never overrides ordinary lifecycle handling",
			desired:  desiredRow("i1", DesiredRunning),
			observed: observedContainer("i1", container.ContainerStateExited),
			self:     selfAttachState{},
			want:     ActionStart,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planFor(tc.desired, tc.observed, true, GenerationTrackingDisabled, tc.self)
			if got.Kind != tc.want {
				t.Fatalf("planFor = %q (%s), want %q", got.Kind, got.Reason, tc.want)
			}
		})
	}
}

// A converged instance — self already attached, container already running —
// takes no self-attach action at all, on either side of the desired/observed
// switch. Idempotency ("a converged pass performs zero socket writes") only
// holds if this is true.
func TestPlanFor_SelfAttachConvergedIsNone(t *testing.T) {
	desired := desiredRow("i1", DesiredRunning)
	observed := observedContainer("i1", container.ContainerStateRunning)
	self := selfAttachState{Enabled: true, Attached: true}

	if got := planFor(desired, observed, true, GenerationTrackingDisabled, self); got.Kind != ActionNone {
		t.Fatalf("planFor = %q, want none — the instance and the self-attach are both already converged", got.Kind)
	}
}

// The self-Inspect hold (#1021 review item 3b: don't create while self status
// is unknown) has to cover ActionBeginFirstGeneration too, not just
// ActionCreate — a generation's container lands on the very network self-attach
// is joining, so first boot must wait behind the same uncertainty a plain
// create would.
func TestPlanFor_SelfAttachGatesFirstGeneration(t *testing.T) {
	tests := []struct {
		name string
		self selfAttachState
		want ActionKind
	}{
		{
			name: "self status unknown holds first boot rather than minting a generation",
			self: selfAttachState{Enabled: true, Unknown: true},
			want: ActionNone,
		},
		{
			name: "self not yet attached joins before generation 1 is minted",
			self: selfAttachState{Enabled: true, Attached: false},
			want: ActionAttachSelf,
		},
		{
			name: "self already attached lets first boot mint generation 1",
			self: selfAttachState{Enabled: true, Attached: true},
			want: ActionBeginFirstGeneration,
		},
		{
			name: "self-attach disabled never blocks first boot",
			self: selfAttachState{},
			want: ActionBeginFirstGeneration,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planFor(desiredRow("i1", DesiredRunning), nil, true, GenerationMissing, tc.self)
			if got.Kind != tc.want {
				t.Fatalf("planFor = %q (%s), want %q", got.Kind, got.Reason, tc.want)
			}
		})
	}
}

// GenerationLive hands the container's own lifecycle to ReconcileRotations,
// but network membership stays the core loop's job (#958 combination
// review): otherwise a recreated Gleipnir would never rejoin a live
// instance's network, and a live instance the operator stops would never be
// detached from.
func TestPlanFor_SelfAttachAppliesUnderGenerationLive(t *testing.T) {
	tests := []struct {
		name    string
		desired *db.PluginContainer
		self    selfAttachState
		want    ActionKind
	}{
		{
			name:    "not attached joins even though rotation owns the container",
			desired: desiredRow("i1", DesiredRunning),
			self:    selfAttachState{Enabled: true, Attached: false},
			want:    ActionAttachSelf,
		},
		{
			name:    "unknown status holds rather than guessing",
			desired: desiredRow("i1", DesiredRunning),
			self:    selfAttachState{Enabled: true, Unknown: true},
			want:    ActionNone,
		},
		{
			name:    "stopped and attached leaves the network",
			desired: desiredRow("i1", DesiredStopped),
			self:    selfAttachState{Enabled: true, Attached: true},
			want:    ActionDetachSelf,
		},
		{
			name:    "already attached is converged",
			desired: desiredRow("i1", DesiredRunning),
			self:    selfAttachState{Enabled: true, Attached: true},
			want:    ActionNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// observed is nil here on purpose: a live generation can be
			// `pending` with no container yet, and self-attach must not
			// depend on one existing.
			got := planFor(tc.desired, nil, true, GenerationLive, tc.self)
			if got.Kind != tc.want {
				t.Fatalf("planFor = %q (%s), want %q", got.Kind, got.Reason, tc.want)
			}
		})
	}
}

// selfStateFor is planPass's own glue between a raw selfInspect and the
// selfAttachState planFor consumes.
func TestSelfStateFor(t *testing.T) {
	net := observedNetwork("i1")

	if got := selfStateFor(selfInspect{}, net); got.Enabled {
		t.Errorf("selfStateFor(not configured) = %+v, want Enabled=false", got)
	}

	notAttached := container.ContainerInfo{ID: "self"}
	if got := selfStateFor(selfInspect{Configured: true, Info: &notAttached}, net); !got.Enabled || got.Attached || got.Unknown {
		t.Errorf("selfStateFor(not attached) = %+v, want Enabled=true, Attached=false, Unknown=false", got)
	}

	attached := container.ContainerInfo{ID: "self", Networks: []container.NetworkID{net.ID}}
	if got := selfStateFor(selfInspect{Configured: true, Info: &attached}, net); !got.Enabled || !got.Attached {
		t.Errorf("selfStateFor(attached) = %+v, want Enabled=true, Attached=true", got)
	}

	// #1021 review item 3b: a failed Inspect reports Unknown, not merely
	// Attached=false — the two must be distinguishable to planFor.
	if got := selfStateFor(selfInspect{Configured: true, Unknown: true}, net); !got.Enabled || got.Attached || !got.Unknown {
		t.Errorf("selfStateFor(unknown) = %+v, want Enabled=true, Attached=false, Unknown=true", got)
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
			if got := planFor(desiredRow("i1", DesiredRunning), observed, true, GenerationTrackingDisabled, selfAttachState{}); got.Kind != tc.want {
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

	if got := planFor(desiredRow("i1", DesiredRunning), observed, true, GenerationTrackingDisabled, selfAttachState{}); got.Kind != ActionDriftDetected {
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

	actions := planPass(nil, observed, nil, nil, false, selfInspect{})
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
	for _, a := range planPass(desired, observed, networks, nil, false, selfInspect{}) {
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
