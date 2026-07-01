package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	"github.com/felag-engineering/gleipnir/internal/policy"
)

// ─── Typed errors for InstanceLifecycle ──────────────────────────────────────
//
// Sentinel (no data) and struct (data-carrying) errors for each gate and
// failure branch. Handlers map these to the EXACT current status codes and
// response bodies (preserved byte-for-byte from the old inline handlers).

// ErrStoreUnavailable is returned by Delete and Uninstall when the *db.Store
// has not been injected (503 "plugin management not configured"). This check
// must come FIRST — before any querier call — preserving the current order in
// the old handlers.
var ErrStoreUnavailable = errors.New("plugin management not configured")

// ErrPluginNotFound is returned when GetPluginByID returns ErrNotFound (=sql.ErrNoRows).
var ErrPluginNotFound = errors.New("plugin not found")

// ErrInstanceNotFound is returned when GetPluginInstanceByID returns ErrNotFound, or
// when the fetched instance belongs to a different plugin (plugin/instance mismatch —
// return 404, not 403, to avoid leaking instance existence across plugins).
var ErrInstanceNotFound = errors.New("instance not found")

// ErrAlreadyInactive is returned by Deactivate when the instance is already inactive.
var ErrAlreadyInactive = errors.New("instance is already deactivated")

// ErrRefetchFailed is returned when the state-transition write succeeded but the
// subsequent GetPluginInstanceByID re-fetch failed. MUST NOT be synthesized;
// returning raw config would risk emitting unredacted secrets (ADR-049).
var ErrRefetchFailed = errors.New("state transition succeeded but re-fetch failed")

// TerminalStateError is returned by Deactivate when the instance is in a
// terminal health state (signature_invalid or verification_error) that cannot
// transition to inactive.
type TerminalStateError struct {
	State model.PluginHealthState
}

func (e TerminalStateError) Error() string {
	return fmt.Sprintf("cannot deactivate instance in terminal state; current state: %s", e.State)
}

// inflightOp identifies which operation produced an InflightError, so the
// handler can pick the correct error message string.
type inflightOp int

const (
	inflightOpDeactivate inflightOp = iota
	inflightOpDelete
)

// InflightError is returned by Deactivate and Delete when in-flight tool calls
// are present. Op controls which message the handler formats (the two callers
// produce different 409 bodies per the plan's ERROR SURFACE table).
type InflightError struct {
	Count int
	Op    inflightOp
}

func (e InflightError) Error() string {
	return fmt.Sprintf("in-flight calls: %d", e.Count)
}

// NotInactiveError is returned by Activate when the instance is not in inactive state.
type NotInactiveError struct {
	State string
}

func (e NotInactiveError) Error() string {
	return fmt.Sprintf("instance is not deactivated; current state: %s", e.State)
}

// PolicyRefError is returned by Delete when policies still reference the instance.
type PolicyRefError struct {
	Names []string
}

func (e PolicyRefError) Error() string {
	return "instance is referenced by policies: " + strings.Join(e.Names, ", ")
}

// AudienceRefError is returned by Delete when audience entries still reference the instance.
type AudienceRefError struct {
	Names []string
}

func (e AudienceRefError) Error() string {
	return "instance is referenced by audiences: " + strings.Join(e.Names, ", ")
}

// InstancesRemainError is returned by Uninstall when instances still exist.
type InstancesRemainError struct {
	Names []string
}

func (e InstancesRemainError) Error() string {
	return "all instances must be removed before uninstalling the plugin: " + strings.Join(e.Names, ", ")
}

// lifecycleInternalError wraps an internal failure with a public message that
// the handler writes verbatim as the response body, plus the underlying error
// for logging. This keeps all exact-string literals in one place (the module),
// so the handler's switch arm only needs to read PublicMsg and Detail.
type lifecycleInternalError struct {
	PublicMsg string
	Detail    string
	Err       error
}

func (e *lifecycleInternalError) Error() string { return e.PublicMsg + ": " + e.Err.Error() }
func (e *lifecycleInternalError) Unwrap() error { return e.Err }

// ─── InstanceLifecycleDeps ───────────────────────────────────────────────────

// InstanceLifecycleDeps holds all constructor-injected dependencies for
// InstanceLifecycle. All fields have sane nil-safe defaults (nil procMgr/trigger/
// inflight/unreg = safe no-ops; empty pluginsDir = skip FS cleanup).
type InstanceLifecycleDeps struct {
	Q          PluginQuerier
	Store      *db.Store
	Publisher  event.Publisher
	Clock      func() time.Time
	ProcMgr    PluginProcessManager
	Trigger    TriggerRestarter
	Inflight   InflightCounter
	Evictor    ToolConnEvictor
	Unreg      ToolUnregistrar
	PluginsDir string
}

// InstanceLifecycle owns the four plugin-instance lifecycle transitions:
// Deactivate, Activate, Delete, and Uninstall. It has no knowledge of HTTP —
// all methods return domain objects or typed errors that the handler maps.
//
// Shared deps (processManager, pluginsDir) are also held by PluginHandler for
// non-extracted handlers (CreateInstance/ApprovePlugin/RejectPlugin). Wire the
// same instance/value to both from main.go.
type InstanceLifecycle struct {
	q          PluginQuerier
	store      *db.Store
	publisher  event.Publisher
	clock      func() time.Time
	procMgr    PluginProcessManager
	trigger    TriggerRestarter
	inflight   InflightCounter
	evictor    ToolConnEvictor
	unreg      ToolUnregistrar
	pluginsDir string
}

// NewInstanceLifecycle constructs an InstanceLifecycle with the given deps.
// clock defaults to time.Now when nil.
func NewInstanceLifecycle(deps InstanceLifecycleDeps) *InstanceLifecycle {
	clk := deps.Clock
	if clk == nil {
		clk = time.Now
	}
	return &InstanceLifecycle{
		q:          deps.Q,
		store:      deps.Store,
		publisher:  deps.Publisher,
		clock:      clk,
		procMgr:    deps.ProcMgr,
		trigger:    deps.Trigger,
		inflight:   deps.Inflight,
		evictor:    deps.Evictor,
		unreg:      deps.Unreg,
		pluginsDir: deps.PluginsDir,
	}
}

// resolveLifecycleInstance fetches the instance row and verifies it belongs to
// pluginID. Returns ErrPluginNotFound / ErrInstanceNotFound on the respective
// failure paths (including the plugin/instance mismatch, which returns
// ErrInstanceNotFound to avoid leaking instance existence across plugins).
func (m *InstanceLifecycle) resolveLifecycleInstance(ctx context.Context, pluginID, instanceID string) (db.Plugin, db.PluginInstance, error) {
	plugin, err := m.q.GetPluginByID(ctx, pluginID)
	if errors.Is(err, ErrNotFound) {
		return db.Plugin{}, db.PluginInstance{}, ErrPluginNotFound
	}
	if err != nil {
		return db.Plugin{}, db.PluginInstance{}, &lifecycleInternalError{
			PublicMsg: "failed to get plugin",
			Err:       err,
		}
	}

	inst, err := m.q.GetPluginInstanceByID(ctx, instanceID)
	if errors.Is(err, ErrNotFound) {
		return db.Plugin{}, db.PluginInstance{}, ErrInstanceNotFound
	}
	if err != nil {
		return db.Plugin{}, db.PluginInstance{}, &lifecycleInternalError{
			PublicMsg: "failed to get instance",
			Err:       err,
		}
	}
	// Return 404 on mismatch to avoid leaking instance existence across plugins.
	if inst.PluginID != pluginID {
		return db.Plugin{}, db.PluginInstance{}, ErrInstanceNotFound
	}
	return plugin, inst, nil
}

// Deactivate soft-deactivates the instance: gates on in-flight calls, transitions
// health to inactive, stops subprocess and trigger stream, emits audit event,
// and re-fetches the updated row. Returns the re-fetched row on success.
//
// Typed errors: ErrPluginNotFound, ErrInstanceNotFound, ErrAlreadyInactive,
// TerminalStateError, InflightError(Op=inflightOpDeactivate), ErrRefetchFailed,
// lifecycleInternalError (failed to set health state).
func (m *InstanceLifecycle) Deactivate(ctx context.Context, pluginID, instanceID string) (db.PluginInstance, error) {
	_, inst, err := m.resolveLifecycleInstance(ctx, pluginID, instanceID)
	if err != nil {
		return db.PluginInstance{}, err
	}

	current := model.PluginHealthState(inst.HealthState)

	if current == model.PluginHealthStateInactive {
		return db.PluginInstance{}, ErrAlreadyInactive
	}

	// Terminal states cannot transition to inactive (state machine enforces this,
	// but we surface a clear error before hitting ErrIllegalTransition).
	if current == model.PluginHealthStateSignatureInvalid || current == model.PluginHealthStateVerificationError {
		return db.PluginInstance{}, TerminalStateError{State: current}
	}

	// Gate on zero in-flight tool calls. TOCTOU is acceptable here: a new call
	// arriving after this check will fail at the dispatch layer once the subprocess
	// is stopped. The gate prevents disrupting calls that are actively running.
	if m.inflight != nil {
		count := m.inflight.InflightCountByInstance(inst.InstanceName)
		if count > 0 {
			return db.PluginInstance{}, InflightError{Count: count, Op: inflightOpDeactivate}
		}
	}

	if err := pluginstate.SetHealthState(ctx, m.q, m.publisher, instanceID, pluginstate.OriginHost,
		model.PluginHealthStateInactive, "deactivated by admin"); err != nil {
		return db.PluginInstance{}, &lifecycleInternalError{
			PublicMsg: "failed to set health state",
			Detail:    err.Error(),
			Err:       err,
		}
	}

	// Best-effort subprocess stop (5s deadline — same pattern as DeleteInstance).
	if m.procMgr != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := m.procMgr.Stop(stopCtx, instanceID); err != nil {
			slog.WarnContext(ctx, "deactivate instance: subprocess stop failed",
				"instance_id", instanceID, "err", err)
		}
		cancel()
	}

	// Best-effort trigger stream stop.
	if m.trigger != nil {
		m.trigger.Stop(instanceID)
	}

	// Evict the cached tool-dispatch connection. procMgr.Stop closed the
	// subprocess UDS above, so the Pool's cached *grpc.ClientConn is now dead;
	// leaving it cached would make every tool call after reactivation fail with
	// "grpc: the client connection is closing" (the conn is re-dialed only on a
	// cache miss). The Pool keys by instance NAME. Best-effort, nil-safe.
	if m.evictor != nil {
		m.evictor.EvictInstance(inst.InstanceName)
	}

	nowStr := m.clock().UTC().Format(time.RFC3339Nano)
	m.writeAuditEvent(ctx, auditInstanceDeactivated, "info", nowStr, map[string]any{
		"plugin_id":     pluginID,
		"instance_id":   instanceID,
		"instance_name": inst.InstanceName,
	})

	// Re-fetch to return the current row after the state transition.
	updated, err := m.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		// State write succeeded but the re-fetch failed. Return ErrRefetchFailed —
		// the client can re-fetch via GET. We must not synthesize a response here
		// because config_json may contain unredacted secrets (ADR-049).
		slog.WarnContext(ctx, "deactivate instance: re-fetch after transition failed",
			"instance_id", instanceID, "err", err)
		return db.PluginInstance{}, ErrRefetchFailed
	}
	return updated, nil
}

// Activate re-activates a previously deactivated instance: transitions health
// to unhealthy, spawns the subprocess, starts the trigger stream, emits an
// audit event, and re-fetches the updated row.
//
// Typed errors: ErrPluginNotFound, ErrInstanceNotFound, NotInactiveError,
// ErrRefetchFailed, lifecycleInternalError (failed to set health state).
func (m *InstanceLifecycle) Activate(ctx context.Context, pluginID, instanceID string) (db.PluginInstance, error) {
	_, inst, err := m.resolveLifecycleInstance(ctx, pluginID, instanceID)
	if err != nil {
		return db.PluginInstance{}, err
	}

	if model.PluginHealthState(inst.HealthState) != model.PluginHealthStateInactive {
		return db.PluginInstance{}, NotInactiveError{State: inst.HealthState}
	}

	// Transition to unhealthy first. The subprocess handshake will drive the
	// state to healthy once the process comes up.
	if err := pluginstate.SetHealthState(ctx, m.q, m.publisher, instanceID, pluginstate.OriginHost,
		model.PluginHealthStateUnhealthy, "reactivated by admin"); err != nil {
		return db.PluginInstance{}, &lifecycleInternalError{
			PublicMsg: "failed to set health state",
			Detail:    err.Error(),
			Err:       err,
		}
	}

	// Best-effort subprocess spawn. StartByPluginID spawns all instances for the
	// plugin; siblings already running return "already running" (swallowed internally).
	// Use context.Background() — r.Context() WriteTimeout applies to HTTP requests;
	// the spawn must outlive the request (matches the original handler's behavior).
	if m.procMgr != nil {
		if err := m.procMgr.StartByPluginID(context.Background(), pluginID); err != nil {
			slog.WarnContext(ctx, "activate instance: spawn failed", "plugin_id", pluginID, "err", err)
		}
	}

	// Best-effort trigger stream start.
	if m.trigger != nil {
		m.trigger.Start(ctx, instanceID)
	}

	// Defensive backstop: evict any lingering cached tool-dispatch connection so
	// the first Call after reactivation re-dials the freshly-spawned subprocess.
	// Deactivate already evicts, but this guards any path that respawns an
	// instance without a preceding Deactivate. Best-effort, nil-safe.
	if m.evictor != nil {
		m.evictor.EvictInstance(inst.InstanceName)
	}

	nowStr := m.clock().UTC().Format(time.RFC3339Nano)
	m.writeAuditEvent(ctx, auditInstanceActivated, "info", nowStr, map[string]any{
		"plugin_id":     pluginID,
		"instance_id":   instanceID,
		"instance_name": inst.InstanceName,
	})

	// Re-fetch to return the current row after the state transition.
	updated, err := m.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		// State write succeeded but the re-fetch failed. Return ErrRefetchFailed —
		// the client can re-fetch via GET. We must not synthesize a response here
		// because config_json may contain unredacted secrets (ADR-049).
		slog.WarnContext(ctx, "activate instance: re-fetch after transition failed",
			"instance_id", instanceID, "err", err)
		return db.PluginInstance{}, ErrRefetchFailed
	}
	return updated, nil
}

// Delete hard-deletes a single plugin instance after verifying no policies or
// audiences reference it and no tool calls are in flight. Returns nil on success.
//
// ErrStoreUnavailable is checked FIRST (before any querier call), preserving
// the current 503-before-DB-access order.
//
// Typed errors: ErrStoreUnavailable, ErrPluginNotFound, ErrInstanceNotFound,
// PolicyRefError, AudienceRefError, InflightError(Op=inflightOpDelete),
// lifecycleInternalError ("internal error" for tx/cleanup steps).
func (m *InstanceLifecycle) Delete(ctx context.Context, pluginID, instanceID string) error {
	// 503 check FIRST — before any DB access (preserves current handler order).
	if m.store == nil {
		return ErrStoreUnavailable
	}

	_, inst, err := m.resolveLifecycleInstance(ctx, pluginID, instanceID)
	if err != nil {
		return err
	}

	// Policy reference guard: refuse deletion if any policy still references
	// this instance's tools or subscribed trigger.
	policyRefs, err := policy.ScanPolicyToolRefsForInstance(ctx, m.q, inst.InstanceName)
	if err != nil {
		slog.ErrorContext(ctx, "delete instance: scan policy refs", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}
	if len(policyRefs) > 0 {
		names := make([]string, len(policyRefs))
		for i, r := range policyRefs {
			names[i] = r.Name
		}
		return PolicyRefError{Names: names}
	}

	// Audience reference guard: refuse deletion if any audience entry references this instance.
	audienceEntries, err := m.q.ListAudienceEntriesByInstance(ctx, instanceID)
	if err != nil {
		slog.ErrorContext(ctx, "delete instance: list audience entries", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}
	if len(audienceEntries) > 0 {
		// Deduplicate audience names — one instance can appear in multiple entries
		// of the same audience.
		seen := make(map[string]bool)
		var audienceNames []string
		for _, ae := range audienceEntries {
			if !seen[ae.AudienceName] {
				seen[ae.AudienceName] = true
				audienceNames = append(audienceNames, ae.AudienceName)
			}
		}
		return AudienceRefError{Names: audienceNames}
	}

	// In-flight gate: refuse deletion if tool calls are actively dispatched to this instance.
	if m.inflight != nil {
		count := m.inflight.InflightCountByInstance(inst.InstanceName)
		if count > 0 {
			return InflightError{Count: count, Op: inflightOpDelete}
		}
	}

	// Best-effort subprocess stop before DB cleanup so in-flight RPCs don't hit
	// a half-deleted instance. A wedged subprocess must not block DB cleanup.
	if m.procMgr != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := m.procMgr.Stop(stopCtx, instanceID); err != nil {
			slog.WarnContext(ctx, "delete instance: subprocess stop failed",
				"instance_id", instanceID, "err", err)
		}
		cancel()
	}

	// Transactional delete in FK-safe order (ADR-003 sqlc/no-ORM):
	//   1. plugin_pending_requests (RESTRICT FK — must clear first)
	//   2. plugin_oauth_nonces (instance_id FK)
	//   3. plugin_instances row (audit events use SET NULL so history survives)
	tx, err := m.store.DB().BeginTx(ctx, nil)
	if err != nil {
		slog.ErrorContext(ctx, "delete instance: begin tx", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.ErrorContext(ctx, "delete instance: rollback failed", "err", rbErr)
		}
	}()

	q := db.New(tx)
	if err := q.DeletePluginPendingRequestsByInstance(ctx, instanceID); err != nil {
		slog.ErrorContext(ctx, "delete instance: clear pending requests", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}
	if err := q.DeletePluginOAuthNoncesByInstance(ctx, instanceID); err != nil {
		slog.ErrorContext(ctx, "delete instance: clear oauth nonces", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}
	if _, err := q.DeletePluginInstance(ctx, instanceID); err != nil {
		slog.ErrorContext(ctx, "delete instance: delete row", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}
	if err := tx.Commit(); err != nil {
		slog.ErrorContext(ctx, "delete instance: commit", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}

	// Best-effort tool-namespace release. Nil-safe: wired to rt.ToolRegistrar in
	// main.go (#574). Tests leave Unreg nil to skip the call without importing
	// internal/plugin/tools.
	if m.unreg != nil {
		m.unreg.UnregisterInstance(ctx, inst.InstanceName)
	}

	nowStr := m.clock().UTC().Format(time.RFC3339Nano)
	m.writeAuditEvent(ctx, auditInstanceDeleted, "info", nowStr, map[string]any{
		"plugin_id":     pluginID,
		"instance_id":   instanceID,
		"instance_name": inst.InstanceName,
	})
	return nil
}

// Uninstall removes the plugin and all its (already-deleted) instances. It
// gates on zero instances remaining, stops all subprocesses (10s deadline),
// removes pending requests and OAuth nonces per instance in a transaction,
// deletes the plugin row (which cascades to plugin_instances), and removes the
// binary directory from disk (containment-checked, best-effort).
//
// ErrStoreUnavailable is checked FIRST (before any querier call).
//
// Typed errors: ErrStoreUnavailable, ErrPluginNotFound,
// InstancesRemainError, lifecycleInternalError ("internal error").
func (m *InstanceLifecycle) Uninstall(ctx context.Context, pluginID string) error {
	// 503 check FIRST — before any DB access (preserves current handler order).
	if m.store == nil {
		return ErrStoreUnavailable
	}

	plugin, err := m.q.GetPluginByID(ctx, pluginID)
	if errors.Is(err, ErrNotFound) {
		return ErrPluginNotFound
	}
	if err != nil {
		return &lifecycleInternalError{PublicMsg: "failed to get plugin", Err: err}
	}

	// Collect all instances. Per #243: removing the plugin (binary + manifest)
	// requires all instances to be removed first — per-instance Delete enforces
	// the policy/audience/in-flight gates individually.
	instances, err := m.q.ListPluginInstancesByPlugin(ctx, pluginID)
	if err != nil {
		slog.ErrorContext(ctx, "uninstall: list instances", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}

	if len(instances) > 0 {
		names := make([]string, len(instances))
		for i, inst := range instances {
			names[i] = inst.InstanceName
		}
		return InstancesRemainError{Names: names}
	}

	// Best-effort: stop all running subprocesses before clearing rows.
	if m.procMgr != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := m.procMgr.StopByPluginID(stopCtx, pluginID); err != nil {
			slog.WarnContext(ctx, "uninstall: stop subprocesses failed",
				"plugin_id", pluginID, "err", err)
		}
		cancel()
	}

	// Transactional delete (ADR-003):
	//   1. For each instance: clear pending requests + OAuth nonces (RESTRICT FKs).
	//   2. DeletePlugin — cascades to plugin_instances via ON DELETE CASCADE.
	tx, err := m.store.DB().BeginTx(ctx, nil)
	if err != nil {
		slog.ErrorContext(ctx, "uninstall: begin tx", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.ErrorContext(ctx, "uninstall: rollback failed", "err", rbErr)
		}
	}()

	q := db.New(tx)
	for _, inst := range instances {
		if err := q.DeletePluginPendingRequestsByInstance(ctx, inst.ID); err != nil {
			slog.ErrorContext(ctx, "uninstall: clear pending requests",
				"instance_id", inst.ID, "err", err)
			return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
		}
		if err := q.DeletePluginOAuthNoncesByInstance(ctx, inst.ID); err != nil {
			slog.ErrorContext(ctx, "uninstall: clear oauth nonces",
				"instance_id", inst.ID, "err", err)
			return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
		}
	}
	if _, err := q.DeletePlugin(ctx, pluginID); err != nil {
		slog.ErrorContext(ctx, "uninstall: delete plugin", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}
	if err := tx.Commit(); err != nil {
		slog.ErrorContext(ctx, "uninstall: commit", "err", err)
		return &lifecycleInternalError{PublicMsg: "internal error", Err: err}
	}

	// Best-effort tool-namespace release for each instance. Nil-safe.
	if m.unreg != nil {
		for _, inst := range instances {
			m.unreg.UnregisterInstance(ctx, inst.InstanceName)
		}
	}

	// Post-commit: remove the binary directory from disk.
	// Containment check (fail-closed per ADR-001): only remove paths that are
	// strictly within pluginsDir. A path that starts with ".." after filepath.Rel
	// is a traversal attempt — we refuse and log a warning.
	binaryPathRemoved := false
	if plugin.BinaryPath != nil && m.pluginsDir != "" {
		dir := filepath.Dir(*plugin.BinaryPath)
		rel, relErr := filepath.Rel(m.pluginsDir, dir)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			slog.WarnContext(ctx, "uninstall: binary path escapes plugins dir — skipping removal",
				"binary_path", *plugin.BinaryPath,
				"plugins_dir", m.pluginsDir)
		} else {
			if err := os.RemoveAll(dir); err != nil {
				slog.WarnContext(ctx, "uninstall: remove binary dir failed",
					"dir", dir, "err", err)
			} else {
				binaryPathRemoved = true
			}
		}
	}

	nowStr := m.clock().UTC().Format(time.RFC3339Nano)
	m.writeAuditEvent(ctx, auditPluginUninstalled, "info", nowStr, map[string]any{
		"plugin_id":           pluginID,
		"plugin_name":         plugin.Name,
		"plugin_version":      plugin.PluginVersion,
		"instance_count":      len(instances),
		"binary_path_removed": binaryPathRemoved,
	})
	return nil
}

// writeAuditEvent inserts a plugin-level audit row with the calling user as
// actor when one is on the request context. Failures are logged but not
// surfaced — the upstream DB change has already committed.
// This is InstanceLifecycle's own copy — it must NOT back-reference PluginHandler.
func (m *InstanceLifecycle) writeAuditEvent(ctx context.Context, eventType, severity, nowStr string, payload map[string]any) {
	caller, _ := auth.UserFromContext(ctx)
	var actorUserID *string
	if caller != nil {
		actorUserID = &caller.ID
	}
	payloadJSON, _ := json.Marshal(payload)
	_, err := m.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: nil,
		EventType:        eventType,
		Severity:         severity,
		ActorUserID:      actorUserID,
		PayloadJson:      string(payloadJSON),
		CreatedAt:        nowStr,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit event failed", "event_type", eventType, "err", err)
	}
}
