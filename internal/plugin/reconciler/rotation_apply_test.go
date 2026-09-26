package reconciler

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// rotFixture stands up a real SQLite store, a fake runtime, and a Reconciler
// wired for rotation.
//
// A real store rather than a fake, because the properties under test here are
// the CAS guards and the revocation query's WHERE clause — a fake would be
// asserting on a reimplementation of exactly the SQL that matters.
func rotFixture(t *testing.T) (*db.Store, *countingRuntime, *Reconciler) {
	t.Helper()

	store, err := db.Open(filepath.Join(t.TempDir(), "rot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rt := &countingRuntime{Runtime: container.NewFake()}
	r, err := New(Config{
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
	return store, rt, r
}

// rotFixtureWithPublisher is rotFixture with a capturePublisher wired in, for
// tests that drive convergence through Start/Kick alone (#955 security
// re-review round 2 item 1) and must synchronize on the published
// EventRotationPassCompleted signal rather than ever calling
// ReconcileRotations directly.
func rotFixtureWithPublisher(t *testing.T) (*db.Store, *countingRuntime, *Reconciler, *capturePublisher) {
	t.Helper()

	store, err := db.Open(filepath.Join(t.TempDir(), "rot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rt := &countingRuntime{Runtime: container.NewFake()}
	pub := &capturePublisher{}
	r, err := New(Config{
		Runtime:   rt,
		Store:     store.Queries(),
		Rotations: store.Queries(),
		Subnets:   testAllocator(t),
		Posture:   container.PostureRootlessPodman,
		Interval:  time.Hour, // kicks drive the tests; the ticker must not race them
		Publisher: pub,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, rt, r, pub
}

// activeGeneration returns the instance's currently active generation, if
// any. Tests driving convergence through Start/Kick alone have no
// PassResult to inspect and must read the store instead.
func activeGeneration(t *testing.T, store *db.Store, instanceID string) (db.PluginContainerGeneration, bool) {
	t.Helper()
	for _, gen := range liveGenerations(t, store, instanceID) {
		if gen.Status == GenActive {
			return gen, true
		}
	}
	return db.PluginContainerGeneration{}, false
}

// kickAndWaitForRotationPass sends a Kick and blocks until the next rotation
// pass has been published, so a test driving convergence through Start/Kick
// alone synchronizes on the signal the loop actually emits rather than
// sleeping a guessed duration.
func kickAndWaitForRotationPass(t *testing.T, r *Reconciler, pub *capturePublisher) {
	t.Helper()
	before := pub.count(EventRotationPassCompleted)
	r.Kick()
	pub.waitForRotationPasses(t, before+1)
}

// rotFixtureWithProxyEnv is rotFixture with EgressEnv and HostEndpointEnv both
// configured, for the #955 security review finding 3 tests: every
// generation-tracked create (first boot and a normal rotation alike) goes
// through the one create path (createRotationContainer, via
// withGenerationEnv) that composes them.
func rotFixtureWithProxyEnv(t *testing.T) (*db.Store, *countingRuntime, *Reconciler) {
	t.Helper()

	store, err := db.Open(filepath.Join(t.TempDir(), "rot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rt := &countingRuntime{Runtime: container.NewFake()}
	r, err := New(Config{
		Runtime:   rt,
		Store:     store.Queries(),
		Rotations: store.Queries(),
		Subnets:   testAllocator(t),
		Posture:   container.PostureRootlessPodman,
		Interval:  time.Hour,
		EgressEnv: func(_ context.Context, instanceID string) []string {
			return []string{
				"HTTP_PROXY=http://10.83.1.1:8118", "HTTPS_PROXY=http://10.83.1.1:8118",
				"http_proxy=http://10.83.1.1:8118", "https_proxy=http://10.83.1.1:8118",
				"NO_PROXY=", "no_proxy=",
			}
		},
		HostEndpointEnv: func(_ context.Context, instanceID string) []string {
			return []string{"GLEIPNIR_HOST_ENDPOINT_URL=http://10.83.1.1:8765"}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, rt, r
}

// assertGenerationEnvScopesHostEndpoint asserts a generation container's Env
// (as captured by countingRuntime) carries the egress proxy variables, the
// host endpoint URL, and NO_PROXY/no_proxy scoped to exactly the host
// endpoint's own host:port -- and nothing else, so nothing plugin-supplied
// can widen or override any of them (#955 security review finding 3).
func assertGenerationEnvScopesHostEndpoint(t *testing.T, env []string) {
	t.Helper()
	want := map[string]string{
		"HTTP_PROXY":                 "http://10.83.1.1:8118",
		"HTTPS_PROXY":                "http://10.83.1.1:8118",
		"http_proxy":                 "http://10.83.1.1:8118",
		"https_proxy":                "http://10.83.1.1:8118",
		"NO_PROXY":                   "10.83.1.1:8765",
		"no_proxy":                   "10.83.1.1:8765",
		"GLEIPNIR_HOST_ENDPOINT_URL": "http://10.83.1.1:8765",
		instanceTokenEnvVar:          "", // checked for presence only, below
	}
	got := map[string]string{}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		got[key] = value
	}
	for key, wantValue := range want {
		gotValue, present := got[key]
		if !present {
			t.Errorf("env missing %s", key)
			continue
		}
		if key == instanceTokenEnvVar {
			if gotValue == "" {
				t.Error("GLEIPNIR_INSTANCE_TOKEN is empty")
			}
			continue
		}
		if gotValue != wantValue {
			t.Errorf("%s = %q, want %q", key, gotValue, wantValue)
		}
	}
}

// seedInstance inserts the plugin/instance/desired-container rows a rotation
// needs, plus an active generation N=1 whose container already exists.
func seedInstance(t *testing.T, store *db.Store, rt *countingRuntime, instanceID, digest, config string) (activeGenID string) {
	t.Helper()
	ctx := context.Background()
	q := store.Queries()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	pluginID := model.NewULID()
	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID: pluginID, Name: "p-" + instanceID, PluginVersion: "1.0.0",
		ManifestSnapshot: "{}", TrustedPubkey: "k", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: instanceID, PluginID: pluginID, InstanceName: "i-" + instanceID,
		ConfigJson: "{}", SubscriptionScopeJson: "{}", HandshakeVersions: "{}",
		HealthState: "healthy", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
	if _, err := q.CreatePluginContainer(ctx, db.CreatePluginContainerParams{
		ID: model.NewULID(), PluginInstanceID: instanceID,
		ImageRef: "ghcr.io/acme/p:1", ImageDigest: digest, ConfigHash: config,
		NetworkName: "gleipnir-plugin-" + instanceID, DesiredState: DesiredRunning,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginContainer: %v", err)
	}

	// The already-serving generation, with a container the runtime knows about.
	id, err := rt.Create(ctx, container.CreateOptions{
		Name:    generationContainerName(instanceID, 1),
		Image:   "ghcr.io/acme/p@" + digest,
		Network: "gleipnir-plugin-" + instanceID,
		Labels: map[string]string{
			LabelManaged: ManagedValue, LabelInstance: instanceID,
			LabelGeneration: "1", LabelImageDigest: digest, LabelConfigHash: config,
		},
	})
	if err != nil {
		t.Fatalf("seed container: %v", err)
	}
	if err := rt.Start(ctx, id); err != nil {
		t.Fatalf("start seed container: %v", err)
	}

	activeGenID = model.NewULID()
	containerID := string(id)
	if _, err := q.CreateContainerGeneration(ctx, db.CreateContainerGenerationParams{
		ID: activeGenID, PluginInstanceID: instanceID, Generation: 1,
		ContainerID: &containerID, ImageDigest: digest, ConfigHash: config,
		TokenHash: HashInstanceToken("gen1-token"), Status: GenActive,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateContainerGeneration: %v", err)
	}
	return activeGenID
}

// seedGenerationContainer adds another generation, with its own running
// container, to an already-seeded instance -- for constructing multi-
// generation states directly rather than walking the state machine to them.
// Some states this builds (draining coexisting with starting, in
// particular) are not reachable through the ordinary drift-triggered
// sequence -- planRotation always finishes draining an in-flight
// supersession before starting a new one -- but the stop path must still
// converge them safely if they ever arise some other way (a hand-edited
// row, a future second producer of RotationBegin), so the test constructs
// the row directly.
func seedGenerationContainer(t *testing.T, store *db.Store, rt *countingRuntime, instanceID string, generation int64, status, digest, config, token string) string {
	t.Helper()
	ctx := context.Background()
	q := store.Queries()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	id, err := rt.Create(ctx, container.CreateOptions{
		Name:    generationContainerName(instanceID, generation),
		Image:   "ghcr.io/acme/p@" + digest,
		Network: "gleipnir-plugin-" + instanceID,
		Labels: map[string]string{
			LabelManaged: ManagedValue, LabelInstance: instanceID,
			LabelGeneration: itoa64(generation), LabelImageDigest: digest, LabelConfigHash: config,
		},
	})
	if err != nil {
		t.Fatalf("seed generation %d container: %v", generation, err)
	}
	if err := rt.Start(ctx, id); err != nil {
		t.Fatalf("start generation %d container: %v", generation, err)
	}

	genID := model.NewULID()
	containerID := string(id)
	if _, err := q.CreateContainerGeneration(ctx, db.CreateContainerGenerationParams{
		ID: genID, PluginInstanceID: instanceID, Generation: generation,
		ContainerID: &containerID, ImageDigest: digest, ConfigHash: config,
		TokenHash: HashInstanceToken(token), Status: status,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateContainerGeneration(%d): %v", generation, err)
	}
	return genID
}

// seedFreshInstance inserts the plugin/instance/desired-container rows for an
// instance that has never booted: no generation row, no container. This is
// what a genuinely new install looks like to the reconciler.
func seedFreshInstance(t *testing.T, store *db.Store, instanceID, digest, config string) {
	t.Helper()
	ctx := context.Background()
	q := store.Queries()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	pluginID := model.NewULID()
	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID: pluginID, Name: "p-" + instanceID, PluginVersion: "1.0.0",
		ManifestSnapshot: "{}", TrustedPubkey: "k", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: instanceID, PluginID: pluginID, InstanceName: "i-" + instanceID,
		ConfigJson: "{}", SubscriptionScopeJson: "{}", HandshakeVersions: "{}",
		HealthState: "unhealthy", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
	if _, err := q.CreatePluginContainer(ctx, db.CreatePluginContainerParams{
		ID: model.NewULID(), PluginInstanceID: instanceID,
		ImageRef: "ghcr.io/acme/p:1", ImageDigest: digest, ConfigHash: config,
		NetworkName: "gleipnir-plugin-" + instanceID, DesiredState: DesiredRunning,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginContainer: %v", err)
	}
}

// setDesiredDigest points the desired-state row at a new image.
func setDesiredDigest(t *testing.T, store *db.Store, instanceID, digest string) {
	t.Helper()
	if _, err := store.DB().ExecContext(context.Background(),
		`UPDATE plugin_containers SET image_digest = ? WHERE plugin_instance_id = ?`,
		digest, instanceID); err != nil {
		t.Fatalf("update desired digest: %v", err)
	}
}

// setDesiredState flips an instance's desired_state, mirroring what an
// operator's stop/start action would write.
func setDesiredState(t *testing.T, store *db.Store, instanceID, state string) {
	t.Helper()
	if _, err := store.DB().ExecContext(context.Background(),
		`UPDATE plugin_containers SET desired_state = ? WHERE plugin_instance_id = ?`,
		state, instanceID); err != nil {
		t.Fatalf("update desired state: %v", err)
	}
}

// deleteDesiredContainer removes an instance's desired-state row while
// leaving the instance (and any live generations) behind -- the scenario
// finding 4's orphan sweep exists for.
func deleteDesiredContainer(t *testing.T, store *db.Store, instanceID string) {
	t.Helper()
	if _, err := store.DB().ExecContext(context.Background(),
		`DELETE FROM plugin_containers WHERE plugin_instance_id = ?`, instanceID); err != nil {
		t.Fatalf("delete desired container: %v", err)
	}
}

func genByID(t *testing.T, store *db.Store, id string) db.PluginContainerGeneration {
	t.Helper()
	got, err := store.Queries().GetContainerGeneration(context.Background(), id)
	if err != nil {
		t.Fatalf("GetContainerGeneration(%s): %v", id, err)
	}
	return got
}

func liveGenerations(t *testing.T, store *db.Store, instanceID string) []db.PluginContainerGeneration {
	t.Helper()
	all, err := store.Queries().ListContainerGenerationsByInstance(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("ListContainerGenerationsByInstance: %v", err)
	}
	return all
}

// The full rotation, driven one pass at a time against a real store and a real
// (fake-backed) runtime. Nothing tells the loop where it is — each pass reads
// the world and takes the one step that world implies.
func TestReconcileRotations_FullRotation(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)

	const (
		oldDigest = "sha256:aaa"
		newDigest = "sha256:bbb"
	)
	oldGenID := seedInstance(t, store, rt, "inst-1", oldDigest, "cfg-1")

	// Converged: nothing to rotate.
	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("ReconcileRotations: %v", err)
	}
	if !result.Converged {
		t.Fatalf("a matching instance produced %d actions, want none", len(result.Actions))
	}

	setDesiredDigest(t, store, "inst-1", newDigest)

	want := []RotationKind{RotationBegin, RotationCreate, RotationPromote, RotationSwitch}
	for i, kind := range want {
		result, err := r.ReconcileRotations(ctx)
		if err != nil {
			t.Fatalf("pass %d (%s): %v", i, kind, err)
		}
		if len(result.Actions) != 1 {
			t.Fatalf("pass %d: %d actions, want exactly 1", i, len(result.Actions))
		}
		if result.Actions[0].Kind != kind {
			t.Fatalf("pass %d: kind = %q (%s), want %q",
				i, result.Actions[0].Kind, result.Actions[0].Reason, kind)
		}
		if result.Errors != 0 {
			t.Fatalf("pass %d (%s) reported %d errors", i, kind, result.Errors)
		}
	}

	// The new generation is serving; the old one is draining.
	gens := liveGenerations(t, store, "inst-1")
	if len(gens) != 2 {
		t.Fatalf("%d generations, want 2", len(gens))
	}
	newGen := gens[0] // ordered by generation DESC
	if newGen.Generation != 2 || newGen.Status != GenActive {
		t.Fatalf("generation 2 = %d/%q, want 2/active", newGen.Generation, newGen.Status)
	}
	if got := genByID(t, store, oldGenID); got.Status != GenDraining {
		t.Fatalf("old generation status = %q, want draining", got.Status)
	}

	// The old generation's token is STILL valid while it drains: revoking it
	// here would fail the in-flight work draining exists to protect.
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err != nil {
		t.Fatalf("old token rejected while still draining: %v", err)
	}

	// Drive the drain deadline past and finish the rotation.
	r.drainTimeout = time.Nanosecond

	for i, kind := range []RotationKind{RotationDrain, RotationRetire} {
		result, err := r.ReconcileRotations(ctx)
		if err != nil {
			t.Fatalf("teardown pass %d (%s): %v", i, kind, err)
		}
		if len(result.Actions) != 1 || result.Actions[0].Kind != kind {
			t.Fatalf("teardown pass %d: got %+v, want %q", i, result.Actions, kind)
		}
		if result.Errors != 0 {
			t.Fatalf("teardown pass %d (%s) reported %d errors", i, kind, result.Errors)
		}
	}

	// Rotation complete: old generation stopped, and its token no longer
	// authenticates anything.
	if got := genByID(t, store, oldGenID); got.Status != GenStopped {
		t.Errorf("old generation status = %q, want stopped", got.Status)
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err == nil {
		t.Error("the retired generation's token still authenticates; revocation did not take")
	}

	// And the loop settles.
	final, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("final pass: %v", err)
	}
	if !final.Converged {
		t.Errorf("a completed rotation did not converge: %+v", final.Actions)
	}
}

// Security review round 3 item 3: retireGeneration now advances the row and
// revokes the token BEFORE attempting Remove, matching every other retire
// path (failLostTokenGeneration, stopInstance, retireOrphanedInstanceGeneration).
// A persistent Remove failure therefore leaves a non-live, revoked
// generation rather than a `draining` row retrying RotationRetire forever --
// the sweep collects the leftover container on a later pass instead.
func TestReconcileRotations_RetireGenerationRevokesBeforeRemove(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)

	const (
		oldDigest = "sha256:aaa"
		newDigest = "sha256:bbb"
	)
	oldGenID := seedInstance(t, store, rt, "inst-1", oldDigest, "cfg-1")
	setDesiredDigest(t, store, "inst-1", newDigest)

	for _, kind := range []RotationKind{RotationBegin, RotationCreate, RotationPromote, RotationSwitch} {
		result, err := r.ReconcileRotations(ctx)
		if err != nil {
			t.Fatalf("pass (%s): %v", kind, err)
		}
		if len(result.Actions) != 1 || result.Actions[0].Kind != kind {
			t.Fatalf("actions = %+v, want a single %q", result.Actions, kind)
		}
	}

	r.drainTimeout = time.Nanosecond
	if _, err := r.ReconcileRotations(ctx); err != nil { // RotationDrain
		t.Fatalf("drain pass: %v", err)
	}

	rt.Runtime.(*container.Fake).RemoveErr = errors.New("remove refused")
	retirePass, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("retire pass: %v", err)
	}
	if len(retirePass.Actions) != 1 || retirePass.Actions[0].Kind != RotationRetire {
		t.Fatalf("retire pass actions = %+v, want a single %q", retirePass.Actions, RotationRetire)
	}
	if retirePass.Errors != 1 {
		t.Fatalf("Errors = %d, want 1 (the refused removal)", retirePass.Errors)
	}

	// Despite the failed removal, the row is already non-live and revoked --
	// not stuck in `draining`, retrying RotationRetire forever.
	if got := genByID(t, store, oldGenID); got.Status != GenStopped {
		t.Errorf("old generation status = %q, want stopped even though Remove failed", got.Status)
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err == nil {
		t.Error("the old generation's token still authenticates despite Remove failing")
	}
	leftover, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(leftover) != 1 {
		t.Fatalf("old generation's container was not left behind: %+v", leftover)
	}

	// The socket recovers; the sweep (not RotationRetire, since the row is no
	// longer live) collects the leftover on the next pass.
	rt.Runtime.(*container.Fake).RemoveErr = nil
	sweepPass, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("sweep pass: %v", err)
	}
	if !sweepPass.Converged {
		t.Fatalf("sweep pass actions = %+v, want convergence (the sweep is not a planned action)", sweepPass.Actions)
	}
	gone, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("old generation's leftover container survived the sweep: %+v", gone)
	}
}

// The invariant: an upgrade that cannot prove itself healthy must not take the
// instance down.
func TestReconcileRotations_HealthGateFailureLeavesTheOldGenerationServing(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)

	oldGenID := seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")
	setDesiredDigest(t, store, "inst-1", "sha256:bbb")

	// Begin + create.
	for i := 0; i < 2; i++ {
		if _, err := r.ReconcileRotations(ctx); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}

	// The new container never becomes healthy and the gate deadline passes.
	r.healthGateTimeout = time.Nanosecond
	markUnhealthy(t, rt, "inst-1", 2)

	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("gate pass: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != RotationAbort {
		t.Fatalf("got %+v, want a single %q", result.Actions, RotationAbort)
	}

	// The old generation never moved.
	old := genByID(t, store, oldGenID)
	if old.Status != GenActive {
		t.Errorf("old generation status = %q, want active — a failed upgrade must not stop the serving generation", old.Status)
	}
	if old.TokenRevokedAt != nil {
		t.Error("the serving generation's token was revoked by a failed rotation")
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err != nil {
		t.Errorf("the serving generation's token stopped authenticating after a failed rotation: %v", err)
	}

	// Its container is still there and still running.
	live, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(live) != 1 || live[0].State != container.ContainerStateRunning {
		t.Fatalf("old generation's container = %+v, want one running", live)
	}

	// The failed generation is recorded as failed, its token revoked, and its
	// container gone.
	var failed db.PluginContainerGeneration
	for _, gen := range liveGenerations(t, store, "inst-1") {
		if gen.Generation == 2 {
			failed = gen
		}
	}
	if failed.Status != GenFailed {
		t.Errorf("new generation status = %q, want failed", failed.Status)
	}
	if failed.TokenRevokedAt == nil {
		t.Error("the failed generation's token was not revoked")
	}
	gone, err := rt.ListByLabel(ctx, LabelGeneration, "2")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("the failed generation's container survived: %+v", gone)
	}
}

// A generation number is never reused, so a failed attempt consumes its number
// permanently — otherwise two different containers would be indistinguishable
// in an audit trail.
func TestReconcileRotations_RetryUsesTheNextNumber(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)

	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")
	setDesiredDigest(t, store, "inst-1", "sha256:bbb")

	for i := 0; i < 2; i++ {
		if _, err := r.ReconcileRotations(ctx); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	r.healthGateTimeout = time.Nanosecond
	markUnhealthy(t, rt, "inst-1", 2)
	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("abort pass: %v", err)
	}

	// Desired still names the new digest, so the next pass begins again.
	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("retry pass: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != RotationBegin {
		t.Fatalf("got %+v, want a fresh %q", result.Actions, RotationBegin)
	}

	var numbers []int64
	for _, gen := range liveGenerations(t, store, "inst-1") {
		numbers = append(numbers, gen.Generation)
	}
	if len(numbers) != 3 {
		t.Fatalf("generations = %v, want three (1 active, 2 failed, 3 pending)", numbers)
	}
	if numbers[0] != 3 {
		t.Errorf("retry took generation %d, want 3 — a failed number is never reused", numbers[0])
	}
}

// A token minted by a process that died before the container existed cannot be
// recovered, and starting a container the host cannot authenticate is worse
// than starting none.
func TestReconcileRotations_LostTokenFailsTheGeneration(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)

	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")
	setDesiredDigest(t, store, "inst-1", "sha256:bbb")

	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("begin pass: %v", err)
	}

	// Model the restart: the in-memory token map is gone.
	r.tokenMu.Lock()
	r.tokens = map[string]string{}
	r.tokenMu.Unlock()

	before := rt.creates
	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("create pass: %v", err)
	}
	if rt.creates != before {
		t.Error("a container was created for a generation whose token was lost")
	}

	for _, gen := range liveGenerations(t, store, "inst-1") {
		if gen.Generation == 2 && gen.Status != GenFailed {
			t.Errorf("generation 2 status = %q, want failed", gen.Status)
		}
	}
}

// Security review (#955 finding 1): a generation whose container was
// actually created, but whose Start then failed before the row ever left
// pending, must not leave a live-looking container behind for the core loop
// to mistake for something to adopt. The retry that follows (takeToken is
// destructive, so it finds nothing) must revoke the token, remove the
// leftover container, and let the next generation get minted clean.
func TestReconcileRotations_CreateSucceedsStartFailsRevokesAndRemoves(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")

	// Network, then begin generation 1.
	for i := 0; i < 2; i++ {
		if _, err := r.ReconcileOnce(ctx); err != nil {
			t.Fatalf("core pass %d: %v", i, err)
		}
	}

	// Create succeeds, but Start fails -- the row never advances past
	// pending, and the container it left behind is still there.
	rt.Runtime.(*container.Fake).StartErr = errors.New("start refused")
	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("create pass (start fails): %v", err)
	}
	before := liveGenerations(t, store, "inst-1")
	if len(before) != 1 || before[0].Status != GenPending {
		t.Fatalf("generation after the failed start = %+v, want a single pending row", before)
	}
	leftover, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(leftover) != 1 {
		t.Fatalf("leftover containers = %d, want 1 (Create succeeded before Start failed)", len(leftover))
	}

	// The retry: Start would succeed now, but it never gets the chance --
	// the stashed token was already consumed by the first attempt.
	rt.Runtime.(*container.Fake).StartErr = nil
	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("retry pass: %v", err)
	}

	var failed db.PluginContainerGeneration
	for _, gen := range liveGenerations(t, store, "inst-1") {
		if gen.Generation == 1 {
			failed = gen
		}
	}
	if failed.Status != GenFailed {
		t.Fatalf("generation 1 status = %q, want failed", failed.Status)
	}
	if failed.TokenRevokedAt == nil {
		t.Error("generation 1's token was not revoked")
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, failed.TokenHash); err == nil {
		t.Error("generation 1's token still authenticates after it was failed and revoked")
	}

	gone, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("generation 1's leftover container survived: %+v — the core loop would read it as something to adopt", gone)
	}

	// The core loop must not have adopted the leftover container: with it
	// already gone and generation 1 now failed (no longer live), the next
	// step is a fresh mint (generation 2), never an ActionStart on a
	// container the loop should have treated as an orphan.
	beginAgain, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("begin-again pass: %v", err)
	}
	if len(beginAgain.Actions) != 1 || beginAgain.Actions[0].Kind != ActionBeginFirstGeneration {
		t.Fatalf("begin-again pass actions = %+v, want a fresh %q", beginAgain.Actions, ActionBeginFirstGeneration)
	}
	var numbers []int64
	for _, gen := range liveGenerations(t, store, "inst-1") {
		numbers = append(numbers, gen.Generation)
	}
	if len(numbers) != 2 || numbers[0] != 2 {
		t.Fatalf("generations = %v, want (1 failed, 2 pending)", numbers)
	}
}

// Security review (#955 finding 2): desired_state=stopped was previously
// ignored once a generation went live -- the GenerationLive short-circuit
// routes the core loop away from planForExisting entirely, and rotation
// never looked at DesiredStopped at all. Stopping now stops the container and
// rejects its token; re-enabling boots a fresh generation, never the retired
// one.
func TestReconcileRotations_DesiredStoppedStopsAndRetiresThenReEnablingBootsAFreshGeneration(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	genID := seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")

	setDesiredState(t, store, "inst-1", DesiredStopped)

	stopPass, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("stop pass: %v", err)
	}
	if len(stopPass.Actions) != 1 || stopPass.Actions[0].Kind != RotationStopInstance {
		t.Fatalf("stop pass actions = %+v, want a single %q", stopPass.Actions, RotationStopInstance)
	}
	live, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(live) != 1 || live[0].State != container.ContainerStateExited {
		t.Fatalf("container after the stop pass = %+v, want exited", live)
	}
	// Revoked and terminalized in this SAME pass (security review round 3
	// finding 1) -- not held valid until a separate "retire" pass the way a
	// single-generation stop used to work.
	if got := genByID(t, store, genID); got.Status != GenStopped {
		t.Errorf("generation status = %q, want stopped", got.Status)
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err == nil {
		t.Error("the stopped generation's token still authenticates")
	}

	// The next pass sweeps the now-terminal generation's leftover (but
	// already-stopped) container; the sweep is not itself a planned rotation
	// action, so the pass converges.
	sweepPass, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("sweep pass: %v", err)
	}
	if !sweepPass.Converged {
		t.Fatalf("sweep pass actions = %+v, want convergence", sweepPass.Actions)
	}
	gone, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("the stopped generation's container survived: %+v", gone)
	}

	settled, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("settled pass: %v", err)
	}
	if !settled.Converged {
		t.Fatalf("settled pass actions = %+v, want convergence while stopped", settled.Actions)
	}

	// Re-enabling: the core loop finds no live generation and no container,
	// so it mints a fresh one -- generation 2, never a resurrected 1.
	// (seedInstance never went through the reconciler's own network-creation
	// step, so the first pass here is that, exactly as first boot's would be.)
	setDesiredState(t, store, "inst-1", DesiredRunning)

	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("network pass: %v", err)
	}
	beginAgain, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("begin-again pass: %v", err)
	}
	if len(beginAgain.Actions) != 1 || beginAgain.Actions[0].Kind != ActionBeginFirstGeneration {
		t.Fatalf("begin-again pass actions = %+v, want a fresh %q", beginAgain.Actions, ActionBeginFirstGeneration)
	}
	for i, kind := range []RotationKind{RotationCreate, RotationPromote, RotationSwitch} {
		result, err := r.ReconcileRotations(ctx)
		if err != nil {
			t.Fatalf("rotation pass %d (%s): %v", i, kind, err)
		}
		if len(result.Actions) != 1 || result.Actions[0].Kind != kind {
			t.Fatalf("rotation pass %d = %+v, want a single %q", i, result.Actions, kind)
		}
	}
	// liveGenerations (despite the name, inherited from the existing test
	// helper) returns the instance's whole history, DESC by number: the
	// retired generation 1 stays for the audit trail, and generation 2 is
	// the only one actually live.
	gens := liveGenerations(t, store, "inst-1")
	if len(gens) != 2 || gens[0].Generation != 2 || gens[0].Status != GenActive {
		t.Fatalf("generations = %+v, want generation 2 active (plus retired generation 1)", gens)
	}
	if gens[1].Generation != 1 || gens[1].Status != GenStopped {
		t.Fatalf("generation 1 = %+v, want it retired as stopped", gens[1])
	}
}

// Security review (#955 finding 2): an active generation's container that
// exited or died on its own -- not superseded by anything -- was never
// restarted, because generationDrift only compares image digest and config
// hash.
func TestReconcileRotations_DeadActiveContainerIsRestarted(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")

	live, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("seeded containers = %d, want 1", len(live))
	}
	if err := rt.Stop(ctx, live[0].ID, time.Second); err != nil {
		t.Fatalf("simulate a crash: %v", err)
	}

	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("restart pass: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != RotationRestartActive {
		t.Fatalf("restart pass actions = %+v, want a single %q", result.Actions, RotationRestartActive)
	}

	recovered, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(recovered) != 1 || recovered[0].State != container.ContainerStateRunning {
		t.Fatalf("container after the restart pass = %+v, want running", recovered)
	}

	settled, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("settled pass: %v", err)
	}
	if !settled.Converged {
		t.Fatalf("settled pass actions = %+v, want convergence once recovered", settled.Actions)
	}
}

// Crash-loop bound (#955 security review item 5): a container that keeps
// dying past maxRestartAttempts is not restarted forever -- rotation gives up
// and mints a fresh generation, which goes through the same create/health-gate
// sequence a genuine upgrade uses.
func TestReconcileRotations_CrashLoopPastTheCapMintsAFreshGeneration(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")

	live, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	containerID := live[0].ID

	for i := 0; i < maxRestartAttempts; i++ {
		if err := rt.Stop(ctx, containerID, time.Second); err != nil {
			t.Fatalf("attempt %d: stop: %v", i, err)
		}
		result, err := r.ReconcileRotations(ctx)
		if err != nil {
			t.Fatalf("attempt %d: ReconcileRotations: %v", i, err)
		}
		if len(result.Actions) != 1 || result.Actions[0].Kind != RotationRestartActive {
			t.Fatalf("attempt %d: actions = %+v, want a single %q", i, result.Actions, RotationRestartActive)
		}
		// The restart backoff would otherwise block the next attempt within
		// this same test's real-time window.
		r.clearRestartState(genByGeneration(t, store, "inst-1", 1).ID)
		r.restartMu.Lock()
		r.restarts[genByGeneration(t, store, "inst-1", 1).ID] = &restartState{attempts: i + 1}
		r.restartMu.Unlock()
	}

	// One more death: the cap is reached, so rotation mints a fresh
	// generation instead of restarting the same container again.
	if err := rt.Stop(ctx, containerID, time.Second); err != nil {
		t.Fatalf("final stop: %v", err)
	}
	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("crash-loop pass: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != RotationBegin {
		t.Fatalf("crash-loop pass actions = %+v, want a single %q", result.Actions, RotationBegin)
	}

	// liveGenerations returns the whole history DESC by number: generation 2
	// (pending, the fresh mint) then generation 1 (still active -- a failed
	// restart never touches it, same invariant as an aborted rotation).
	var numbers []int64
	for _, gen := range liveGenerations(t, store, "inst-1") {
		numbers = append(numbers, gen.Generation)
	}
	if len(numbers) != 2 || numbers[0] != 2 {
		t.Fatalf("generations = %v, want (2 pending, 1 active)", numbers)
	}
}

// Security review round 2, item 5: clearing the crash-loop counter merely
// because one pass observed the container running would let a container that
// dies shortly after every restart reset its own counter every single pass,
// and the cap it exists to enforce would never be reached. This drives the
// same restart loop as TestReconcileRotations_CrashLoopPastTheCapMintsAFreshGeneration,
// but interleaves a "briefly recovered" pass after each restart -- the
// container is observed running, just not for the minimum recovery uptime --
// and proves the cap is still reached afterward.
func TestReconcileRotations_CrashLoopCounterSurvivesABriefRecovery(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")

	live, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	containerID := live[0].ID

	for i := 0; i < maxRestartAttempts; i++ {
		if err := rt.Stop(ctx, containerID, time.Second); err != nil {
			t.Fatalf("attempt %d: stop: %v", i, err)
		}
		result, err := r.ReconcileRotations(ctx)
		if err != nil {
			t.Fatalf("attempt %d: ReconcileRotations: %v", i, err)
		}
		if len(result.Actions) != 1 || result.Actions[0].Kind != RotationRestartActive {
			t.Fatalf("attempt %d: actions = %+v, want a single %q", i, result.Actions, RotationRestartActive)
		}

		// The container is running again (Start just succeeded). A pass that
		// merely observes it running -- without it staying up for
		// minRestartRecoveryUptime -- must not clear the crash-loop counter:
		// only actually surviving defeats the cap.
		recoveryPass, err := r.ReconcileRotations(ctx)
		if err != nil {
			t.Fatalf("attempt %d: recovery-observation pass: %v", i, err)
		}
		if !recoveryPass.Converged {
			t.Fatalf("attempt %d: recovery-observation pass = %+v, want convergence (container is running)", i, recoveryPass.Actions)
		}

		// Bypass the real backoff wait between attempts (tests cannot sleep
		// seconds/minutes) without touching the attempt count itself -- that
		// count, surviving the recovery-observation pass above, is exactly
		// what this test proves.
		genID := genByGeneration(t, store, "inst-1", 1).ID
		r.restartMu.Lock()
		if st, ok := r.restarts[genID]; ok {
			st.nextAttempt = time.Time{}
		}
		r.restartMu.Unlock()
	}

	// One more death: if any of the recovery-observation passes above had
	// cleared the counter, this would restart yet again instead of reaching
	// the cap.
	if err := rt.Stop(ctx, containerID, time.Second); err != nil {
		t.Fatalf("final stop: %v", err)
	}
	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("crash-loop pass: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != RotationBegin {
		t.Fatalf("crash-loop pass actions = %+v, want a single %q (the counter must have survived every brief recovery)", result.Actions, RotationBegin)
	}
}

// genByGeneration finds a live generation row by its number.
func genByGeneration(t *testing.T, store *db.Store, instanceID string, number int64) db.PluginContainerGeneration {
	t.Helper()
	for _, gen := range liveGenerations(t, store, instanceID) {
		if gen.Generation == number {
			return gen
		}
	}
	t.Fatalf("no live generation %d for instance %q", number, instanceID)
	return db.PluginContainerGeneration{}
}

// Security review (#955 finding 4): a live generation whose instance has no
// desired-state row at all was previously never visited by
// ReconcileRotations -- the per-row loop only iterates rows IN the desired
// set -- so its token kept authenticating indefinitely. Re-adding the row
// must boot a fresh generation, never resurrect the orphaned one.
func TestReconcileRotations_OrphanedInstanceRevokesTokenAndRetires(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	genID := seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")

	deleteDesiredContainer(t, store, "inst-1")

	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("orphan sweep: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != RotationRetireOrphanedInstance {
		t.Fatalf("orphan sweep actions = %+v, want a single %q", result.Actions, RotationRetireOrphanedInstance)
	}
	if got := genByID(t, store, genID); got.Status != GenStopped {
		t.Errorf("orphaned generation status = %q, want stopped", got.Status)
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err == nil {
		t.Error("the orphaned instance's token still authenticates")
	}
	gone, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("the orphaned instance's container survived: %+v", gone)
	}

	settled, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("settled pass: %v", err)
	}
	if !settled.Converged {
		t.Fatalf("settled pass actions = %+v, want convergence once the orphan is swept", settled.Actions)
	}

	// Re-adding the SAME instance's desired row finds nothing live for it and
	// boots generation 2, never a resurrected generation 1.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.Queries().CreatePluginContainer(ctx, db.CreatePluginContainerParams{
		ID: model.NewULID(), PluginInstanceID: "inst-1",
		ImageRef: "ghcr.io/acme/p:1", ImageDigest: "sha256:aaa", ConfigHash: "cfg-1",
		NetworkName: "gleipnir-plugin-inst-1", DesiredState: DesiredRunning,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("re-add desired container: %v", err)
	}

	// inst-1 never went through the reconciler's own network-creation step
	// (seedInstance creates its container directly), so the first pass here
	// is that, exactly as first boot's would be.
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("network pass: %v", err)
	}
	beginAgain, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("begin-again pass: %v", err)
	}
	var beginAction *Action
	for i := range beginAgain.Actions {
		if beginAgain.Actions[i].InstanceID == "inst-1" {
			beginAction = &beginAgain.Actions[i]
		}
	}
	if beginAction == nil || beginAction.Kind != ActionBeginFirstGeneration {
		t.Fatalf("begin-again pass actions = %+v, want a fresh %q for inst-1", beginAgain.Actions, ActionBeginFirstGeneration)
	}

	// liveGenerations returns the whole history DESC by number: the fresh
	// pending generation 2, plus the orphaned generation 1 retired earlier
	// (kept as stopped for the audit trail, never reused).
	var numbers []int64
	for _, gen := range liveGenerations(t, store, "inst-1") {
		numbers = append(numbers, gen.Generation)
	}
	if len(numbers) != 2 || numbers[0] != 2 {
		t.Fatalf("live generations = %v, want a fresh generation 2 (plus retired generation 1)", numbers)
	}
}

// Security review (#955 finding 3): a first-boot generation's container gets
// the egress proxy AND the host endpoint URL, with the host endpoint's own
// host:port scoped into NO_PROXY so its bearer-token-carrying calls never go
// through the same proxy that mediates the instance's egress.
func TestReconcileOnce_FirstBootGenerationEnvScopesNoProxyToTheHostEndpoint(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixtureWithProxyEnv(t)
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")

	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("network pass: %v", err)
	}
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("begin pass: %v", err)
	}
	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("create pass: %v", err)
	}

	assertGenerationEnvScopesHostEndpoint(t, rt.createEnv())
}

// Security review (#955 finding 3): the same is true of a normal rotation's
// generation, proving withGenerationEnv's NO_PROXY scoping applies uniformly
// through createRotationContainer's one create path -- first boot is not a
// special case of it.
func TestReconcileRotations_RotationGenerationEnvScopesNoProxyToTheHostEndpoint(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixtureWithProxyEnv(t)
	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")
	setDesiredDigest(t, store, "inst-1", "sha256:bbb")

	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("begin pass: %v", err)
	}
	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("create pass: %v", err)
	}

	assertGenerationEnvScopesHostEndpoint(t, rt.createEnv())
}

// --- stop acts on every live generation in one pass (#955 security re-review round 3 finding 1) --

// assertGenerationStoppedAndRevoked is the shared assertion for the two
// multi-generation stop tests below: a generation's row is terminal, its
// token no longer authenticates, and its container is stopped -- all after
// the FIRST stop pass, never a later one.
func assertGenerationStoppedAndRevoked(t *testing.T, store *db.Store, rt *countingRuntime, name, genID, generation, token, wantStatus string) {
	t.Helper()
	ctx := context.Background()

	got := genByID(t, store, genID)
	if got.Status != wantStatus {
		t.Errorf("%s status = %q, want %q", name, got.Status, wantStatus)
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken(token)); err == nil {
		t.Errorf("%s token still authenticates", name)
	}
	live, err := rt.ListByLabel(ctx, LabelGeneration, generation)
	if err != nil {
		t.Fatalf("ListByLabel(%s): %v", generation, err)
	}
	if len(live) != 1 || live[0].State != container.ContainerStateExited {
		t.Errorf("%s container = %+v, want a single exited one", name, live)
	}
}

// A serving (active) generation must not wait behind a superseded
// (draining) one, or vice versa: both are revoked, marked terminal, and
// have their containers stopped in the SAME first stop pass.
func TestReconcileRotations_StopActsOnDrainingAndActiveInTheFirstPass(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	gen1ID := seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1") // active, generation 1

	// Supersede gen 1 by hand: gen 2 active, gen 1 draining -- both still
	// running, exactly as they briefly are right after a real RotationSwitch.
	if _, err := store.Queries().UpdateContainerGenerationStatus(ctx, db.UpdateContainerGenerationStatusParams{
		Status: GenDraining, StatusDetail: strPtr("superseded"),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ID:        gen1ID, ExpectedStatus: GenActive,
	}); err != nil {
		t.Fatalf("demote gen1 to draining: %v", err)
	}
	gen2ID := seedGenerationContainer(t, store, rt, "inst-1", 2, GenActive, "sha256:bbb", "cfg-1", "gen2-token")

	setDesiredState(t, store, "inst-1", DesiredStopped)

	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("stop pass: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != RotationStopInstance {
		t.Fatalf("stop pass actions = %+v, want a single %q", result.Actions, RotationStopInstance)
	}

	assertGenerationStoppedAndRevoked(t, store, rt, "gen1 (draining)", gen1ID, "1", "gen1-token", GenStopped)
	assertGenerationStoppedAndRevoked(t, store, rt, "gen2 (active)", gen2ID, "2", "gen2-token", GenStopped)
}

// A generation still coming up (starting) must not wait behind a superseded
// (draining) one either.
func TestReconcileRotations_StopActsOnDrainingAndStartingInTheFirstPass(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	gen1ID := seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1") // active, generation 1

	if _, err := store.Queries().UpdateContainerGenerationStatus(ctx, db.UpdateContainerGenerationStatusParams{
		Status: GenDraining, StatusDetail: strPtr("superseded"),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ID:        gen1ID, ExpectedStatus: GenActive,
	}); err != nil {
		t.Fatalf("demote gen1 to draining: %v", err)
	}
	gen2ID := seedGenerationContainer(t, store, rt, "inst-1", 2, GenStarting, "sha256:bbb", "cfg-1", "gen2-token")

	setDesiredState(t, store, "inst-1", DesiredStopped)

	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("stop pass: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != RotationStopInstance {
		t.Fatalf("stop pass actions = %+v, want a single %q", result.Actions, RotationStopInstance)
	}

	assertGenerationStoppedAndRevoked(t, store, rt, "gen1 (draining)", gen1ID, "1", "gen1-token", GenStopped)
	// A starting candidate never took over serving, so it reads as failed --
	// same as a health-gate abort -- not stopped.
	assertGenerationStoppedAndRevoked(t, store, rt, "gen2 (starting)", gen2ID, "2", "gen2-token", GenFailed)
}

// --- sweep leftover generation containers (#955 security re-review round 2 item 4) --

// A container whose generation is no longer live, but whose INSTANCE still
// has a different live generation, is invisible to the core loop's own
// orphan check: that check is gated on the instance having no live
// generation at all (GenerationLive routes it away before it ever looks at
// individual containers), and an instance mid-rotation almost always still
// has one. Only ReconcileRotations's own sweep -- now run every pass -- finds
// and retries a leftover like this.
func TestReconcileRotations_SweepRemovesOrphanedGenerationContainerLeftByAFailedAbort(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1") // gen1 active
	setDesiredDigest(t, store, "inst-1", "sha256:bbb")

	// Begin, then create gen2 (starting).
	for i := 0; i < 2; i++ {
		if _, err := r.ReconcileRotations(ctx); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}

	// gen2 fails its health gate (its container exited) -- but its
	// container's removal fails during the abort, leaving it behind even
	// though gen2's row is already terminal.
	markUnhealthy(t, rt, "inst-1", 2)
	rt.Runtime.(*container.Fake).RemoveErr = errors.New("remove refused")
	abortResult, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("abort pass: %v", err)
	}
	if len(abortResult.Actions) != 1 || abortResult.Actions[0].Kind != RotationAbort {
		t.Fatalf("abort pass actions = %+v, want a single %q", abortResult.Actions, RotationAbort)
	}
	if abortResult.Errors != 1 {
		t.Fatalf("Errors = %d, want 1 (the refused removal)", abortResult.Errors)
	}

	leftover, err := rt.ListByLabel(ctx, LabelGeneration, "2")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(leftover) != 1 {
		t.Fatalf("gen2's container was not left behind: %+v", leftover)
	}

	// gen1 is STILL active -- a live generation for this instance -- which is
	// exactly what would make the core loop's per-instance orphan check skip
	// straight past gen2's leftover container.
	if got := genByGeneration(t, store, "inst-1", 1); got.Status != GenActive {
		t.Fatalf("gen1 status = %q, want still active", got.Status)
	}

	// The socket recovers; the next rotation pass sweeps the leftover even
	// though nothing about gen1 changed.
	rt.Runtime.(*container.Fake).RemoveErr = nil
	sweepResult, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("sweep pass: %v", err)
	}
	if sweepResult.Errors != 0 {
		t.Fatalf("sweep pass Errors = %d, want 0", sweepResult.Errors)
	}

	gone, err := rt.ListByLabel(ctx, LabelGeneration, "2")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("gen2's leftover container survived the sweep: %+v", gone)
	}
}

// --- revoke before the socket work (#955 security re-review round 2 item 3) --

// A Remove failure during an operator-requested stop must never leave a live,
// still-authenticating token behind: the token is revoked and the row marked
// terminal BEFORE the removal is attempted, matching failLostTokenGeneration's
// order, so a socket refusal costs a retry on the next pass, not a live
// credential nobody meant to keep valid.
func TestReconcileRotations_RetireStoppedRevokesBeforeRemove(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	genID := seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")
	setDesiredState(t, store, "inst-1", DesiredStopped)

	if _, err := r.ReconcileRotations(ctx); err != nil {
		t.Fatalf("stop pass: %v", err)
	}

	rt.Runtime.(*container.Fake).RemoveErr = errors.New("remove refused")
	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("retire pass: %v", err)
	}
	if result.Errors != 1 {
		t.Fatalf("Errors = %d, want 1 (the refused removal)", result.Errors)
	}

	if got := genByID(t, store, genID); got.Status != GenStopped {
		t.Errorf("status = %q, want stopped even though Remove failed", got.Status)
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err == nil {
		t.Error("the token still authenticates despite the generation being marked stopped")
	}
}

// The same ordering for the orphaned-instance teardown sweep.
func TestReconcileRotations_OrphanedInstanceRevokesBeforeRemove(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	genID := seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")
	deleteDesiredContainer(t, store, "inst-1")

	rt.Runtime.(*container.Fake).RemoveErr = errors.New("remove refused")
	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("orphan sweep: %v", err)
	}
	if result.Errors != 1 {
		t.Fatalf("Errors = %d, want 1 (the refused removal)", result.Errors)
	}

	if got := genByID(t, store, genID); got.Status != GenStopped {
		t.Errorf("status = %q, want stopped even though Remove failed", got.Status)
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err == nil {
		t.Error("the orphaned instance's token still authenticates despite Remove failing")
	}
}

// runPass runs ReconcileRotations even when ReconcileOnce fails, and reports
// both via errors.Join (#955 security re-review round 3 finding 2): an
// operator-requested stop lives entirely on the rotation side, and a
// transient failure reading the core loop's OWN state (its network list, in
// this test -- ReconcileRotations never touches networks) must not withhold
// it.
func TestReconciler_RunPassRunsRotationsEvenWhenReconcileOnceErrors(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	gen1ID := seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")
	setDesiredState(t, store, "inst-1", DesiredStopped)

	listErr := errors.New("list networks failed")
	rt.ListNetworksErr = listErr

	err := r.runPass(ctx)
	if err == nil {
		t.Fatal("runPass: want a non-nil error (the injected ReconcileOnce failure)")
	}
	if !errors.Is(err, listErr) {
		t.Errorf("runPass error = %v, want it to wrap the injected ReconcileOnce failure", err)
	}

	// Despite ReconcileOnce failing, ReconcileRotations still ran and
	// converged the stop.
	assertGenerationStoppedAndRevoked(t, store, rt, "gen1", gen1ID, "1", "gen1-token", GenStopped)
}

// --- rotationMu serializes every rotation pass (#955 security re-review round 3 item 4) --

// concurrencyDetectingStore wraps a Store and records the highest number of
// concurrent ListPluginContainers calls it ever saw, with an artificial delay
// to widen the window a race would need. sweepOrphanedGenerationContainers
// treats "list the world, then act on it" as one logical step; this proves
// two rotation passes (runPass's own, and a direct ReconcileRotations call)
// never overlap enough to read that world from different snapshots.
type concurrencyDetectingStore struct {
	Store
	delay time.Duration

	mu        sync.Mutex
	active    int
	maxActive int
}

func (s *concurrencyDetectingStore) ListPluginContainers(ctx context.Context) ([]db.PluginContainer, error) {
	s.mu.Lock()
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()

	time.Sleep(s.delay)

	s.mu.Lock()
	s.active--
	s.mu.Unlock()

	return s.Store.ListPluginContainers(ctx)
}

func TestReconciler_RotationMuSerializesRunPassAndReconcileRotations(t *testing.T) {
	ctx := context.Background()
	dbStore, err := db.Open(filepath.Join(t.TempDir(), "rotmu.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbStore.Close() })
	if err := dbStore.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedFreshInstance(t, dbStore, "inst-1", "sha256:aaa", "cfg-1")

	cds := &concurrencyDetectingStore{Store: dbStore.Queries(), delay: 10 * time.Millisecond}
	r, err := New(Config{
		Runtime: &countingRuntime{Runtime: container.NewFake()}, Store: cds,
		Rotations: dbStore.Queries(), Subnets: testAllocator(t),
		Posture: container.PostureRootlessPodman, Interval: time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// runPass (the run loop's own combined cycle) and a direct
	// ReconcileRotations call, racing each other repeatedly. Without
	// rotationMu, `go test -race` would also have a data race to report on
	// r.tokens/r.restarts; this test's own assertion is the concurrency
	// count, which does not depend on -race to be meaningful.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = r.runPass(ctx)
		}()
		go func() {
			defer wg.Done()
			_, _ = r.ReconcileRotations(ctx)
		}()
	}
	wg.Wait()

	cds.mu.Lock()
	max := cds.maxActive
	cds.mu.Unlock()
	if max > 1 {
		t.Errorf("max concurrent ListPluginContainers calls = %d, want 1 (rotationMu should serialize runPass and ReconcileRotations)", max)
	}
}

// --- the run loop drives rotation (#955 security re-review round 2 item 1) --

// Before this, the run loop never called ReconcileRotations at all -- only
// tests did, by calling it directly. These three tests drive first boot, a
// desired-stop, and a teardown entirely through Start/Kick, never calling
// ReconcileRotations themselves, and synchronize on the published
// EventRotationPassCompleted signal rather than sleeping.

func TestReconciler_FirstBootConvergesThroughStartAndKickAlone(t *testing.T) {
	ctx := context.Background()
	store, rt, r, pub := rotFixtureWithPublisher(t)
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)
	pub.waitForRotationPasses(t, 1) // the boot pass

	deadline := time.Now().Add(10 * time.Second)
	for {
		if gen, ok := activeGeneration(t, store, "inst-1"); ok && gen.Generation == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first boot never reached active through Start/Kick alone")
		}
		kickAndWaitForRotationPass(t, r, pub)
	}

	live, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(live) != 1 || live[0].State != container.ContainerStateRunning {
		t.Fatalf("container = %+v, want a single one running", live)
	}
}

func TestReconciler_DesiredStopConvergesThroughStartAndKickAlone(t *testing.T) {
	ctx := context.Background()
	store, rt, r, pub := rotFixtureWithPublisher(t)
	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)
	pub.waitForRotationPasses(t, 1) // the boot pass: already converged

	setDesiredState(t, store, "inst-1", DesiredStopped)

	deadline := time.Now().Add(10 * time.Second)
	for {
		live, err := rt.ListByLabel(ctx, LabelGeneration, "1")
		if err != nil {
			t.Fatalf("ListByLabel: %v", err)
		}
		if len(live) == 0 {
			break // stopped and retired
		}
		if time.Now().After(deadline) {
			t.Fatalf("desired stop never converged through Start/Kick alone; container = %+v", live)
		}
		kickAndWaitForRotationPass(t, r, pub)
	}

	gens := liveGenerations(t, store, "inst-1")
	if len(gens) != 1 || gens[0].Status != GenStopped {
		t.Fatalf("generations = %+v, want a single stopped one", gens)
	}
	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err == nil {
		t.Error("the stopped instance's token still authenticates")
	}
}

func TestReconciler_TeardownConvergesThroughStartAndKickAlone(t *testing.T) {
	ctx := context.Background()
	store, rt, r, pub := rotFixtureWithPublisher(t)
	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)
	pub.waitForRotationPasses(t, 1) // the boot pass: already converged

	deleteDesiredContainer(t, store, "inst-1")

	deadline := time.Now().Add(10 * time.Second)
	for {
		gens := liveGenerations(t, store, "inst-1")
		if len(gens) == 1 && gens[0].TokenRevokedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("teardown never converged through Start/Kick alone; generations = %+v", gens)
		}
		kickAndWaitForRotationPass(t, r, pub)
	}

	if _, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken("gen1-token")); err == nil {
		t.Error("the torn-down instance's token still authenticates")
	}
	live, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("torn-down instance's container survived: %+v", live)
	}
}

// --- first boot ---------------------------------------------------------

// A genuinely fresh instance mints generation 1 with a token, and rotation's
// existing create/health-gate/switch sequence carries it the rest of the way
// to active — first boot adds nothing beyond the mint, exactly as the package
// doc for rotation.go describes.
func TestReconcileOnce_FirstBootMintsGenerationOneAndReachesActive(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")

	// Network first — a container cannot attach to one that does not exist.
	netPass, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("network pass: %v", err)
	}
	if len(netPass.Actions) != 1 || netPass.Actions[0].Kind != ActionCreateNetwork {
		t.Fatalf("network pass actions = %+v, want a single create_network", netPass.Actions)
	}

	beginPass, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("begin pass: %v", err)
	}
	if len(beginPass.Actions) != 1 || beginPass.Actions[0].Kind != ActionBeginFirstGeneration {
		t.Fatalf("begin pass actions = %+v, want a single %q", beginPass.Actions, ActionBeginFirstGeneration)
	}

	// From here the core loop has nothing left to do for this instance —
	// ReconcileRotations owns create, the health gate, and the switch.
	settledPass, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("settled core-loop pass: %v", err)
	}
	if !settledPass.Converged {
		t.Fatalf("core loop actions = %+v, want it to defer entirely to rotation", settledPass.Actions)
	}

	for i, kind := range []RotationKind{RotationCreate, RotationPromote, RotationSwitch} {
		result, err := r.ReconcileRotations(ctx)
		if err != nil {
			t.Fatalf("rotation pass %d (%s): %v", i, kind, err)
		}
		if len(result.Actions) != 1 || result.Actions[0].Kind != kind {
			t.Fatalf("rotation pass %d = %+v, want a single %q", i, result.Actions, kind)
		}
	}

	gens := liveGenerations(t, store, "inst-1")
	if len(gens) != 1 {
		t.Fatalf("generations = %d, want exactly 1", len(gens))
	}
	if gens[0].Generation != 1 || gens[0].Status != GenActive {
		t.Fatalf("generation = %d/%q, want 1/active", gens[0].Generation, gens[0].Status)
	}
	if gens[0].TokenHash == "" {
		t.Error("the first-boot generation has no token hash")
	}

	token := instanceTokenFrom(rt.createEnv())
	if token == "" {
		t.Fatal("the first-boot container was created with no GLEIPNIR_INSTANCE_TOKEN")
	}
	if HashInstanceToken(token) != gens[0].TokenHash {
		t.Error("the token handed to the container does not hash to the stored token_hash")
	}
	if got, err := store.Queries().GetContainerGenerationByTokenHash(ctx, HashInstanceToken(token)); err != nil || got.PluginInstanceID != "inst-1" {
		t.Errorf("GetContainerGenerationByTokenHash(firstBootToken) = (%+v, %v), want inst-1's generation", got, err)
	}

	// The container carries the same generation-owned naming and labels a
	// later rotation's container would, so gc.go and ReconcileRotations
	// recognize it identically.
	live, err := rt.ListByLabel(ctx, LabelGeneration, "1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(live) != 1 || live[0].Name != generationContainerName("inst-1", 1) {
		t.Fatalf("live containers = %+v, want one named %q", live, generationContainerName("inst-1", 1))
	}

	final, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("final core-loop pass: %v", err)
	}
	if !final.Converged {
		t.Errorf("core loop actions = %+v, want convergence once active", final.Actions)
	}
	finalRotation, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("final rotation pass: %v", err)
	}
	if !finalRotation.Converged {
		t.Errorf("rotation actions = %+v, want convergence once active", finalRotation.Actions)
	}
}

// instanceTokenFrom extracts GLEIPNIR_INSTANCE_TOKEN from a container's Env,
// mirroring how plugin-sdk/hostclient reads it in the real container.
func instanceTokenFrom(env []string) string {
	const prefix = instanceTokenEnvVar + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

// A restart between minting generation 1 and creating its container must not
// mint a second LIVE generation: the core loop sees the pending row already
// there and defers, exactly as it would if the process had never restarted.
func TestReconcileOnce_FirstBootIsIdempotentAcrossARestartBeforeCreate(t *testing.T) {
	ctx := context.Background()
	store, rt, first := rotFixture(t)
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")

	if _, err := first.ReconcileOnce(ctx); err != nil {
		t.Fatalf("network pass: %v", err)
	}
	if _, err := first.ReconcileOnce(ctx); err != nil {
		t.Fatalf("begin pass: %v", err)
	}

	// The process dies here, before generation 1's container is ever created.
	// A new Reconciler shares nothing with the old one — no stashed token, no
	// in-memory state at all.
	second, err := New(Config{
		Runtime: rt, Store: store.Queries(), Rotations: store.Queries(),
		Subnets: testAllocator(t), Posture: container.PostureRootlessPodman, Interval: time.Hour,
	})
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}

	rebootPass, err := second.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("reboot pass: %v", err)
	}
	if !rebootPass.Converged {
		t.Fatalf("reboot pass actions = %+v, want the core loop to defer to rotation rather than mint again", rebootPass.Actions)
	}
	if gens := liveGenerations(t, store, "inst-1"); len(gens) != 1 {
		t.Fatalf("generations after reboot = %d, want still exactly 1 — no second live generation was minted", len(gens))
	}

	// Rotation picks up where the crashed process left off. Its token is
	// gone, so this generation fails and is superseded by generation 2 —
	// never generation 1 again.
	if _, err := second.ReconcileRotations(ctx); err != nil {
		t.Fatalf("create pass after reboot: %v", err)
	}
	var gen1 db.PluginContainerGeneration
	for _, g := range liveGenerations(t, store, "inst-1") {
		if g.Generation == 1 {
			gen1 = g
		}
	}
	if gen1.Status != GenFailed {
		t.Fatalf("generation 1 status = %q, want failed — its token was lost in the restart", gen1.Status)
	}

	beginAgain, err := second.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("begin-again pass: %v", err)
	}
	if len(beginAgain.Actions) != 1 || beginAgain.Actions[0].Kind != ActionBeginFirstGeneration {
		t.Fatalf("begin-again pass actions = %+v, want a fresh %q", beginAgain.Actions, ActionBeginFirstGeneration)
	}

	all := liveGenerations(t, store, "inst-1")
	if len(all) != 2 {
		t.Fatalf("generations = %d, want 2 (1 failed, 2 pending)", len(all))
	}
	var numbers []int64
	for _, g := range all {
		numbers = append(numbers, g.Generation)
	}
	if numbers[0] != 2 {
		t.Errorf("the retry took generation %v, want 2 — generation 1 is never reused", numbers)
	}
}

// A first-boot-minted active generation's token is exactly as immune to GC's
// retention sweep as any other active generation's — the WHERE clause is
// keyed on revocation, not on who minted the row.
func TestReconcileGC_FirstBootTokenSurvivesTheRetentionWindow(t *testing.T) {
	ctx := context.Background()
	store, _, r := rotFixture(t)
	r.gc = store.Queries()
	seedFreshInstance(t, store, "inst-1", "sha256:aaa", "cfg-1")

	for i := 0; i < 2; i++ {
		if _, err := r.ReconcileOnce(ctx); err != nil {
			t.Fatalf("core pass %d: %v", i, err)
		}
	}
	for i, kind := range []RotationKind{RotationCreate, RotationPromote, RotationSwitch} {
		if _, err := r.ReconcileRotations(ctx); err != nil {
			t.Fatalf("rotation pass %d (%s): %v", i, kind, err)
		}
	}

	gens := liveGenerations(t, store, "inst-1")
	if len(gens) != 1 || gens[0].Status != GenActive {
		t.Fatalf("generations = %+v, want a single active one", gens)
	}
	tokenHash := gens[0].TokenHash

	r.gcNow = func() time.Time { return rotationTimeNow().Add(30 * 24 * time.Hour) }
	result, err := r.ReconcileGC(ctx)
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if result.TokensPurged != 0 {
		t.Errorf("TokensPurged = %d, want 0 — an active generation is never revoked, so its token is never eligible", result.TokensPurged)
	}

	if got := genByID(t, store, gens[0].ID); got.TokenHash != tokenHash {
		t.Error("the active first-boot generation's token hash was purged")
	}
}

// Manual posture never rotates: the operator owns those containers, and
// replacing one would replace something Gleipnir did not create.
func TestReconcileRotations_ManualPostureIsInert(t *testing.T) {
	ctx := context.Background()
	store, rt, r := rotFixture(t)
	r.posture = container.PostureManual

	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")
	setDesiredDigest(t, store, "inst-1", "sha256:bbb")

	before := rt.creates
	result, err := r.ReconcileRotations(ctx)
	if err != nil {
		t.Fatalf("ReconcileRotations: %v", err)
	}
	if !result.Converged || len(result.Actions) != 0 {
		t.Errorf("manual posture planned %+v, want nothing", result.Actions)
	}
	if rt.creates != before {
		t.Error("manual posture reached the socket")
	}
}

func TestReconcileRotations_RequiresARotationStore(t *testing.T) {
	store, _, r := rotFixture(t)
	_ = store
	r.rotations = nil

	if _, err := r.ReconcileRotations(context.Background()); err == nil {
		t.Fatal("ReconcileRotations ran without a rotation store")
	}
}

func TestMintInstanceToken(t *testing.T) {
	token, hash, err := mintInstanceToken()
	if err != nil {
		t.Fatalf("mintInstanceToken: %v", err)
	}
	if token == "" || hash == "" {
		t.Fatal("mintInstanceToken returned an empty token or hash")
	}
	if hash == token {
		t.Fatal("the stored hash equals the raw token; a database leak would hand out working tokens")
	}
	if got := HashInstanceToken(token); got != hash {
		t.Errorf("HashInstanceToken disagrees with minting: %q vs %q", got, hash)
	}

	other, _, err := mintInstanceToken()
	if err != nil {
		t.Fatalf("mintInstanceToken: %v", err)
	}
	if other == token {
		t.Error("two mints produced the same token")
	}
}

func TestParseGenerationLabel(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{in: "1", want: 1, ok: true},
		{in: "42", want: 42, ok: true},
		{in: ""},
		{in: "abc"},
		{in: "1x"},
		{in: "-1"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseGenerationLabel(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("parseGenerationLabel(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// markUnhealthy drives a generation's container into a state the gate fails on.
// The fake has no healthcheck simulation, so stopping the container is how a
// test says "this generation did not come up".
func markUnhealthy(t *testing.T, rt *countingRuntime, instanceID string, generation int64) {
	t.Helper()
	ctx := context.Background()
	found, err := rt.ListByLabel(ctx, LabelGeneration, itoa64(generation))
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	for _, info := range found {
		if info.Labels[LabelInstance] != instanceID {
			continue
		}
		if err := rt.Stop(ctx, info.ID, time.Second); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}
}

// unused keeps the sql import honest if a future case needs ErrNoRows directly.
var _ = sql.ErrNoRows
