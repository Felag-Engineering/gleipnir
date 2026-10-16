package reconciler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// rotationClock is a settable stand-in for rotationTimeNow. Tests that use it
// must not call t.Parallel(): the clock is package-level.
type rotationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *rotationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *rotationClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// useRotationClock swaps rotationTimeNow for a fake clock for the rest of the
// test. Every timestamp the rotation path writes comes from the same clock,
// so a backoff computed from a row's updated_at and checked against "now"
// is entirely in the test's hands.
func useRotationClock(t *testing.T) *rotationClock {
	t.Helper()
	clock := &rotationClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	prev := rotationTimeNow
	rotationTimeNow = clock.Now
	t.Cleanup(func() { rotationTimeNow = prev })
	return clock
}

func TestStartupBackoff(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{1, startupBackoffBase},
		{2, 2 * startupBackoffBase},
		{3, 4 * startupBackoffBase},
		{6, 32 * startupBackoffBase},
		{7, startupBackoffMax}, // 640s would pass the cap
		{50, startupBackoffMax},
		{1 << 20, startupBackoffMax}, // never overflows into a negative wait
	}
	for _, tc := range tests {
		if got := startupBackoff(tc.failures); got != tc.want {
			t.Errorf("startupBackoff(%d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
}

func TestPlanStartupHold(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }
	desired := db.PluginContainer{ImageDigest: "sha256:aaa", ConfigHash: "cfg-1"}

	gen := func(n int64, status, detail, digest string, updated time.Duration) db.PluginContainerGeneration {
		g := db.PluginContainerGeneration{
			Generation: n, Status: status, ImageDigest: digest, ConfigHash: "cfg-1", UpdatedAt: at(updated),
		}
		if detail != "" {
			g.StatusDetail = strPtr(detail)
		}
		return g
	}
	startFail := startupFailurePrefix + "start generation N container: no such sysctl"

	tests := []struct {
		name         string
		history      []db.PluginContainerGeneration
		wantFailures int
		wantUntil    time.Time
	}{
		{
			name: "no history holds nothing",
		},
		{
			name:         "one startup failure holds for the base",
			history:      []db.PluginContainerGeneration{gen(1, GenFailed, startFail, "sha256:aaa", 0)},
			wantFailures: 1,
			wantUntil:    base.Add(startupBackoffBase),
		},
		{
			name: "consecutive failures double the wait from the newest one",
			// Out of order on purpose: the history's order is not assumed.
			history: []db.PluginContainerGeneration{
				gen(1, GenFailed, startFail, "sha256:aaa", 0),
				gen(3, GenFailed, startFail, "sha256:aaa", time.Minute),
				gen(2, GenFailed, startFail, "sha256:aaa", 30*time.Second),
			},
			wantFailures: 3,
			wantUntil:    base.Add(time.Minute + 4*startupBackoffBase),
		},
		{
			name: "a health-gate abort counts like a start failure",
			history: []db.PluginContainerGeneration{
				gen(1, GenFailed, startupFailurePrefix+"new generation's container is exited during the health gate", "sha256:aaa", 0),
			},
			wantFailures: 1,
			wantUntil:    base.Add(startupBackoffBase),
		},
		{
			name: "an active generation ends the streak",
			history: []db.PluginContainerGeneration{
				gen(1, GenFailed, startFail, "sha256:aaa", 0),
				gen(2, GenActive, "promoted to active", "sha256:aaa", time.Minute),
				gen(3, GenFailed, startFail, "sha256:aaa", 2*time.Minute),
			},
			wantFailures: 1,
			wantUntil:    base.Add(2*time.Minute + startupBackoffBase),
		},
		{
			name: "an operator stop is not a startup failure and ends the streak",
			history: []db.PluginContainerGeneration{
				gen(1, GenFailed, startFail, "sha256:aaa", 0),
				gen(2, GenFailed, "desired state is stopped", "sha256:aaa", time.Minute),
			},
		},
		{
			name: "a token lost to a restart is not a startup failure",
			history: []db.PluginContainerGeneration{
				gen(1, GenFailed, "instance token was lost before the container was created; a new generation will be minted", "sha256:aaa", 0),
			},
		},
		{
			name: "a failure under a different desired image does not hold the new one",
			history: []db.PluginContainerGeneration{
				gen(1, GenFailed, startFail, "sha256:old", 0),
			},
		},
		{
			name: "only failures at the current image count",
			history: []db.PluginContainerGeneration{
				gen(1, GenFailed, startFail, "sha256:old", 0),
				gen(2, GenFailed, startFail, "sha256:old", time.Minute),
				gen(3, GenFailed, startFail, "sha256:aaa", 2*time.Minute),
			},
			wantFailures: 1,
			wantUntil:    base.Add(2*time.Minute + startupBackoffBase),
		},
		{
			name: "an unreadable timestamp holds nothing rather than forever",
			history: []db.PluginContainerGeneration{
				{Generation: 1, Status: GenFailed, StatusDetail: strPtr(startFail), ImageDigest: "sha256:aaa", ConfigHash: "cfg-1", UpdatedAt: "not a time"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hold := planStartupHold(tc.history, desired)
			if hold.Failures != tc.wantFailures {
				t.Fatalf("Failures = %d, want %d", hold.Failures, tc.wantFailures)
			}
			if tc.wantFailures == 0 {
				if hold.heldAt(base.Add(time.Hour)) || hold.heldAt(base) {
					t.Error("held with no counted failures")
				}
				return
			}
			if !hold.Until.Equal(tc.wantUntil) {
				t.Errorf("Until = %v, want %v", hold.Until, tc.wantUntil)
			}
			if !hold.heldAt(tc.wantUntil.Add(-time.Nanosecond)) {
				t.Error("not held just before the deadline")
			}
			if hold.heldAt(tc.wantUntil) {
				t.Error("still held at the deadline")
			}
			if !strings.HasPrefix(hold.LastFailure, startupFailurePrefix) {
				t.Errorf("LastFailure = %q, want the newest failure's detail", hold.LastFailure)
			}
		})
	}
}

// bootFreshInstanceToFirstMint runs the core loop's network and first-mint
// passes for an instance seeded by seedFreshInstance.
func bootFreshInstanceToFirstMint(t *testing.T, r *Reconciler) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := r.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("core pass %d: %v", i, err)
		}
	}
}

// allGenerations is every generation the instance has ever had, newest first.
func allGenerations(t *testing.T, store *db.Store, instanceID string) []db.PluginContainerGeneration {
	t.Helper()
	gens, err := store.Queries().ListContainerGenerationsByInstance(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("ListContainerGenerationsByInstance: %v", err)
	}
	return gens
}

// The DooD-suite failure (a kernel without IPv6 sysctls, so every container
// start fails): one failed start must fail its generation in the same pass,
// with the start error on the row, and must not leave a container or a live
// token behind.
func TestReconcileRotations_StartFailureFailsTheGenerationWithItsCause(t *testing.T) {
	ctx := context.Background()
	useRotationClock(t)
	store, rt, r := rotFixture(t)
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")
	bootFreshInstanceToFirstMint(t, r)

	rt.Runtime.(*container.Fake).StartErr = errors.New("open sysctl net.ipv6.conf.all.disable_ipv6: no such file or directory")
	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("create pass: %v", err)
	}
	if result.Errors != 1 {
		t.Errorf("Errors = %d, want 1 — a failed start is still a failed step", result.Errors)
	}

	gens := allGenerations(t, store, "inst-1")
	if len(gens) != 1 {
		t.Fatalf("generations = %d, want 1", len(gens))
	}
	failed := gens[0]
	if failed.Status != GenFailed {
		t.Fatalf("status = %q, want failed in the same pass the start failed", failed.Status)
	}
	if failed.StatusDetail == nil || !strings.HasPrefix(*failed.StatusDetail, startupFailurePrefix) ||
		!strings.Contains(*failed.StatusDetail, "disable_ipv6") {
		t.Errorf("status_detail = %v, want the start error under %q", failed.StatusDetail, startupFailurePrefix)
	}
	if failed.TokenRevokedAt == nil {
		t.Error("the failed generation's token was not revoked")
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, failed.TokenHash); err == nil {
		t.Error("the failed generation's token still authenticates")
	}
	left, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("the failed generation's container survived: %+v", left)
	}
}

// The churn itself. Every pass of the run loop is ReconcileOnce then
// ReconcileRotations; this drives that cycle on a fake clock for a long window
// with Start failing throughout. Passes are 5s apart rather than the DooD
// suite's 1s purely to keep the race lane fast — without the hold either
// cadence mints one generation per pass, so the bound below separates the two
// just as sharply.
// Before the backoff, every cycle minted a generation (1→60 in two minutes
// in the DooD suite). Now the number minted is bounded by the backoff
// schedule, not by the number of passes — and the moment Start works again,
// the next attempt boots the instance through to active.
func TestReconcile_PersistentStartFailureBacksOffAndRecovers(t *testing.T) {
	tests := []struct {
		name string
		// seed puts the instance in its starting state and returns the
		// generation that must still be serving afterwards, if any.
		seed func(t *testing.T, store *db.Store, rt *countingRuntime, r *Reconciler) (servingGenID string)
		// wantFirstNewGeneration is the first generation number the failing
		// attempts take; anything below it is the seeded serving generation.
		wantFirstNewGeneration int64
	}{
		{
			name: "first boot",
			seed: func(t *testing.T, store *db.Store, _ *countingRuntime, _ *Reconciler) string {
				seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")
				return ""
			},
			wantFirstNewGeneration: 1,
		},
		{
			name: "rotation to a new image",
			seed: func(t *testing.T, store *db.Store, rt *countingRuntime, _ *Reconciler) string {
				genID := seedInstance(t, store, rt, "inst-1", "sha256:old", "cfg-1")
				setDesiredDigest(t, store, "inst-1", "sha256:aaa")
				return genID
			},
			wantFirstNewGeneration: 2,
		},
	}

	const (
		passInterval = 5 * time.Second
		window       = 2 * time.Hour
	)
	// Attempts the schedule allows across the window: the ramp to the cap
	// (10s, 20s, … 320s — six waits summing to 630s), then one per
	// startupBackoffMax. Plus one for the very first attempt.
	ramp := 0 * time.Second
	rampAttempts := 0
	for ; startupBackoff(rampAttempts+1) < startupBackoffMax; rampAttempts++ {
		ramp += startupBackoff(rampAttempts + 1)
	}
	maxAttempts := 1 + rampAttempts + int((window-ramp)/startupBackoffMax) + 1

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clock := useRotationClock(t)
			store, rt, r := rotFixture(t)
			servingGenID := tc.seed(t, store, rt, r)

			fake := rt.Runtime.(*container.Fake)
			pass := func() {
				t.Helper()
				if _, err := r.ReconcileOnce(ctx); err != nil {
					t.Fatalf("ReconcileOnce: %v", err)
				}
				if _, err := r.ReconcileRotations(ctx); err != nil {
					t.Fatalf("ReconcileRotations: %v", err)
				}
				clock.Advance(passInterval)
			}

			// Bring up the network (first boot only needs it; harmless
			// otherwise) before Start starts failing.
			pass()
			fake.StartErr = errors.New("open sysctl net.ipv6.conf.all.disable_ipv6: no such file or directory")

			passes := int(window / passInterval)
			for i := 0; i < passes; i++ {
				pass()
			}

			failed := 0
			for _, gen := range allGenerations(t, store, "inst-1") {
				if gen.Generation < tc.wantFirstNewGeneration {
					continue
				}
				if gen.Status != GenFailed {
					t.Errorf("generation %d status = %q while Start fails, want failed", gen.Generation, gen.Status)
				}
				failed++
			}
			if failed == 0 {
				t.Fatal("no generation was ever attempted")
			}
			if failed > maxAttempts {
				t.Fatalf("%d generations failed over %d passes, want at most %d — minting is not backing off", failed, passes, maxAttempts)
			}
			if failed >= passes/10 {
				t.Fatalf("%d generations over %d passes is still churning", failed, passes)
			}

			// The serving generation, if there was one, was never touched.
			if servingGenID != "" {
				if got := genByID(t, store, servingGenID); got.Status != GenActive || got.TokenRevokedAt != nil {
					t.Fatalf("serving generation = %q (revoked %v), want active and unrevoked throughout", got.Status, got.TokenRevokedAt != nil)
				}
			}

			// No leftover containers and no live tokens from the failures.
			for _, gen := range allGenerations(t, store, "inst-1") {
				if gen.Status == GenFailed && gen.TokenRevokedAt == nil {
					t.Errorf("failed generation %d's token was never revoked", gen.Generation)
				}
			}
			managed, err := rt.ListByLabel(ctx, LabelManaged, ManagedValue)
			if err != nil {
				t.Fatalf("ListByLabel: %v", err)
			}
			wantContainers := 0
			if servingGenID != "" {
				wantContainers = 1
			}
			if len(managed) != wantContainers {
				t.Errorf("managed containers = %d, want %d — failed starts must not accumulate containers", len(managed), wantContainers)
			}

			// What the operator is shown is what the reconciler acted on:
			// every failure counted, the start error named, and the serving
			// generation reported rather than the instance read as down.
			fault, ok := DescribeStartupFault(allGenerations(t, store, "inst-1"), storedDesiredRow(t, store, "inst-1"))
			if !ok {
				t.Fatal("DescribeStartupFault reports nothing for an instance that keeps failing to start")
			}
			if fault.Failures != failed || !strings.Contains(fault.LastError, "disable_ipv6") {
				t.Errorf("fault = %d failures, %q; want %d failures naming the start error", fault.Failures, fault.LastError, failed)
			}
			wantServing := int64(0)
			if servingGenID != "" {
				wantServing = genByID(t, store, servingGenID).Generation
			}
			if fault.ServingGeneration != wantServing {
				t.Errorf("ServingGeneration = %d, want %d", fault.ServingGeneration, wantServing)
			}

			// Recovery: Start works again. Within one full backoff the next
			// attempt runs and the instance converges to a new active
			// generation, superseding the old one if there was one.
			fake.StartErr = nil
			before := allGenerations(t, store, "inst-1")[0].Generation
			for i := 0; i < int(startupBackoffMax/passInterval)+10; i++ {
				pass()
				if gen, ok := activeGeneration(t, store, "inst-1"); ok && gen.Generation > before {
					break
				}
			}
			active, ok := activeGeneration(t, store, "inst-1")
			if !ok || active.Generation != before+1 {
				t.Fatalf("after recovery active = %+v (ok=%v), want generation %d — the first attempt after Start recovers should succeed", active, ok, before+1)
			}
			if fault, ok := DescribeStartupFault(allGenerations(t, store, "inst-1"), storedDesiredRow(t, store, "inst-1")); ok {
				t.Errorf("fault still reported after a generation reached active: %+v", fault)
			}
		})
	}
}

// A backoff earned by one image must not delay a different one: an operator
// who ships a fixed bundle should see it tried at once.
func TestReconcile_NewDesiredImageBypassesTheStartupHold(t *testing.T) {
	ctx := context.Background()
	useRotationClock(t)
	store, rt, r := rotFixture(t)
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")
	bootFreshInstanceToFirstMint(t, r)

	rt.Runtime.(*container.Fake).StartErr = errors.New("start refused")
	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("create pass: %v", err)
	}

	// Held at the same image: the core loop still reports the step, but
	// nothing is minted.
	held, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("held pass: %v", err)
	}
	if len(held.Actions) != 1 || held.Actions[0].Kind != ActionBeginFirstGeneration {
		t.Fatalf("held pass actions = %+v, want the first-mint step still reported", held.Actions)
	}
	if n := len(allGenerations(t, store, "inst-1")); n != 1 {
		t.Fatalf("generations = %d while held, want 1", n)
	}

	// A new image: minted on the very next pass, no clock advance.
	setDesiredDigest(t, store, "inst-1", "sha256:bbb")
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("new-image pass: %v", err)
	}
	gens := allGenerations(t, store, "inst-1")
	if len(gens) != 2 || gens[0].Generation != 2 || gens[0].ImageDigest != "sha256:bbb" {
		t.Fatalf("generations = %+v, want generation 2 minted at once for the new image", gens)
	}
}

// A create the runtime refuses churns exactly like a start that fails: the
// token is already taken by then, so before this the row sat pending until
// the next pass failed it as a lost token and minted another.
func TestReconcileRotations_CreateFailureIsAStartupFailure(t *testing.T) {
	ctx := context.Background()
	clock := useRotationClock(t)
	store, rt, r := rotFixture(t)
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")
	bootFreshInstanceToFirstMint(t, r)

	rt.Runtime.(*container.Fake).CreateErr = errors.New("daemon refused the create")
	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("create pass: %v", err)
	}
	gen := allGenerations(t, store, "inst-1")[0]
	if !isStartupFailure(gen) || !strings.Contains(*gen.StatusDetail, "daemon refused the create") {
		t.Fatalf("generation 1 = %q / %v, want a startup failure naming the create error", gen.Status, gen.StatusDetail)
	}

	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("held pass: %v", err)
	}
	if n := len(allGenerations(t, store, "inst-1")); n != 1 {
		t.Fatalf("generations = %d inside the backoff, want 1", n)
	}

	clock.Advance(startupBackoffBase)
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("retry pass: %v", err)
	}
	if n := len(allGenerations(t, store, "inst-1")); n != 2 {
		t.Fatalf("generations = %d once the backoff elapsed, want 2", n)
	}
}

// The hold is derived from durable rows, not memory: a fresh Reconciler over
// the same store (a process restart) is held exactly as the old one was,
// rather than forgiving the failures with an immediate burst of attempts.
func TestReconcile_StartupHoldSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	clock := useRotationClock(t)
	store, rt, r := rotFixture(t)
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")
	bootFreshInstanceToFirstMint(t, r)

	rt.Runtime.(*container.Fake).StartErr = errors.New("start refused")
	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("create pass: %v", err)
	}

	restarted, err := New(Config{
		Runtime:   rt,
		Store:     store.Queries(),
		Rotations: store.Queries(),
		Subnets:   testAllocator(t),
		Posture:   container.PostureRootlessPodman,
		Interval:  time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	clock.Advance(startupBackoffBase / 2)
	if _, err := restarted.ReconcileOnce(ctx); err != nil {
		t.Fatalf("post-restart pass: %v", err)
	}
	if n := len(allGenerations(t, store, "inst-1")); n != 1 {
		t.Fatalf("generations = %d after a restart inside the backoff, want 1", n)
	}
	clock.Advance(startupBackoffBase / 2)
	if _, err := restarted.ReconcileOnce(ctx); err != nil {
		t.Fatalf("post-backoff pass: %v", err)
	}
	if n := len(allGenerations(t, store, "inst-1")); n != 2 {
		t.Fatalf("generations = %d once the backoff elapsed, want 2", n)
	}
}

// desired_state=stopped is still honoured first: an instance held in startup
// backoff with nothing live has nothing to stop, and stopping one mid-attempt
// terminalizes it as ever — the hold never stands between an operator and a
// stop.
func TestReconcileRotations_StopIsHonouredDuringStartupBackoff(t *testing.T) {
	ctx := context.Background()
	useRotationClock(t)
	store, rt, r := rotFixture(t)
	seed := seedInstance(t, store, rt, "inst-1", "sha256:old", "cfg-1")
	setDesiredDigest(t, store, "inst-1", "sha256:aaa")

	rt.Runtime.(*container.Fake).StartErr = errors.New("start refused")
	for i := 0; i < 2; i++ { // begin, then create (fails)
		if _, err := r.ReconcileRotations(ctx); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}

	setDesiredState(t, store, "inst-1", DesiredStopped)
	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("stop pass: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != RotationStopInstance {
		t.Fatalf("stop pass actions = %+v, want a single %q", result.Actions, RotationStopInstance)
	}
	if got := genByID(t, store, seed); got.Status != GenStopped || got.TokenRevokedAt == nil {
		t.Fatalf("serving generation after stop = %q (revoked %v), want stopped and revoked", got.Status, got.TokenRevokedAt != nil)
	}
}

// storedDesiredRow reads an instance's plugin_containers row back.
func storedDesiredRow(t *testing.T, store *db.Store, instanceID string) db.PluginContainer {
	t.Helper()
	row, err := store.Queries().GetPluginContainerByInstance(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("GetPluginContainerByInstance: %v", err)
	}
	return row
}
