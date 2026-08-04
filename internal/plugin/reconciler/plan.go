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

	// ActionDriftDetected reports that a running container no longer matches
	// its desired image digest or config hash. The core loop does NOT act on
	// it: replacing a running container is a generation rotation
	// (start-new → health-gate → switch → drain → stop), which is its own
	// issue. Reporting it keeps the drift visible instead of silently
	// tolerated.
	ActionDriftDetected ActionKind = "drift_detected"
)

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

// planFor decides the one step to take for a single instance. It is a pure
// function of (desired row, observed container) so the whole convergence table
// is testable without a runtime: desired == nil means no desired-state row
// exists (an orphan), observed == nil means no container carries this
// instance's label.
//
// Exactly one step is returned even when several are needed. A container that
// must be created and then started takes two passes, and a running orphan
// takes two more (stop, then remove) — the loop converges over N passes rather
// than trying to drive a sequence to completion inside one.
func planFor(desired *db.PluginContainer, observed *container.ContainerInfo) Action {
	switch {
	case desired == nil && observed == nil:
		// Nothing on either side; not reachable through Reconcile's diff, but
		// defined so the function is total.
		return Action{Kind: ActionNone}

	case desired == nil:
		// An orphan: a container we manage with no desired-state row behind
		// it. Stop it before removing it — a running container removed by
		// force gets no chance to shut down cleanly.
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

	case observed == nil:
		// Desired but absent. Only create what is meant to run: creating a
		// container purely to leave it stopped would be a socket write with no
		// converging effect.
		if desired.DesiredState == DesiredRunning {
			return Action{
				Kind:       ActionCreate,
				InstanceID: desired.PluginInstanceID,
				Reason:     "desired container does not exist",
			}
		}
		return Action{Kind: ActionNone, InstanceID: desired.PluginInstanceID}

	default:
		return planForExisting(desired, observed)
	}
}

// planForExisting handles the both-sides-present case.
func planForExisting(desired *db.PluginContainer, observed *container.ContainerInfo) Action {
	act := Action{InstanceID: desired.PluginInstanceID, ContainerID: observed.ID}

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
