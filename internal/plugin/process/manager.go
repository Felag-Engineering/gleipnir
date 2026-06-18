package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// blockedHealthStates is the set of health states that should not have a
// subprocess started. These states either represent security problems
// (signature_invalid, verification_error) or pending-admin-action conditions
// that must be resolved before a subprocess may run.
//
// crashed is also included because auto-restart (spec §13.5) is out of scope
// for #291. A crashed instance must be manually restarted by an operator
// action; we record the crash in the health state and leave it there.
var blockedHealthStates = map[model.PluginHealthState]bool{
	model.PluginHealthStateSignatureInvalid:        true,
	model.PluginHealthStateVerificationError:       true,
	model.PluginHealthStatePendingKeyApproval:      true,
	model.PluginHealthStatePendingConfigMigration:  true,
	model.PluginHealthStatePendingManifestApproval: true,
	model.PluginHealthStateCrashed:                 true,
	// inactive means deliberately deactivated by an admin (#243). StartAllActive
	// must not re-spawn deactivated instances on server restart.
	model.PluginHealthStateInactive: true,
}

// querier is the narrow database interface required by Manager. Using an
// interface (not *db.Queries directly) keeps Manager unit-testable with a
// fake querier and mirrors the pattern used elsewhere in the codebase.
type querier interface {
	ListPluginsByStatus(ctx context.Context, status string) ([]db.Plugin, error)
	ListPluginInstancesByPlugin(ctx context.Context, pluginID string) ([]db.PluginInstance, error)
	GetPluginByID(ctx context.Context, id string) (db.Plugin, error)
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	UpdatePluginInstanceHealth(ctx context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error)
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
}

// processStarter is the function signature for starting a subprocess. The
// default is process.Start; tests inject a stub to avoid real subprocess
// spawning.
type processStarter func(ctx context.Context, cfg Config) (*Instance, error)

// ToolRegistrar registers and unregisters plugin tool names in the cross-source
// namespace arbiter. The production implementation is *tools.Registrar from
// internal/plugin/tools.
type ToolRegistrar interface {
	RegisterInstanceTools(ctx context.Context, instanceID, instanceName string, toolNames []string, generation int64) error
	UnregisterInstance(ctx context.Context, instanceName string)
}

// ManagerConfig holds all constructor parameters for a Manager.
type ManagerConfig struct {
	// Querier provides read access to plugin and plugin_instance rows.
	Querier querier

	// Publisher is used by the HealthSetter callback to emit health_changed
	// events. May be nil; events are skipped when nil.
	Publisher event.Publisher

	// IdentityIssuer mints and revokes per-instance identity tokens.
	IdentityIssuer IdentityIssuer

	// DefaultStartupTimeout is applied to every Config when not explicitly set.
	// If zero, process.Start applies its own default (10s).
	DefaultStartupTimeout time.Duration

	// DefaultStopGrace is the maximum time Stop waits for each subprocess to
	// exit. If zero, process.Start applies its own default (10s).
	DefaultStopGrace time.Duration

	// HostServerFor returns the hostwire.HostServer to register on the broker
	// for a given instance. If nil, defaults to a function that always returns
	// NoopHostServer{}. Returns the shared *hostsvc.Server for every instance ID
	// (one server per host). Per-instance routing happens at RPC time: the token
	// interceptor reads gleipnir-instance-token metadata and binds the resolved
	// instance ID into the request context before the handler runs.
	HostServerFor func(instanceID string) hostwire.HostServer

	// ServerInterceptors are chained onto the host-side gRPC broker server for
	// every subprocess this Manager spawns. Production wiring supplies
	// [UnaryInstanceTokenInterceptor, UnaryGenerationRefcountInterceptor,
	// UnaryCallIDInterceptor] (chain order is significant; token must be first).
	// nil means no interceptors are added, preserving handshake-only test behaviour.
	ServerInterceptors []grpc.UnaryServerInterceptor

	// Logger is the base logger. If nil, slog.Default() is used.
	Logger *slog.Logger

	// GenerationController tracks in-flight Host RPC refcounts per instance and
	// coordinates hot-reload drains. When nil, the Manager skips generation
	// tracking — this preserves test injection ergonomics. ReloadInstance
	// requires a non-nil controller and returns an error when one is not
	// configured.
	GenerationController *generation.Controller

	// ToolRegistrar claims and releases plugin tool dot-names in the shared
	// namespace arbiter. When nil (tests), tool registration is skipped.
	// Production callers must set this.
	ToolRegistrar ToolRegistrar

	// TestProcessStarter overrides process.Start. Intended for unit tests only;
	// production callers must leave this nil. When set, Manager.Start calls
	// this function instead of the real process.Start, which avoids spawning
	// actual subprocesses in DB-free unit tests.
	TestProcessStarter processStarter
}

// Manager owns the lifecycle of all running plugin subprocesses. It maintains
// a map from instance_id → *Instance, protected by a mutex, and exposes Start,
// Stop, StopAll, StartAllActive, and Lookup.
type Manager struct {
	cfg       ManagerConfig
	mu        sync.Mutex
	instances map[string]*Instance
	// spawning tracks instance IDs that are currently in the process of being
	// started (between the guard check and the post-spawn map insert). This is
	// the sentinel that closes the double-spawn race: a second concurrent Start
	// for the same instance ID will observe the sentinel under m.mu and return
	// an error without calling the starter a second time. The entry is removed
	// whether the spawn succeeds or fails.
	spawning map[string]struct{}
	starter  processStarter

	// toolGenMu guards toolGenerations.
	toolGenMu sync.Mutex
	// toolGenerations tracks the tool-registrar generation per instance ID.
	// This counter is independent of the host-RPC generation in
	// generation.Controller (see generation/controller.go). The tool generation
	// is int64 and tracks stale capability snapshots; the host-RPC generation
	// is uint64 and tracks in-flight RPC refcounts. They live in different
	// epochs — do not attempt to correlate them.
	toolGenerations map[string]int64
}

// NewManager constructs a Manager from cfg. The returned manager has no running
// instances; call StartAllActive or Start to spawn subprocesses.
func NewManager(cfg ManagerConfig) *Manager {
	starter := cfg.TestProcessStarter
	if starter == nil {
		starter = Start
	}

	return &Manager{
		cfg:             cfg,
		instances:       make(map[string]*Instance),
		spawning:        make(map[string]struct{}),
		starter:         starter,
		toolGenerations: make(map[string]int64),
	}
}

// Start spawns a subprocess for the given plugin instance.
//
// It refuses to start if:
//   - The plugin's status is not PluginStatusActive.
//   - An instance with the same instance_id is already running.
//   - The instance's current health state is in blockedHealthStates.
//
// On success the Instance is stored in the internal map. Callers should call
// Stop or StopAll to terminate running instances.
func (m *Manager) Start(ctx context.Context, plugin db.Plugin, instance db.PluginInstance, binaryPath string) error {
	if plugin.Status != string(model.PluginStatusActive) {
		return fmt.Errorf("manager: plugin %s is not active (status=%s)", plugin.ID, plugin.Status)
	}

	healthState := model.PluginHealthState(instance.HealthState)
	if blockedHealthStates[healthState] {
		// Log at debug; this is an expected condition for freshly-installed or
		// crashed instances, not an error.
		m.logger().Debug("skipping subprocess start for blocked health state",
			"instance_id", instance.ID,
			"health_state", instance.HealthState,
		)
		return nil
	}

	// Insert a spawning sentinel under the lock before calling the starter.
	// This closes the double-spawn race: a second concurrent Start for the same
	// instance ID will observe either the sentinel or the live *Instance entry
	// and return an error without calling the starter a second time.
	m.mu.Lock()
	_, alreadyRunning := m.instances[instance.ID]
	_, alreadySpawning := m.spawning[instance.ID]
	if !alreadyRunning && !alreadySpawning {
		m.spawning[instance.ID] = struct{}{}
	}
	m.mu.Unlock()

	if alreadyRunning || alreadySpawning {
		return fmt.Errorf("manager: instance %s is already running", instance.ID)
	}
	// Remove the sentinel when Start returns, whether it succeeds or fails.
	defer func() {
		m.mu.Lock()
		delete(m.spawning, instance.ID)
		m.mu.Unlock()
	}()

	host := hostwire.HostServer(NoopHostServer{})
	if m.cfg.HostServerFor != nil {
		host = m.cfg.HostServerFor(instance.ID)
	}

	cfg := Config{
		BinaryPath:         binaryPath,
		InstanceID:         instance.ID,
		PluginID:           plugin.ID,
		InstanceName:       instance.InstanceName,
		StartupTimeout:     m.cfg.DefaultStartupTimeout,
		StopGrace:          m.cfg.DefaultStopGrace,
		IdentityIssuer:     m.cfg.IdentityIssuer,
		HealthSetter:       m.HealthSetter(),
		HostServer:         host,
		Logger:             m.cfg.Logger,
		ServerInterceptors: m.cfg.ServerInterceptors,
	}

	inst, err := m.starter(ctx, cfg)
	if err != nil {
		m.handleLaunchFailure(ctx, plugin, instance, err)
		return fmt.Errorf("manager: start instance %s: %w", instance.ID, err)
	}

	// Register with the generation controller before the instance is visible in
	// the instances map. Idempotent: safe on cold start and post-reload restart
	// (RegisterInstance never resets the counter after BeginDrain has bumped it).
	if m.cfg.GenerationController != nil {
		m.cfg.GenerationController.RegisterInstance(instance.ID)
	}

	m.mu.Lock()
	m.instances[instance.ID] = inst
	m.mu.Unlock()

	// Register the plugin's manifest-declared tools in the shared namespace
	// arbiter. Parsing the manifest here (rather than in the caller) keeps
	// tool registration tightly coupled to the spawn lifecycle. On conflict,
	// the Registrar already drives the instance to unhealthy and writes an audit
	// event (#194); we surface the error to the caller so the subsystem can
	// decide how to handle it.
	if m.cfg.ToolRegistrar != nil {
		var mfst sdkmanifest.Manifest
		if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &mfst); parseErr != nil {
			m.logger().Warn("could not parse manifest for tool registration",
				"instance_id", instance.ID,
				"err", parseErr,
			)
		} else if len(mfst.Tools) > 0 {
			toolNames := make([]string, len(mfst.Tools))
			for i, td := range mfst.Tools {
				toolNames[i] = td.Name
			}
			gen := m.nextToolGeneration(instance.ID)
			if regErr := m.cfg.ToolRegistrar.RegisterInstanceTools(
				ctx, instance.ID, instance.InstanceName, toolNames, gen,
			); regErr != nil {
				m.logger().Warn("plugin tool registration failed",
					"instance_id", instance.ID,
					"instance_name", instance.InstanceName,
					"err", regErr,
				)
				return fmt.Errorf("manager: register tools for instance %s: %w", instance.ID, regErr)
			}
		}
	}

	return nil
}

// Stop terminates the subprocess for instanceID and removes it from the
// running-instances map. Returns nil if instanceID is not found (idempotent).
func (m *Manager) Stop(ctx context.Context, instanceID string) error {
	// Capture the instance name before stopWithoutUnregister deletes it from
	// the map — tool unregistration is keyed by name, not ULID.
	var instanceName string
	m.mu.Lock()
	if inst, ok := m.instances[instanceID]; ok {
		instanceName = inst.cfg.InstanceName
	}
	m.mu.Unlock()

	if err := m.stopWithoutUnregister(ctx, instanceID); err != nil {
		return err
	}
	if m.cfg.GenerationController != nil {
		m.cfg.GenerationController.UnregisterInstance(instanceID)
	}
	if m.cfg.ToolRegistrar != nil && instanceName != "" {
		m.cfg.ToolRegistrar.UnregisterInstance(ctx, instanceName)
	}
	m.removeToolGeneration(instanceID)
	return nil
}

// stopWithoutUnregister terminates the subprocess and revokes the identity token
// but does NOT call GenerationController.UnregisterInstance. ReloadInstance uses
// this helper so it can call BeginDrain before stopping and then let the
// post-reload Start's RegisterInstance reuse the already-bumped generation.
func (m *Manager) stopWithoutUnregister(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	inst, ok := m.instances[instanceID]
	if ok {
		delete(m.instances, instanceID)
	}
	m.mu.Unlock()

	if !ok {
		return nil
	}

	// Belt-and-braces: revoke by instance even if the Instance.Stop path also
	// revokes by token. identity.RevokeInstance is idempotent.
	if r, ok := m.cfg.IdentityIssuer.(interface{ RevokeInstance(string) }); ok {
		r.RevokeInstance(instanceID)
	}

	return inst.Stop(ctx)
}

// ReloadInstance stops the current subprocess for instanceID, drains its
// in-flight Host RPCs (in-flight RPCs continue under a cancellable context for
// up to graceTimeout, then are force-cancelled per spec §13.8; new RPCs return
// codes.Unavailable while drain is active), and starts a fresh subprocess for
// the same instance ID. The new generation does not begin accepting Host RPCs
// until BeginDrain returns.
//
// Note: if Start fails after BeginDrain and stopWithoutUnregister have already
// succeeded, the instance is in a stopped state with a bumped generation but no
// running subprocess. The caller is responsible for calling Stop to fully
// unregister the instance from the generation controller before retrying.
//
// The actual hot-reload trigger (loader watcher → fresh tarball → calling
// ReloadInstance) is out of scope for #294; this method is the public API that
// #295 / the loader will call.
//
// See issue #294.
func (m *Manager) ReloadInstance(ctx context.Context, plugin db.Plugin, instance db.PluginInstance, binaryPath string, graceTimeout time.Duration) error {
	if m.cfg.GenerationController == nil {
		return errors.New("manager: generation controller not configured; reload requires #294 wiring")
	}

	// BeginDrain must run BEFORE we stop the subprocess so that in-flight Host
	// RPCs under the old generation can complete within the grace window. It also
	// bumps the generation counter — Start's RegisterInstance call afterward is
	// idempotent and returns the already-bumped value.
	_, drained, err := m.cfg.GenerationController.BeginDrain(ctx, instance.ID, graceTimeout)
	if err != nil {
		return fmt.Errorf("manager: begin drain for %s: %w", instance.ID, err)
	}
	if !drained {
		m.logger().Warn("plugin reload: not all in-flight Host RPCs drained within grace; force-cancelled",
			"instance_id", instance.ID,
		)
	}

	// Stop the subprocess without unregistering from the controller: the
	// generation was already bumped by BeginDrain, and RegisterInstance (called
	// inside Start below) is idempotent and will not reset it.
	if err := m.stopWithoutUnregister(ctx, instance.ID); err != nil {
		return fmt.Errorf("manager: stop instance %s for reload: %w", instance.ID, err)
	}

	// Release old tool reservations before starting the new generation. If the
	// manifest changed during hot-reload (different tool set), stale reservations
	// would block the new generation from registering cleanly. We do NOT call
	// removeToolGeneration here — nextToolGeneration increments from the previous
	// value when Start registers the new tool set, preserving monotonicity.
	if m.cfg.ToolRegistrar != nil {
		m.cfg.ToolRegistrar.UnregisterInstance(ctx, instance.InstanceName)
	}

	if err := m.Start(ctx, plugin, instance, binaryPath); err != nil {
		return fmt.Errorf("manager: restart instance %s after reload: %w", instance.ID, err)
	}

	return nil
}

// StopAll stops all running instances concurrently. Each Stop call shares the
// same ctx deadline, so all instances get the full remaining context budget.
// All errors are collected and joined; a partial failure does not prevent other
// instances from being stopped.
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	snapshot := make(map[string]*Instance, len(m.instances))
	for id, inst := range m.instances {
		snapshot[id] = inst
		delete(m.instances, id)
	}
	m.mu.Unlock()

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	for id, inst := range snapshot {
		wg.Add(1)
		go func(id string, inst *Instance) {
			defer wg.Done()
			if err := inst.Stop(ctx); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("instance %s: %w", id, err))
				mu.Unlock()
			}
			// Best-effort tool unregistration. Not strictly necessary during
			// shutdown (the arbiter and toolGenerations are in-memory and die
			// with the process), but keeps the invariant that Stop always
			// releases tool names.
			//
			// GenerationController.UnregisterInstance is intentionally NOT called
			// here (pre-existing gap; StopAll deletes the map in bulk). Both
			// omissions are harmless: the in-memory state dies with the process.
			if m.cfg.ToolRegistrar != nil {
				m.cfg.ToolRegistrar.UnregisterInstance(ctx, inst.cfg.InstanceName)
			}
		}(id, inst)
	}

	wg.Wait()
	return errors.Join(errs...)
}

// StartAllActive queries the DB for all active plugins and their instances,
// then calls Start for each instance that is not in a blocked health state.
//
// Per-instance errors are logged at Warn and do not abort the loop — one
// misbehaving plugin binary must not prevent others from starting. The method
// returns nil regardless of per-instance errors, consistent with the
// "best-effort bulk start" semantics at server startup.
func (m *Manager) StartAllActive(ctx context.Context) error {
	plugins, err := m.cfg.Querier.ListPluginsByStatus(ctx, string(model.PluginStatusActive))
	if err != nil {
		return fmt.Errorf("manager: list active plugins: %w", err)
	}

	for _, p := range plugins {
		instances, err := m.cfg.Querier.ListPluginInstancesByPlugin(ctx, p.ID)
		if err != nil {
			m.logger().Warn("could not list instances for plugin",
				"plugin_id", p.ID, "err", err)
			continue
		}

		if p.BinaryPath == nil || *p.BinaryPath == "" {
			m.logger().Warn("plugin row has no binary_path; skipping spawn (likely a legacy row — redeploy the tarball to repair)",
				"plugin_id", p.ID)
			continue
		}

		for _, inst := range instances {
			if err := m.Start(ctx, p, inst, *p.BinaryPath); err != nil {
				m.logger().Warn("StartAllActive: failed to start instance",
					"plugin_id", p.ID, "instance_id", inst.ID, "err", err)
			}
		}
	}

	return nil
}

// StartByPluginID reads the plugin row for pluginID, lists its instances, and
// calls Start for each instance that is not already running and not in a blocked
// health state. Per-instance failures are logged at Warn and do not abort the
// loop — one bad instance must not block others.
//
// Returns an error only when the DB lookup fails. Per-instance start errors are
// considered transient and are not surfaced to the caller.
func (m *Manager) StartByPluginID(ctx context.Context, pluginID string) error {
	p, err := m.cfg.Querier.GetPluginByID(ctx, pluginID)
	if err != nil {
		return fmt.Errorf("manager: get plugin %s: %w", pluginID, err)
	}

	if p.BinaryPath == nil || *p.BinaryPath == "" {
		m.logger().Warn("StartByPluginID: plugin row has no binary_path; cannot spawn",
			"plugin_id", pluginID)
		return nil
	}

	instances, err := m.cfg.Querier.ListPluginInstancesByPlugin(ctx, pluginID)
	if err != nil {
		return fmt.Errorf("manager: list instances for plugin %s: %w", pluginID, err)
	}

	for _, inst := range instances {
		if err := m.Start(ctx, p, inst, *p.BinaryPath); err != nil {
			m.logger().Warn("StartByPluginID: failed to start instance",
				"plugin_id", pluginID, "instance_id", inst.ID, "err", err)
		}
	}
	return nil
}

// StopByPluginID stops all running instances that belong to pluginID. It takes
// a snapshot of the running instances under the mutex so the loop does not hold
// the lock while calling Stop (which blocks on subprocess teardown). Per-instance
// stop errors are logged and joined; a partial failure does not prevent remaining
// instances from being stopped.
func (m *Manager) StopByPluginID(ctx context.Context, pluginID string) error {
	m.mu.Lock()
	var toStop []*Instance
	for _, inst := range m.instances {
		if inst.PluginID() == pluginID {
			toStop = append(toStop, inst)
		}
	}
	m.mu.Unlock()

	var errs []error
	for _, inst := range toStop {
		if err := m.Stop(ctx, inst.InstanceID()); err != nil {
			errs = append(errs, fmt.Errorf("instance %s: %w", inst.InstanceID(), err))
		}
	}
	return errors.Join(errs...)
}

// InstanceInfo holds a snapshot of the fields needed for RSS sampling. Using
// this struct instead of exposing *Instance directly keeps the sampler
// decoupled from Instance internals and makes Snapshot() safe for concurrent
// callers (the sampler holds a copy, not a live pointer).
type InstanceInfo struct {
	Pid          int
	InstanceName string
	PluginID     string
}

// Snapshot returns a point-in-time copy of the running-instances map as
// InstanceInfo values keyed by instance ID. The sampler calls this on every
// tick to discover which PIDs to read RSS for; a copy (not a live reference)
// avoids holding the mutex across the /proc read loop.
func (m *Manager) Snapshot() map[string]InstanceInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string]InstanceInfo, len(m.instances))
	for id, inst := range m.instances {
		out[id] = InstanceInfo{
			Pid:          inst.Pid(),
			InstanceName: inst.cfg.InstanceName,
			PluginID:     inst.cfg.PluginID,
		}
	}
	return out
}

// Lookup returns the running Instance for instanceID, or nil if no subprocess
// is running for that instance. #292 uses this to obtain the Client for
// dispatching tool calls.
func (m *Manager) Lookup(instanceID string) *Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.instances[instanceID]
}

// LookupByName returns the running Instance whose InstanceName matches name, or
// nil if no such subprocess is running.
//
// Manager's primary index is by ULID (instance.ID); this helper supports the
// dispatch layer, whose ConnFactory contract is keyed on the human-readable
// instance_name (the same value persisted in plugin_instances.instance_name and
// threaded through dispatch.Pool/dispatch.Dispatcher).
//
// Implementation is a linear scan under m.mu. The instances map is small (one
// entry per running plugin instance) so this is fine; a second name→id index
// would add lifecycle complexity for no measurable benefit.
func (m *Manager) LookupByName(name string) *Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inst := range m.instances {
		if inst.cfg.InstanceName == name {
			return inst
		}
	}
	return nil
}

// HealthSetter returns a closure that routes plugin-instance health-state
// transitions through pluginstate.SetHealthState with the manager's DB
// querier and publisher. Internal callbacks (crash handlers) and external
// callers (e.g. the trigger supervisor) share the same closure so they
// participate in the same state machine without importing internal/plugin/state
// directly. ErrIllegalTransition is treated as warn-and-continue because the
// instance may already be in a terminal state from a concurrent writer.
//
// Safe to call after NewManager; the returned function captures the manager's
// querier and publisher by reference so it reflects any hot-reloaded querier.
func (m *Manager) HealthSetter() func(ctx context.Context, instanceID string, target model.PluginHealthState, detail string) {
	return func(ctx context.Context, instanceID string, target model.PluginHealthState, detail string) {
		err := pluginstate.SetHealthState(
			ctx,
			m.cfg.Querier,
			m.cfg.Publisher,
			instanceID,
			pluginstate.OriginHost,
			target,
			detail,
		)
		if err == nil {
			return
		}
		if errors.Is(err, pluginstate.ErrIllegalTransition) {
			logctx.Logger(ctx).Warn("plugin health transition skipped (illegal)",
				"instance_id", instanceID,
				"target", string(target),
				"err", err,
			)
			return
		}
		logctx.Logger(ctx).Error("plugin health setter failed",
			"instance_id", instanceID,
			"target", string(target),
			"err", err,
		)
	}
}

// handleLaunchFailure is called when process.Start (or the TestProcessStarter)
// returns an error. It:
//  1. Emits a plugin_crashed audit event with the error and stderr excerpt.
//  2. Calls HealthSetter with a human-readable handshake-failure detail so the
//     admin UI shows something more actionable than a raw gRPC error string.
//
// Both steps are best-effort: failures are logged but do not propagate.
func (m *Manager) handleLaunchFailure(ctx context.Context, plugin db.Plugin, instance db.PluginInstance, launchErr error) {
	// Extract stderr from LaunchError when available.
	var stderrExcerpt string
	var le *LaunchError
	if errors.As(launchErr, &le) && len(le.Stderr) > 0 {
		stderrExcerpt = string(le.Stderr)
	}

	// Build a concise reason for the health detail. Use the first 120 chars of
	// the cause so the admin UI card is readable without wrapping.
	reason := launchErr.Error()
	if le != nil {
		reason = le.Cause.Error()
	}
	const maxReasonLen = 120
	if len(reason) > maxReasonLen {
		reason = reason[:maxReasonLen] + "..."
	}
	healthDetail := "subprocess_handshake_failed: " + reason

	// Update health state to unhealthy with the actionable detail. We call the
	// HealthSetter directly (same path the crash watchdog uses) so the state
	// machine is the single source of truth.
	if healthSetter := m.HealthSetter(); healthSetter != nil {
		healthSetter(ctx, instance.ID, model.PluginHealthStateUnhealthy, healthDetail)
	}

	// Write the audit event. Best-effort: log and continue on DB failure.
	instanceID := instance.ID
	payload := map[string]any{
		"plugin_id":   plugin.ID,
		"instance_id": instanceID,
		"error":       launchErr.Error(),
	}
	if stderrExcerpt != "" {
		payload["stderr_excerpt"] = stderrExcerpt
	}
	body, _ := json.Marshal(payload)
	_, auditErr := m.cfg.Querier.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &instanceID,
		EventType:        "plugin_crashed",
		Severity:         "high",
		ActorUserID:      nil,
		PayloadJson:      string(body),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	})
	if auditErr != nil {
		m.logger().Warn("handleLaunchFailure: could not write plugin_crashed audit event",
			"instance_id", instanceID, "err", auditErr)
	}
}

// nextToolGeneration returns the next tool-registrar generation for instanceID.
// On cold start (no previous entry) it returns 1. On reload it increments the
// previous value, preserving monotonicity. This counter is independent of the
// host-RPC generation.Controller (generation/controller.go).
func (m *Manager) nextToolGeneration(instanceID string) int64 {
	m.toolGenMu.Lock()
	defer m.toolGenMu.Unlock()
	prev := m.toolGenerations[instanceID]
	next := prev + 1
	m.toolGenerations[instanceID] = next
	return next
}

// removeToolGeneration removes the tool-generation entry for instanceID. Called
// on Stop (full teardown). Not called on ReloadInstance — the reload path uses
// nextToolGeneration to increment instead, preserving the monotonic counter.
func (m *Manager) removeToolGeneration(instanceID string) {
	m.toolGenMu.Lock()
	defer m.toolGenMu.Unlock()
	delete(m.toolGenerations, instanceID)
}

// logger returns the configured logger, falling back to slog.Default().
func (m *Manager) logger() *slog.Logger {
	if m.cfg.Logger != nil {
		return m.cfg.Logger
	}
	return slog.Default()
}
