package trigger

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"

	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
)

// InstanceLookup is the narrow interface the Supervisor uses from
// *process.Manager — it only needs to look up a running instance by ID.
//
// In production, *process.Manager satisfies this via managerAdapter.
// In tests, a fake can be injected via Config.TestInstanceLookup.
type InstanceLookup interface {
	// LookupInstance returns the TriggerServiceClient and plugin ID for
	// instanceID, or (nil, "") when no subprocess is running.
	LookupInstance(instanceID string) (client triggerv1.TriggerServiceClient, pluginID string)
}

// managerAdapter wraps *process.Manager to satisfy InstanceLookup.
type managerAdapter struct {
	mgr *process.Manager
}

func (a *managerAdapter) LookupInstance(instanceID string) (triggerv1.TriggerServiceClient, string) {
	inst := a.mgr.Lookup(instanceID)
	if inst == nil {
		return nil, ""
	}
	return inst.Client().Trigger, inst.PluginID()
}

// EventDispatcher is the narrow interface the Supervisor uses from *Dispatcher.
// It allows tests to inject a fake dispatcher without constructing the full
// Dispatcher dependency graph (Querier, Launcher, etc.).
type EventDispatcher interface {
	Handle(ctx context.Context, evt Event) error
}

const (
	defaultBackoffInitial = time.Second
	defaultBackoffMax     = 60 * time.Second
	defaultUnhealthyAfter = 5
)

// Config holds all dependencies for a Supervisor.
type Config struct {
	// Manager is the *process.Manager for subprocess lifecycle. It is used in
	// two ways:
	//   - StartAll calls Lookup to check whether a subprocess is already running.
	//   - streamLoop calls LookupInstance (via the internal managerAdapter) to
	//     obtain the gRPC client for each Start call.
	//
	// Satisfies InstanceLookup via an internal adapter; tests may inject
	// InstanceLookup directly via TestInstanceLookup.
	Manager *process.Manager

	// Querier is used by StartAll to enumerate active plugins and their instances.
	Querier Querier

	// Dispatcher receives each event from the stream recv loop. The *Dispatcher
	// concrete type satisfies EventDispatcher.
	Dispatcher *Dispatcher

	// TestInstanceLookup overrides Manager for unit tests so tests can inject a
	// fake without constructing a real *process.Manager. When non-nil, Manager
	// is ignored for stream opening. Production callers must leave this nil.
	TestInstanceLookup InstanceLookup

	// TestEventDispatcher overrides Dispatcher for unit tests. When non-nil,
	// Dispatcher is ignored. Production callers must leave this nil.
	TestEventDispatcher EventDispatcher

	// HealthSetter is called by the supervisor when a stream's consecutive
	// failure count crosses UnhealthyAfter (marks instance Unhealthy) or
	// recovers (marks instance Healthy). Wire to
	// processManager.HealthSetter() in main.go.
	HealthSetter func(ctx context.Context, instanceID string, target model.PluginHealthState, detail string)

	// Logger is the base logger. If nil, slog.Default() is used.
	Logger *slog.Logger

	// BackoffInitial is the starting sleep duration between reconnect attempts.
	// Defaults to 1s.
	BackoffInitial time.Duration

	// BackoffMax caps the exponential growth of BackoffInitial. Defaults to 60s.
	BackoffMax time.Duration

	// UnhealthyAfter is the number of consecutive stream failures before the
	// supervisor marks the instance Unhealthy. Defaults to 5.
	UnhealthyAfter int
}

// Supervisor manages per-instance trigger stream goroutines. It opens one
// long-lived TriggerService.Start stream per plugin instance and auto-restarts
// on failure (spec §13.5).
//
// All public methods are safe for concurrent use.
type Supervisor struct {
	cfg        Config
	lookup     InstanceLookup  // resolved from cfg.Manager in NewSupervisor
	dispatcher EventDispatcher // resolved from cfg.Dispatcher in NewSupervisor

	mu sync.Mutex
	// instances maps instanceID → cancel func for the stream goroutine context.
	// The cancel func stops the goroutine when Stop or StopAll is called.
	instances map[string]context.CancelFunc
	// done maps instanceID → closed channel signalling goroutine exit.
	done map[string]chan struct{}
}

// NewSupervisor constructs a Supervisor with the given config. Apply defaults
// for zero-value timing fields.
func NewSupervisor(cfg Config) *Supervisor {
	if cfg.BackoffInitial == 0 {
		cfg.BackoffInitial = defaultBackoffInitial
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = defaultBackoffMax
	}
	if cfg.UnhealthyAfter == 0 {
		cfg.UnhealthyAfter = defaultUnhealthyAfter
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	var lookup InstanceLookup
	if cfg.TestInstanceLookup != nil {
		lookup = cfg.TestInstanceLookup
	} else if cfg.Manager != nil {
		lookup = &managerAdapter{mgr: cfg.Manager}
	}

	var dispatcher EventDispatcher
	if cfg.TestEventDispatcher != nil {
		dispatcher = cfg.TestEventDispatcher
	} else if cfg.Dispatcher != nil {
		dispatcher = cfg.Dispatcher
	}

	return &Supervisor{
		cfg:        cfg,
		lookup:     lookup,
		dispatcher: dispatcher,
		instances:  make(map[string]context.CancelFunc),
		done:       make(map[string]chan struct{}),
	}
}

// StartAll enumerates active plugins via the DB Querier, skips plugins that do
// not declare TriggerService in their manifest (ADR-001: capability gate), and
// spawns one stream goroutine per instance via Start.
//
// Instances whose subprocess is not yet running (Manager.Lookup returns nil)
// are silently skipped — StartManager and StartAll run concurrently at boot;
// the missing instance is an accepted gap for v1.
//
// StartAll is intended to be called once at boot in a goroutine:
//
//	go triggerSupervisor.StartAll(ctx)
func (s *Supervisor) StartAll(ctx context.Context) error {
	plugins, err := s.cfg.Querier.ListPluginsByStatus(ctx, "active")
	if err != nil {
		return err
	}

	for _, p := range plugins {
		// Parse the manifest to gate on TriggerService capability.
		var m sdkmanifest.Manifest
		if parseErr := sdkmanifest.Unmarshal([]byte(p.ManifestSnapshot), &m); parseErr != nil {
			s.cfg.Logger.Warn("trigger supervisor: failed to parse manifest snapshot; skipping plugin",
				"plugin_id", p.ID, "err", parseErr)
			continue
		}
		if m.Services.Trigger == "" {
			// Plugin does not declare TriggerService — do not open a stream.
			continue
		}

		instances, err := s.cfg.Querier.ListPluginInstancesByPlugin(ctx, p.ID)
		if err != nil {
			s.cfg.Logger.Warn("trigger supervisor: failed to list instances for plugin; skipping",
				"plugin_id", p.ID, "err", err)
			continue
		}

		for _, inst := range instances {
			if s.lookup == nil {
				continue
			}
			client, _ := s.lookup.LookupInstance(inst.ID)
			if client == nil {
				// Subprocess not yet spawned — acceptable gap at boot.
				s.cfg.Logger.Debug("trigger supervisor: instance subprocess not yet running; skipping",
					"plugin_id", p.ID, "instance_id", inst.ID)
				continue
			}
			s.Start(ctx, inst.ID)
		}
	}
	return nil
}

// Start spawns a stream goroutine for instanceID if one is not already running.
// The goroutine calls TriggerService.Start on the plugin, drains events into
// Dispatcher.Handle, and auto-restarts on failure with exponential backoff.
//
// The goroutine exits when ctx is cancelled (host shutdown) or Stop/StopAll is
// called. Callers may observe goroutine exit via the done channel returned by
// startedDone; for external callers the done channel is internal.
func (s *Supervisor) Start(ctx context.Context, instanceID string) {
	s.mu.Lock()
	if _, alreadyRunning := s.instances[instanceID]; alreadyRunning {
		s.mu.Unlock()
		return
	}
	streamCtx, cancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	s.instances[instanceID] = cancel
	s.done[instanceID] = doneCh
	s.mu.Unlock()

	go s.streamLoop(streamCtx, instanceID, doneCh)
}

// Stop cancels the stream goroutine for instanceID and waits for it to exit.
// No-op if instanceID is not supervised.
func (s *Supervisor) Stop(instanceID string) {
	s.mu.Lock()
	cancel, ok := s.instances[instanceID]
	doneCh := s.done[instanceID]
	if ok {
		delete(s.instances, instanceID)
		delete(s.done, instanceID)
	}
	s.mu.Unlock()

	if !ok {
		return
	}
	cancel()
	<-doneCh
}

// Restart stops the existing stream goroutine for instanceID (if any) and
// starts a fresh one. The new goroutine re-fetches SubscriptionScopeJson from
// the DB, so scope changes take effect immediately.
//
// If instanceID is not currently supervised, Restart is a no-op.
//
// Lock discipline: the lookup-delete-cancel is done under a single s.mu
// acquisition so a concurrent Stop arriving mid-Restart sees either the old
// map entry (and wins the cancel race) or no entry at all (no-ops). In
// either case at most one goroutine is alive for the instance after Restart
// returns.
func (s *Supervisor) Restart(ctx context.Context, instanceID string) {
	s.mu.Lock()
	oldCancel, ok := s.instances[instanceID]
	oldDone := s.done[instanceID]
	if ok {
		delete(s.instances, instanceID)
		delete(s.done, instanceID)
	}
	s.mu.Unlock()

	if !ok {
		return // nothing to restart; not currently supervised
	}
	oldCancel()
	<-oldDone // await OLD goroutine OUTSIDE the lock

	s.Start(ctx, instanceID)
}

// StopAll cancels every running stream goroutine and waits for all of them to
// exit. Intended to be called from the host shutdown path.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	// Snapshot and clear the maps atomically so concurrent Stop calls do not
	// race with StopAll's iteration.
	cancels := make([]context.CancelFunc, 0, len(s.instances))
	dones := make([]chan struct{}, 0, len(s.done))
	for _, cancel := range s.instances {
		cancels = append(cancels, cancel)
	}
	for _, ch := range s.done {
		dones = append(dones, ch)
	}
	s.instances = make(map[string]context.CancelFunc)
	s.done = make(map[string]chan struct{})
	s.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, ch := range dones {
		<-ch
	}
}

// isEmptyScope returns true when scope is empty or the zero-object "{}",
// meaning the operator has not yet configured what the instance should watch.
func isEmptyScope(scope string) bool {
	return scope == "" || scope == "{}"
}

// streamLoop is the goroutine body for a single instance trigger stream. It
// opens TriggerService.Start on the plugin, drains events synchronously into
// Dispatcher.Handle to preserve per-stream ordering, and reconnects on failure.
//
// Backoff: BackoffInitial * 2^n, capped at BackoffMax, jittered ±25%.
// After UnhealthyAfter consecutive failures, HealthSetter is called with
// Unhealthy. On the first successful Recv post-recovery the instance is reset
// to Healthy.
//
// Scope gate: if the instance's subscription_scope_json is empty or "{}" the
// stream is not opened. The goroutine sleeps for BackoffMax and re-checks.
// It is NOT counted as a failure — this is a normal "not yet configured"
// state, not a connectivity problem. When the operator saves a non-empty scope
// and the admin handler calls Restart, this goroutine is cancelled and a fresh
// one starts with the new scope.
func (s *Supervisor) streamLoop(ctx context.Context, instanceID string, doneCh chan struct{}) {
	defer close(doneCh)

	log := s.cfg.Logger.With("instance_id", instanceID)

	consecutive := 0 // consecutive reconnect failures without a successful Recv
	markedUnhealthy := false
	loggedEmptyScope := false // rate-limit the "scope not configured" log line

	for {
		if ctx.Err() != nil {
			return
		}

		if s.lookup == nil {
			// No lookup seam configured — cannot open a stream. Exit.
			return
		}
		triggerClient, _ := s.lookup.LookupInstance(instanceID)
		if triggerClient == nil {
			// Subprocess not running yet — wait one backoff unit and retry.
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			continue
		}

		// Fetch the instance row to obtain the watch scope JSON (stored in
		// plugin_instances.config_json). This is done once per connection
		// attempt; re-fetching on reconnect picks up any config changes
		// saved by an operator while the stream was disconnected.
		dbInst, dbErr := s.cfg.Querier.GetPluginInstanceByID(ctx, instanceID)
		if dbErr != nil {
			log.WarnContext(ctx, "trigger supervisor: failed to fetch instance config; will retry",
				"err", dbErr, "consecutive", consecutive)
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
			continue
		}

		// Scope gate: skip opening a stream when the operator hasn't configured
		// what the instance should watch yet. This is not a failure — do not
		// increment consecutive or mark Unhealthy. Log once at INFO (not on
		// every retry tick) and sleep for BackoffMax before re-checking.
		//
		// When the operator saves a non-empty scope via
		// PUT .../subscription-scope, the admin handler calls Restart which
		// cancels this goroutine and starts a fresh one that will pass the gate.
		if isEmptyScope(dbInst.SubscriptionScopeJson) {
			if !loggedEmptyScope {
				log.InfoContext(ctx, "trigger supervisor: subscription scope not configured; skipping stream until scope is set",
					"instance_id", instanceID)
				loggedEmptyScope = true
			}
			if !s.sleep(ctx, s.cfg.BackoffMax) {
				return
			}
			continue
		}
		// Scope is now non-empty — reset the log-once gate so a future empty →
		// non-empty → empty cycle (edge case) logs again.
		loggedEmptyScope = false

		// Use a long-lived stream — no deadline. Health pings cover liveness
		// (spec §13.6). The parent ctx cancellation propagates on shutdown.
		// SubscriptionScopeJson (not ConfigJson) is the coarse scope — the two
		// fields were conflated in the original stub (#223 fixes that).
		stream, err := triggerClient.Start(ctx, &triggerv1.StartRequest{
			WatchScopeJson: dbInst.SubscriptionScopeJson,
		})
		if err != nil {
			log.WarnContext(ctx, "trigger supervisor: failed to open trigger stream; will retry",
				"err", err, "consecutive", consecutive)
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
			continue
		}

		// Recv loop — single goroutine to preserve per-stream event ordering.
		recvErr := s.recvLoop(ctx, instanceID, stream, log, &markedUnhealthy, &consecutive)
		if recvErr == nil || ctx.Err() != nil {
			// Clean shutdown via ctx cancellation.
			return
		}

		log.WarnContext(ctx, "trigger supervisor: stream ended; will retry",
			"err", recvErr, "consecutive", consecutive)
		if !s.sleep(ctx, s.backoff(consecutive)) {
			return
		}
		consecutive++
		s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
	}
}

// recvLoop drains messages from stream until EOF, an error, or ctx cancellation.
// It resets the consecutive-failure counter and heals the health state on the
// first successful Recv. Returns nil on clean context cancellation.
func (s *Supervisor) recvLoop(
	ctx context.Context,
	instanceID string,
	stream triggerv1.TriggerService_StartClient,
	log *slog.Logger,
	markedUnhealthy *bool,
	consecutive *int,
) error {
	firstEvent := true

	for {
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if err == io.EOF {
				return err
			}
			return err
		}

		// First successful Recv after failures: reset health and counter.
		if firstEvent {
			firstEvent = false
			if *markedUnhealthy {
				if s.cfg.HealthSetter != nil {
					s.cfg.HealthSetter(ctx, instanceID, model.PluginHealthStateHealthy, "")
				}
				*markedUnhealthy = false
			}
			*consecutive = 0
		}

		evt := Event{
			InstanceID:  instanceID,
			EventKind:   resp.GetEventKind(),
			EventID:     resp.GetEventId(),
			PayloadJSON: []byte(resp.GetPayloadJson()),
			ObservedAt:  time.Now(),
		}

		// Resolve PluginID from the running instance. PluginID is carried by
		// the event for observability events on the SSE bus. It may remain
		// empty if the instance is mid-reload; that is fine for dispatch.
		if s.lookup != nil {
			if _, pid := s.lookup.LookupInstance(instanceID); pid != "" {
				evt.PluginID = pid
			}
		}

		// Handle is synchronous: this blocks until dispatch is complete, which
		// provides back-pressure to the plugin and preserves ordering.
		// TODO: consider a per-instance ring-buffer + worker goroutine if
		// Launch latency causes the plugin to see excessive back-pressure.
		if s.dispatcher != nil {
			if handleErr := s.dispatcher.Handle(ctx, evt); handleErr != nil {
				if ctx.Err() != nil {
					return nil
				}
				log.WarnContext(ctx, "trigger supervisor: dispatcher Handle error",
					"event_id", evt.EventID, "err", handleErr)
			}
		}
	}
}

// maybeMarkUnhealthy calls HealthSetter with Unhealthy when consecutive
// failures reach UnhealthyAfter for the first time.
func (s *Supervisor) maybeMarkUnhealthy(ctx context.Context, instanceID string, consecutive int, marked *bool) {
	if !*marked && consecutive >= s.cfg.UnhealthyAfter {
		if s.cfg.HealthSetter != nil {
			s.cfg.HealthSetter(ctx, instanceID, model.PluginHealthStateUnhealthy,
				"trigger stream reconnect failed")
		}
		*marked = true
	}
}

// backoff returns the sleep duration for the given consecutive failure count,
// with exponential growth capped at BackoffMax and ±25% jitter.
func (s *Supervisor) backoff(consecutive int) time.Duration {
	if consecutive < 0 {
		consecutive = 0
	}
	// 2^consecutive growth, but cap the shift to avoid overflow.
	const maxShift = 20
	shift := consecutive
	if shift > maxShift {
		shift = maxShift
	}
	d := s.cfg.BackoffInitial * (1 << shift)
	if d > s.cfg.BackoffMax || d <= 0 {
		d = s.cfg.BackoffMax
	}
	// ±25% jitter: multiply by a random float in [0.75, 1.25).
	jitter := 0.75 + rand.Float64()*0.5
	d = time.Duration(float64(d) * jitter)
	if d > s.cfg.BackoffMax {
		d = s.cfg.BackoffMax
	}
	return d
}

// sleep waits for d or ctx cancellation. Returns false when ctx is done.
func (s *Supervisor) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
