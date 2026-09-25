package reconciler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
//
// Serialized against runPass via rotationMu (#955 security re-review round 3
// item 4): sweepOrphanedGenerationContainers reads the world (live
// generations, observed containers) and acts on it as a single logical step,
// and that is only safe if no OTHER rotation pass — the run loop's own, most
// notably — can be mutating the same generations and containers from a
// different, concurrently-read snapshot at the same time. A direct caller
// (a test, or a future "reconcile now" admin action) gets the exact same
// exclusion the run loop gives itself.
func (r *Reconciler) ReconcileRotations(ctx context.Context) (RotationPassResult, error) {
	r.rotationMu.Lock()
	defer r.rotationMu.Unlock()
	return r.reconcileRotationsLocked(ctx)
}

// reconcileRotationsLocked is ReconcileRotations' body, factored out so
// runPass can hold rotationMu across ReconcileOnce AND this pass in one
// critical section without the exported method taking the same mutex a
// second time (sync.Mutex is not reentrant).
func (r *Reconciler) reconcileRotationsLocked(ctx context.Context) (RotationPassResult, error) {
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

	// A stashed token whose generation is no longer pending (superseded,
	// failed, or its instance torn down before create ever ran) would
	// otherwise sit in memory forever for a container that will never be
	// created (#955 security review item 5).
	r.pruneStashedTokens(generations)

	byInstance := groupGenerations(generations)
	containers := groupContainersByGeneration(observed)

	// A container whose generation label names a generation that is no
	// longer live is a leftover from a Stop or Remove that failed partway —
	// during drain/retire, or the desired-stopped path above — and nothing
	// else in this pass would otherwise retry it (#955 security review round
	// 2 item 4). The core loop's own orphan check does not catch this: it is
	// gated on the INSTANCE having no live generation at all, and an instance
	// mid-rotation almost always still has one (the new generation), so its
	// GenerationLive short-circuit never looks at this specific container.
	if errs := r.sweepOrphanedGenerationContainers(ctx, byInstance, containers); errs > 0 {
		result.Errors += errs
	}

	desiredInstances := make(map[string]bool, len(desired))
	for _, row := range desired {
		desiredInstances[row.PluginInstanceID] = true
	}

	// A live generation whose instance has no desired-state row left is an
	// instance-level orphan (#955 security review finding 4): the row is
	// gone, but the per-row loop below only ever visits rows IN `desired`, so
	// nothing would otherwise revoke what such a generation's token still
	// authenticates. Re-adding the row later finds nothing live and boots a
	// fresh generation, same as first boot.
	for instanceID, gens := range byInstance {
		if desiredInstances[instanceID] {
			continue
		}
		for _, gen := range gens {
			action := RotationAction{
				Kind: RotationRetireOrphanedInstance, InstanceID: instanceID,
				GenerationID: gen.ID, Generation: gen.Generation,
				Reason: "instance has no desired-state row; revoking its token",
			}
			result.Actions = append(result.Actions, action)
			if err := r.retireOrphanedInstanceGeneration(ctx, gen, containers[instanceID]); err != nil {
				result.Errors++
				slog.ErrorContext(ctx, "retiring orphaned instance's generation failed",
					"instance_id", instanceID, "generation", gen.Generation, "err", err)
			}
		}
	}

	for _, row := range desired {
		gens := byInstance[row.PluginInstanceID]
		obs := containers[row.PluginInstanceID]

		var restartAttempts int
		if active, ok := indexGenerations(gens)[GenActive]; ok {
			if info, present := obs[active.Generation]; present && isRunning(info.State) {
				r.observeActiveRunning(active.ID, rotationTimeNow())
			} else {
				r.observeActiveNotRunning(active.ID)
			}
			restartAttempts = r.restartAttempts(active.ID)
		}

		action := planRotation(RotationInputs{
			Desired:           row,
			Generations:       gens,
			Observed:          obs,
			Now:               rotationTimeNow(),
			HealthGateTimeout: r.healthGateTimeout,
			DrainTimeout:      r.drainTimeout,
			RestartAttempts:   restartAttempts,
		})
		if action.Kind == RotationNone {
			continue
		}
		result.Actions = append(result.Actions, action)
		if err := r.applyRotation(ctx, action, row, gens, obs); err != nil {
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
func (r *Reconciler) applyRotation(ctx context.Context, act RotationAction, desired db.PluginContainer, gens []db.PluginContainerGeneration, observed map[int64]container.ContainerInfo) error {
	now := rotationTimeNow().UTC().Format(time.RFC3339Nano)

	switch act.Kind {
	case RotationBegin:
		return r.beginRotation(ctx, act, desired, now)
	case RotationCreate:
		return r.createRotationContainer(ctx, act, desired, gens, observed, now)
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
	case RotationStopInstance:
		return r.stopInstance(ctx, act, gens, observed, now)
	case RotationRestartActive:
		return r.restartActiveGeneration(ctx, act)
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

	if act.CrashLoop {
		// Worth an operator's attention, not just a routine upgrade log line
		// (#955 security review item 5's "surface the crash loop").
		slog.WarnContext(ctx, "reconciler: instance crash-looping; minting a fresh generation",
			"instance_id", act.InstanceID, "generation", next, "reason", act.Reason)
	} else {
		slog.InfoContext(ctx, "rotation begun",
			"instance_id", act.InstanceID, "generation", next, "reason", act.Reason)
	}
	return nil
}

// createRotationContainer creates and starts the new generation's container.
//
// Both in one step, unlike first boot's create-then-start. The health gate has
// to distinguish "still starting" from "started and immediately exited", and a
// created-but-never-started container is indistinguishable from the latter.
func (r *Reconciler) createRotationContainer(ctx context.Context, act RotationAction, desired db.PluginContainer, gens []db.PluginContainerGeneration, observed map[int64]container.ContainerInfo, now string) error {
	token, ok := r.takeToken(act.GenerationID)
	if !ok {
		// The token is gone: minted in a process that has since died, or
		// already consumed by an earlier attempt for this same generation
		// whose Create succeeded but whose Start then failed before the row
		// ever advanced past pending (takeToken is destructive, so a retry
		// finds nothing either way). The generation cannot be authenticated,
		// so it is failed rather than retried: a container the host cannot
		// recognize is worse than no container.
		return r.failLostTokenGeneration(ctx, act, observed, now)
	}

	gen, ok := findGeneration(gens, act.GenerationID)
	if !ok {
		return fmt.Errorf("generation %s vanished before its container was created", act.GenerationID)
	}

	opts, err := r.withGenerationEnv(ctx, r.createOptions(desired), act.InstanceID)
	if err != nil {
		return fmt.Errorf("generation %d container env: %w", gen.Generation, err)
	}
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

// failLostTokenGeneration fails a pending generation whose token cannot be
// recovered (security review, #955 finding 1).
//
// A crash is not the only way here: Create can succeed and Start can then
// fail (or the process can die) before the row ever advances past pending,
// and because takeToken is destructive, the retry that follows finds the
// token already gone. Left alone that retry silently failed the generation
// while leaving three things live: the token's DB row unrevoked, and — when
// Create had already succeeded — a container carrying this generation's
// label that nothing would ever adopt, since planFor treats a
// generation-labelled container with no live generation as an orphan rather
// than something to start. This closes all three in one place: the row is
// failed, its token is revoked (defense in depth alongside
// GetContainerGenerationByTokenHash's own status filter), and any leftover
// container is stopped and removed immediately rather than left for the
// core loop's orphan sweep to find on a later pass.
func (r *Reconciler) failLostTokenGeneration(ctx context.Context, act RotationAction, observed map[int64]container.ContainerInfo, now string) error {
	if err := r.advanceGeneration(ctx, act.GenerationID, GenPending, GenFailed,
		"instance token was lost before the container was created; a new generation will be minted", now); err != nil {
		return err
	}
	if _, err := r.rotations.RevokeContainerGenerationToken(ctx, db.RevokeContainerGenerationTokenParams{
		TokenRevokedAt: strPtr(now),
		UpdatedAt:      now,
		ID:             act.GenerationID,
	}); err != nil {
		return fmt.Errorf("revoke lost-token generation's token: %w", err)
	}

	info, present := observed[act.Generation]
	if !present {
		return nil
	}
	if isRunning(info.State) {
		if err := r.runtime.Stop(ctx, info.ID, stopTimeout); err != nil {
			return fmt.Errorf("stop lost-token generation %d's leftover container: %w", act.Generation, err)
		}
	}
	if err := r.runtime.Remove(ctx, info.ID, false); err != nil {
		return fmt.Errorf("remove lost-token generation %d's leftover container: %w", act.Generation, err)
	}
	return nil
}

// abortRotation fails the new generation and cleans up after it. The old
// generation is never touched — that is the whole point of the gate.
func (r *Reconciler) abortRotation(ctx context.Context, act RotationAction, now string) error {
	// Reachable from more than the health-gate path now: an explicit stop
	// (#955 security review finding 2 round 2) can abort a pending, starting,
	// or healthy candidate alike, so the CAS expects act.FromStatus rather
	// than the health gate's fixed GenStarting.
	from := act.FromStatus
	if from == "" {
		from = GenStarting
	}
	// Status first: a crash after the container is gone but before the row is
	// updated would leave a generation with no container, which the planner
	// reads as another abort — idempotent, but noisier than recording the
	// decision first.
	if err := r.advanceGeneration(ctx, act.GenerationID, from, GenFailed, act.Reason, now); err != nil {
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

	slog.WarnContext(ctx, "reconciler: generation aborted",
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
	// Advance, then revoke, then Remove (#955 security review round 3 item
	// 3) — matching every other retire path (failLostTokenGeneration,
	// stopInstance, retireOrphanedInstanceGeneration): a Remove that fails
	// partway must never leave a live, still-authenticating token behind. A
	// persistent Remove failure now leaves a non-live, revoked generation —
	// exactly what sweepOrphanedGenerationContainers exists to collect on a
	// later pass, rather than a `draining` row that (before this fix) kept
	// retrying RotationRetire forever with its own bespoke retry path.
	if err := r.advanceGeneration(ctx, act.GenerationID, GenDraining, GenStopped,
		"retired after drain", now); err != nil {
		return err
	}
	if _, err := r.rotations.RevokeContainerGenerationToken(ctx, db.RevokeContainerGenerationTokenParams{
		TokenRevokedAt: strPtr(now),
		UpdatedAt:      now,
		ID:             act.GenerationID,
	}); err != nil {
		return fmt.Errorf("revoke retired generation's token: %w", err)
	}
	if act.ContainerID != "" {
		if err := r.runtime.Remove(ctx, act.ContainerID, false); err != nil {
			return fmt.Errorf("remove retired generation %d: %w", act.Generation, err)
		}
	}

	slog.InfoContext(ctx, "rotation complete; previous generation retired",
		"instance_id", act.InstanceID, "generation", act.Generation)
	return nil
}

// stopInstance converges EVERY live generation of an instance toward terminal
// in one action (#955 security review round 3 finding 1): a serving (active)
// or still-up (draining) container must not wait behind a merely-formal
// pending-generation fail-and-revoke that touches no container at all.
//
// Revoke and mark terminal BEFORE the socket work, for every generation,
// matching failLostTokenGeneration's order: a Stop that fails partway must
// never leave a live, still-authenticating token behind waiting on a retry.
// Only once every generation's row and token are settled does this stop the
// containers that are still running. Removal is deliberately NOT done here —
// it is left to sweepOrphanedGenerationContainers on the next pass, the same
// sweep that already collects a leftover from any other failed Stop/Remove,
// so this one action stays exactly two phases (revoke-and-terminalize, then
// stop) rather than three.
func (r *Reconciler) stopInstance(ctx context.Context, act RotationAction, gens []db.PluginContainerGeneration, observed map[int64]container.ContainerInfo, now string) error {
	var errs []error

	for _, gen := range gens {
		terminal := terminalStatusForStop(gen.Status)
		if err := r.advanceGeneration(ctx, gen.ID, gen.Status, terminal, act.Reason, now); err != nil {
			errs = append(errs, fmt.Errorf("advance generation %d to %s: %w", gen.Generation, terminal, err))
			continue
		}
		if _, err := r.rotations.RevokeContainerGenerationToken(ctx, db.RevokeContainerGenerationTokenParams{
			TokenRevokedAt: strPtr(now),
			UpdatedAt:      now,
			ID:             gen.ID,
		}); err != nil {
			errs = append(errs, fmt.Errorf("revoke generation %d's token: %w", gen.Generation, err))
			continue
		}
		r.clearRestartState(gen.ID)
	}

	for _, gen := range gens {
		info, present := observed[gen.Generation]
		if !present || !isRunning(info.State) {
			continue
		}
		if err := r.runtime.Stop(ctx, info.ID, stopTimeout); err != nil {
			errs = append(errs, fmt.Errorf("stop generation %d's container: %w", gen.Generation, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	slog.InfoContext(ctx, "reconciler: instance stopped by desired state",
		"instance_id", act.InstanceID, "generations", len(gens))
	return nil
}

// restartActiveGeneration restarts an active generation's container that is
// no longer running (#955 security review finding 2). Bounded by an
// exponential backoff between attempts (item 5): a container that keeps
// dying must not be hammered with Start calls on every reconcile pass. The
// backoff check happens here, not in the planner, mirroring
// drainGeneration's split — planRotation reports the step every pass so a
// restart loop stays visible, and the applier decides whether the deadline
// has actually passed.
func (r *Reconciler) restartActiveGeneration(ctx context.Context, act RotationAction) error {
	now := rotationTimeNow()
	if !r.restartDue(act.GenerationID, now) {
		return nil
	}
	if err := r.runtime.Start(ctx, act.ContainerID); err != nil {
		return fmt.Errorf("restart active generation %d: %w", act.Generation, err)
	}
	r.recordRestartAttempt(act.GenerationID, now)
	slog.WarnContext(ctx, "reconciler: restarted a dead active generation's container",
		"instance_id", act.InstanceID, "generation", act.Generation)
	return nil
}

// retireOrphanedInstanceGeneration revokes a live generation's token and
// retires it when its instance has no desired-state row at all (#955
// security review finding 4). Called outside planRotation/applyRotation's
// per-row machinery — see RotationRetireOrphanedInstance's doc comment.
func (r *Reconciler) retireOrphanedInstanceGeneration(ctx context.Context, gen db.PluginContainerGeneration, observed map[int64]container.ContainerInfo) error {
	now := rotationTimeNow().UTC().Format(time.RFC3339Nano)

	// Revoke and mark terminal BEFORE the socket work (#955 security review
	// round 2 item 3), matching failLostTokenGeneration's order: a Stop or
	// Remove that fails partway must never leave a live, still-authenticating
	// token behind for the instance nothing claims any more.
	//
	// Pending/starting never reached serving traffic; everything else
	// (healthy, active, draining) did, however briefly. Both read as terminal
	// either way, but the distinction is worth keeping in the audit trail a
	// human reads later.
	terminal := GenStopped
	if gen.Status == GenPending || gen.Status == GenStarting {
		terminal = GenFailed
	}
	if err := r.advanceGeneration(ctx, gen.ID, gen.Status, terminal,
		"instance has no desired-state row", now); err != nil {
		return err
	}
	if _, err := r.rotations.RevokeContainerGenerationToken(ctx, db.RevokeContainerGenerationTokenParams{
		TokenRevokedAt: strPtr(now),
		UpdatedAt:      now,
		ID:             gen.ID,
	}); err != nil {
		return fmt.Errorf("revoke orphaned instance's generation %d token: %w", gen.Generation, err)
	}

	if info, present := observed[gen.Generation]; present {
		if isRunning(info.State) {
			if err := r.runtime.Stop(ctx, info.ID, stopTimeout); err != nil {
				return fmt.Errorf("stop orphaned instance's generation %d container: %w", gen.Generation, err)
			}
		}
		if err := r.runtime.Remove(ctx, info.ID, false); err != nil {
			return fmt.Errorf("remove orphaned instance's generation %d container: %w", gen.Generation, err)
		}
	}
	r.clearRestartState(gen.ID)
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

// pruneStashedTokens drops every stashed token whose generation is no longer
// pending (#955 security review item 5). A token is minted and stashed only
// to be handed to the ONE create attempt that follows it; if that generation
// left pending some other way — superseded, failed, or its instance torn
// down before create ever ran — the stash would otherwise hold live-looking
// credential material in memory for a container that will never exist.
func (r *Reconciler) pruneStashedTokens(liveGenerations []db.PluginContainerGeneration) {
	pending := make(map[string]bool, len(liveGenerations))
	for _, gen := range liveGenerations {
		if gen.Status == GenPending {
			pending[gen.ID] = true
		}
	}

	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	for id := range r.tokens {
		if !pending[id] {
			delete(r.tokens, id)
		}
	}
}

// restartState is the crash-loop bookkeeping for one active generation whose
// container has died (#955 security review item 5).
type restartState struct {
	attempts    int
	nextAttempt time.Time

	// runningSince is when the container was FIRST observed running since
	// its last restart attempt, or the zero Time while it is down (or has
	// never been restarted). attempts is cleared only once the container has
	// stayed up continuously for at least minRestartRecoveryUptime — never
	// merely because one pass happened to observe it running (round 2 item
	// 5): a container that dies again shortly after every restart would
	// otherwise reset its own counter every single pass, and the cap it
	// exists to enforce would never be reached.
	runningSince time.Time
}

// restartBackoffBase and restartBackoffMax bound the wait between restart
// attempts: exponential, doubling from the base and capped at the max, so a
// container that keeps dying is retried with decreasing frequency rather than
// hammered every reconcile pass.
const (
	restartBackoffBase = 5 * time.Second
	restartBackoffMax  = 5 * time.Minute
)

// minRestartRecoveryUptime is how long an active generation's container must
// stay running, continuously, before its crash-loop counter is cleared (#955
// security review round 2 item 5).
const minRestartRecoveryUptime = 2 * time.Minute

// restartAttempts reports how many consecutive restart attempts a generation
// has already had.
func (r *Reconciler) restartAttempts(generationID string) int {
	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	if st, ok := r.restarts[generationID]; ok {
		return st.attempts
	}
	return 0
}

// restartDue reports whether enough time has passed since the last restart
// attempt for another one — true when there has never been one.
func (r *Reconciler) restartDue(generationID string, now time.Time) bool {
	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	st, ok := r.restarts[generationID]
	return !ok || !now.Before(st.nextAttempt)
}

// recordRestartAttempt bumps a generation's attempt count and sets the next
// backoff deadline using the package's injectable clock. The uptime streak
// resets too: a fresh restart attempt starts a fresh streak, not a
// continuation of however long the container had (briefly) stayed up before
// this crash.
func (r *Reconciler) recordRestartAttempt(generationID string, now time.Time) {
	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	st, ok := r.restarts[generationID]
	if !ok {
		st = &restartState{}
		r.restarts[generationID] = st
	}
	st.attempts++
	st.runningSince = time.Time{}
	wait := restartBackoffBase << (st.attempts - 1) // exponential from the base
	if wait > restartBackoffMax || wait <= 0 {
		wait = restartBackoffMax
	}
	st.nextAttempt = now.Add(wait)
}

// observeActiveRunning records that a generation's container is running as of
// now, and clears its crash-loop counter once it has been running
// continuously for minRestartRecoveryUptime. A generation with no restart
// history is a no-op — there is nothing to recover from.
func (r *Reconciler) observeActiveRunning(generationID string, now time.Time) {
	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	st, ok := r.restarts[generationID]
	if !ok {
		return
	}
	if st.runningSince.IsZero() {
		st.runningSince = now
		return
	}
	if now.Sub(st.runningSince) >= minRestartRecoveryUptime {
		delete(r.restarts, generationID)
	}
}

// observeActiveNotRunning breaks a generation's uptime streak: it is down
// again, so however long it had stayed up no longer counts toward recovery.
// The attempt count itself is untouched — only an actual restart (via
// recordRestartAttempt) or a full recovery (via observeActiveRunning) changes
// it.
func (r *Reconciler) observeActiveNotRunning(generationID string) {
	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	if st, ok := r.restarts[generationID]; ok {
		st.runningSince = time.Time{}
	}
}

// clearRestartState drops a generation's crash-loop bookkeeping outright: it
// was retired or superseded, and neither leaves anything worth remembering a
// backoff against.
func (r *Reconciler) clearRestartState(generationID string) {
	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	delete(r.restarts, generationID)
}

// sweepOrphanedGenerationContainers removes every observed container whose
// generation label names a generation that is no longer live (#955 security
// review round 2 item 4): a leftover from a Stop or Remove that failed
// partway during drain, retire, or an explicit stop. Returns the number of
// removals that failed, for the caller to fold into its error count.
func (r *Reconciler) sweepOrphanedGenerationContainers(
	ctx context.Context,
	byInstance map[string][]db.PluginContainerGeneration,
	containers map[string]map[int64]container.ContainerInfo,
) (errs int) {
	for instanceID, byGeneration := range containers {
		live := make(map[int64]bool, len(byInstance[instanceID]))
		for _, gen := range byInstance[instanceID] {
			live[gen.Generation] = true
		}
		for number, info := range byGeneration {
			if live[number] {
				continue
			}
			if isRunning(info.State) {
				if err := r.runtime.Stop(ctx, info.ID, stopTimeout); err != nil {
					errs++
					slog.ErrorContext(ctx, "reconciler: stopping orphaned generation container failed",
						"instance_id", instanceID, "generation", number, "err", err)
					continue
				}
			}
			if err := r.runtime.Remove(ctx, info.ID, false); err != nil {
				errs++
				slog.ErrorContext(ctx, "reconciler: removing orphaned generation container failed",
					"instance_id", instanceID, "generation", number, "err", err)
				continue
			}
			slog.InfoContext(ctx, "reconciler: removed orphaned generation container",
				"instance_id", instanceID, "generation", number)
		}
	}
	return errs
}
