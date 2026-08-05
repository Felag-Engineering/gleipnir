package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// EventPassCompleted is published after every reconcile pass, converged or not.
// Tests and the UI synchronize on it rather than polling for side effects; it
// is the only signal that a pass has finished, so it fires even when the pass
// took no action.
const EventPassCompleted = "container.reconcile_pass"

// defaultInterval is the periodic pass cadence when none is configured. The
// loop is level-triggered, so this is a safety net for drift nobody announced
// — the kick channel is what makes an intentional change converge promptly.
const defaultInterval = 30 * time.Second

// stopTimeout bounds a graceful container stop before the runtime kills it.
const stopTimeout = 10 * time.Second

// Store is the narrow read side of the desired state this loop needs.
// *db.Queries satisfies it; nothing here writes to the desired-state tables,
// because desired state is an input to reconciliation, never an output of it.
type Store interface {
	ListPluginContainers(ctx context.Context) ([]db.PluginContainer, error)
}

// Config holds the Reconciler's dependencies.
type Config struct {
	Runtime container.Runtime
	Store   Store

	// Posture decides whether this loop touches the socket at all. In
	// container.PostureManual the operator owns the containers and Gleipnir
	// only observes, so the loop is inert — see Start.
	Posture container.Posture

	// Interval is the periodic pass cadence; zero uses defaultInterval.
	Interval time.Duration

	// Publisher receives EventPassCompleted after each pass. Optional.
	Publisher event.Publisher

	// Subnets allocates each instance its dedicated /24 (spec §7). Required
	// for the network lifecycle: without it the loop can converge containers
	// on networks something else created, but cannot create one itself.
	Subnets *SubnetAllocator

	// NetworkNameFor overrides how an instance's network name is derived.
	// Optional — the default is derived from the desired row, and per-instance
	// network creation is a separate concern that owns the real naming.
	NetworkNameFor func(desired db.PluginContainer) string
}

// PassResult summarizes one reconcile pass.
type PassResult struct {
	// Desired and Observed are the two sides of the diff, for the operator
	// reading a log line or event.
	Desired  int `json:"desired"`
	Observed int `json:"observed"`

	// Actions are the steps taken (or reported, for drift). At most one per
	// instance per pass.
	Actions []Action `json:"-"`

	// Errors counts actions that failed. A failed action is not fatal: the
	// next pass re-reads the world and tries again from whatever state the
	// failure left behind.
	Errors int `json:"errors"`

	// Converged is true when the pass found nothing to do. Idempotency means
	// a converged pass performs zero socket writes.
	Converged bool `json:"converged"`
}

// Reconciler runs the level-triggered convergence loop.
type Reconciler struct {
	runtime   container.Runtime
	store     Store
	posture   container.Posture
	interval  time.Duration
	publisher event.Publisher
	subnets   *SubnetAllocator
	networkFn func(db.PluginContainer) string

	// kick carries a nudge from a desired-state write. Buffered at 1 and sent
	// non-blocking: a burst of writes coalesces into one extra pass, which is
	// exactly right for a level-triggered loop — it re-reads everything anyway.
	kick chan struct{}

	wg         sync.WaitGroup
	mu         sync.Mutex
	rootCancel context.CancelFunc
}

// New constructs a Reconciler.
func New(cfg Config) (*Reconciler, error) {
	if cfg.Runtime == nil {
		return nil, fmt.Errorf("reconciler: Runtime is required")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("reconciler: Store is required")
	}

	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	networkFn := cfg.NetworkNameFor
	if networkFn == nil {
		networkFn = defaultNetworkName
	}

	return &Reconciler{
		runtime:   cfg.Runtime,
		store:     cfg.Store,
		posture:   cfg.Posture,
		interval:  interval,
		publisher: cfg.Publisher,
		subnets:   cfg.Subnets,
		networkFn: networkFn,
		kick:      make(chan struct{}, 1),
	}, nil
}

// Start runs the boot-time convergence pass and then the periodic loop.
//
// The boot pass is synchronous on purpose: it completes before Start returns,
// so a caller can treat "Start returned" as "the substrate has been converged
// once" and only then report the plugin subsystem ready. Its error is
// returned; the periodic loop that follows never fails the process, because a
// transient socket error is something the next pass retries rather than a
// reason to refuse to run.
//
// In manual posture the loop does not start at all. The operator declares
// those containers in their own compose file, and a level-triggered loop with
// no desired-state rows for them would read them as orphans and remove them —
// so manual mode is enforced by not running, not by hoping the diff is empty.
func (r *Reconciler) Start(ctx context.Context) error {
	if r.posture == container.PostureManual {
		logctx.Logger(ctx).InfoContext(ctx, "reconciler: manual posture; the loop will not touch the container socket")
		return nil
	}

	rootCtx, rootCancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.rootCancel = rootCancel
	r.mu.Unlock()

	if _, err := r.ReconcileOnce(rootCtx); err != nil {
		rootCancel()
		return fmt.Errorf("boot convergence pass: %w", err)
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.loop(rootCtx)
	}()
	return nil
}

// Kick nudges the loop to run a pass now. Safe to call from any goroutine and
// from a request path: the send is non-blocking, so a caller never waits on
// reconciliation, and a coalesced kick loses nothing because the next pass
// re-reads the whole desired set regardless.
func (r *Reconciler) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Wait blocks until the loop goroutine has exited. Call after cancelling the
// context passed to Start (or after Stop) to drain cleanly during shutdown.
func (r *Reconciler) Wait() { r.wg.Wait() }

// Stop cancels the loop and waits for it to exit. Equivalent to cancelling the
// context passed to Start; provided for callers that do not own that context.
// Safe when Start was never called.
func (r *Reconciler) Stop() {
	r.mu.Lock()
	cancel := r.rootCancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.wg.Wait()
}

// loop runs passes on the interval or on a kick, until ctx is cancelled.
func (r *Reconciler) loop(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.kick:
		}

		if _, err := r.ReconcileOnce(ctx); err != nil {
			// Never fatal. The loop's whole contract is that the next pass
			// reads the world fresh, so a failed pass costs latency, not
			// correctness.
			if ctx.Err() == nil {
				logctx.Logger(ctx).ErrorContext(ctx, "reconciler: pass failed", "err", err)
			}
		}
	}
}

// ReconcileOnce runs a single pass: list the real containers by label, diff
// them against the desired-state rows, and take one converging step per
// instance.
//
// It returns an error only when the pass could not be attempted (the desired
// state or the container list was unreadable). An individual action that fails
// is counted in PassResult.Errors and logged — the next pass sees whatever
// state the failure left and plans again from there.
func (r *Reconciler) ReconcileOnce(ctx context.Context) (PassResult, error) {
	desired, err := r.store.ListPluginContainers(ctx)
	if err != nil {
		return PassResult{}, fmt.Errorf("listing desired containers: %w", err)
	}

	observed, err := r.runtime.ListByLabel(ctx, LabelManaged, ManagedValue)
	if err != nil {
		return PassResult{}, fmt.Errorf("listing managed containers: %w", err)
	}

	networks, err := r.runtime.ListNetworksByLabel(ctx, LabelManaged, ManagedValue)
	if err != nil {
		return PassResult{}, fmt.Errorf("listing managed networks: %w", err)
	}

	result := PassResult{Desired: len(desired), Observed: len(observed)}
	for _, action := range planPass(desired, observed, networks) {
		if action.Kind == ActionNone {
			continue
		}
		result.Actions = append(result.Actions, action)
		if action.Kind == ActionDriftDetected {
			// Reported, not acted on: replacing a running container is a
			// rotation, which this loop does not perform.
			logctx.Logger(ctx).WarnContext(ctx, "reconciler: container drift",
				"instance_id", action.InstanceID, "reason", action.Reason)
			continue
		}
		if err := r.apply(ctx, action, desired); err != nil {
			result.Errors++
			logctx.Logger(ctx).ErrorContext(ctx, "reconciler: action failed",
				"action", string(action.Kind), "instance_id", action.InstanceID, "err", err)
		}
	}

	result.Converged = len(result.Actions) == 0
	r.publishPass(result)
	return result, nil
}

// planPass diffs the two sides and produces at most one action per instance.
// It is separated from ReconcileOnce so the whole convergence table can be
// tested without a runtime or a store.
//
// Observed containers are keyed by their instance label. A managed container
// with no instance label cannot be matched to any desired row, so it is
// planned as an orphan — which is the correct reading: Gleipnir labelled it as
// managed, and nothing claims it.
func planPass(desired []db.PluginContainer, observed []container.ContainerInfo, networks []container.NetworkInfo) []Action {
	byInstance := make(map[string]*container.ContainerInfo, len(observed))
	var unlabelled []container.ContainerInfo
	for i := range observed {
		id := observed[i].Labels[LabelInstance]
		if id == "" {
			unlabelled = append(unlabelled, observed[i])
			continue
		}
		byInstance[id] = &observed[i]
	}

	networkByInstance := make(map[string]container.NetworkInfo, len(networks))
	for _, n := range networks {
		if id := n.Labels[LabelInstance]; id != "" {
			networkByInstance[id] = n
		}
	}

	actions := make([]Action, 0, len(desired)+len(observed)+len(networks))
	seen := make(map[string]bool, len(desired))
	for i := range desired {
		row := &desired[i]
		seen[row.PluginInstanceID] = true
		_, hasNetwork := networkByInstance[row.PluginInstanceID]
		actions = append(actions, planFor(row, byInstance[row.PluginInstanceID], hasNetwork))
	}

	// Everything managed that no desired row claims is an orphan. A container
	// goes first; its network is torn down on a later pass, once the container
	// is actually gone.
	for id, info := range byInstance {
		if !seen[id] {
			_, hasNetwork := networkByInstance[id]
			actions = append(actions, planFor(nil, info, hasNetwork))
		}
	}
	for i := range unlabelled {
		actions = append(actions, planFor(nil, &unlabelled[i], false))
	}

	// A network whose instance has neither a desired row nor a container left
	// is the second half of a teardown.
	for id := range networkByInstance {
		if seen[id] {
			continue
		}
		if _, stillRunning := byInstance[id]; stillRunning {
			continue
		}
		action := planFor(nil, nil, true)
		action.InstanceID = id
		actions = append(actions, action)
	}
	return actions
}

// apply performs one action's socket write.
func (r *Reconciler) apply(ctx context.Context, action Action, desired []db.PluginContainer) error {
	switch action.Kind {
	case ActionCreate:
		row, ok := findDesired(desired, action.InstanceID)
		if !ok {
			// The desired set was read at the top of this pass, so this is
			// unreachable; returning an error rather than panicking keeps a
			// future refactor from turning a bug into a crash.
			return fmt.Errorf("no desired row for instance %q", action.InstanceID)
		}
		id, err := r.runtime.Create(ctx, r.createOptions(row))
		if err != nil {
			return fmt.Errorf("creating container for instance %q: %w", action.InstanceID, err)
		}
		logctx.Logger(ctx).InfoContext(ctx, "reconciler: created container",
			"instance_id", action.InstanceID, "container_id", string(id))
		return nil

	case ActionStart:
		if err := r.runtime.Start(ctx, action.ContainerID); err != nil {
			return fmt.Errorf("starting container %q: %w", action.ContainerID, err)
		}
		return nil

	case ActionStop:
		if err := r.runtime.Stop(ctx, action.ContainerID, stopTimeout); err != nil {
			return fmt.Errorf("stopping container %q: %w", action.ContainerID, err)
		}
		return nil

	case ActionCreateNetwork:
		row, ok := findDesired(desired, action.InstanceID)
		if !ok {
			return fmt.Errorf("no desired row for instance %q", action.InstanceID)
		}
		return r.createNetwork(ctx, row)

	case ActionRemoveNetwork:
		return r.removeNetwork(ctx, action.InstanceID)

	case ActionRemove:
		// force=false deliberately: a container this pass believes is stopped
		// but the runtime still considers running means the two disagree, and
		// forcing the removal would destroy the evidence. The next pass
		// re-reads and stops it properly.
		if err := r.runtime.Remove(ctx, action.ContainerID, false); err != nil {
			return fmt.Errorf("removing container %q: %w", action.ContainerID, err)
		}
		return nil

	default:
		return fmt.Errorf("unhandled action kind %q", action.Kind)
	}
}

// createNetwork allocates the instance's subnet and creates its dedicated
// internal network.
//
// Allocation happens first and is idempotent, so a pass that creates the subnet
// row and then fails at the socket leaves an allocation the next pass reuses
// rather than a leaked slot. The reverse order — create the network, then
// record the subnet — could leave a network nothing knows about.
func (r *Reconciler) createNetwork(ctx context.Context, row db.PluginContainer) error {
	if r.subnets == nil {
		return fmt.Errorf("no subnet allocator configured; cannot create a network for instance %q", row.PluginInstanceID)
	}

	subnet, err := r.subnets.Allocate(ctx, row.PluginInstanceID)
	if err != nil {
		return fmt.Errorf("allocating subnet for instance %q: %w", row.PluginInstanceID, err)
	}

	name := r.networkFn(row)
	id, err := r.runtime.CreateNetwork(ctx, container.NetworkOptions{
		Name: name,
		Labels: map[string]string{
			LabelManaged:  ManagedValue,
			LabelInstance: row.PluginInstanceID,
		},
		Subnet: subnet.String(),
		// Internal is the default-deny the egress-grants work builds on: a
		// plugin container has no route off its own network until something
		// deliberately gives it one.
		Internal: true,
	})
	if err != nil {
		return fmt.Errorf("creating network %q for instance %q: %w", name, row.PluginInstanceID, err)
	}

	logctx.Logger(ctx).InfoContext(ctx, "reconciler: created instance network",
		"instance_id", row.PluginInstanceID, "network", name, "network_id", string(id), "subnet", subnet.String())
	return nil
}

// removeNetwork tears down an instance's network and returns its subnet to the
// pool.
//
// The subnet is released only after the network is gone. Releasing first would
// let another instance be handed a subnet that a still-existing network is
// using, which the runtime would reject at create time — turning a clean
// teardown into a stuck one.
func (r *Reconciler) removeNetwork(ctx context.Context, instanceID string) error {
	networks, err := r.runtime.ListNetworksByLabel(ctx, LabelInstance, instanceID)
	if err != nil {
		return fmt.Errorf("listing networks for instance %q: %w", instanceID, err)
	}
	for _, n := range networks {
		if err := r.runtime.RemoveNetwork(ctx, n.ID); err != nil {
			return fmt.Errorf("removing network %q: %w", n.Name, err)
		}
	}

	if r.subnets != nil {
		if err := r.subnets.Release(ctx, instanceID); err != nil {
			return err
		}
	}
	logctx.Logger(ctx).InfoContext(ctx, "reconciler: removed instance network", "instance_id", instanceID)
	return nil
}

// createOptions builds the create request for a desired row. Every field the
// self-constraint cares about (no extra mounts, no privileges, an internal
// per-instance network) is set here and validated inside Runtime.Create —
// this function cannot opt out of that check.
func (r *Reconciler) createOptions(row db.PluginContainer) container.CreateOptions {
	opts := container.CreateOptions{
		Name:  containerName(row.PluginInstanceID),
		Image: pinnedImage(row.ImageRef, row.ImageDigest),
		Labels: map[string]string{
			LabelManaged:     ManagedValue,
			LabelInstance:    row.PluginInstanceID,
			LabelConfigHash:  row.ConfigHash,
			LabelImageDigest: row.ImageDigest,
		},
		Volume: container.VolumeMount{
			Name:      volumeName(row.PluginInstanceID),
			MountPath: instanceVolumeMountPath,
		},
		Network: r.networkFn(row),
	}
	if row.MemoryLimitBytes != nil {
		opts.Resources.MemoryBytes = *row.MemoryLimitBytes
	}
	if row.CpuLimitMillicores != nil {
		// The runtime speaks nano-CPUs (1e9 == one core); the desired row
		// stores millicores (1000 == one core).
		opts.Resources.NanoCPUs = *row.CpuLimitMillicores * 1_000_000
	}
	return opts
}

// pinnedImage returns the digest-pinned reference to run (spec §7:
// "digest-pinned images inside a signed tarball"). A reference that already
// carries a digest is used as-is; otherwise the digest is appended. A row with
// no digest falls back to the bare reference — the loader is what guarantees a
// digest is recorded, and refusing to run here would turn its omission into a
// silent no-op instead of a visible one.
func pinnedImage(ref, digest string) string {
	if digest == "" || strings.Contains(ref, "@") {
		return ref
	}
	return ref + "@" + digest
}

const instanceVolumeMountPath = "/data"

func containerName(instanceID string) string { return "gleipnir-plugin-" + instanceID }
func volumeName(instanceID string) string    { return "gleipnir-plugin-" + instanceID + "-data" }

// defaultNetworkName derives an instance's network name from the desired row.
// The row's own network_name wins when set; the fallback keeps a row written
// before network management existed from failing the self-constraint's
// "must attach to a network" rule.
func defaultNetworkName(row db.PluginContainer) string {
	if row.NetworkName != "" {
		return row.NetworkName
	}
	return "gleipnir-plugin-" + row.PluginInstanceID
}

func findDesired(rows []db.PluginContainer, instanceID string) (db.PluginContainer, bool) {
	for _, row := range rows {
		if row.PluginInstanceID == instanceID {
			return row, true
		}
	}
	return db.PluginContainer{}, false
}

// publishPass emits EventPassCompleted. Best-effort: a publisher failure must
// never affect convergence, and a marshal failure is a logging problem.
func (r *Reconciler) publishPass(result PassResult) {
	if r.publisher == nil {
		return
	}
	payload := struct {
		PassResult
		ActionCount int `json:"action_count"`
	}{PassResult: result, ActionCount: len(result.Actions)}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("reconciler: marshalling pass event failed", "err", err)
		return
	}
	r.publisher.Publish(EventPassCompleted, data)
}
