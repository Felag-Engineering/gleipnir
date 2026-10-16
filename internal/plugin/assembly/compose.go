package assembly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/plugin/inapptask"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle/adminv2"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader"
	"github.com/felag-engineering/gleipnir/internal/plugin/logcapture"
	"github.com/felag-engineering/gleipnir/internal/plugin/pluginmetrics"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
	plugintrigger "github.com/felag-engineering/gleipnir/internal/plugin/trigger"
)

// Option adjusts how New composes the substrate. The options exist so tests
// can run the real composition against container.Fake, or against a pinned
// posture, rather than whatever socket the host happens to have.
type Option func(*options)

// WithDetectConfig replaces the posture inputs New would read from the
// environment and the filesystem (GLEIPNIR_CONTAINER_RUNTIME_MODE, the
// standard socket locations). A test that boots the whole binary uses it so
// the outcome does not depend on the container socket of the machine it runs on.
func WithDetectConfig(cfg container.DetectConfig) Option {
	return func(o *options) { o.detect = &cfg }
}

// WithRuntime supplies the container runtime instead of dialing the socket the
// posture resolved to. Manual posture still wraps it read-only.
func WithRuntime(rt container.Runtime) Option {
	return func(o *options) { o.runtime = rt }
}

// WithReconcileInterval replaces the reconciler's periodic pass cadence (30s by
// default). The loop is level-triggered and takes one converging step per
// instance per pass, so a caller with no way to kick it, such as the DooD suite
// (#985), shortens the interval to bring a fresh instance up in seconds rather
// than minutes.
func WithReconcileInterval(d time.Duration) Option {
	return func(o *options) { o.reconcileInterval = d }
}

type options struct {
	detect            *container.DetectConfig
	runtime           container.Runtime
	selfContainerID   *container.ContainerID
	checkForwarding   func() error
	reconcileInterval time.Duration

	// watchedInstaller wraps the installer the directory watcher calls, and
	// watcherDebounce replaces its settle window. Tests use them to observe
	// each install completing and to avoid waiting out the default window.
	watchedInstaller func(loader.BundleInstaller) loader.BundleInstaller
	watcherDebounce  time.Duration
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

	// The assembly owns a cancellable child of the process context so Shutdown
	// can stop its own loops rather than relying on the caller to have
	// cancelled first.
	ctx, cancel := context.WithCancel(ctx)
	a := &Assembly{ctx: ctx, cancel: cancel}
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
			cancel()
			return nil, fmt.Errorf("plugin substrate: %w", err)
		}
		slog.Warn("plugins are disabled: no container-runtime socket was found", "err", err)
		return a, nil
	}
	container.LogPosture(slog.Default(), posture)

	pool, err := netip.ParsePrefix(deps.Config.PluginSubnetPool)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("plugin substrate: parsing GLEIPNIR_PLUGIN_SUBNET_POOL %q: %w", deps.Config.PluginSubnetPool, err)
	}

	rt, err := openRuntime(posture, o.runtime)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("plugin substrate: %w", err)
	}
	if err := a.compose(ctx, deps, q, o, posture.Posture, pool, rt); err != nil {
		_ = rt.Close()
		cancel()
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

// channelParts is the task-backed human-in-the-loop route: the poller that
// observes plugin-routed tasks, the adapters that put approval and feedback
// asks on it, and the notifier that fans a fixed message out alongside.
type channelParts struct {
	poller   *mcp.PollScheduler
	adapters *runpkg.TaskChannelAdapters
	notifier *hitl.Notifier
}

// composeChannels builds the route from one shared registry, so the assurance
// gate, the poll and the discover probe resolve a plugin's client the same way
// (see managedClients).
func composeChannels(ctx context.Context, deps Deps, q *db.Queries, health *caphealth.Registry) (channelParts, error) {
	// The scheduler needs the waiter's OnResolved hook at construction and the
	// waiter needs the scheduler as its canceler; the closure resolves the
	// forward reference.
	var waiter *runpkg.TaskWaiter
	poller := mcp.NewPollScheduler(q, deps.MCPRegistry,
		mcp.WithTaskPublisher(deps.Publisher),
		mcp.WithOnResolved(func(ctx context.Context, task db.McpTask, err error) {
			waiter.OnResolved(ctx, task, err)
		}))
	waiter = runpkg.NewTaskWaiter(poller, q)

	clients := managedClients{ctx: ctx, servers: q, clients: deps.MCPRegistry}
	inApp := inapptask.NewManager(q)
	router, err := hitl.New(hitl.Config{
		Clients:   requestClients{clients},
		InApp:     inApp,
		Completer: inApp,
		Tasks:     q,
	})
	if err != nil {
		return channelParts{}, fmt.Errorf("hitl router: %w", err)
	}

	adapters, err := runpkg.NewTaskChannelAdapters(router, waiter, runpkg.NewDBAudienceEntriesResolver(q), q)
	if err != nil {
		return channelParts{}, fmt.Errorf("task channel adapters: %w", err)
	}
	// Without a directory every plugin-routed approval is refused outright, so
	// this is what makes an approval from a channel possible at all.
	adapters.WithActorResolver(hostendpoint.DBActorDirectory{Querier: q})

	notifier := hitl.NewNotifier(hitl.NotifyConfig{
		Clients: notifyClients{clients},
		Health:  health,
		Audit:   q,
	})
	return channelParts{poller: poller, adapters: adapters, notifier: notifier}, nil
}

// compose builds every component in dependency order and stores them on a.
// The reconciler itself is deferred to a.newReconciler, and the events
// supervisor to a.bindEvents.
func (a *Assembly) compose(
	ctx context.Context,
	deps Deps,
	q *db.Queries,
	o options,
	posture container.Posture,
	pool netip.Prefix,
	rt container.Runtime,
) error {
	if deps.MCPRegistry == nil {
		return errors.New("an MCP registry is required (Deps.MCPRegistry)")
	}

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
	endpoints := &managedEndpoints{
		registrar: registrar,
		servers:   q,
		tools:     deps.MCPRegistry,
		arbiter:   deps.Arbiter,
	}

	// Task-backed channels come before the host endpoint because AuthorizeActor
	// hints the poller to resolve a clicked request now rather than at the next
	// poll tick.
	channels, err := composeChannels(ctx, deps, q, health)
	if err != nil {
		return err
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
		PollHint:  channels.poller,
	})...)
	hostServer.Register(hostendpoint.UserLinkTools(hostendpoint.UserLinkDeps{Querier: q})...)
	hostListeners := hostendpoint.NewListenerSet(
		hostendpoint.Chain(hostServer, hostendpoint.GenerationTokenResolver{Querier: q}, generations))

	// Egress proxy: the only way out of a plugin's internal network.
	gateways := egress.NewGatewayRegistry()
	proxy, err := egress.New(egress.Config{Resolver: gateways, Auditor: egressAuditor{store: q}})
	if err != nil {
		return fmt.Errorf("egress proxy: %w", err)
	}
	egressListeners := hostendpoint.NewListenerSet(proxy)

	// Observers.
	discover, err := events.NewDiscoverProbe(q, deps.MCPRegistry)
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

	desired := lifecycle.NewDesiredState(nil)
	a.kicker = &lateKicker{}
	creds := newCredentialParts(deps, q)
	installer := composeInstaller(deps, q, rt)
	watched := loader.BundleInstaller(installer)
	if o.watchedInstaller != nil {
		watched = o.watchedInstaller(watched)
	}
	var watchOpts []loader.WatcherOption
	if o.watcherDebounce > 0 {
		watchOpts = append(watchOpts, loader.WithDebounce(o.watcherDebounce))
	}
	a.watcher = loader.NewWatcher(deps.Config.PluginsDir, watched, watchOpts...)
	a.admin = AdminDeps{
		Installer:     installer,
		PluginsDir:    deps.Config.PluginsDir,
		ProcMgr:       adminv2.NewProcessManager(q, desired, a.kicker, endpoints),
		Trigger:       adminv2.NewTriggerRestarter(lateEvents{a}),
		Inflight:      adminv2.NewInflightCounter(q, q, deps.MCPRegistry),
		Evictor:       adminv2.ToolConnEvictor{},
		RSSAggregator: adminv2.NewRSSAggregator(collector),
		Unregistrar:   namespaceReleaser{arbiter: deps.Arbiter},
		Provisioner:   desired,
		// Set only here, where the reconciler can run what gets approved.
		ApproveV2Manifests: true,
	}
	if creds != nil {
		a.admin.OAuthHandler = creds.oauth
		a.admin.CredentialsHandler = creds.credentials
		a.admin.CredentialSeeder = creds.store
		a.admin.OnPublicURLChanged = creds.onPublicURLChanged
	}

	a.health = health
	a.prober = prober
	a.collector = collector
	a.hostListeners = hostListeners
	a.egressProxy = egressListeners
	a.poller = channels.poller
	a.notifier = channels.notifier
	a.backgrounds = newBackgroundLoops(deps, q, creds)
	a.launcher = LauncherDeps{
		ApprovalDispatcher: channels.adapters,
		FeedbackDispatcher: channels.adapters,
		ToolClassifier:     &managedToolClassifier{q: q},
	}
	a.bindEvents = func(launcher plugintrigger.RunLauncher) eventStreams {
		// A nil *settings.Service must not become a non-nil interface: the
		// dispatcher treats a nil resolver as "policies carry their own model".
		var models plugintrigger.ModelResolver
		if deps.SystemSettings != nil {
			models = deps.SystemSettings
		}
		dispatcher := plugintrigger.NewDispatcher(plugintrigger.DispatcherConfig{
			Launcher:      launcher,
			Querier:       q,
			Dedup:         dedup.NewDBStore(q),
			Publisher:     deps.Publisher,
			ModelResolver: models,
			Logger:        slog.Default(),
		})
		// HealthSetter is left unset on purpose: instance health has one
		// writer, the capability prober, and a second one here would fight it.
		return events.NewSupervisor(events.Config{
			Querier:    q,
			Servers:    q,
			Clients:    deps.MCPRegistry,
			Capability: health,
			Cursor:     events.NewDBStore(q),
			Sink:       plugintrigger.NewListenSinkAdapter(dispatcher),
			Publisher:  deps.Publisher,
			RootCtx:    a.ctx,
			Logger:     slog.Default(),
		})
	}
	a.newReconciler = func(guard netguard.Guard, streams reconciler.EventStreams) (*reconciler.Reconciler, error) {
		return reconciler.New(reconciler.Config{
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
			Images:                  q,
			Attach:                  &reconciler.AttachDeps{Store: q, Endpoints: endpoints},
			Observe: &reconciler.ObserveDeps{
				Events: streams,
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
