// Package desiredstate holds the small, stdlib-only vocabulary that both
// internal/plugin/lifecycle and internal/admin need to share: the
// plugin_containers.desired_state values and the ErrPluginNotActive sentinel
// internal/admin maps to 409.
//
// It exists purely to keep internal/admin's import graph small. The full
// internal/plugin/lifecycle package (which owns the actual read/write logic
// in desired.go) imports internal/plugin/loader for its OCI-install adapter,
// which in turn imports internal/plugin/container — pulling the whole v2
// container substrate into admin's production build graph merely to
// reference two constants and one error would be exactly the kind of
// accidental coupling package boundaries exist to prevent. internal/admin
// imports this leaf directly instead; internal/plugin/lifecycle aliases these
// same values rather than redeclaring them, so errors.Is and equality checks
// see identical values on both sides of the boundary.
package desiredstate

import "errors"

// Desired-state values for plugin_containers.desired_state (schema CHECK).
const (
	Running = "running"
	Stopped = "stopped"
)

// ErrPluginNotActive is returned when an operation would make (or keep) a
// plugin_containers row converge toward "running" for a plugin that has not
// passed admin review (still pending_review) or has been removed.
//
// v1 gates subprocess spawn on plugin.Status inside process.Manager.Start, so
// a pending/removed plugin never runs even if an instance row exists for it.
// The reconciler has no such gate: it converges ANY plugin_containers row it
// finds toward "running", so this check has to live at every point that could
// create one or flip one back to running — a desired-state row (or a
// re-activation) is itself the thing that would make an un-reviewed plugin run.
var ErrPluginNotActive = errors.New("desiredstate: plugin is not active")
