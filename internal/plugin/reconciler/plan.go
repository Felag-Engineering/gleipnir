// Package reconciler converges the container substrate toward the desired
// state recorded in SQLite (ADR-056, mcp-realignment-spec.md §7).
//
// One rule bounds everything here: **level-triggered reconciliation only**.
// Every pass re-lists the real containers by label, diffs them against the
// desired-state rows, and takes ONE converging step per instance. There is no
// imperative sequence that must run to completion, so there is nothing to
// resume: a crash between any two steps leaves a state the next pass reads
// fresh and converges from. That is why identity comes from container LABELS
// rather than a stored container ID — the loop can always rediscover what it
// is managing, including after a restart that lost every in-memory handle.
//
// Deliberately not here: generation rotation, network and subnet management,
// egress grants, and image GC. Each layers on this loop in its own issue.
package reconciler

import (
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// Label keys on every container this package manages. LabelManaged is the
// list-by-label discovery key — anything carrying it is Gleipnir's to converge,
// and anything without it is invisible to this loop, which is what keeps an
// operator's own containers (and manual-mode plugin containers) safe.
const (
	LabelManaged     = "gleipnir.managed"
	LabelInstance    = "gleipnir.plugin.instance"
	LabelConfigHash  = "gleipnir.plugin.config-hash"
	LabelImageDigest = "gleipnir.plugin.image-digest"

	// ManagedValue is the value LabelManaged carries. A constant rather than
	// "true" inline so the discovery query and the create call cannot drift.
	ManagedValue = "true"
)

// ActionKind is the single step a pass decides to take for one instance.
type ActionKind string

const (
	// ActionNone means the instance is already converged, or is in a
	// transient runtime state (restarting, removing) that the next pass
	// should re-read rather than act on.
	ActionNone ActionKind = "none"

	// ActionCreate creates the container. It deliberately does NOT start it —
	// that is the next pass's step. Splitting them keeps every action a single
	// socket write whose result the next pass observes independently.
	ActionCreate ActionKind = "create"

	ActionStart  ActionKind = "start"
	ActionStop   ActionKind = "stop"
	ActionRemove ActionKind = "remove"

	// ActionCreateNetwork creates the instance's dedicated internal network
	// (spec §7). It is its own step ahead of ActionCreate because a container
	// cannot attach to a network that does not exist, and because allocating
	// the subnet is a write whose result the next pass should observe rather
	// than assume.
	ActionCreateNetwork ActionKind = "create_network"

	// ActionRemoveNetwork tears the network down and returns its subnet to the
	// pool. It runs only after the instance's container is gone — removing a
	// network still in use fails at the socket, and a subnet released while a
	// container still holds addresses in it could be handed to another
	// instance.
	ActionRemoveNetwork ActionKind = "remove_network"

	// ActionAttachSelf joins Gleipnir's own container to an instance's
	// network, once that network exists and before the plugin container
	// itself is created (spec §7: Gleipnir must reach the instance's gateway
	// and container IPs, which under rootless Podman it can only do from
	// inside the network). It is its own step, after ActionCreateNetwork and
	// before ActionCreate, for the same reason those two are split: a socket
	// write whose result the next pass should observe independently.
	ActionAttachSelf ActionKind = "attach_self"

	// ActionDetachSelf leaves an instance's network before it is removed.
	// Symmetric with ActionAttachSelf: it runs only once the instance's
	// container is already gone, ahead of ActionRemoveNetwork — a network
	// Gleipnir is still attached to is a network still in use, which the
	// runtime would refuse to remove anyway, but detaching first keeps the
	// two steps as independently-observable socket writes rather than
	// hoping removal quietly detaches everything for us.
	ActionDetachSelf ActionKind = "detach_self"

	// ActionDriftDetected reports that a running container no longer matches
	// its desired image digest or config hash. The core loop does NOT act on
	// it: replacing a running container is a generation rotation
	// (start-new → health-gate → switch → drain → stop), which is its own
	// issue. Reporting it keeps the drift visible instead of silently
	// tolerated.
	ActionDriftDetected ActionKind = "drift_detected"

	// ActionBeginFirstGeneration mints an instance's generation 1 (a pending
	// row with a fresh instance token) ahead of its very first container.
	// It is the core loop's ONLY generation-aware step: everything from the
	// container's create-and-start through its health gate and its switch to
	// active is ReconcileRotations' job, because that is exactly the sequence
	// a later rotation needs too (rotation.go's package doc) — first boot
	// gets it for free by minting the row rotation already knows how to carry
	// the rest of the way.
	ActionBeginFirstGeneration ActionKind = "begin_first_generation"
)

// selfAttachState is what planFor needs to know about Gleipnir's own
// self-attach to an instance's network, however that was determined.
// Enabled is false when the reconciler has no self container ID configured
// (Gleipnir is not running in a container) — in that case attach/detach is
// never planned, which is also correct for host-process mode, since that mode
// is unsupported once plugins are in play at all.
type selfAttachState struct {
	Enabled  bool
	Attached bool
	// Unknown is true when self-attach IS configured but this pass could not
	// confirm attachment status — Gleipnir's own Inspect call failed (#1021
	// review item 3b). Attached is always false alongside Unknown (there was
	// no successful Inspect to set it from), but the two read differently to
	// planFor: "not attached" says go ahead and attach; "unknown" says wait
	// for a pass that can tell, because creating a plugin container — or
	// re-issuing an attach the daemon might refuse as a duplicate — without
	// knowing the real state risks either outcome.
	Unknown bool
}

// Action is a planned step plus the container it applies to.
type Action struct {
	Kind       ActionKind
	InstanceID string
	// ContainerID is set for every action against an existing container
	// (start, stop, remove, drift). Empty for create.
	ContainerID container.ContainerID
	// Reason is a short operator-facing explanation, carried into the
	// published event and the log line.
	Reason string
}

// Desired state vocabulary, matching the plugin_containers.desired_state CHECK
// constraint.
const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
)

// GenerationState is what planFor needs to know about an instance's
// generation tracking, without touching the database itself.
type GenerationState int

const (
	// GenerationTrackingDisabled means this Reconciler has no Rotations store
	// configured — the legacy fallback some callers (and most of this
	// package's own tests) still use. planFor behaves exactly as it did
	// before generation minting existed: a missing container is a plain
	// ActionCreate, and an existing one is planForExisting's business as
	// usual. ActionBeginFirstGeneration is never reachable in this state.
	GenerationTrackingDisabled GenerationState = iota

	// GenerationMissing means tracking is enabled and this instance has no
	// live generation yet: its very first container must not be created
	// until generation 1 exists to authenticate it.
	GenerationMissing

	// GenerationLive means tracking is enabled and a live generation already
	// exists. ReconcileRotations owns this instance's container lifecycle end
	// to end from here — create, start, health gate, switch, drain, retire —
	// and the core loop must not act on it at all, whatever `observed` shows.
	// That is not merely tidy separation: during a rotation two containers
	// can carry the same instance label at once (the superseded generation
	// draining alongside the new one starting), and the core loop's
	// single-container-per-instance bookkeeping has no way to tell which is
	// which.
	GenerationLive
)

// planFor decides the one step to take for a single instance. It is a pure
// function of (desired row, observed container, network present, generation
// state) so the whole convergence table is testable without a runtime or a
// store: desired == nil means no desired-state row exists (an orphan),
// observed == nil means no container carries this instance's label, and
// hasNetwork reports whether the instance's dedicated internal network
// already exists.
//
// Exactly one step is returned even when several are needed. A container that
// must be created and then started takes two passes, and a running orphan
// takes two more (stop, then remove) — the loop converges over N passes rather
// than trying to drive a sequence to completion inside one.
func planFor(desired *db.PluginContainer, observed *container.ContainerInfo, hasNetwork bool, gen GenerationState, self selfAttachState) Action {
	switch {
	case desired == nil && observed == nil:
		// No container and no desired row. A network may still be left behind
		// by an instance whose container is already gone — that is the second
		// half of the teardown, and the only case this branch is reachable for.
		if hasNetwork {
			if self.Enabled && self.Attached {
				return Action{Kind: ActionDetachSelf, Reason: "orphan: leaving this instance's network before it is removed"}
			}
			return Action{Kind: ActionRemoveNetwork, Reason: "orphan: no desired-state row for this network"}
		}
		return Action{Kind: ActionNone}

	case desired == nil:
		// An orphan: a container we manage with no desired-state row behind
		// it. Stop it before removing it — a running container removed by
		// force gets no chance to shut down cleanly. Whatever generation it
		// belonged to is gone along with the instance, so generation state
		// plays no part here.
		if isRunning(observed.State) {
			return Action{
				Kind:        ActionStop,
				InstanceID:  observed.Labels[LabelInstance],
				ContainerID: observed.ID,
				Reason:      "orphan: no desired-state row for this container",
			}
		}
		return Action{
			Kind:        ActionRemove,
			InstanceID:  observed.Labels[LabelInstance],
			ContainerID: observed.ID,
			Reason:      "orphan: no desired-state row for this container",
		}

	case gen == GenerationLive:
		// ReconcileRotations owns this instance's container lifecycle end to
		// end from here (create, start, health gate, switch, drain, retire),
		// but network MEMBERSHIP is the core loop's job regardless of
		// generation state (#958 combination review): rotation never
		// touches self-attach, so an unconditional ActionNone here would
		// mean a recreated Gleipnir (a fresh container ID, so Attached
		// starts false again) never rejoins a live instance's network, and
		// an operator-stopped live instance is never detached from. These
		// are exactly planForExisting's own self-attach checks, applied
		// before the lifecycle handoff rather than instead of it — a
		// generation-tracked instance's desired row still carries the same
		// DesiredRunning/DesiredStopped meaning planForExisting reads.
		if desired.DesiredState == DesiredRunning {
			if self.Enabled && !self.Unknown && !self.Attached {
				return Action{
					Kind:       ActionAttachSelf,
					InstanceID: desired.PluginInstanceID,
					Reason:     "gleipnir has not joined this instance's network yet",
				}
			}
		} else if self.Enabled && self.Attached {
			return Action{
				Kind:       ActionDetachSelf,
				InstanceID: desired.PluginInstanceID,
				Reason:     "instance desired stopped; leaving its network",
			}
		}
		return Action{Kind: ActionNone, InstanceID: desired.PluginInstanceID}

	// Once generation tracking is enabled (the branch above already disposed
	// of the case where a live generation exists), the core loop must NEVER
	// adopt a container carrying a generation label -- ownership of every
	// such container belongs to ReconcileRotations end to end, never to this
	// loop's plain start/stop bookkeeping (security review, #955 finding 1).
	// With no live generation for this instance, a generation-labelled
	// container here is a leftover from an attempt that never became live: a
	// create that succeeded but whose generation then failed (lost token,
	// health-gate abort) before anything claimed the container. It is an
	// orphan exactly like a desired-row-less container is -- stop it, then
	// remove it -- and once it is gone, ActionBeginFirstGeneration mints the
	// next attempt rather than this loop mistaking it for something to start.
	case gen != GenerationTrackingDisabled && observed != nil && observed.Labels[LabelGeneration] != "":
		if isRunning(observed.State) {
			return Action{
				Kind:        ActionStop,
				InstanceID:  desired.PluginInstanceID,
				ContainerID: observed.ID,
				Reason:      "orphaned generation container: no live generation claims it",
			}
		}
		return Action{
			Kind:        ActionRemove,
			InstanceID:  desired.PluginInstanceID,
			ContainerID: observed.ID,
			Reason:      "orphaned generation container: no live generation claims it",
		}

	case observed == nil:
		// Desired but absent. Only create what is meant to run: creating a
		// container purely to leave it stopped would be a socket write with no
		// converging effect.
		if desired.DesiredState != DesiredRunning {
			// A stopped instance is one Gleipnir has no need to be attached to
			// (#958 finding 7) — independent of whether its container exists at
			// all right now. Detaching here, rather than waiting for the
			// desired row to disappear entirely, is what makes self-attach a
			// property of "is this instance meant to be running", not of the
			// container's own transient lifecycle.
			if hasNetwork && self.Enabled && self.Attached {
				return Action{Kind: ActionDetachSelf, InstanceID: desired.PluginInstanceID, Reason: "instance desired stopped; leaving its network"}
			}
			return Action{Kind: ActionNone, InstanceID: desired.PluginInstanceID}
		}
		// The network comes first — a container cannot attach to one that does
		// not exist yet.
		if !hasNetwork {
			return Action{
				Kind:       ActionCreateNetwork,
				InstanceID: desired.PluginInstanceID,
				Reason:     "instance has no dedicated internal network yet",
			}
		}
		// Gleipnir joins next, before the plugin container exists — it must be
		// able to reach the instance the moment it starts, and under rootless
		// Podman it can only do that from inside the network (spec §7). This
		// also gates ActionBeginFirstGeneration below, not just ActionCreate:
		// a generation's container lands on the same instance network, so the
		// hold "don't create while self status is unknown" has to cover
		// minting generation 1 too, or the very first container a tracked
		// instance ever gets could still come up before Gleipnir can reach it.
		if self.Enabled {
			if self.Unknown {
				// #1021 review item 3b: this pass could not confirm whether
				// Gleipnir is already attached. Creating the container
				// anyway could mean it comes up before Gleipnir can reach
				// it, and attaching anyway could be a duplicate the daemon
				// refuses — wait for a pass that can actually tell.
				return Action{
					Kind:       ActionNone,
					InstanceID: desired.PluginInstanceID,
					Reason:     "gleipnir's self-attach status is unknown this pass (inspect failed); retrying rather than creating without confirming attachment",
				}
			}
			if !self.Attached {
				return Action{
					Kind:       ActionAttachSelf,
					InstanceID: desired.PluginInstanceID,
					Reason:     "gleipnir has not joined this instance's network yet",
				}
			}
		}
		if gen == GenerationMissing {
			return Action{
				Kind:       ActionBeginFirstGeneration,
				InstanceID: desired.PluginInstanceID,
				Reason:     "instance has no generation yet; minting generation 1",
			}
		}
		return Action{
			Kind:       ActionCreate,
			InstanceID: desired.PluginInstanceID,
			Reason:     "desired container does not exist",
		}

	default:
		return planForExisting(desired, observed, self)
	}
}

// planForExisting handles the both-sides-present case.
func planForExisting(desired *db.PluginContainer, observed *container.ContainerInfo, self selfAttachState) Action {
	act := Action{InstanceID: desired.PluginInstanceID, ContainerID: observed.ID}

	// Self-attach/detach is independent of the container's own lifecycle
	// (#958 finding 7), and checked before drift/lifecycle: a Gleipnir that
	// was just recreated (a fresh container ID) must rejoin a network whose
	// plugin is ALREADY running and fully converged, and an instance the
	// operator stopped is one Gleipnir has no reason to stay attached to —
	// neither of those is a fact about whether the plugin's own container
	// needs a lifecycle action this pass.
	if desired.DesiredState == DesiredRunning {
		// Unknown skips the attach decision entirely rather than guessing
		// either way (#1021 review item 3b) — the container already exists
		// and needs no self-attach action to keep running, so falling
		// through to ordinary lifecycle handling below is safe here in a way
		// it is not for a container that does not exist yet.
		if self.Enabled && !self.Unknown && !self.Attached {
			act.Kind = ActionAttachSelf
			act.Reason = "gleipnir has not joined this instance's network yet"
			return act
		}
	} else if self.Enabled && self.Attached {
		act.Kind = ActionDetachSelf
		act.Reason = "instance desired stopped; leaving its network"
		return act
	}

	// Drift is checked before lifecycle: a container running the wrong image
	// is not "converged" just because it is running, and reporting it as
	// started-and-fine would hide the divergence rotation exists to fix.
	if drift := driftReason(desired, observed); drift != "" {
		act.Kind = ActionDriftDetected
		act.Reason = drift
		return act
	}

	if desired.DesiredState == DesiredStopped {
		if isRunning(observed.State) {
			act.Kind = ActionStop
			act.Reason = "desired state is stopped"
			return act
		}
		act.Kind = ActionNone
		return act
	}

	switch observed.State {
	case container.ContainerStateRunning:
		act.Kind = ActionNone
	case container.ContainerStateCreated, container.ContainerStateExited, container.ContainerStateDead:
		act.Kind = ActionStart
		act.Reason = "desired state is running; container is " + string(observed.State)
	default:
		// paused, restarting, removing — transient states the runtime is
		// already moving through. Acting on them races the runtime; the next
		// pass reads the settled state.
		act.Kind = ActionNone
		act.Reason = "transient runtime state " + string(observed.State)
	}
	return act
}

// driftReason reports how a container diverges from its desired row, or "" when
// it matches. Both inputs come from labels written at create time, so this
// comparison needs no socket call beyond the list the pass already did.
func driftReason(desired *db.PluginContainer, observed *container.ContainerInfo) string {
	if got := observed.Labels[LabelImageDigest]; got != desired.ImageDigest {
		return "image digest drift: running " + got + ", desired " + desired.ImageDigest
	}
	if got := observed.Labels[LabelConfigHash]; got != desired.ConfigHash {
		return "config hash drift: running " + got + ", desired " + desired.ConfigHash
	}
	return ""
}

// isRunning reports whether a container is doing work the runtime would have
// to interrupt. Restarting counts: the runtime is actively bringing it back up.
func isRunning(state container.ContainerState) bool {
	switch state {
	case container.ContainerStateRunning, container.ContainerStateRestarting, container.ContainerStatePaused:
		return true
	}
	return false
}
