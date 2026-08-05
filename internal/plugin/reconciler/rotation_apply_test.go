package reconciler

import (
	"context"
	"database/sql"
	"path/filepath"
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

// setDesiredDigest points the desired-state row at a new image.
func setDesiredDigest(t *testing.T, store *db.Store, instanceID, digest string) {
	t.Helper()
	if _, err := store.DB().ExecContext(context.Background(),
		`UPDATE plugin_containers SET image_digest = ? WHERE plugin_instance_id = ?`,
		digest, instanceID); err != nil {
		t.Fatalf("update desired digest: %v", err)
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
