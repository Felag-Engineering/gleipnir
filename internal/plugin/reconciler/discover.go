package reconciler

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// Manual-mode discovery (ADR-056 spec §7 socket posture 3; issue #817).
//
// Manual mode is a supported first-class configuration, not a degraded one.
// The operator declares plugin containers in their own compose file; Gleipnir
// discovers them by label, health-checks them, and wires them into the MCP
// client — and never writes to the socket. The "never writes" part is enforced
// by container.ReadOnlyRuntime, not by this file remembering to behave: a
// posture guaranteed by caller discipline is a posture one refactor away from
// being untrue.
//
// What this file adds is the other half: saying clearly what Gleipnir found.
// The failure modes here are all silent by nature — a container that is simply
// absent, or present under the wrong label — so each one gets a named state
// rather than an instance that is quietly never routed to.

// DiscoveryState is what discovery concluded about one instance or container.
type DiscoveryState string

const (
	// DiscoveryMatched — an installed instance has exactly one container
	// carrying its label, at the expected generation.
	DiscoveryMatched DiscoveryState = "matched"

	// DiscoveryNotFound — the instance is installed but no container carries
	// its label. The operator declared a plugin and did not run it (or named
	// it differently), and without this state the instance would sit
	// permanently unhealthy with nothing saying why.
	DiscoveryNotFound DiscoveryState = "declared_but_not_found"

	// DiscoveryNotInstalled — a container carries a Gleipnir instance label
	// that matches no installed instance. Usually a stale container from a
	// removed plugin, or a typo'd label. Gleipnir will not touch it — in
	// manual mode nothing is Gleipnir's to remove — so naming it is the only
	// thing that can be done about it.
	DiscoveryNotInstalled DiscoveryState = "found_but_not_installed"

	// DiscoveryWrongGeneration — the container is labelled for a generation
	// other than the one the desired row expects. The operator updated the
	// desired image and has not re-created the container, so the plugin
	// running is not the plugin that was approved.
	DiscoveryWrongGeneration DiscoveryState = "wrong_generation"

	// DiscoveryAmbiguous — several containers carry one instance's label.
	// Gleipnir will not guess which is authoritative: routing to the wrong one
	// would be indistinguishable from routing to the right one until something
	// went wrong.
	DiscoveryAmbiguous DiscoveryState = "ambiguous"
)

// Discovered is one discovery conclusion.
type Discovered struct {
	InstanceID string
	State      DiscoveryState

	// ContainerID is set when a container was found. Empty for
	// DiscoveryNotFound.
	ContainerID container.ContainerID

	// Detail is the operator-facing explanation, suitable for an instance's
	// health detail.
	Detail string

	// Health is the runtime healthcheck verdict, when a container was found.
	Health string
}

// Discover diffs installed instances against labelled containers.
//
// Pure over its inputs, so the whole state table is testable without a socket —
// the same reason planFor is pure in the managed loop.
//
// The result is sorted by instance ID so a pass produces a stable order; an
// operator comparing two passes should see a diff only where something changed.
func Discover(desired []db.PluginContainer, observed []container.ContainerInfo) []Discovered {
	byInstance := make(map[string][]container.ContainerInfo, len(observed))
	for _, info := range observed {
		instanceID := info.Labels[LabelInstance]
		if instanceID == "" {
			// No instance label: not a container claiming to be ours, whatever
			// else it is. Silently ignored rather than reported — the label
			// selector already scoped the list, and anything without the label
			// is somebody else's business.
			continue
		}
		byInstance[instanceID] = append(byInstance[instanceID], info)
	}

	out := make([]Discovered, 0, len(desired)+len(byInstance))
	seen := make(map[string]struct{}, len(desired))

	for _, row := range desired {
		seen[row.PluginInstanceID] = struct{}{}
		out = append(out, discoverOne(row, byInstance[row.PluginInstanceID]))
	}

	// Containers labelled for instances that are not installed.
	for instanceID, infos := range byInstance {
		if _, ok := seen[instanceID]; ok {
			continue
		}
		out = append(out, Discovered{
			InstanceID:  instanceID,
			State:       DiscoveryNotInstalled,
			ContainerID: infos[0].ID,
			Health:      infos[0].Health,
			Detail: fmt.Sprintf("container %s is labelled for instance %s, which is not installed here",
				infos[0].Name, instanceID),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].InstanceID != out[j].InstanceID {
			return out[i].InstanceID < out[j].InstanceID
		}
		return out[i].ContainerID < out[j].ContainerID
	})
	return out
}

// discoverOne concludes about a single desired instance.
func discoverOne(row db.PluginContainer, infos []container.ContainerInfo) Discovered {
	result := Discovered{InstanceID: row.PluginInstanceID}

	switch len(infos) {
	case 0:
		result.State = DiscoveryNotFound
		result.Detail = fmt.Sprintf(
			"no container carries the label %s=%s; declare one in your compose file or check the label",
			LabelInstance, row.PluginInstanceID)
		return result
	case 1:
	default:
		// Picking one would be a guess, and a wrong guess is invisible until
		// something goes wrong.
		result.State = DiscoveryAmbiguous
		result.Detail = fmt.Sprintf("%d containers carry the label %s=%s; exactly one must",
			len(infos), LabelInstance, row.PluginInstanceID)
		return result
	}

	info := infos[0]
	result.ContainerID = info.ID
	result.Health = info.Health

	if want, ok := expectedGeneration(row); ok {
		if got := info.Labels[LabelGeneration]; got != want {
			result.State = DiscoveryWrongGeneration
			result.Detail = fmt.Sprintf(
				"container %s is labelled generation %q but this instance expects %q; the running plugin is not the one that was approved",
				info.Name, got, want)
			return result
		}
	}

	result.State = DiscoveryMatched
	result.Detail = fmt.Sprintf("container %s", info.Name)
	return result
}

// expectedGeneration reads the generation the desired row expects, if the row
// tracks one.
//
// A row that does not is not an error: generation labelling belongs to the
// rotation machinery, and a manual-mode operator who never rotates has nothing
// to disagree with. Only a row that DOES name a generation can be contradicted.
func expectedGeneration(row db.PluginContainer) (string, bool) {
	if row.Version <= 0 {
		return "", false
	}
	return strconv.FormatInt(row.Version, 10), true
}

// DiscoverPass runs one manual-mode discovery pass.
//
// It performs exactly two socket READS — a list by label and nothing else —
// and no writes at all. The read-only posture is enforced by the runtime
// wrapper; this method simply never asks for anything it could not have.
func (r *Reconciler) DiscoverPass(ctx context.Context) ([]Discovered, error) {
	desired, err := r.store.ListPluginContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconciler: listing desired containers: %w", err)
	}
	observed, err := r.runtime.ListByLabel(ctx, LabelManaged, ManagedValue)
	if err != nil {
		return nil, fmt.Errorf("reconciler: listing labelled containers: %w", err)
	}
	return Discover(desired, observed), nil
}
