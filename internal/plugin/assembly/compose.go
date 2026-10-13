package assembly

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/plugin/logcapture"
	"github.com/felag-engineering/gleipnir/internal/plugin/pluginmetrics"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
)

// Option adjusts how New composes the substrate. The only constructors are
// unexported: they exist so in-package tests can run the real composition
// against container.Fake rather than a socket.
type Option func(*options)

type options struct {
	detect            *container.DetectConfig
	runtime           container.Runtime
	selfContainerID   *container.ContainerID
	checkForwarding   func() error
	reconcileInterval time.Duration
}

// New composes the v2 plugin substrate; see the package doc for what it starts
// (nothing) and when (OperatorListenerReady).
//
// An explicit GLEIPNIR_CONTAINER_RUNTIME_MODE that cannot be satisfied is an
// error: the operator asked for something specific. Only auto mode with no
// socket degrades to plugins-disabled, with a WARN.
func New(ctx context.Context, deps Deps, opts ...Option) (*Assembly, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	a := &Assembly{ctx: ctx}
	if deps.Store == nil {
		return a, nil
	}
	q := deps.Store.Queries()
	a.manifestSnap = configvalidate.NewSnapshotter(q)

	detect := container.LoadDetectConfig()
	if o.detect != nil {
		detect = *o.detect
	}
	posture, err := container.DetectPosture(detect)
	if err != nil {
		if !isAutoMode(detect.Mode) {
			return nil, fmt.Errorf("plugin substrate: %w", err)
		}
		slog.Warn("plugins are disabled: no container-runtime socket was found", "err", err)
		return a, nil
	}
	container.LogPosture(slog.Default(), posture)

	pool, err := netip.ParsePrefix(deps.Config.PluginSubnetPool)
	if err != nil {
		return nil, fmt.Errorf("plugin substrate: parsing GLEIPNIR_PLUGIN_SUBNET_POOL %q: %w", deps.Config.PluginSubnetPool, err)
	}

	rt, err := openRuntime(posture, o.runtime)
	if err != nil {
		return nil, fmt.Errorf("plugin substrate: %w", err)
	}
	if err := a.compose(ctx, deps, q, o, posture.Posture, pool, rt); err != nil {
		_ = rt.Close()
		return nil, fmt.Errorf("plugin substrate: %w", err)
	}
	a.runtime = rt
	a.enabled = true
	return a, nil
}

func isAutoMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		return true
	}
	return false
}

// openRuntime connects to the socket the posture resolved to. Manual posture
// gets a read-only runtime so a write is impossible, not merely unlikely.
func openRuntime(posture container.PostureResult, override container.Runtime) (container.Runtime, error) {
	base := override
	if base == nil && posture.Posture != container.PostureManual {
		docker, err := container.NewDockerRuntime(posture.SocketPath)
		if err != nil {
			return nil, fmt.Errorf("connecting to container runtime at %q: %w", posture.SocketPath, err)
		}
		base = docker
	}
	if posture.Posture == container.PostureManual {
		return container.NewReadOnlyRuntime(base), nil
	}
	return base, nil
}

// compose builds every component in dependency order and stores them on a.
// The reconciler itself is deferred to a.newReconciler.
func (a *Assembly) compose(
	ctx context.Context,
	deps Deps,
	q *db.Queries,
	o options,
	posture container.Posture,
	pool netip.Prefix,
	rt container.Runtime,
) error {
	subnets, err := reconciler.NewSubnetAllocator(q, pool, nil)
	if err != nil {
		return fmt.Errorf("subnet allocator: %w", err)
	}

	var selfID container.ContainerID
	switch {
	case o.selfContainerID != nil:
		selfID = *o.selfContainerID
	case posture != container.PostureManual:
		selfID, err = container.ResolveSelfContainerID(ctx, rt)
		if err != nil {
			return fmt.Errorf("resolving gleipnir's own container: %w", err)
		}
	}

	health := caphealth.NewRegistry()

	registrar, err := mcp.NewManagedRegistrar(q, model.NewULID, nil)
	if err != nil {
		return fmt.Errorf("managed registrar: %w", err)
	}

	// Host endpoint: one server, one listener per instance network.
	generations := generation.New()
	hostServer := hostendpoint.NewServer()
	hostServer.Register(hostendpoint.Tier1Tools(hostendpoint.Tier1Deps{
		Querier:       q,
		EncryptionKey: deps.EncryptionKey,
		Metrics:       pluginmetrics.New(),
		Health:        health,
	})...)
	hostServer.Register(hostendpoint.Tier2Tools(hostendpoint.Tier2Deps{Querier: q})...)
	hostServer.Register(hostendpoint.AuthorizeActorTools(hostendpoint.AuthorizeActorDeps{
		Querier:   q,
		Directory: hostendpoint.DBActorDirectory{Querier: q},
	})...)
	hostServer.Register(hostendpoint.UserLinkTools(hostendpoint.UserLinkDeps{Querier: q})...)
	hostListeners := hostendpoint.NewListenerSet(
		hostendpoint.Chain(hostServer, hostendpoint.GenerationTokenResolver{Querier: q}, generations))

	// Egress proxy: the only way out of a plugin's internal network.
	gateways := egress.NewGatewayRegistry()
	proxy, err := egress.New(egress.Config{Resolver: gateways})
	if err != nil {
		return fmt.Errorf("egress proxy: %w", err)
	}
	egressListeners := hostendpoint.NewListenerSet(proxy)

	// Observers.
	mcpRegistry := mcp.NewRegistry(q,
		mcp.WithMCPTimeout(deps.Config.MCPTimeout),
		mcp.WithEncryptionKey(deps.EncryptionKey),
		mcp.WithToolNamespaceArbiter(deps.Arbiter),
	)
	discover, err := events.NewDiscoverProbe(q, mcpRegistry)
	if err != nil {
		return fmt.Errorf("discover probe: %w", err)
	}
	probeTargets := &caphealth.TargetSet{}
	prober, err := caphealth.New(caphealth.Config{
		Registry:  health,
		Targets:   probeTargets,
		Container: containerHealth{runtime: rt},
		Discover:  discover,
		Publisher: deps.Publisher,
	})
	if err != nil {
		return fmt.Errorf("capability prober: %w", err)
	}
	capture, err := logcapture.New(logcapture.Config{Source: rt})
	if err != nil {
		return fmt.Errorf("log capture: %w", err)
	}
	statsTargets := &resources.TargetSet{}
	collector, err := resources.NewCollector(resources.CollectorConfig{Source: rt, Targets: statsTargets})
	if err != nil {
		return fmt.Errorf("resource collector: %w", err)
	}

	a.health = health
	a.prober = prober
	a.collector = collector
	a.hostListeners = hostListeners
	a.egressProxy = egressListeners
	a.newReconciler = func(guard netguard.Guard) (loop, error) {
		rec, err := reconciler.New(reconciler.Config{
			Runtime:                 rt,
			Store:                   q,
			Posture:                 posture,
			Interval:                o.reconcileInterval,
			Publisher:               deps.Publisher,
			Subnets:                 subnets,
			SelfContainerID:         selfID,
			CheckForwardingDisabled: o.checkForwarding,
			OperatorAPIGuard:        guard,
			Rotations:               q,
			GC:                      q,
			Attach:                  &reconciler.AttachDeps{Store: q, Endpoints: registrar},
			Observe: &reconciler.ObserveDeps{
				Probe:  kickingProbeTargets{targets: probeTargets, prober: prober},
				Health: health,
				Logs:   capture,
				Stats:  statsTargets,
			},
			HostListeners:   hostListeners,
			EgressListeners: egressListeners,
			Gateways:        gateways,
			Generations:     generations,
			EgressEnv:       reconciler.EgressEnv(subnets, reconciler.DefaultEgressProxyPort),
			HostEndpointEnv: reconciler.HostEndpointEnv(subnets, reconciler.DefaultHostEndpointPort),
		})
		if err != nil {
			return nil, err
		}
		return rec, nil
	}
	return nil
}

// kickingProbeTargets wakes the prober whenever the observe pass changes its
// target set, so a new generation is probed now rather than at the next tick.
// The observe pass only calls SetTargets when the set changed.
type kickingProbeTargets struct {
	targets *caphealth.TargetSet
	prober  *caphealth.Prober
}

func (k kickingProbeTargets) SetTargets(targets []caphealth.Target) {
	k.targets.SetTargets(targets)
	k.prober.Kick()
}

// containerHealth answers the prober's container-level liveness question from
// the runtime. It applies the same rule as the rotation health gate: a running
// container with no declared healthcheck counts as healthy.
type containerHealth struct {
	runtime container.Runtime
}

func (c containerHealth) ContainerHealthy(ctx context.Context, containerID string) (bool, string, error) {
	info, err := c.runtime.Inspect(ctx, container.ContainerID(containerID))
	if err != nil {
		return false, "", fmt.Errorf("inspecting container %s: %w", containerID, err)
	}
	if info.State != container.ContainerStateRunning {
		return false, fmt.Sprintf("container is %s", info.State), nil
	}
	switch info.Health {
	case "", "healthy":
		return true, "", nil
	}
	return false, fmt.Sprintf("container healthcheck reports %s", info.Health), nil
}
