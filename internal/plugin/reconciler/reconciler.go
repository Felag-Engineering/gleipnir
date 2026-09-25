package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
)

// EventPassCompleted is published after every reconcile pass, converged or not.
// Tests and the UI synchronize on it rather than polling for side effects; it
// is the only signal that a pass has finished, so it fires even when the pass
// took no action.
const EventPassCompleted = "container.reconcile_pass"

// EventRotationPassCompleted is published after every rotation pass, mirroring
// EventPassCompleted for ReconcileOnce. It fires strictly AFTER
// EventPassCompleted within one runPass call (#955 security re-review round 2
// item 1): the two passes are serialized in that order, and a test or caller
// synchronizing on EventPassCompleted alone would observe the core loop's
// half of a cycle before rotation — which may depend on what the core loop
// just wrote (a freshly minted `pending` generation, for instance) — has run.
const EventRotationPassCompleted = "container.reconcile_rotation_pass"

// defaultInterval is the periodic pass cadence when none is configured. The
// loop is level-triggered, so this is a safety net for drift nobody announced
// — the kick channel is what makes an intentional change converge promptly.
const defaultInterval = 30 * time.Second

// stopTimeout bounds a graceful container stop before the runtime kills it.
const stopTimeout = 10 * time.Second

// DefaultHostEndpointPort is the port a plugin's host-endpoint URL (#875)
// points at. A constant rather than an env var: host configuration lives in
// the app, not in environment variables — the env var this issue's
// Config.HostEndpointEnv hook injects is the plugin-side contract (what a
// container is TOLD), which is a different thing from how the host itself is
// configured. If this ever needs to be operator-configurable, it becomes a
// system_settings value, not an env var (#957).
const DefaultHostEndpointPort = 8765

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

	// SelfContainerID is the container ID Gleipnir resolved for itself
	// (container.ResolveSelfContainerID), used to join and leave each
	// instance's network so Gleipnir can reach it — under rootless Podman a
	// host process can reach neither the network's gateway nor a container on
	// it, which is why plugins are unsupported outside a container in the
	// alpha. Empty means Gleipnir is not running in a container: the loop
	// still converges containers and networks, it just never attempts a
	// self-attach.
	SelfContainerID container.ContainerID

	// CheckForwardingDisabled gates self-attach on Gleipnir's own kernel
	// having IP forwarding disabled on both stacks (#958 finding 2). Nil uses
	// container.CheckForwardingDisabled, the real /proc read; tests inject a
	// fixed answer so self-attach behavior does not depend on the sysctls of
	// whatever host happens to run the test.
	CheckForwardingDisabled func() error

	// OperatorAPIGuarded gates self-attach on the operator-facing admin API
	// listener having been wrapped by the network guard that refuses
	// cross-instance traffic (netguard, PR #1029 — not merged as of #958's
	// own landing, so this stays a plain bool rather than importing an
	// opaque marker type from an unmerged package). Without that guard,
	// Gleipnir joining every instance network while its own operator API
	// remains reachable FROM those networks would hand a compromised plugin
	// a path to the admin surface — self-attach's other checks (managed
	// label, Internal, instance match, subnet containment, forwarding
	// disabled) say nothing about that path, because it runs through
	// Gleipnir's OWN container being multi-homed, not through a network
	// Gleipnir joined incorrectly.
	//
	// The caller wiring the reconciler (#962, once it exists) must set this
	// to true ONLY once the netguard listener wrapper is actually in front
	// of the operator listener, never speculatively "because the code
	// compiles" — a true value here that does not reflect a real guard is
	// worse than leaving self-attach disabled, since it reads as an
	// intentional decision instead of a gap. Defaults to false, so a
	// caller that says nothing about it gets self-attach OFF, the same
	// fail-closed default the forwarding check gets.
	OperatorAPIGuarded bool

	// Rotations is the generation-record store. Optional: nil disables
	// generation tracking entirely, and the core loop falls back to plain,
	// tokenless container create/start/stop (the legacy behavior this package
	// had before generation minting existed). Configured, it also drives the
	// core loop's first-boot step (ActionBeginFirstGeneration mints an
	// instance's generation 1 ahead of its first container) in addition to
	// ReconcileRotations.
	Rotations RotationStore

	// HealthGateTimeout and DrainTimeout bound the two rotation waits. Zero
	// uses the package defaults.
	HealthGateTimeout time.Duration
	DrainTimeout      time.Duration

	// NetworkNameFor overrides how an instance's network name is derived.
	// Optional — the default is derived from the desired row, and per-instance
	// network creation is a separate concern that owns the real naming.
	NetworkNameFor func(desired db.PluginContainer) string

	// EgressEnv returns the proxy environment entries a container is created
	// with (#812). Optional: absent means no proxy is pointed at, and on an
	// internal-only network that means the instance reaches nothing — which is
	// the correct default, not a degraded one.
	EgressEnv func(ctx context.Context, instanceID string) []string

	// HostEndpointEnv returns the environment entries pointing a generation's
	// container at the host endpoint (#875) — the URL a plugin's SDK client
	// needs for server→host callbacks. Optional: absent means no host
	// endpoint is configured, which is only correct before the reconciler is
	// wired into main.go. The production implementation (#957, in the v2
	// assembly that composes this package with internal/plugin/hostendpoint)
	// builds the URL from Gleipnir's reserved address on the instance network
	// (egress.GleipnirAddrOf), never the gateway: under rootful Docker the
	// gateway address is the host itself, and a URL pointed there would send
	// a generation's bearer token straight to the host rather than through
	// withGenerationEnv's NO_PROXY-scoped path to it. DefaultHostEndpointPort
	// supplies the port; this Config field is only the hook. Applied,
	// like EgressEnv, after StripProxyEnv — both share the same
	// generation-container create path (createRotationContainer, used for the
	// first generation and every later one alike), so this is the one place
	// either variable is wired in.
	HostEndpointEnv func(ctx context.Context, instanceID string) []string

	// GC is the cleanup store. Required only for ReconcileGC; neither the core
	// convergence loop nor rotation touches it.
	GC GCStore

	// TokenRetention is how long a revoked generation token's hash is kept
	// before GC tombstones it. Zero uses defaultTokenRetention.
	TokenRetention time.Duration

	// ImagesPerPass bounds image reclaims in one GC pass. Zero uses
	// defaultImagesPerPass.
	ImagesPerPass int

	// Now is the clock GC reads for the token-retention cutoff. Zero uses
	// time.Now; tests inject a fixed instant so a retention window is a
	// property of the input rather than of how long the test took to run.
	Now func() time.Time
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

	// selfAttacher joins/leaves instance networks on Gleipnir's own behalf.
	// Nil when Config.SelfContainerID was empty — the loop then never plans
	// or applies an attach/detach step at all.
	selfAttacher *container.SelfAttacher

	// egressEnv supplies the proxy environment a container is created with
	// (ADR-056 §7 egress containment, #812). A hook rather than a direct
	// dependency so the reconciler does not import the proxy: it converges
	// containers, and where the one way out points is the proxy's business.
	// Nil means no proxy is configured, which — on an internal-only network —
	// means the instance reaches nothing.
	egressEnv func(ctx context.Context, instanceID string) []string

	// hostEndpointEnv supplies the host-endpoint URL a generation's container
	// is created with (#875, #955). Same hook pattern as egressEnv, and for
	// the same reason: the reconciler converges containers, it does not own
	// where the host endpoint listens.
	hostEndpointEnv func(ctx context.Context, instanceID string) []string

	rotations         RotationStore
	healthGateTimeout time.Duration
	drainTimeout      time.Duration

	// rotationMu serializes every rotation pass — runPass (the run loop's own
	// combined ReconcileOnce+ReconcileRotations cycle) and a direct call to
	// the exported ReconcileRotations alike (#955 security re-review round 3
	// item 4). sweepOrphanedGenerationContainers treats "list the world, then
	// act on it" as one logical step; two rotation passes running
	// concurrently would each act on their own stale snapshot of the same
	// generations and containers, which the sweep's correctness depends on
	// not happening.
	rotationMu sync.Mutex

	// gc and its bounds back ReconcileGC (#818). Nil gc means the cleanup pass
	// is not configured; it refuses rather than silently doing nothing, since
	// "GC ran and reclaimed zero" and "GC never ran" are answers an operator
	// looking at rising disk usage must be able to tell apart.
	gc             GCStore
	tokenRetention time.Duration
	imagesPerPass  int
	gcNow          func() time.Time

	// tokens holds raw per-generation instance tokens between minting them and
	// handing them to the container they belong to. In memory only and never
	// persisted: a stored token is a token a database leak hands to an
	// attacker. A restart loses them, and the create step treats a lost token
	// as a failed generation rather than starting a container it cannot
	// authenticate.
	tokenMu sync.Mutex
	tokens  map[string]string

	// restarts tracks per-generation crash-loop bookkeeping (#955 security
	// review item 5) for an active generation whose container has died:
	// consecutive restart attempts and the earliest instant the next one may
	// run. In memory only, like tokens, and for the same reason — a process
	// restart forgives the count, which only ever means a freshly restarted
	// host gives a crashing plugin one more immediate attempt before backing
	// off again.
	restartMu sync.Mutex
	restarts  map[string]*restartState

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
	tokenRetention := cfg.TokenRetention
	if tokenRetention <= 0 {
		tokenRetention = defaultTokenRetention
	}
	imagesPerPass := cfg.ImagesPerPass
	if imagesPerPass <= 0 {
		imagesPerPass = defaultImagesPerPass
	}
	gcNow := cfg.Now
	if gcNow == nil {
		gcNow = time.Now
	}

	var selfAttacher *container.SelfAttacher
	if cfg.SelfContainerID != "" {
		// Self-attach is refused outright, not just validated per-call,
		// unless BOTH preconditions hold — each is a LOG, not a fatal New()
		// error, since the core convergence loop still has containers to
		// converge even when self-attach specifically cannot be enabled.
		//
		// 1. The operator API is guarded against cross-instance traffic
		//    (#1021 review item 1): without this, Gleipnir joining every
		//    instance network while its own admin surface remains reachable
		//    FROM those networks is a path none of self-attach's other
		//    checks (managed label, Internal, instance match, subnet
		//    containment) can see, because it runs through Gleipnir's own
		//    container being multi-homed, not through a network Gleipnir
		//    joined incorrectly.
		switch {
		case !cfg.OperatorAPIGuarded:
			slog.Error("reconciler: refusing to enable gleipnir self-attach; " +
				"the operator API is not confirmed guarded against cross-instance traffic (Config.OperatorAPIGuarded)")

		// 2. Gleipnir's own kernel has forwarding disabled on both stacks
		//    (#958 finding 2): a container attached to two isolated instance
		//    networks with forwarding enabled could route packets between
		//    them, undoing east-west isolation through the one component
		//    every instance network trusts enough to let in.
		default:
			checkForwarding := cfg.CheckForwardingDisabled
			if checkForwarding == nil {
				checkForwarding = container.CheckForwardingDisabled
			}
			if err := checkForwarding(); err != nil {
				slog.Error("reconciler: refusing to enable gleipnir self-attach; "+
					"a plugin container could otherwise be bridged to another instance's network through gleipnir's own container",
					"err", err)
			} else {
				pool := netip.Prefix{}
				if cfg.Subnets != nil {
					pool = cfg.Subnets.Pool()
				}
				selfAttacher = container.NewSelfAttacher(container.SelfAttacherConfig{
					Runtime:           cfg.Runtime,
					ContainerID:       cfg.SelfContainerID,
					ManagedLabelKey:   LabelManaged,
					ManagedLabelValue: ManagedValue,
					InstanceLabelKey:  LabelInstance,
					Pool:              pool,
				})
			}
		}
	}

	return &Reconciler{
		runtime:         cfg.Runtime,
		store:           cfg.Store,
		posture:         cfg.Posture,
		interval:        interval,
		publisher:       cfg.Publisher,
		subnets:         cfg.Subnets,
		networkFn:       networkFn,
		egressEnv:       cfg.EgressEnv,
		hostEndpointEnv: cfg.HostEndpointEnv,
		selfAttacher:    selfAttacher,
		kick:            make(chan struct{}, 1),

		rotations:         cfg.Rotations,
		healthGateTimeout: cfg.HealthGateTimeout,
		drainTimeout:      cfg.DrainTimeout,
		tokens:            make(map[string]string),
		restarts:          make(map[string]*restartState),

		gc:             cfg.GC,
		tokenRetention: tokenRetention,
		imagesPerPass:  imagesPerPass,
		gcNow:          gcNow,
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

	if err := r.runPass(rootCtx); err != nil {
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

// runPass performs one full convergence cycle: ReconcileOnce, then — in the
// same goroutine, strictly after it, never concurrently with it — a rotation
// pass via ReconcileRotations when generation tracking is configured (#955
// security re-review round 2 item 1: the run loop never called
// ReconcileRotations at all; only tests did). The two are not independent:
// rotation reads what the core loop just wrote in this SAME pass — a freshly
// minted `pending` generation from ActionBeginFirstGeneration, most notably —
// so running them out of step, or on separate goroutines that could race,
// would leave first boot, a desired stop, or an instance-teardown sweep
// waiting for a tick that never specifically does the next step.
//
// ReconcileRotations runs even when ReconcileOnce fails (#955 security
// re-review round 3 finding 2): the two converge different things — a
// container toward its row, a generation row SET toward stopped/desired —
// and a transient failure reading one side (the core loop's own network
// list, say) must not withhold an operator-requested stop, which lives
// entirely on the rotation side, until the NEXT tick happens to find the
// core loop's read healthy again. Both errors are reported via errors.Join
// rather than either one being silently dropped.
//
// Holds rotationMu across both halves (#955 security re-review round 3 item
// 4), calling the unexported reconcileRotationsLocked rather than the
// exported ReconcileRotations so the lock is taken exactly once per pass —
// see ReconcileRotations' doc comment for why the sweep needs this
// exclusion at all.
func (r *Reconciler) runPass(ctx context.Context) error {
	r.rotationMu.Lock()
	defer r.rotationMu.Unlock()

	_, onceErr := r.ReconcileOnce(ctx)

	if r.rotations == nil {
		return onceErr
	}

	result, rotErr := r.reconcileRotationsLocked(ctx)
	if rotErr == nil {
		r.publishRotationPass(result)
	}
	return errors.Join(onceErr, rotErr)
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

		if err := r.runPass(ctx); err != nil {
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

	// Generation tracking is optional (r.rotations may be nil, the legacy
	// fallback); when it is configured, the core loop needs to know which
	// instances already have a live generation so it mints exactly one
	// (ActionBeginFirstGeneration) rather than creating a tokenless container
	// itself.
	var generations []db.PluginContainerGeneration
	if r.rotations != nil {
		generations, err = r.rotations.ListLiveContainerGenerations(ctx)
		if err != nil {
			return PassResult{}, fmt.Errorf("listing live generations: %w", err)
		}
	}

	// A failed Inspect of Gleipnir's own container does not abort the whole
	// pass (#958 finding 7): the core convergence loop still has containers
	// to bring up, bring down, and orphans to clean up, none of which
	// depend on self-attach succeeding this particular pass. It does NOT
	// mean "self-attach disabled" though — self is marked Unknown rather
	// than absent (#1021 review item 3b), which planFor treats differently
	// from "not configured at all": an instance that would otherwise be
	// CREATED is left alone instead, because creating it without knowing
	// whether Gleipnir is already attached risks either a container nobody
	// can reach yet, or a duplicate-attach the daemon would refuse. The next
	// pass tries Inspect again.
	self := selfInspect{Configured: r.selfAttacher != nil}
	if self.Configured {
		info, err := r.runtime.Inspect(ctx, r.selfAttacher.ContainerID())
		if err != nil {
			logctx.Logger(ctx).WarnContext(ctx, "reconciler: inspecting gleipnir's own container failed; self-attach status unknown for this pass",
				"err", err)
			self.Unknown = true
		} else {
			self.Info = &info
		}
	}

	result := PassResult{Desired: len(desired), Observed: len(observed)}
	for _, action := range planPass(desired, observed, networks, generations, r.rotations != nil, self) {
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
//
// generations is the instance's live (non-terminal) generation rows, and
// generationTrackingEnabled says whether this Reconciler has a Rotations
// store configured at all — a reconciler-wide mode, not a per-instance fact,
// which is why it travels separately from the (necessarily empty, when
// disabled) generations slice: an instance with tracking disabled must fall
// back to the legacy create path, not be read as "no generation yet".
//
// self describes what this pass learned about Gleipnir's own self-attach
// status — not configured at all, confirmed (with its most recent Inspect),
// or configured but Unknown this pass (Inspect failed). Passing the whole
// three-way state through here, rather than a bare pointer, is what lets
// selfStateFor tell "no self-attach configured" apart from "configured but
// this pass could not confirm it" — the two read identically as "not
// attached" but planFor must react to them differently (#1021 review item
// 3b).
func planPass(desired []db.PluginContainer, observed []container.ContainerInfo, networks []container.NetworkInfo, generations []db.PluginContainerGeneration, generationTrackingEnabled bool, self selfInspect) []Action {
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

	hasLiveGeneration := make(map[string]bool, len(generations))
	for _, gen := range generations {
		hasLiveGeneration[gen.PluginInstanceID] = true
	}

	actions := make([]Action, 0, len(desired)+len(observed)+len(networks))
	seen := make(map[string]bool, len(desired))
	for i := range desired {
		row := &desired[i]
		seen[row.PluginInstanceID] = true
		net, hasNetwork := networkByInstance[row.PluginInstanceID]
		gen := GenerationTrackingDisabled
		if generationTrackingEnabled {
			if hasLiveGeneration[row.PluginInstanceID] {
				gen = GenerationLive
			} else {
				gen = GenerationMissing
			}
		}
		actions = append(actions, planFor(row, byInstance[row.PluginInstanceID], hasNetwork, gen, selfStateFor(self, net)))
	}

	// Everything managed that no desired row claims is an orphan. A container
	// goes first; its network is torn down on a later pass, once the container
	// is actually gone. Generation state is irrelevant to an orphan — desired
	// == nil short-circuits planFor before it is ever consulted.
	for id, info := range byInstance {
		if !seen[id] {
			net, hasNetwork := networkByInstance[id]
			actions = append(actions, planFor(nil, info, hasNetwork, GenerationTrackingDisabled, selfStateFor(self, net)))
		}
	}
	for i := range unlabelled {
		actions = append(actions, planFor(nil, &unlabelled[i], false, GenerationTrackingDisabled, selfAttachState{}))
	}

	// A network whose instance has neither a desired row nor a container left
	// is the second half of a teardown.
	for id, net := range networkByInstance {
		if seen[id] {
			continue
		}
		if _, stillRunning := byInstance[id]; stillRunning {
			continue
		}
		action := planFor(nil, nil, true, GenerationTrackingDisabled, selfStateFor(self, net))
		action.InstanceID = id
		actions = append(actions, action)
	}
	return actions
}

// selfInspect describes what one reconcile pass learned about Gleipnir's own
// self-attach status, before that status is narrowed to a single instance's
// network by selfStateFor.
type selfInspect struct {
	// Configured is true when Config.SelfContainerID was set and the
	// preconditions checked at construction (operator API guard, forwarding
	// disabled) passed — i.e. Reconciler.selfAttacher != nil. False means
	// self-attach is not in play at all this run (host-process mode, or a
	// precondition failure logged at New()).
	Configured bool
	// Info is the most recent Inspect result for Gleipnir's own container,
	// or nil when Configured is false or the Inspect call failed this pass
	// (Unknown distinguishes those two).
	Info *container.ContainerInfo
	// Unknown is true when Configured is true but this pass's Inspect call
	// failed — status could not be confirmed, as opposed to confirmed absent.
	Unknown bool
}

// selfStateFor reports what planFor needs to know about Gleipnir's own
// self-attach to net.
func selfStateFor(self selfInspect, net container.NetworkInfo) selfAttachState {
	if !self.Configured {
		return selfAttachState{}
	}
	if self.Unknown {
		return selfAttachState{Enabled: true, Unknown: true}
	}
	return selfAttachState{Enabled: true, Attached: container.SelfAttached(*self.Info, net)}
}

// apply performs one action's socket write.
func (r *Reconciler) apply(ctx context.Context, action Action, desired []db.PluginContainer) error {
	switch action.Kind {
	case ActionBeginFirstGeneration:
		row, ok := findDesired(desired, action.InstanceID)
		if !ok {
			return fmt.Errorf("no desired row for instance %q", action.InstanceID)
		}
		if r.rotations == nil {
			// Unreachable: planFor only emits this action when ReconcileOnce
			// found a rotation store to pass it. Checked anyway rather than
			// left to panic inside beginRotation.
			return fmt.Errorf("reconciler: no rotation store configured; cannot mint instance %q's first generation", action.InstanceID)
		}
		now := rotationTimeNow().UTC().Format(time.RFC3339Nano)
		// beginRotation mints "latest + 1" for the instance, which is 1 when
		// no generation has ever existed — first boot needs nothing beyond
		// that rotation does not already do. From here, ReconcileRotations
		// carries the new pending generation through create, health gate, and
		// switch to active exactly as it would for any later rotation.
		return r.beginRotation(ctx, RotationAction{InstanceID: action.InstanceID, Reason: action.Reason}, row, now)

	case ActionCreate:
		row, ok := findDesired(desired, action.InstanceID)
		if !ok {
			// The desired set was read at the top of this pass, so this is
			// unreachable; returning an error rather than panicking keeps a
			// future refactor from turning a bug into a crash.
			return fmt.Errorf("no desired row for instance %q", action.InstanceID)
		}
		id, err := r.runtime.Create(ctx, r.withEgressEnv(ctx, r.createOptions(row), row.PluginInstanceID))
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

	case ActionAttachSelf:
		return r.attachSelf(ctx, action.InstanceID)

	case ActionDetachSelf:
		return r.detachSelf(ctx, action.InstanceID)

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

	// Dynamic allocation is confined to the upper half of the subnet (#1021
	// review item 3): the lower half — including the network address, the
	// gateway, and the address self-attach reserves for Gleipnir two below
	// it — is never handed to a plugin container by the daemon's own IPAM,
	// which is what makes the reservation actually hold rather than just
	// being the address Gleipnir HAPPENS to grab first.
	ipRange, err := upperHalfIPRange(subnet)
	if err != nil {
		return fmt.Errorf("computing dynamic-allocation range for instance %q: %w", row.PluginInstanceID, err)
	}

	name := r.networkFn(row)
	id, err := r.runtime.CreateNetwork(ctx, container.NetworkOptions{
		Name: name,
		Labels: map[string]string{
			LabelManaged:  ManagedValue,
			LabelInstance: row.PluginInstanceID,
		},
		Subnet:  subnet.String(),
		IPRange: ipRange.String(),
		// Internal is the default-deny the egress-grants work builds on: a
		// plugin container has no route off its own network until something
		// deliberately gives it one.
		Internal: true,
		// EnableIPv6 is left at its zero value (false) deliberately (#1021
		// review item V1): a plugin container has no legitimate use for a
		// link-local fe80 address, and IPv6 on an instance network is a
		// surface the operator-API guard (#1021) would otherwise also have to
		// consider.
		EnableIPv6: false,
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

// attachSelf joins Gleipnir's own container to instanceID's network, so it
// can already reach the instance the moment its container starts. Looked up
// by instance label rather than carried on the Action, the same pattern
// removeNetwork uses — the pass that planned this step already confirmed
// exactly one such network exists, and a crash between planning and applying
// still finds it on retry.
//
// The address requested is ALWAYS the reserved, deterministic one
// (egress.GleipnirAddrOf) — never left to the daemon to pick — so plugins and
// the host endpoint can be told a single address to reach Gleipnir at,
// regardless of attach order (#958 finding 4).
func (r *Reconciler) attachSelf(ctx context.Context, instanceID string) error {
	net, err := r.instanceNetwork(ctx, instanceID)
	if err != nil {
		return err
	}
	addr, err := egress.GleipnirAddrOf(net.Subnet)
	if err != nil {
		return fmt.Errorf("computing gleipnir's address on network %q for instance %q: %w", net.Name, instanceID, err)
	}
	target := container.AttachTarget{
		Network:        net,
		InstanceID:     instanceID,
		ExpectedSubnet: r.expectedSubnetFor(ctx, instanceID),
	}
	if err := r.selfAttacher.Attach(ctx, target, addr); err != nil {
		return fmt.Errorf("attaching gleipnir to network %q for instance %q: %w", net.Name, instanceID, err)
	}
	logctx.Logger(ctx).InfoContext(ctx, "reconciler: gleipnir joined instance network",
		"instance_id", instanceID, "network", net.Name, "address", addr)
	return nil
}

// detachSelf leaves instanceID's network, ahead of its removal.
func (r *Reconciler) detachSelf(ctx context.Context, instanceID string) error {
	net, err := r.instanceNetwork(ctx, instanceID)
	if err != nil {
		return err
	}
	target := container.AttachTarget{
		Network:        net,
		InstanceID:     instanceID,
		ExpectedSubnet: r.expectedSubnetFor(ctx, instanceID),
	}
	if err := r.selfAttacher.Detach(ctx, target); err != nil {
		return fmt.Errorf("detaching gleipnir from network %q for instance %q: %w", net.Name, instanceID, err)
	}
	logctx.Logger(ctx).InfoContext(ctx, "reconciler: gleipnir left instance network",
		"instance_id", instanceID, "network", net.Name)
	return nil
}

// expectedSubnetFor returns the subnet the database has recorded as allocated
// to instanceID, or "" when unknown — no subnet allocator configured, no row
// yet, or an unreadable one. SelfAttacher treats "" as "skip this specific
// check" rather than a mismatch (#958 finding 5): a caller that does not know
// the expected value is a different fact from one that knows it and disagrees.
func (r *Reconciler) expectedSubnetFor(ctx context.Context, instanceID string) string {
	if r.subnets == nil {
		return ""
	}
	prefix, ok, err := r.subnets.Lookup(ctx, instanceID)
	if err != nil || !ok {
		return ""
	}
	return prefix.String()
}

// instanceNetwork looks up instanceID's dedicated network by label, the same
// way removeNetwork does. More than one match refuses outright (#958 finding
// 5) rather than picking the first: self-attach must know unambiguously which
// network it is joining or leaving, and a duplicate-labelled network is a
// state worth surfacing loudly rather than silently resolving one way.
func (r *Reconciler) instanceNetwork(ctx context.Context, instanceID string) (container.NetworkInfo, error) {
	networks, err := r.runtime.ListNetworksByLabel(ctx, LabelInstance, instanceID)
	if err != nil {
		return container.NetworkInfo{}, fmt.Errorf("listing networks for instance %q: %w", instanceID, err)
	}
	if len(networks) == 0 {
		return container.NetworkInfo{}, fmt.Errorf("no network found for instance %q", instanceID)
	}
	if len(networks) > 1 {
		return container.NetworkInfo{}, fmt.Errorf("%d networks are labelled for instance %q, want exactly one", len(networks), instanceID)
	}
	return networks[0], nil
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
		// Every capability is dropped (#958 finding 2, tightened by #1021
		// review item V2) — including NET_RAW, which lets a container craft
		// raw/ICMP-adjacent packets and is the specific tool an east-west
		// attack from inside a plugin's own container would reach for. An
		// image whose entrypoint needs a capability (e.g. CHOWN to fix a
		// mounted volume's ownership before dropping to non-root) is expected
		// to do that at build time, not at container-create time.
		CapDrop: []string{"ALL"},
		// no-new-privileges closes the door CapDrop=[ALL] leaves open: with
		// every capability already gone, a setuid-root binary shipped in the
		// image is the remaining way a process inside the container could
		// regain privilege.
		SecurityOpt: []string{"no-new-privileges"},
	}
	// The desired row holds the ALREADY-RESOLVED envelope (manifest, then admin
	// override, per resources.Resolve). A NULL here therefore means "nobody
	// specified", not "no limit" — so the host default applies rather than the
	// container running uncapped. An unlimited container on a homelab host is
	// one plugin away from an OOM that takes Gleipnir with it, which is the
	// whole reason §7 moved from sampling RSS to enforcing cgroup caps.
	var declared resources.Limits
	if row.MemoryLimitBytes != nil {
		declared.MemoryBytes = *row.MemoryLimitBytes
	}
	if row.CpuLimitMillicores != nil {
		declared.CPUMillicores = *row.CpuLimitMillicores
	}
	effective := resources.Resolve(declared, resources.Limits{})
	opts.Resources.MemoryBytes = effective.MemoryBytes
	// The runtime speaks nano-CPUs (1e9 == one core); the row stores
	// millicores (1000 == one core).
	opts.Resources.NanoCPUs = effective.NanoCPUs()
	return opts
}

// withEgressEnv points a container at the host's egress proxy.
//
// Existing proxy variables are stripped first. An image that ships its own
// HTTP_PROXY, or an operator config that sets NO_PROXY, would otherwise leave a
// bypass in place while every other line still looked correct — and on an
// internal-only network a bypass does not mean "reaches the internet another
// way", it means "reaches nothing and looks broken".
func (r *Reconciler) withEgressEnv(ctx context.Context, opts container.CreateOptions, instanceID string) container.CreateOptions {
	if r.egressEnv == nil {
		return opts
	}
	return withStrippedEnv(opts, r.egressEnv(ctx, instanceID))
}

// withGenerationEnv composes the environment a per-generation container is
// created with: the egress proxy pointer (#812) and the host endpoint URL
// (#875, #955), both appended after any inherited proxy variables are
// stripped. It is the counterpart to withEgressEnv for createRotationContainer
// — the one create path shared by an instance's first generation and every
// later one — so this is the single place either variable is wired into a
// generation's container.
//
// The host endpoint's own host:port is then added to NO_PROXY (#955 security
// review finding 3) — the one deliberate exception to egress.ProxyEnv's own
// "NO_PROXY is empty" rule. A server→host callback carries the generation's
// bearer token in every request; routing it through the same forward proxy
// that mediates egress to the outside world would hand that token to
// whatever the proxy is configured to reach, when the call must go straight
// to the host instead.
//
// An error here refuses the create outright rather than silently degrading:
// see refuseHostEndpointAtGateway.
func (r *Reconciler) withGenerationEnv(ctx context.Context, opts container.CreateOptions, instanceID string) (container.CreateOptions, error) {
	var env []string
	if r.egressEnv != nil {
		env = append(env, r.egressEnv(ctx, instanceID)...)
	}
	var hostEndpointEnv []string
	if r.hostEndpointEnv != nil {
		hostEndpointEnv = r.hostEndpointEnv(ctx, instanceID)
		if err := r.refuseHostEndpointAtGateway(ctx, instanceID, hostEndpointEnv); err != nil {
			return container.CreateOptions{}, err
		}
		env = append(env, hostEndpointEnv...)
	}
	env = scopeNoProxyToHostEndpoint(env, hostEndpointEnv)
	return withStrippedEnv(opts, env), nil
}

// refuseHostEndpointAtGateway fails closed when hostEndpointEnv's
// GLEIPNIR_HOST_ENDPOINT_URL names the instance network's gateway address
// rather than Gleipnir's own reserved address on it (egress.GleipnirAddrOf).
// Under rootful Docker the gateway IS the host, so a URL there would hand a
// generation's bearer token straight to the host — bypassing the NO_PROXY
// scoping above entirely, not merely routing around it (combination review
// following #955/#958).
//
// Best-effort: an instance with no subnet allocator, or no allocated subnet
// yet, skips the check rather than refusing every create on it — this is a
// defense-in-depth guard against a misconfigured HostEndpointEnv hook, not
// the mechanism that keeps the URL correct (that is the hook's own
// production implementation, #957).
func (r *Reconciler) refuseHostEndpointAtGateway(ctx context.Context, instanceID string, hostEndpointEnv []string) error {
	if r.subnets == nil {
		return nil
	}
	host, ok := hostEndpointHost(hostEndpointEnv)
	if !ok {
		return nil
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	subnet, found, err := r.subnets.Lookup(ctx, instanceID)
	if err != nil || !found {
		return nil
	}
	gateway := subnetGatewayAddr(subnet)
	if addr != gateway {
		return nil
	}
	return fmt.Errorf(
		"host endpoint URL for instance %q resolves to its network's gateway address %s; refusing to create its container rather than hand a bearer token to the gateway (want gleipnir's reserved address instead, egress.GleipnirAddrOf)",
		instanceID, gateway,
	)
}

// subnetGatewayAddr computes a subnet's gateway address — the first usable
// address, e.g. .1 — for comparison in refuseHostEndpointAtGateway. Mirrors
// internal/plugin/container's own gleipnirReservedAddr (base+2, Gleipnir's
// reserved address one past the gateway); this is base+1, the gateway
// itself. Duplicated rather than imported for the same reason that one is:
// this package must not depend on internal/plugin/egress or vice versa.
// Kept in sync by hand.
func subnetGatewayAddr(subnet netip.Prefix) netip.Addr {
	base := subnet.Masked().Addr().As4()
	base[3]++
	return netip.AddrFrom4(base)
}

// hostEndpointURLEnvVar names the environment variable Config.HostEndpointEnv
// is expected to set. Duplicated here rather than imported from
// plugin-sdk/hostclient (which reads it under the exported
// HostEndpointURLEnvVar) for the same reason plugin-sdk/serve and
// plugin-sdk/hostclient each carry their own copy of InstanceTokenEnvVar: an
// internal package must not pull in plugin-sdk just to share one string
// constant. Keep the two literals equal by hand.
const hostEndpointURLEnvVar = "GLEIPNIR_HOST_ENDPOINT_URL"

// scopeNoProxyToHostEndpoint adds the host endpoint's own host:port to every
// NO_PROXY variable already present in env. hostEndpointEnv is the raw
// entries Config.HostEndpointEnv returned, read only for the URL's host:port
// — nothing else about it feeds back into env. A no-op when no NO_PROXY
// variable is present at all (EgressEnv unset means nothing routes through a
// proxy in the first place, so there is nothing to scope an exception into).
func scopeNoProxyToHostEndpoint(env, hostEndpointEnv []string) []string {
	hostport, ok := hostEndpointHostPort(hostEndpointEnv)
	if !ok {
		return env
	}
	out := make([]string, len(env))
	copy(out, env)
	for i, entry := range out {
		key, value, found := strings.Cut(entry, "=")
		if !found || !strings.EqualFold(key, "no_proxy") {
			continue
		}
		out[i] = key + "=" + addNoProxyHost(value, hostport)
	}
	return out
}

// addNoProxyHost appends host to a NO_PROXY value, comma-joined per the
// variable's conventional format, unless it is already there.
func addNoProxyHost(existing, host string) string {
	if existing == "" {
		return host
	}
	for _, part := range strings.Split(existing, ",") {
		if strings.TrimSpace(part) == host {
			return existing
		}
	}
	return existing + "," + host
}

// hostEndpointHostPort extracts the host:port from Config.HostEndpointEnv's
// GLEIPNIR_HOST_ENDPOINT_URL entry, or reports false when it set none (the
// hook is unconfigured, or omitted the URL for a reason of its own).
func hostEndpointHostPort(env []string) (string, bool) {
	u, ok := parseHostEndpointURL(env)
	if !ok {
		return "", false
	}
	return u.Host, true
}

// hostEndpointHost extracts the bare host (no port) from
// Config.HostEndpointEnv's GLEIPNIR_HOST_ENDPOINT_URL entry, for comparing
// against a subnet's gateway address in refuseHostEndpointAtGateway — a
// comparison that must ignore the port, since a gateway match is a gateway
// match whatever the configured port.
func hostEndpointHost(env []string) (string, bool) {
	u, ok := parseHostEndpointURL(env)
	if !ok {
		return "", false
	}
	return u.Hostname(), true
}

// parseHostEndpointURL finds and parses Config.HostEndpointEnv's
// GLEIPNIR_HOST_ENDPOINT_URL entry, or reports false when it set none.
func parseHostEndpointURL(env []string) (*url.URL, bool) {
	for _, kv := range env {
		val, ok := strings.CutPrefix(kv, hostEndpointURLEnvVar+"=")
		if !ok {
			continue
		}
		u, err := url.Parse(val)
		if err != nil || u.Host == "" {
			return nil, false
		}
		return u, true
	}
	return nil, false
}

// withStrippedEnv appends env to opts.Env after stripping any proxy variables
// the image or an earlier layer already set, so neither caller can leave a
// bypass in place while every other line still looks correct. A no-op when
// env is empty, so an unconfigured hook leaves the environment untouched
// rather than paying for a strip that changes nothing.
func withStrippedEnv(opts container.CreateOptions, env []string) container.CreateOptions {
	if len(env) == 0 {
		return opts
	}
	opts.Env = append(egress.StripProxyEnv(opts.Env), env...)
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

// publishRotationPass emits EventRotationPassCompleted, mirroring publishPass.
// Best-effort for the same reason: a publisher failure must never affect
// convergence, and a marshal failure is a logging problem.
func (r *Reconciler) publishRotationPass(result RotationPassResult) {
	if r.publisher == nil {
		return
	}
	payload := struct {
		RotationPassResult
		ActionCount int `json:"action_count"`
	}{RotationPassResult: result, ActionCount: len(result.Actions)}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("reconciler: marshalling rotation pass event failed", "err", err)
		return
	}
	r.publisher.Publish(EventRotationPassCompleted, data)
}
