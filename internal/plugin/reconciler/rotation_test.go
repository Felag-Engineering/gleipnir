package reconciler

import (
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// rotNow is the fixed reference instant. Every case expresses its timestamps
// relative to it, so the table reads as durations rather than clock values.
var rotNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func ts(offset time.Duration) string {
	return rotNow.Add(offset).UTC().Format(time.RFC3339Nano)
}

// gen builds a generation row. updatedAgo is how long ago it entered its
// current status, which is what both deadlines measure against.
func gen(id string, number int64, status string, digest, config string, updatedAgo time.Duration) db.PluginContainerGeneration {
	return db.PluginContainerGeneration{
		ID:               id,
		PluginInstanceID: "inst-1",
		Generation:       number,
		ImageDigest:      digest,
		ConfigHash:       config,
		TokenHash:        "hash-" + id,
		Status:           status,
		CreatedAt:        ts(-updatedAgo),
		UpdatedAt:        ts(-updatedAgo),
	}
}

// ctr builds an observed container for a generation.
func ctr(number int64, state container.ContainerState, health string) container.ContainerInfo {
	return container.ContainerInfo{
		ID:     container.ContainerID("c-" + itoa64(number)),
		Name:   containerName("inst-1"),
		State:  state,
		Health: health,
		Labels: map[string]string{
			LabelManaged:    ManagedValue,
			LabelInstance:   "inst-1",
			LabelGeneration: itoa64(number),
		},
	}
}

// rotDesired is the desired-state row a rotation converges toward. Named apart
// from plan_test.go's desiredRow, which returns a pointer for the core loop's
// nil-means-orphan contract; rotation always has a row.
func rotDesired(digest, config string) db.PluginContainer {
	return db.PluginContainer{
		PluginInstanceID: "inst-1",
		ImageDigest:      digest,
		ConfigHash:       config,
		DesiredState:     DesiredRunning,
	}
}

// The whole rotation, one step per pass, plus every state a crash can leave
// behind. Each row is a complete world state — desired row, generation rows,
// observed containers — and asserts the single step that world implies.
//
// Because every row is a full state rather than a step in a sequence, the table
// IS the crash-between-every-pair test: a process that died after step N leaves
// exactly the world the row for step N+1 describes.
func TestPlanRotation(t *testing.T) {
	const (
		oldDigest = "sha256:aaa"
		newDigest = "sha256:bbb"
		cfg       = "cfg-1"
	)

	tests := []struct {
		name       string
		desired    db.PluginContainer
		gens       []db.PluginContainerGeneration
		observed   map[int64]container.ContainerInfo
		wantKind   RotationKind
		wantGenNum int64
	}{
		{
			name:     "converged: active generation matches desired",
			desired:  rotDesired(oldDigest, cfg),
			gens:     []db.PluginContainerGeneration{gen("g1", 1, GenActive, oldDigest, cfg, time.Hour)},
			observed: map[int64]container.ContainerInfo{1: ctr(1, container.ContainerStateRunning, healthHealthy)},
			wantKind: RotationNone,
		},
		{
			name:       "image drift begins a rotation",
			desired:    rotDesired(newDigest, cfg),
			gens:       []db.PluginContainerGeneration{gen("g1", 1, GenActive, oldDigest, cfg, time.Hour)},
			observed:   map[int64]container.ContainerInfo{1: ctr(1, container.ContainerStateRunning, healthHealthy)},
			wantKind:   RotationBegin,
			wantGenNum: 1,
		},
		{
			name:       "config drift begins a rotation",
			desired:    rotDesired(oldDigest, "cfg-2"),
			gens:       []db.PluginContainerGeneration{gen("g1", 1, GenActive, oldDigest, cfg, time.Hour)},
			observed:   map[int64]container.ContainerInfo{1: ctr(1, container.ContainerStateRunning, healthHealthy)},
			wantKind:   RotationBegin,
			wantGenNum: 1,
		},
		{
			// Crash right after RotationBegin: the row exists, no container does.
			name:    "pending generation gets a container",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenPending, newDigest, cfg, time.Second),
			},
			observed:   map[int64]container.ContainerInfo{1: ctr(1, container.ContainerStateRunning, healthHealthy)},
			wantKind:   RotationCreate,
			wantGenNum: 2,
		},
		{
			// Crash right after RotationCreate: container exists, gate not decided.
			name:    "starting generation still coming up waits",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenStarting, newDigest, cfg, 5*time.Second),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateRunning, healthStarting),
			},
			wantKind:   RotationNone,
			wantGenNum: 2,
		},
		{
			name:    "starting generation reporting healthy is promoted",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenStarting, newDigest, cfg, 5*time.Second),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateRunning, healthHealthy),
			},
			wantKind:   RotationPromote,
			wantGenNum: 2,
		},
		{
			// An image with no healthcheck reports "" forever. Treating that as
			// never-healthy would make every such plugin un-rotatable.
			name:    "running with no declared healthcheck passes the container gate",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenStarting, newDigest, cfg, 5*time.Second),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateRunning, ""),
			},
			wantKind:   RotationPromote,
			wantGenNum: 2,
		},
		{
			// An exited container has answered the question; waiting out the
			// deadline would only delay a decision already made.
			name:    "exited during the gate aborts immediately",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenStarting, newDigest, cfg, time.Second),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateExited, ""),
			},
			wantKind:   RotationAbort,
			wantGenNum: 2,
		},
		{
			name:    "container vanished during the gate aborts",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenStarting, newDigest, cfg, time.Second),
			},
			observed:   map[int64]container.ContainerInfo{1: ctr(1, container.ContainerStateRunning, healthHealthy)},
			wantKind:   RotationAbort,
			wantGenNum: 2,
		},
		{
			// Unhealthy is not immediately fatal — a container can report it
			// while still coming up, and its healthcheck retries are the
			// runtime's business.
			name:    "unhealthy inside the deadline still waits",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenStarting, newDigest, cfg, 10*time.Second),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateRunning, healthUnhealthy),
			},
			wantKind:   RotationNone,
			wantGenNum: 2,
		},
		{
			name:    "unhealthy past the deadline aborts",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenStarting, newDigest, cfg, 10*time.Minute),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateRunning, healthUnhealthy),
			},
			wantKind:   RotationAbort,
			wantGenNum: 2,
		},
		{
			// A gate with no deadline is not a gate: the rotation would hang
			// open forever with the instance permanently mid-upgrade.
			name:    "still starting past the deadline aborts",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenStarting, newDigest, cfg, 10*time.Minute),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateRunning, healthStarting),
			},
			wantKind:   RotationAbort,
			wantGenNum: 2,
		},
		{
			// Crash right after RotationPromote.
			name:    "healthy generation switches",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenHealthy, newDigest, cfg, time.Second),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateRunning, healthHealthy),
			},
			wantKind:   RotationSwitch,
			wantGenNum: 2,
		},
		{
			// Crash right after RotationSwitch: old is draining, still running.
			name:    "draining generation with a live container drains",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenDraining, oldDigest, cfg, time.Second),
				gen("g2", 2, GenActive, newDigest, cfg, time.Second),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateRunning, healthHealthy),
			},
			wantKind:   RotationDrain,
			wantGenNum: 1,
		},
		{
			// Crash right after RotationDrain stopped the container.
			name:    "draining generation whose container is gone retires",
			desired: rotDesired(newDigest, cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenDraining, oldDigest, cfg, time.Minute),
				gen("g2", 2, GenActive, newDigest, cfg, time.Minute),
			},
			observed:   map[int64]container.ContainerInfo{2: ctr(2, container.ContainerStateRunning, healthHealthy)},
			wantKind:   RotationRetire,
			wantGenNum: 1,
		},
		{
			// Crash right after RotationRetire: rotation is complete.
			name:       "rotation complete converges to none",
			desired:    rotDesired(newDigest, cfg),
			gens:       []db.PluginContainerGeneration{gen("g2", 2, GenActive, newDigest, cfg, time.Minute)},
			observed:   map[int64]container.ContainerInfo{2: ctr(2, container.ContainerStateRunning, healthHealthy)},
			wantKind:   RotationNone,
			wantGenNum: 0,
		},
		{
			// Finishing an in-flight rotation beats starting another, even when
			// desired has moved again mid-rotation. Otherwise a fast-changing
			// desired row would strand generations nothing ever drains.
			name:    "a second drift mid-rotation does not preempt the first",
			desired: rotDesired("sha256:ccc", cfg),
			gens: []db.PluginContainerGeneration{
				gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
				gen("g2", 2, GenStarting, newDigest, cfg, 5*time.Second),
			},
			observed: map[int64]container.ContainerInfo{
				1: ctr(1, container.ContainerStateRunning, healthHealthy),
				2: ctr(2, container.ContainerStateRunning, healthHealthy),
			},
			wantKind:   RotationPromote,
			wantGenNum: 2,
		},
		{
			// First boot belongs to the core loop; acting here would race it.
			name:     "no active generation is not rotation's business",
			desired:  rotDesired(newDigest, cfg),
			gens:     nil,
			observed: nil,
			wantKind: RotationNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planRotation(RotationInputs{
				Desired:     tc.desired,
				Generations: tc.gens,
				Observed:    tc.observed,
				Now:         rotNow,
			})
			if got.Kind != tc.wantKind {
				t.Fatalf("kind = %q (%s), want %q", got.Kind, got.Reason, tc.wantKind)
			}
			if tc.wantGenNum != 0 && got.Generation != tc.wantGenNum {
				t.Errorf("generation = %d, want %d", got.Generation, tc.wantGenNum)
			}
			if got.Kind != RotationNone && got.InstanceID != "inst-1" {
				t.Errorf("instance = %q, want inst-1", got.InstanceID)
			}
		})
	}
}

// Driving the full sequence through the planner: each step's outcome is applied
// to the world, and the planner is asked again. It must walk begin → create →
// promote → switch → drain → retire → none without ever being told where it is.
func TestPlanRotation_WalksTheWholeSequence(t *testing.T) {
	const (
		oldDigest = "sha256:aaa"
		newDigest = "sha256:bbb"
		cfg       = "cfg-1"
	)

	desired := rotDesired(newDigest, cfg)
	gens := []db.PluginContainerGeneration{gen("g1", 1, GenActive, oldDigest, cfg, time.Hour)}
	observed := map[int64]container.ContainerInfo{1: ctr(1, container.ContainerStateRunning, healthHealthy)}

	want := []RotationKind{
		RotationBegin, RotationCreate, RotationPromote,
		RotationSwitch, RotationDrain, RotationRetire, RotationNone,
	}

	for i, expect := range want {
		act := planRotation(RotationInputs{
			Desired: desired, Generations: gens, Observed: observed, Now: rotNow,
		})
		if act.Kind != expect {
			t.Fatalf("step %d: kind = %q (%s), want %q", i, act.Kind, act.Reason, expect)
		}

		// Apply the step to the world, exactly as the real applier would.
		switch act.Kind {
		case RotationBegin:
			gens = append(gens, gen("g2", 2, GenPending, newDigest, cfg, 0))
		case RotationCreate:
			setStatus(gens, "g2", GenStarting)
			observed[2] = ctr(2, container.ContainerStateRunning, healthHealthy)
		case RotationPromote:
			setStatus(gens, "g2", GenHealthy)
		case RotationSwitch:
			setStatus(gens, "g2", GenActive)
			setStatus(gens, "g1", GenDraining)
		case RotationDrain:
			delete(observed, 1)
		case RotationRetire:
			gens = dropGeneration(gens, "g1")
		}
	}
}

// The invariant the whole design exists for: an upgrade that cannot prove
// itself healthy must not be able to take the instance down.
func TestPlanRotation_HealthGateFailureNeverStopsTheOldGeneration(t *testing.T) {
	const (
		oldDigest = "sha256:aaa"
		newDigest = "sha256:bbb"
		cfg       = "cfg-1"
	)

	desired := rotDesired(newDigest, cfg)
	gens := []db.PluginContainerGeneration{
		gen("g1", 1, GenActive, oldDigest, cfg, time.Hour),
		gen("g2", 2, GenStarting, newDigest, cfg, 10*time.Minute), // past the gate
	}
	observed := map[int64]container.ContainerInfo{
		1: ctr(1, container.ContainerStateRunning, healthHealthy),
		2: ctr(2, container.ContainerStateRunning, healthUnhealthy),
	}

	act := planRotation(RotationInputs{Desired: desired, Generations: gens, Observed: observed, Now: rotNow})
	if act.Kind != RotationAbort {
		t.Fatalf("kind = %q, want %q", act.Kind, RotationAbort)
	}
	if act.Generation != 2 {
		t.Fatalf("aborting generation %d, want the NEW one (2)", act.Generation)
	}
	if act.ContainerID == container.ContainerID("c-1") {
		t.Fatal("the abort targets the old generation's container; the serving generation must never be touched")
	}

	// After the abort, the old generation is still active and still serving,
	// and the planner has nothing further to do until desired changes or the
	// operator retries.
	gens = dropGeneration(gens, "g2")
	delete(observed, 2)

	next := planRotation(RotationInputs{Desired: desired, Generations: gens, Observed: observed, Now: rotNow})
	// desired still names the new digest, so a retry begins — from the OLD
	// generation, which never stopped serving.
	if next.Kind != RotationBegin {
		t.Fatalf("kind = %q, want a fresh %q after the failed attempt", next.Kind, RotationBegin)
	}
	if next.Generation != 1 {
		t.Errorf("retry is based on generation %d, want the still-serving 1", next.Generation)
	}
	if _, stillThere := observed[1]; !stillThere {
		t.Error("the old generation's container was removed by a failed rotation")
	}
}

// A drain that never finishes must not hold the container forever.
func TestDrainDeadlinePassed(t *testing.T) {
	tests := []struct {
		name    string
		updated string
		timeout time.Duration
		want    bool
	}{
		{name: "inside the window", updated: ts(-time.Minute), timeout: 5 * time.Minute},
		{name: "past the window", updated: ts(-10 * time.Minute), timeout: 5 * time.Minute, want: true},
		{name: "zero timeout uses the default", updated: ts(-time.Minute), timeout: 0},
		{name: "zero timeout, past the default", updated: ts(-time.Hour), timeout: 0, want: true},
		// Draining already means "superseded, finish up" — the new generation
		// is serving — so an unreadable timestamp resolves toward stopping
		// rather than waiting forever.
		{name: "unreadable timestamp exhausts the window", updated: "not a timestamp", timeout: time.Minute, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := db.PluginContainerGeneration{UpdatedAt: tc.updated}
			if got := DrainDeadlinePassed(g, rotNow, tc.timeout); got != tc.want {
				t.Errorf("DrainDeadlinePassed = %v, want %v", got, tc.want)
			}
		})
	}
}

// An unreadable timestamp on a STARTING generation resolves the other way from
// draining: not-expired, so the container-state checks decide instead of a
// storage oddity aborting a healthy upgrade.
func TestGateExpired_UnreadableTimestampDoesNotAbort(t *testing.T) {
	g := db.PluginContainerGeneration{UpdatedAt: "nonsense"}
	if gateExpired(g, rotNow, time.Minute) {
		t.Error("an unreadable timestamp expired the health gate; a storage oddity must not abort an upgrade")
	}
}

// Two rows in one status is not a state this machine produces. When it happens
// anyway, the newest wins so a stray old row cannot hold a rotation hostage.
func TestIndexGenerations_PrefersTheNewest(t *testing.T) {
	gens := []db.PluginContainerGeneration{
		gen("g2", 2, GenPending, "d", "c", 0),
		gen("g5", 5, GenPending, "d", "c", 0),
		gen("g3", 3, GenPending, "d", "c", 0),
	}
	got := indexGenerations(gens)
	if got[GenPending].Generation != 5 {
		t.Errorf("selected generation %d, want 5", got[GenPending].Generation)
	}
}

func TestItoa64(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{{0, "0"}, {7, "7"}, {42, "42"}, {-3, "-3"}, {1234567890, "1234567890"}}
	for _, tc := range tests {
		if got := itoa64(tc.in); got != tc.want {
			t.Errorf("itoa64(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// setStatus advances a generation in place, as a CAS update would.
func setStatus(gens []db.PluginContainerGeneration, id, status string) {
	for i := range gens {
		if gens[i].ID == id {
			gens[i].Status = status
			gens[i].UpdatedAt = ts(0)
			return
		}
	}
}

// dropGeneration removes a row, as a terminal transition would remove it from
// the live set.
func dropGeneration(gens []db.PluginContainerGeneration, id string) []db.PluginContainerGeneration {
	out := gens[:0:0]
	for _, g := range gens {
		if g.ID != id {
			out = append(out, g)
		}
	}
	return out
}
