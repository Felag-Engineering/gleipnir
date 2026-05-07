package process

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
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
}

// querier is the narrow database interface required by Manager. Using an
// interface (not *db.Queries directly) keeps Manager unit-testable with a
// fake querier and mirrors the pattern used elsewhere in the codebase.
type querier interface {
	ListPluginsByStatus(ctx context.Context, status string) ([]db.Plugin, error)
	ListPluginInstancesByPlugin(ctx context.Context, pluginID string) ([]db.PluginInstance, error)
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	UpdatePluginInstanceHealth(ctx context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error)
}

// processStarter is the function signature for starting a subprocess. The
// default is process.Start; tests inject a stub to avoid real subprocess
// spawning.
type processStarter func(ctx context.Context, cfg Config) (*Instance, error)

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
	// NoopHostServer{}. #292 will inject a function that constructs a live
	// hostsvc.Server per instance.
	HostServerFor func(instanceID string) hostwire.HostServer

	// Logger is the base logger. If nil, slog.Default() is used.
	Logger *slog.Logger

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
	starter   processStarter
}

// NewManager constructs a Manager from cfg. The returned manager has no running
// instances; call StartAllActive or Start to spawn subprocesses.
func NewManager(cfg ManagerConfig) *Manager {
	starter := cfg.TestProcessStarter
	if starter == nil {
		starter = Start
	}

	return &Manager{
		cfg:       cfg,
		instances: make(map[string]*Instance),
		starter:   starter,
	}
}

// Start spawns a subprocess for the given plugin instance.
//
// It refuses to start if:
//   - The plugin's status is not "active".
//   - An instance with the same instance_id is already running.
//   - The instance's current health state is in blockedHealthStates.
//
// On success the Instance is stored in the internal map. Callers should call
// Stop or StopAll to terminate running instances.
func (m *Manager) Start(ctx context.Context, plugin db.Plugin, instance db.PluginInstance, binaryPath string) error {
	if plugin.Status != "active" {
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

	m.mu.Lock()
	_, alreadyRunning := m.instances[instance.ID]
	m.mu.Unlock()

	if alreadyRunning {
		return fmt.Errorf("manager: instance %s is already running", instance.ID)
	}

	host := hostwire.HostServer(NoopHostServer{})
	if m.cfg.HostServerFor != nil {
		host = m.cfg.HostServerFor(instance.ID)
	}

	cfg := Config{
		BinaryPath:     binaryPath,
		InstanceID:     instance.ID,
		PluginID:       plugin.ID,
		InstanceName:   instance.InstanceName,
		StartupTimeout: m.cfg.DefaultStartupTimeout,
		StopGrace:      m.cfg.DefaultStopGrace,
		IdentityIssuer: m.cfg.IdentityIssuer,
		HealthSetter:   m.buildHealthSetter(),
		HostServer:     host,
		Logger:         m.cfg.Logger,
	}

	inst, err := m.starter(ctx, cfg)
	if err != nil {
		return fmt.Errorf("manager: start instance %s: %w", instance.ID, err)
	}

	m.mu.Lock()
	m.instances[instance.ID] = inst
	m.mu.Unlock()

	return nil
}

// Stop terminates the subprocess for instanceID and removes it from the
// running-instances map. Returns nil if instanceID is not found (idempotent).
func (m *Manager) Stop(ctx context.Context, instanceID string) error {
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
	plugins, err := m.cfg.Querier.ListPluginsByStatus(ctx, "active")
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

		for _, inst := range instances {
			// BinaryPath is not stored in the DB; the loader writes the extracted
			// binary to a well-known path under PluginsDir. Manager expects that
			// path to be resolved by the caller. For StartAllActive the binary path
			// is not yet available (#291 scope: the watcher and installer resolve
			// it; we log and skip for now).
			//
			// TODO #291 follow-up: pass binaryPath through Start when the installer
			// exposes a DB-persisted binary path column.
			_ = inst
			m.logger().Warn("StartAllActive: subprocess spawn not yet wired for persisted instances",
				"plugin_id", p.ID, "instance_id", inst.ID,
				"note", "binary path column pending installer work")
		}
	}

	return nil
}

// Lookup returns the running Instance for instanceID, or nil if no subprocess
// is running for that instance. #292 uses this to obtain the Client for
// dispatching tool calls.
func (m *Manager) Lookup(instanceID string) *Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.instances[instanceID]
}

// buildHealthSetter returns a HealthSetter closure that routes through
// pluginstate.SetHealthState with the manager's DB querier and publisher.
// ErrIllegalTransition is treated as warn-and-continue because the instance
// may already be in a terminal state from a concurrent writer.
func (m *Manager) buildHealthSetter() func(ctx context.Context, instanceID string, target model.PluginHealthState, detail string) {
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

// logger returns the configured logger, falling back to slog.Default().
func (m *Manager) logger() *slog.Logger {
	if m.cfg.Logger != nil {
		return m.cfg.Logger
	}
	return slog.Default()
}
