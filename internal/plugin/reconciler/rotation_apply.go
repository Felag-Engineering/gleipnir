package reconciler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// RotationStore is the read/write surface rotation needs on the generation
// tables. It is separate from Store because rotation is the one part of this
// loop that DOES write desired-state-adjacent rows: generation records are the
// durable state machine, so advancing them is the work rather than a side
// effect of it.
type RotationStore interface {
	ListLiveContainerGenerations(ctx context.Context) ([]db.PluginContainerGeneration, error)
	GetLatestContainerGeneration(ctx context.Context, instanceID string) (db.PluginContainerGeneration, error)
	CreateContainerGeneration(ctx context.Context, arg db.CreateContainerGenerationParams) (db.PluginContainerGeneration, error)
	UpdateContainerGenerationStatus(ctx context.Context, arg db.UpdateContainerGenerationStatusParams) (int64, error)
	SetContainerGenerationContainerID(ctx context.Context, arg db.SetContainerGenerationContainerIDParams) (int64, error)
	RevokeContainerGenerationToken(ctx context.Context, arg db.RevokeContainerGenerationTokenParams) (int64, error)
}

// instanceTokenBytes is the entropy behind a per-generation instance token.
// 256 bits, matching internal/plugin/identity's v1 tokens — the substrate
// changed, the threat model did not.
const instanceTokenBytes = 32

// mintInstanceToken returns a fresh token and the hex SHA-256 hash that is what
// actually gets stored.
//
// The raw token is returned once, handed to the container it belongs to, and
// never persisted. A stored token is a token a database leak hands to an
// attacker; a stored hash is not, and the authentication path only ever needs
// to compare hashes.
func mintInstanceToken() (token, hash string, err error) {
	buf := make([]byte, instanceTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("mint instance token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

// HashInstanceToken renders the stored form of a token, for the authentication
// lookup. Exported so the host endpoint hashes tokens exactly the way rotation
// stored them — two implementations of "the stored form" is one more than the
// number that can be right.
func HashInstanceToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// rotationTimeNow is the package's injectable clock for rotation deadlines
// (CLAUDE.md "Testing time-dependent code"). Tests swap it via t.Cleanup and
// must not call t.Parallel() while it is swapped.
var rotationTimeNow = func() time.Time { return time.Now() }

// ReconcileRotations runs one rotation pass across every instance with a
// desired-state row: plan one step per instance, apply it, return what was
// done.
//
// It is a separate pass from ReconcileOnce rather than a branch inside it. The
// core loop converges a container toward its row; rotation converges a ROW SET
// toward a desired image. Merging them would mean one planner deciding between
// "start this container" and "supersede this generation", which are answers to
// different questions.
func (r *Reconciler) ReconcileRotations(ctx context.Context) (RotationPassResult, error) {
	var result RotationPassResult

	if r.posture == container.PostureManual {
		// The operator owns these containers; rotating one would replace
		// something Gleipnir did not create.
		result.Converged = true
		return result, nil
	}
	if r.rotations == nil {
		return result, fmt.Errorf("reconciler: rotation store is not configured")
	}

	desired, err := r.store.ListPluginContainers(ctx)
	if err != nil {
		return result, fmt.Errorf("list desired containers: %w", err)
	}
	generations, err := r.rotations.ListLiveContainerGenerations(ctx)
	if err != nil {
		return result, fmt.Errorf("list live generations: %w", err)
	}
	observed, err := r.runtime.ListByLabel(ctx, LabelManaged, ManagedValue)
	if err != nil {
		return result, fmt.Errorf("list managed containers: %w", err)
	}

	byInstance := groupGenerations(generations)
	containers := groupContainersByGeneration(observed)

	for _, row := range desired {
		action := planRotation(RotationInputs{
			Desired:           row,
			Generations:       byInstance[row.PluginInstanceID],
			Observed:          containers[row.PluginInstanceID],
			Now:               rotationTimeNow(),
			HealthGateTimeout: r.healthGateTimeout,
			DrainTimeout:      r.drainTimeout,
		})
		if action.Kind == RotationNone {
			continue
		}
		result.Actions = append(result.Actions, action)
		if err := r.applyRotation(ctx, action, row, byInstance[row.PluginInstanceID]); err != nil {
			result.Errors++
			slog.ErrorContext(ctx, "rotation step failed",
				"instance_id", action.InstanceID, "step", string(action.Kind), "err", err)
		}
	}

	result.Converged = len(result.Actions) == 0
	return result, nil
}

// RotationPassResult summarizes one rotation pass.
type RotationPassResult struct {
	Actions   []RotationAction `json:"-"`
	Errors    int              `json:"errors"`
	Converged bool             `json:"converged"`
}

// applyRotation performs one planned step. Every step is a single durable
// transition plus at most one socket write, in that order where it matters:
// the row that says "a container should exist" is written before the container,
// so a crash in between leaves work to pick up rather than a container nothing
// knows about.
func (r *Reconciler) applyRotation(ctx context.Context, act RotationAction, desired db.PluginContainer, gens []db.PluginContainerGeneration) error {
	now := rotationTimeNow().UTC().Format(time.RFC3339Nano)

	switch act.Kind {
	case RotationBegin:
		return r.beginRotation(ctx, act, desired, now)
	case RotationCreate:
		return r.createRotationContainer(ctx, act, desired, gens, now)
	case RotationPromote:
		return r.advanceGeneration(ctx, act.GenerationID, GenStarting, GenHealthy, act.Reason, now)
	case RotationAbort:
		return r.abortRotation(ctx, act, now)
	case RotationSwitch:
		return r.switchGeneration(ctx, act, gens, now)
	case RotationDrain:
		return r.drainGeneration(ctx, act, gens, now)
	case RotationRetire:
		return r.retireGeneration(ctx, act, now)
	}
	return nil
}

// beginRotation mints generation N+1 as a pending row with a fresh token.
//
// The number comes from the LATEST generation whatever its status, including
// terminal ones: a generation number is never reused, so a failed attempt
// consumes its number permanently. Reusing it would make two different
// containers indistinguishable in an audit trail.
func (r *Reconciler) beginRotation(ctx context.Context, act RotationAction, desired db.PluginContainer, now string) error {
	latest, err := r.rotations.GetLatestContainerGeneration(ctx, act.InstanceID)
	next := int64(1)
	if err == nil {
		next = latest.Generation + 1
	}

	token, hash, err := mintInstanceToken()
	if err != nil {
		return err
	}

	created, err := r.rotations.CreateContainerGeneration(ctx, db.CreateContainerGenerationParams{
		ID:               model.NewULID(),
		PluginInstanceID: act.InstanceID,
		Generation:       next,
		ImageDigest:      desired.ImageDigest,
		ConfigHash:       desired.ConfigHash,
		TokenHash:        hash,
		Status:           GenPending,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		return fmt.Errorf("create generation %d for %s: %w", next, act.InstanceID, err)
	}

	// The raw token exists only here and in the container it is about to be
	// handed to. Held in memory keyed by generation so the create step can
	// inject it; a restart in between loses it, and the next pass mints a new
	// generation rather than starting a container it cannot authenticate.
	r.stashToken(created.ID, token)

	slog.InfoContext(ctx, "rotation begun",
		"instance_id", act.InstanceID, "generation", next, "reason", act.Reason)
	return nil
}

// createRotationContainer creates and starts the new generation's container.
//
// Both in one step, unlike first boot's create-then-start. The health gate has
// to distinguish "still starting" from "started and immediately exited", and a
// created-but-never-started container is indistinguishable from the latter.
func (r *Reconciler) createRotationContainer(ctx context.Context, act RotationAction, desired db.PluginContainer, gens []db.PluginContainerGeneration, now string) error {
	token, ok := r.takeToken(act.GenerationID)
	if !ok {
		// The token was minted in a process that has since died. The generation
		// cannot be authenticated, so it is failed rather than started: a
		// container the host cannot recognize is worse than no container.
		return r.advanceGeneration(ctx, act.GenerationID, GenPending, GenFailed,
			"instance token was lost before the container was created; a new generation will be minted", now)
	}

	gen, ok := findGeneration(gens, act.GenerationID)
	if !ok {
		return fmt.Errorf("generation %s vanished before its container was created", act.GenerationID)
	}

	opts := r.createOptions(desired)
	opts.Name = generationContainerName(act.InstanceID, gen.Generation)
	opts.Labels[LabelGeneration] = itoa64(gen.Generation)
	opts.Labels[LabelImageDigest] = gen.ImageDigest
	opts.Labels[LabelConfigHash] = gen.ConfigHash
	opts.Env = append(opts.Env, instanceTokenEnvVar+"="+token)

	id, err := r.runtime.Create(ctx, opts)
	if err != nil {
		return fmt.Errorf("create generation %d container: %w", gen.Generation, err)
	}
	if _, err := r.rotations.SetContainerGenerationContainerID(ctx, db.SetContainerGenerationContainerIDParams{
		ContainerID: strPtr(string(id)),
		UpdatedAt:   now,
		ID:          act.GenerationID,
	}); err != nil {
		return fmt.Errorf("record generation %d container id: %w", gen.Generation, err)
	}
	if err := r.runtime.Start(ctx, id); err != nil {
		return fmt.Errorf("start generation %d container: %w", gen.Generation, err)
	}

	return r.advanceGeneration(ctx, act.GenerationID, GenPending, GenStarting,
		"container created and started; health gate open", now)
}

// abortRotation fails the new generation and cleans up after it. The old
// generation is never touched — that is the whole point of the gate.
func (r *Reconciler) abortRotation(ctx context.Context, act RotationAction, now string) error {
	// Status first: a crash after the container is gone but before the row is
	// updated would leave a `starting` generation with no container, which the
	// planner reads as another abort — idempotent, but noisier than recording
	// the decision first.
	if err := r.advanceGeneration(ctx, act.GenerationID, GenStarting, GenFailed, act.Reason, now); err != nil {
		return err
	}
	if _, err := r.rotations.RevokeContainerGenerationToken(ctx, db.RevokeContainerGenerationTokenParams{
		TokenRevokedAt: strPtr(now),
		UpdatedAt:      now,
		ID:             act.GenerationID,
	}); err != nil {
		return fmt.Errorf("revoke failed generation's token: %w", err)
	}

	if act.ContainerID != "" {
		if err := r.runtime.Stop(ctx, act.ContainerID, stopTimeout); err != nil {
			slog.WarnContext(ctx, "stopping a failed generation's container", "err", err)
		}
		if err := r.runtime.Remove(ctx, act.ContainerID, true); err != nil {
			return fmt.Errorf("remove failed generation's container: %w", err)
		}
	}

	slog.WarnContext(ctx, "rotation aborted; previous generation still serving",
		"instance_id", act.InstanceID, "generation", act.Generation, "reason", act.Reason)
	return nil
}

// switchGeneration flips routing: the healthy generation becomes active and the
// previously-active one starts draining.
//
// The new generation is promoted BEFORE the old one is demoted. Between the two
// writes there are briefly two active generations, which is a state the planner
// tolerates (it picks the newest); the alternative ordering has a window with
// NO active generation, and a crash there would leave the instance unrouted.
func (r *Reconciler) switchGeneration(ctx context.Context, act RotationAction, gens []db.PluginContainerGeneration, now string) error {
	if err := r.advanceGeneration(ctx, act.GenerationID, GenHealthy, GenActive,
		"promoted to active", now); err != nil {
		return err
	}

	for _, gen := range gens {
		if gen.Status != GenActive || gen.ID == act.GenerationID {
			continue
		}
		if err := r.advanceGeneration(ctx, gen.ID, GenActive, GenDraining,
			"superseded by generation "+itoa64(act.Generation), now); err != nil {
			return err
		}
	}

	slog.InfoContext(ctx, "rotation switched",
		"instance_id", act.InstanceID, "generation", act.Generation)
	return nil
}

// drainGeneration stops the superseded container once its window has closed.
//
// Until then the step is a no-op that the next pass repeats — in-flight work
// gets a chance to finish, not a veto over shutdown.
func (r *Reconciler) drainGeneration(ctx context.Context, act RotationAction, gens []db.PluginContainerGeneration, _ string) error {
	gen, ok := findGeneration(gens, act.GenerationID)
	if !ok {
		return fmt.Errorf("draining generation %s vanished", act.GenerationID)
	}
	if !DrainDeadlinePassed(gen, rotationTimeNow(), r.drainTimeout) {
		return nil
	}
	if err := r.runtime.Stop(ctx, act.ContainerID, stopTimeout); err != nil {
		return fmt.Errorf("stop drained generation %d: %w", act.Generation, err)
	}
	return nil
}

// retireGeneration removes the superseded container and revokes its token.
//
// Revocation happens HERE, after the container is gone — not at switch time. A
// token revoked while the old generation is still finishing in-flight calls
// would fail that work at the host boundary, which is exactly what draining
// exists to avoid.
func (r *Reconciler) retireGeneration(ctx context.Context, act RotationAction, now string) error {
	if act.ContainerID != "" {
		if err := r.runtime.Remove(ctx, act.ContainerID, false); err != nil {
			return fmt.Errorf("remove retired generation %d: %w", act.Generation, err)
		}
	}
	if _, err := r.rotations.RevokeContainerGenerationToken(ctx, db.RevokeContainerGenerationTokenParams{
		TokenRevokedAt: strPtr(now),
		UpdatedAt:      now,
		ID:             act.GenerationID,
	}); err != nil {
		return fmt.Errorf("revoke retired generation's token: %w", err)
	}
	if err := r.advanceGeneration(ctx, act.GenerationID, GenDraining, GenStopped,
		"retired after drain", now); err != nil {
		return err
	}

	slog.InfoContext(ctx, "rotation complete; previous generation retired",
		"instance_id", act.InstanceID, "generation", act.Generation)
	return nil
}

// advanceGeneration performs one CAS status transition.
//
// A zero-row result is NOT an error. It means another pass already made this
// move, which on a level-triggered loop is an ordinary race rather than a
// failure — the next pass re-reads the world and finds it further along.
func (r *Reconciler) advanceGeneration(ctx context.Context, id, from, to, detail, now string) error {
	rows, err := r.rotations.UpdateContainerGenerationStatus(ctx, db.UpdateContainerGenerationStatusParams{
		Status:         to,
		StatusDetail:   strPtr(detail),
		UpdatedAt:      now,
		ID:             id,
		ExpectedStatus: from,
	})
	if err != nil {
		return fmt.Errorf("advance generation %s %s→%s: %w", id, from, to, err)
	}
	if rows == 0 {
		slog.DebugContext(ctx, "generation already advanced by another pass",
			"generation_id", id, "from", from, "to", to)
	}
	return nil
}

// groupGenerations buckets live generation rows by instance.
func groupGenerations(gens []db.PluginContainerGeneration) map[string][]db.PluginContainerGeneration {
	out := make(map[string][]db.PluginContainerGeneration)
	for _, gen := range gens {
		out[gen.PluginInstanceID] = append(out[gen.PluginInstanceID], gen)
	}
	return out
}

// groupContainersByGeneration indexes observed containers by instance and then
// by generation number, from labels alone. A container without a parseable
// generation label predates rotation and is left to the core loop.
func groupContainersByGeneration(observed []container.ContainerInfo) map[string]map[int64]container.ContainerInfo {
	out := make(map[string]map[int64]container.ContainerInfo)
	for _, info := range observed {
		instanceID := info.Labels[LabelInstance]
		if instanceID == "" {
			continue
		}
		number, ok := parseGenerationLabel(info.Labels[LabelGeneration])
		if !ok {
			continue
		}
		if out[instanceID] == nil {
			out[instanceID] = make(map[int64]container.ContainerInfo)
		}
		out[instanceID][number] = info
	}
	return out
}

// parseGenerationLabel reads a generation number from its label.
func parseGenerationLabel(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

func findGeneration(gens []db.PluginContainerGeneration, id string) (db.PluginContainerGeneration, bool) {
	for _, gen := range gens {
		if gen.ID == id {
			return gen, true
		}
	}
	return db.PluginContainerGeneration{}, false
}

// generationContainerName derives a per-generation container name. The
// generation suffix is what lets two generations coexist during a rotation —
// the core loop's single name per instance cannot.
func generationContainerName(instanceID string, generation int64) string {
	return containerName(instanceID) + "-g" + itoa64(generation)
}

// instanceTokenEnvVar is how a container receives its per-generation token.
// Same variable name the v1 subprocess substrate used: the delivery mechanism
// changed from process env to container env, the contract did not.
const instanceTokenEnvVar = "GLEIPNIR_INSTANCE_TOKEN"

func strPtr(s string) *string { return &s }

// stashToken holds a freshly minted token until the container that owns it
// exists.
func (r *Reconciler) stashToken(generationID, token string) {
	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	r.tokens[generationID] = token
}

// takeToken consumes a stashed token. It is a take rather than a read so one
// token can only ever reach one container: a second create attempt for the same
// generation finds nothing and fails the generation instead of starting a
// duplicate container with the same identity.
func (r *Reconciler) takeToken(generationID string) (string, bool) {
	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	token, ok := r.tokens[generationID]
	delete(r.tokens, generationID)
	return token, ok
}
