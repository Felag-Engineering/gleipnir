// Package reconciler — this file adds generation rotation (ADR-056, spec §7):
// replacing a running plugin container with a new one carrying a new image or
// config, without the instance going dark.
//
// Rotation is NOT a procedure. It is a sequence of desired-state transitions
// over generation records, and every pass takes exactly one of them. That
// distinction is the whole design: a procedure has to be resumed after a crash,
// and resuming means knowing where it got to. A state machine over durable rows
// has nothing to resume — whatever state the next pass reads, there is exactly
// one next step, including for a process that died between any two of them.
//
// The sequence, one pass each:
//
//	begin      — a live generation drifts from desired ⇒ mint generation N+1
//	             (pending) with a fresh instance token
//	create     — pending ⇒ starting: create and start the new container
//	health     — starting ⇒ healthy, or ⇒ failed on a gate failure/timeout
//	switch     — healthy ⇒ active; the previous active ⇒ draining
//	drain      — draining ⇒ stop the old container once in-flight work finishes
//	retire     — the old container has stopped ⇒ remove it and revoke its token
//
// The invariant that matters most: **the old generation keeps serving until the
// new one has passed its gate.** A health-gate failure converges backward — the
// new generation is stopped and marked failed, the old one is never touched.
// An upgrade that cannot prove itself healthy must not be able to take the
// instance down.
package reconciler

import (
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// LabelGeneration is the generation number a container belongs to. Without it
// two generations of one instance are indistinguishable in a list-by-label
// pass, and the loop cannot tell which container it is about to stop.
const LabelGeneration = "gleipnir.plugin.generation"

// Generation status vocabulary, matching the
// plugin_container_generations.status CHECK constraint.
const (
	GenPending  = "pending"
	GenStarting = "starting"
	GenHealthy  = "healthy"
	GenActive   = "active"
	GenDraining = "draining"
	GenStopped  = "stopped"
	GenFailed   = "failed"
)

// defaultHealthGateTimeout bounds how long a new generation may sit in
// `starting` before the gate gives up.
//
// A gate with no deadline is not a gate: a container that never reports healthy
// would hold a rotation open forever, with the old generation stuck in `active`
// and the instance permanently mid-upgrade. Failing is recoverable — the old
// generation is still serving, and an operator sees a failed generation with a
// reason. Hanging is not.
const defaultHealthGateTimeout = 2 * time.Minute

// defaultDrainTimeout bounds how long a superseded generation may sit in
// `draining` before its container is stopped regardless of in-flight work.
// Mirrors GLEIPNIR_DRAIN_TIMEOUT's role for runs: work gets a chance to
// finish, not a veto over shutdown.
const defaultDrainTimeout = 5 * time.Minute

// RotationKind is the one rotation step a pass decides to take for an instance.
type RotationKind string

const (
	// RotationNone means no rotation step applies: either nothing has drifted,
	// or the current step is still in flight and the next pass should re-read
	// rather than act.
	RotationNone RotationKind = "none"

	// RotationBegin mints the next generation record. It creates no container —
	// the row exists first so a crash immediately afterwards leaves a `pending`
	// generation the next pass picks up, rather than a container nothing knows
	// about.
	RotationBegin RotationKind = "rotation_begin"

	// RotationCreate creates and starts the new generation's container.
	//
	// Unlike the first-boot path (ActionCreate then ActionStart over two
	// passes), rotation creates and starts in one step. The reason is the
	// health gate: a created-but-not-started container is indistinguishable
	// from one that started and immediately exited, and the gate has to be able
	// to tell those apart to decide between "still starting" and "failed".
	RotationCreate RotationKind = "rotation_create"

	// RotationPromote passes the new generation's health gate: starting ⇒
	// healthy. It changes no container state — it records that the new
	// generation has proven itself, which is what makes the switch safe.
	RotationPromote RotationKind = "rotation_promote"

	// RotationAbort fails the new generation and leaves the old one serving.
	// The converging-backward step: an upgrade that cannot prove itself healthy
	// must not be able to take the instance down.
	RotationAbort RotationKind = "rotation_abort"

	// RotationSwitch flips routing to the new generation: healthy ⇒ active, and
	// the previous active ⇒ draining. This is the only step where traffic moves.
	RotationSwitch RotationKind = "rotation_switch"

	// RotationDrain stops the superseded generation's container once its
	// in-flight work has finished or the drain deadline has passed.
	RotationDrain RotationKind = "rotation_drain"

	// RotationRetire removes the superseded container and revokes its token.
	// Revocation happens here, after the container is gone, rather than at
	// switch time: a token revoked while the old generation is still finishing
	// in-flight calls would fail that work at the host boundary, which is
	// exactly what draining exists to avoid.
	RotationRetire RotationKind = "rotation_retire"
)

// RotationAction is one planned rotation step.
type RotationAction struct {
	Kind       RotationKind
	InstanceID string

	// GenerationID is the plugin_container_generations row the step acts on.
	GenerationID string

	// Generation is that row's number, carried so a label can be written or
	// matched without re-reading the row.
	Generation int64

	// ContainerID is the container the step acts on, when one exists.
	ContainerID container.ContainerID

	// Reason is the operator-facing explanation, carried into the audit event
	// and the log line.
	Reason string
}

// RotationInputs is everything planRotation needs for one instance. It is a
// struct rather than an argument list because the planner's whole value is
// being a pure function of an explicitly-named state — a signature that grows
// silently is a state machine that grows silently.
type RotationInputs struct {
	// Desired is the instance's desired-state row: the image digest and config
	// hash a generation must match to be current.
	Desired db.PluginContainer

	// Generations are the instance's live (non-terminal) generation rows.
	// Order is not assumed; the planner selects by status and number.
	Generations []db.PluginContainerGeneration

	// Observed maps generation number to the container carrying that
	// generation's label, for the generations that have one.
	Observed map[int64]container.ContainerInfo

	// Now is the reference instant for the gate and drain deadlines.
	Now time.Time

	// HealthGateTimeout and DrainTimeout are the two deadlines. Zero means the
	// package default.
	HealthGateTimeout time.Duration
	DrainTimeout      time.Duration
}

// planRotation decides the single rotation step for one instance.
//
// Ordering is the crash-resume property. In-flight rotations are finished
// before a new one begins, and within a rotation the steps are checked in
// reverse order of the sequence — retire before drain, drain before switch, and
// so on. Reading from the end means the planner always finds the FURTHEST-along
// state first, so a pass that crashed halfway resumes at exactly the step it
// was on rather than restarting the sequence.
func planRotation(in RotationInputs) RotationAction {
	instanceID := in.Desired.PluginInstanceID
	none := RotationAction{Kind: RotationNone, InstanceID: instanceID}

	byStatus := indexGenerations(in.Generations)

	// Retire: a draining generation whose container has stopped, or is gone
	// entirely. Last step first.
	//
	// "Stopped" and "gone" are the same answer here on purpose. Drain STOPS the
	// container; retire REMOVES it. Keying retire on absence alone would leave
	// a stopped container planning `drain` forever, because the step that would
	// remove it never runs.
	if gen, ok := byStatus[GenDraining]; ok {
		info, present := in.Observed[gen.Generation]
		if !present || !isRunning(info.State) {
			return RotationAction{
				Kind:         RotationRetire,
				InstanceID:   instanceID,
				GenerationID: gen.ID,
				Generation:   gen.Generation,
				ContainerID:  info.ID,
				Reason:       "superseded generation has stopped; removing it and revoking its token",
			}
		}
		// Drain: still holding a running container. The deadline is advisory
		// here — this planner cannot see in-flight calls, so it reports the
		// step and the applier decides whether the drain window has closed.
		// Reporting it every pass is what makes a stuck drain visible rather
		// than silent.
		return RotationAction{
			Kind:         RotationDrain,
			InstanceID:   instanceID,
			GenerationID: gen.ID,
			Generation:   gen.Generation,
			ContainerID:  info.ID,
			Reason:       "generation superseded; draining before stop",
		}
	}

	// Switch: a generation that passed its gate is waiting to take over.
	if gen, ok := byStatus[GenHealthy]; ok {
		info := in.Observed[gen.Generation]
		return RotationAction{
			Kind:         RotationSwitch,
			InstanceID:   instanceID,
			GenerationID: gen.ID,
			Generation:   gen.Generation,
			ContainerID:  info.ID,
			Reason:       "new generation passed its health gate; switching routing",
		}
	}

	// Health gate: a starting generation is proving itself, failing, or has
	// run out of time.
	if gen, ok := byStatus[GenStarting]; ok {
		return planHealthGate(in, gen)
	}

	// Create: a pending generation has a row and no container yet.
	if gen, ok := byStatus[GenPending]; ok {
		return RotationAction{
			Kind:         RotationCreate,
			InstanceID:   instanceID,
			GenerationID: gen.ID,
			Generation:   gen.Generation,
			Reason:       "new generation has no container yet",
		}
	}

	// Nothing in flight. Begin a rotation only if the serving generation has
	// drifted from what is desired.
	active, ok := byStatus[GenActive]
	if !ok {
		// No active generation and nothing in flight: this instance has not
		// been through first boot yet, which is the core loop's job, not
		// rotation's. Doing anything here would race it.
		return none
	}
	if reason := generationDrift(in.Desired, active); reason != "" {
		return RotationAction{
			Kind:         RotationBegin,
			InstanceID:   instanceID,
			GenerationID: active.ID,
			Generation:   active.Generation,
			Reason:       reason,
		}
	}
	return none
}

// planHealthGate decides between promote, abort, and wait for a starting
// generation.
func planHealthGate(in RotationInputs, gen db.PluginContainerGeneration) RotationAction {
	act := RotationAction{
		InstanceID:   in.Desired.PluginInstanceID,
		GenerationID: gen.ID,
		Generation:   gen.Generation,
	}

	info, present := in.Observed[gen.Generation]
	if present {
		act.ContainerID = info.ID
	}

	timeout := in.HealthGateTimeout
	if timeout <= 0 {
		timeout = defaultHealthGateTimeout
	}
	expired := gateExpired(gen, in.Now, timeout)

	switch {
	case !present:
		// The container vanished mid-gate — it was removed, or it never
		// materialized. Either way this generation cannot prove itself.
		act.Kind = RotationAbort
		act.Reason = "new generation's container disappeared during the health gate"

	case info.State == container.ContainerStateExited || info.State == container.ContainerStateDead:
		// A container that exited during its gate has answered the question.
		// Waiting out the deadline would only delay a decision already made.
		act.Kind = RotationAbort
		act.Reason = "new generation's container is " + string(info.State) + " during the health gate"

	case info.Health == healthUnhealthy && expired:
		// Unhealthy is not immediately fatal: a container can report unhealthy
		// while still starting up, and its own healthcheck retries are the
		// runtime's business. It becomes fatal when the deadline passes.
		act.Kind = RotationAbort
		act.Reason = "new generation reported unhealthy and did not recover before the health gate deadline"

	case expired:
		act.Kind = RotationAbort
		act.Reason = "new generation did not pass its health gate within " + timeout.String()

	case isGateHealthy(info):
		act.Kind = RotationPromote
		act.Reason = "new generation passed its health gate"

	default:
		act.Kind = RotationNone
		act.Reason = "new generation is still starting"
	}
	return act
}

// Runtime healthcheck status values (Docker/Podman vocabulary).
const (
	healthStarting  = "starting"
	healthHealthy   = "healthy"
	healthUnhealthy = "unhealthy"
)

// isGateHealthy reports whether a container has satisfied the container-level
// half of the health gate.
//
// An image that declares NO healthcheck reports an empty Health string forever.
// Treating that as never-healthy would make every such plugin un-rotatable, so
// a running container with no healthcheck passes this half of the gate. The
// deeper `server/discover` readiness probe — which does not care whether an
// image declared a healthcheck — is the other half, and belongs to the
// per-capability health work; this planner intentionally decides only what it
// can see in a list-by-label result.
func isGateHealthy(info container.ContainerInfo) bool {
	if info.State != container.ContainerStateRunning {
		return false
	}
	switch info.Health {
	case healthHealthy, "":
		return true
	}
	return false
}

// gateExpired reports whether a starting generation has run out of gate time.
// An unparseable created_at is treated as NOT expired: failing a rotation
// because a timestamp could not be read would turn a storage oddity into an
// aborted upgrade, and the container-state checks above still catch a genuinely
// broken generation.
func gateExpired(gen db.PluginContainerGeneration, now time.Time, timeout time.Duration) bool {
	started, err := time.Parse(time.RFC3339Nano, gen.UpdatedAt)
	if err != nil {
		return false
	}
	return now.Sub(started) > timeout
}

// DrainDeadlinePassed reports whether a draining generation has exhausted its
// drain window. The applier calls it to decide whether in-flight work still
// gets to finish or the container is stopped regardless.
func DrainDeadlinePassed(gen db.PluginContainerGeneration, now time.Time, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = defaultDrainTimeout
	}
	started, err := time.Parse(time.RFC3339Nano, gen.UpdatedAt)
	if err != nil {
		// Unreadable timestamp: treat the window as exhausted rather than
		// waiting forever. This direction is safe because draining is already
		// the "superseded, finish up" state — the new generation is serving.
		return true
	}
	return now.Sub(started) > timeout
}

// generationDrift reports how a serving generation diverges from the desired
// row, or "" when it matches.
//
// This is the rotation trigger, and it is the same comparison ActionDriftDetected
// reports in the core loop — that action names the divergence, this function
// decides to act on it.
func generationDrift(desired db.PluginContainer, active db.PluginContainerGeneration) string {
	if active.ImageDigest != desired.ImageDigest {
		return "image digest drift: generation " + itoa64(active.Generation) +
			" runs " + active.ImageDigest + ", desired " + desired.ImageDigest
	}
	if active.ConfigHash != desired.ConfigHash {
		return "config hash drift: generation " + itoa64(active.Generation) +
			" runs " + active.ConfigHash + ", desired " + desired.ConfigHash
	}
	return ""
}

// indexGenerations selects at most one generation per status.
//
// More than one generation in the same non-terminal status is not a state this
// machine can produce — every transition is a CAS from a specific expected
// status — so when it happens anyway (a hand-edited row, a bug elsewhere) the
// HIGHEST-numbered one wins. Picking the newest means a stray old row cannot
// hold a rotation hostage, and the older one is left where the orphan path can
// see it.
func indexGenerations(gens []db.PluginContainerGeneration) map[string]db.PluginContainerGeneration {
	out := make(map[string]db.PluginContainerGeneration, len(gens))
	for _, gen := range gens {
		if existing, ok := out[gen.Status]; ok && existing.Generation >= gen.Generation {
			continue
		}
		out[gen.Status] = gen
	}
	return out
}

// itoa64 renders a generation number without pulling strconv into a file that
// otherwise needs no formatting.
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
