package caphealth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// EventProbePassCompleted is published after every probe pass. It is the
// signal-don't-poll synchronization point: a test waits on this rather than
// sleeping until the registry happens to be populated (CLAUDE.md testing rules).
const EventProbePassCompleted = "plugin.health_probe_pass"

// defaultProbeInterval is the periodic probe cadence.
const defaultProbeInterval = 30 * time.Second

// Target is one instance the prober checks.
type Target struct {
	InstanceID string

	// ContainerID is the runtime handle for the healthcheck read.
	ContainerID string

	// AttestedEventKinds are the kinds the signed manifest attests. Empty when
	// the plugin declares no event source.
	AttestedEventKinds []string

	// Profiles are the capability profiles the manifest declares. Only declared
	// profiles get health entries — a profile a plugin does not implement
	// serves nothing, so it has nothing to be healthy or unhealthy about.
	Profiles []Profile
}

// ContainerHealthProbe reads the runtime's healthcheck verdict for a container.
//
// Narrow by design: the prober takes this rather than a container.Runtime so
// the whole loop is testable without a socket, and so it is structurally unable
// to do anything to a container besides ask how it is.
type ContainerHealthProbe interface {
	// ContainerHealthy reports whether the container passes its healthcheck.
	// An image with no declared healthcheck reports (true, "") — see the same
	// reasoning in the rotation gate: treating "no healthcheck" as unhealthy
	// would make every such plugin permanently unroutable.
	ContainerHealthy(ctx context.Context, containerID string) (bool, string, error)
}

// DiscoverProbe asks an instance's MCP endpoint to answer `server/discover`,
// and reports the event kinds it advertises.
//
// The kinds come back from the same call that establishes liveness because
// they are the same round trip: a separate discovery call would double the
// traffic and open a window where liveness and drift disagree about what the
// server said.
type DiscoverProbe interface {
	Discover(ctx context.Context, instanceID string) (DiscoverResult, error)
}

// DiscoverResult is what one `server/discover` probe learned.
type DiscoverResult struct {
	// EventKinds the server advertises. Nil when it declares no event source.
	EventKinds []string
}

// TargetLister supplies the instances to probe on each pass. It is re-read
// every pass rather than cached: instances are installed, deactivated, and
// removed while the loop runs, and a cached list would keep probing something
// that no longer exists.
type TargetLister interface {
	ProbeTargets(ctx context.Context) ([]Target, error)
}

// RollupSink receives the instance-level rollup so it can be written to
// plugin_instances.health_state — the value the existing chip UI reads.
//
// It is an interface rather than a direct *state.Machine call so the prober
// does not need a DB in its tests, and so the write can be a no-op in contexts
// (like a read-only health endpoint) that only want the in-memory picture.
type RollupSink interface {
	SetInstanceHealth(ctx context.Context, instanceID string, s model.PluginHealthState, detail string) error
}

// Config wires a Prober.
type Config struct {
	Registry  *Registry
	Targets   TargetLister
	Container ContainerHealthProbe
	Discover  DiscoverProbe

	// Rollup is optional. Without it the prober maintains the in-memory
	// registry and writes nothing.
	Rollup RollupSink

	// Interval is the probe cadence; zero uses defaultProbeInterval.
	Interval time.Duration

	// Publisher receives EventProbePassCompleted. Optional.
	Publisher event.Publisher
}

// Prober periodically establishes liveness and capability health for every
// instance.
type Prober struct {
	registry   *Registry
	targets    TargetLister
	container  ContainerHealthProbe
	discover   DiscoverProbe
	rollup     RollupSink
	interval   time.Duration
	publisher  event.Publisher
	kick       chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
	rootCancel context.CancelFunc
}

// timeNow is the package's injectable clock (CLAUDE.md "Testing time-dependent
// code"). Tests swap it via t.Cleanup and must not call t.Parallel() while it
// is swapped.
var timeNow = func() time.Time { return time.Now() }

func New(cfg Config) (*Prober, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("caphealth: Registry is required")
	}
	if cfg.Targets == nil {
		return nil, fmt.Errorf("caphealth: Targets is required")
	}
	if cfg.Container == nil {
		return nil, fmt.Errorf("caphealth: Container probe is required")
	}
	if cfg.Discover == nil {
		return nil, fmt.Errorf("caphealth: Discover probe is required")
	}

	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultProbeInterval
	}
	return &Prober{
		registry:  cfg.Registry,
		targets:   cfg.Targets,
		container: cfg.Container,
		discover:  cfg.Discover,
		rollup:    cfg.Rollup,
		interval:  interval,
		publisher: cfg.Publisher,
		kick:      make(chan struct{}, 1),
	}, nil
}

// PassResult summarizes one probe pass.
type PassResult struct {
	Probed    int `json:"probed"`
	Unhealthy int `json:"unhealthy"`
	Partial   int `json:"partial"`
	Errors    int `json:"errors"`
}

// ProbeOnce runs a single pass over every target.
//
// A probe error is not fatal to the pass: one unreachable instance must not
// stop the host learning about the others, which is the same reason the
// reconciler treats a failed action as a next-pass retry rather than an abort.
func (p *Prober) ProbeOnce(ctx context.Context) (PassResult, error) {
	var result PassResult

	targets, err := p.targets.ProbeTargets(ctx)
	if err != nil {
		return result, fmt.Errorf("list probe targets: %w", err)
	}

	for _, target := range targets {
		result.Probed++
		if err := p.probeTarget(ctx, target); err != nil {
			result.Errors++
			slog.WarnContext(ctx, "health probe failed",
				"instance_id", target.InstanceID, "err", err)
		}

		health := p.registry.Get(target.InstanceID)
		if rollup := health.Rollup(); rollup != model.PluginHealthStateHealthy {
			result.Unhealthy++
		}
		if health.Partial() {
			result.Partial++
		}
		if p.rollup != nil {
			if err := p.rollup.SetInstanceHealth(ctx, target.InstanceID, health.Rollup(), health.RollupDetail()); err != nil {
				slog.WarnContext(ctx, "writing rolled-up instance health",
					"instance_id", target.InstanceID, "err", err)
			}
		}
	}

	p.publishPass(result)
	return result, nil
}

// probeTarget establishes one instance's liveness and capability health.
func (p *Prober) probeTarget(ctx context.Context, target Target) error {
	live := Liveness{}

	healthy, detail, err := p.container.ContainerHealthy(ctx, target.ContainerID)
	switch {
	case err != nil:
		live.Detail = "container healthcheck unavailable: " + err.Error()
	case !healthy:
		live.Detail = "container healthcheck failing"
		if detail != "" {
			live.Detail += ": " + detail
		}
	default:
		live.ContainerHealthy = true
	}

	discovered, discoverErr := p.discover.Discover(ctx, target.InstanceID)
	if discoverErr != nil {
		if live.Detail == "" {
			// Only overwrite when the container check passed. A container that
			// is down explains the discover failure, and reporting the
			// downstream symptom instead of the cause sends an operator to the
			// wrong place.
			live.Detail = "server/discover probe failed: " + discoverErr.Error()
		}
	} else {
		live.DiscoverOK = true
	}

	p.registry.SetLiveness(target.InstanceID, live)

	if !live.OK() {
		// Capability health is not re-derived for an unreachable instance. Its
		// last-known entries are left in place rather than blanked: the rollup
		// already reports unhealthy, and blanking would destroy the detail an
		// operator needs to see what was wrong before it went away.
		if discoverErr != nil {
			return discoverErr
		}
		return nil
	}

	p.seedDeclaredProfiles(target)
	p.applyEventDrift(target, discovered)
	return nil
}

// seedDeclaredProfiles gives every profile the manifest declares an entry, so
// the registry reflects the instance's whole surface rather than only the parts
// something has complained about.
//
// Without it "serves" and "is recorded as healthy" come apart: an undeclared
// capability serves by default (silence is not a fault), but it contributes
// nothing to the rollup, so an instance with one broken capability and three
// silent ones would report a total failure rather than a partial one.
//
// It seeds ONLY profiles with no entry yet. A fault recorded by something else
// — a plugin self-report over the host endpoint, once that lands — must survive
// a probe pass that had no opinion about it. The prober overwrites only what it
// establishes itself.
func (p *Prober) seedDeclaredProfiles(target Target) {
	existing := p.registry.Get(target.InstanceID)
	have := make(map[Capability]struct{}, len(existing.Entries))
	for _, e := range existing.Entries {
		have[e.Capability] = struct{}{}
	}

	for _, profile := range target.Profiles {
		capability := Capability{Profile: profile}
		if _, ok := have[capability]; ok {
			continue
		}
		p.registry.SetCapability(target.InstanceID, Entry{
			Capability: capability,
			State:      model.PluginHealthStateHealthy,
		})
	}
}

// applyEventDrift turns manifest-vs-discovery disagreement into a
// capability-level fault on the event-source profile.
func (p *Prober) applyEventDrift(target Target, discovered DiscoverResult) {
	if !hasProfile(target.Profiles, ProfileEventSource) {
		return
	}
	capability := Capability{Profile: ProfileEventSource}

	if detail := DriftDetail(target.AttestedEventKinds, discovered.EventKinds); detail != "" {
		p.registry.SetCapability(target.InstanceID, Entry{
			Capability: capability,
			State:      model.PluginHealthStateUnhealthy,
			Detail:     detail,
		})
		return
	}
	p.registry.SetCapability(target.InstanceID, Entry{
		Capability: capability,
		State:      model.PluginHealthStateHealthy,
	})
}

func hasProfile(profiles []Profile, want Profile) bool {
	for _, p := range profiles {
		if p == want {
			return true
		}
	}
	return false
}

// Start runs a synchronous first pass and then the periodic loop.
//
// The first pass is synchronous so a caller can treat "Start returned" as "every
// instance has been looked at once" — until then the registry reports every
// instance as not-live, which is correct but would make a readiness check
// racy if the loop were purely asynchronous.
func (p *Prober) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.rootCancel = cancel
	p.mu.Unlock()

	if _, err := p.ProbeOnce(runCtx); err != nil {
		cancel()
		return err
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.loop(runCtx)
	}()
	return nil
}

// Kick requests an out-of-band pass. Non-blocking and coalescing: a burst of
// requests becomes one extra pass, which is right for a loop that re-reads
// everything anyway.
func (p *Prober) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Wait blocks until the loop has stopped.
func (p *Prober) Wait() { p.wg.Wait() }

// Stop cancels the loop. Safe to call more than once.
func (p *Prober) Stop() {
	p.mu.Lock()
	cancel := p.rootCancel
	p.rootCancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *Prober) loop(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.kick:
		}
		if _, err := p.ProbeOnce(ctx); err != nil {
			// A failed pass is never fatal: the next one re-reads the world.
			slog.WarnContext(ctx, "health probe pass failed", "err", err)
		}
	}
}

func (p *Prober) publishPass(result PassResult) {
	if p.publisher == nil {
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return
	}
	p.publisher.Publish(EventProbePassCompleted, payload)
}
