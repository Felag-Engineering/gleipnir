package admin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newTestLifecycle(q PluginQuerier, store *db.Store, opts ...func(*InstanceLifecycleDeps)) *InstanceLifecycle {
	deps := InstanceLifecycleDeps{
		Q:     q,
		Store: store,
		Clock: func() time.Time { return time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC) },
	}
	for _, o := range opts {
		o(&deps)
	}
	return NewInstanceLifecycle(deps)
}

func withProcMgr(pm PluginProcessManager) func(*InstanceLifecycleDeps) {
	return func(d *InstanceLifecycleDeps) { d.ProcMgr = pm }
}

func withTrigger(t TriggerRestarter) func(*InstanceLifecycleDeps) {
	return func(d *InstanceLifecycleDeps) { d.Trigger = t }
}

func withInflight(c InflightCounter) func(*InstanceLifecycleDeps) {
	return func(d *InstanceLifecycleDeps) { d.Inflight = c }
}

func withPluginsDir(dir string) func(*InstanceLifecycleDeps) {
	return func(d *InstanceLifecycleDeps) { d.PluginsDir = dir }
}

func withUnreg(u ToolUnregistrar) func(*InstanceLifecycleDeps) {
	return func(d *InstanceLifecycleDeps) { d.Unreg = u }
}

func withEvictor(e ToolConnEvictor) func(*InstanceLifecycleDeps) {
	return func(d *InstanceLifecycleDeps) { d.Evictor = e }
}

// fakeEvictor records the instance names passed to EvictInstance so tests can
// assert the deactivate/activate lifecycle drops the stale tool-dispatch conn.
type fakeEvictor struct {
	mu      sync.Mutex
	evicted []string
}

func (f *fakeEvictor) EvictInstance(instanceName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = append(f.evicted, instanceName)
}

// ─── Deactivate ──────────────────────────────────────────────────────────────

func TestInstanceLifecycle_Deactivate(t *testing.T) {
	ctx := context.Background()

	t.Run("ErrPluginNotFound when plugin missing", func(t *testing.T) {
		q := newFakePluginQuerier()
		m := newTestLifecycle(q, nil)
		_, err := m.Deactivate(ctx, "nonexistent", "inst-1")
		if !errors.Is(err, ErrPluginNotFound) {
			t.Errorf("err = %v, want ErrPluginNotFound", err)
		}
	})

	t.Run("ErrInstanceNotFound when instance missing", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		m := newTestLifecycle(q, nil)
		_, err := m.Deactivate(ctx, "plugin-1", "nonexistent-inst")
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("err = %v, want ErrInstanceNotFound", err)
		}
	})

	t.Run("ErrInstanceNotFound when instance belongs to different plugin", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-other", InstanceName: "prod", HealthState: "healthy"})
		m := newTestLifecycle(q, nil)
		_, err := m.Deactivate(ctx, "plugin-1", "inst-1")
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("err = %v, want ErrInstanceNotFound", err)
		}
	})

	t.Run("ErrAlreadyInactive when already inactive", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "inactive"})
		m := newTestLifecycle(q, nil)
		_, err := m.Deactivate(ctx, "plugin-1", "inst-1")
		if !errors.Is(err, ErrAlreadyInactive) {
			t.Errorf("err = %v, want ErrAlreadyInactive", err)
		}
	})

	t.Run("TerminalStateError for signature_invalid state", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "signature_invalid"})
		m := newTestLifecycle(q, nil)
		_, err := m.Deactivate(ctx, "plugin-1", "inst-1")
		var termErr TerminalStateError
		if !errors.As(err, &termErr) {
			t.Errorf("err = %v, want TerminalStateError", err)
		}
	})

	t.Run("TerminalStateError for verification_error state", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "verification_error"})
		m := newTestLifecycle(q, nil)
		_, err := m.Deactivate(ctx, "plugin-1", "inst-1")
		var termErr TerminalStateError
		if !errors.As(err, &termErr) {
			t.Errorf("err = %v, want TerminalStateError", err)
		}
	})

	t.Run("InflightError with Op=inflightOpDeactivate when calls in progress", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		m := newTestLifecycle(q, nil,
			withInflight(&fakeInflightCounter{counts: map[string]int{"prod": 2}}),
		)
		_, err := m.Deactivate(ctx, "plugin-1", "inst-1")
		var inflightErr InflightError
		if !errors.As(err, &inflightErr) {
			t.Errorf("err = %v, want InflightError", err)
		}
		if inflightErr.Count != 2 {
			t.Errorf("InflightError.Count = %d, want 2", inflightErr.Count)
		}
		if inflightErr.Op != inflightOpDeactivate {
			t.Errorf("InflightError.Op = %v, want inflightOpDeactivate", inflightErr.Op)
		}
	})

	t.Run("happy path: transitions health to inactive, stops subprocess and trigger", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 0})

		pm := &fakeProcessManager{}
		restarter := &fakeTriggerRestarter{}
		ev := &fakeEvictor{}
		m := newTestLifecycle(q, nil,
			withProcMgr(pm),
			withTrigger(restarter),
			withInflight(&fakeInflightCounter{counts: map[string]int{"prod": 0}}),
			withEvictor(ev),
		)

		inst, err := m.Deactivate(ctx, "plugin-1", "inst-1")
		if err != nil {
			t.Fatalf("Deactivate: unexpected error: %v", err)
		}
		if inst.HealthState != "inactive" {
			t.Errorf("HealthState = %q, want \"inactive\"", inst.HealthState)
		}
		// Subprocess must have been stopped.
		pm.mu.Lock()
		stopped := pm.stoppedIDs
		pm.mu.Unlock()
		if len(stopped) != 1 || stopped[0] != "inst-1" {
			t.Errorf("Stop called with %v, want [inst-1]", stopped)
		}
		// The stale tool-dispatch connection must be evicted by instance NAME so a
		// later reactivation re-dials the fresh subprocess (deactivate→activate bug).
		ev.mu.Lock()
		evicted := ev.evicted
		ev.mu.Unlock()
		if len(evicted) != 1 || evicted[0] != "prod" {
			t.Errorf("EvictInstance called with %v, want [prod]", evicted)
		}
	})

	t.Run("ErrRefetchFailed when re-fetch after transition fails", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 0})
		// GetPluginInstanceByID call order:
		//   1. resolveLifecycleInstance (succeeds)
		//   2. pluginstate.SetHealthState internally (succeeds)
		//   3. re-fetch after transition (should fail → ErrRefetchFailed)
		q.getInstanceErrAfterN["inst-1"] = 2

		m := newTestLifecycle(q, nil,
			withInflight(&fakeInflightCounter{counts: map[string]int{"prod": 0}}),
		)
		_, err := m.Deactivate(ctx, "plugin-1", "inst-1")
		if !errors.Is(err, ErrRefetchFailed) {
			t.Errorf("err = %v, want ErrRefetchFailed", err)
		}
	})

	t.Run("nil inflight: proceeds without in-flight check", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 0})
		// No inflight dep — must not panic.
		m := newTestLifecycle(q, nil)
		_, err := m.Deactivate(ctx, "plugin-1", "inst-1")
		if err != nil {
			t.Fatalf("Deactivate: unexpected error: %v", err)
		}
	})
}

// ─── Activate ────────────────────────────────────────────────────────────────

func TestInstanceLifecycle_Activate(t *testing.T) {
	ctx := context.Background()

	t.Run("ErrPluginNotFound when plugin missing", func(t *testing.T) {
		q := newFakePluginQuerier()
		m := newTestLifecycle(q, nil)
		_, err := m.Activate(ctx, "nonexistent", "inst-1")
		if !errors.Is(err, ErrPluginNotFound) {
			t.Errorf("err = %v, want ErrPluginNotFound", err)
		}
	})

	t.Run("ErrInstanceNotFound when instance missing", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		m := newTestLifecycle(q, nil)
		_, err := m.Activate(ctx, "plugin-1", "nonexistent-inst")
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("err = %v, want ErrInstanceNotFound", err)
		}
	})

	t.Run("NotInactiveError when instance is healthy", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		m := newTestLifecycle(q, nil)
		_, err := m.Activate(ctx, "plugin-1", "inst-1")
		var notInactive NotInactiveError
		if !errors.As(err, &notInactive) {
			t.Errorf("err = %v, want NotInactiveError", err)
		}
		if notInactive.State != "healthy" {
			t.Errorf("NotInactiveError.State = %q, want \"healthy\"", notInactive.State)
		}
	})

	t.Run("happy path: transitions to unhealthy, spawns subprocess, starts trigger", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "inactive", Version: 0})

		pm := &fakeProcessManager{}
		restarter := &fakeTriggerRestarter{}
		m := newTestLifecycle(q, nil,
			withProcMgr(pm),
			withTrigger(restarter),
		)

		inst, err := m.Activate(ctx, "plugin-1", "inst-1")
		if err != nil {
			t.Fatalf("Activate: unexpected error: %v", err)
		}
		if inst.HealthState != "unhealthy" {
			t.Errorf("HealthState = %q, want \"unhealthy\"", inst.HealthState)
		}
		// Subprocess must have been started.
		pm.mu.Lock()
		started := pm.startedByPlugin
		pm.mu.Unlock()
		if len(started) != 1 || started[0] != "plugin-1" {
			t.Errorf("StartByPluginID called with %v, want [plugin-1]", started)
		}
	})

	t.Run("ErrRefetchFailed when re-fetch after transition fails", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "inactive", Version: 0})
		// GetPluginInstanceByID call order:
		//   1. resolveLifecycleInstance (succeeds)
		//   2. pluginstate.SetHealthState internally (succeeds)
		//   3. re-fetch after transition (should fail → ErrRefetchFailed)
		q.getInstanceErrAfterN["inst-1"] = 2

		m := newTestLifecycle(q, nil)
		_, err := m.Activate(ctx, "plugin-1", "inst-1")
		if !errors.Is(err, ErrRefetchFailed) {
			t.Errorf("err = %v, want ErrRefetchFailed", err)
		}
	})

	t.Run("nil procMgr: does not panic", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "inactive", Version: 0})
		m := newTestLifecycle(q, nil) // no procMgr
		_, err := m.Activate(ctx, "plugin-1", "inst-1")
		if err != nil {
			t.Fatalf("Activate: unexpected error: %v", err)
		}
	})
}

// ─── Delete ──────────────────────────────────────────────────────────────────

func TestInstanceLifecycle_Delete(t *testing.T) {
	ctx := context.Background()

	t.Run("ErrStoreUnavailable when store is nil", func(t *testing.T) {
		q := newFakePluginQuerier()
		m := newTestLifecycle(q, nil) // no store
		err := m.Delete(ctx, "plugin-1", "inst-1")
		if !errors.Is(err, ErrStoreUnavailable) {
			t.Errorf("err = %v, want ErrStoreUnavailable", err)
		}
	})

	t.Run("ErrPluginNotFound when plugin missing", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		m := newTestLifecycle(q, store)
		err := m.Delete(ctx, "nonexistent", "inst-1")
		if !errors.Is(err, ErrPluginNotFound) {
			t.Errorf("err = %v, want ErrPluginNotFound", err)
		}
	})

	t.Run("ErrInstanceNotFound when instance missing", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		m := newTestLifecycle(q, store)
		err := m.Delete(ctx, "plugin-1", "nonexistent-inst")
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("err = %v, want ErrInstanceNotFound", err)
		}
	})

	t.Run("PolicyRefError when policies reference the instance", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "slack-prod", HealthState: "healthy"})
		q.seedPolicy(db.Policy{
			ID:   "pol-1",
			Name: "Incident Policy",
			Yaml: "trigger:\n  type: webhook\ncapabilities:\n  tools:\n    - tool: slack-prod.post_message\n",
		})
		m := newTestLifecycle(q, store)
		err := m.Delete(ctx, "plugin-1", "inst-1")
		var policyRef PolicyRefError
		if !errors.As(err, &policyRef) {
			t.Errorf("err = %v, want PolicyRefError", err)
		}
		if len(policyRef.Names) != 1 || policyRef.Names[0] != "Incident Policy" {
			t.Errorf("PolicyRefError.Names = %v, want [Incident Policy]", policyRef.Names)
		}
	})

	t.Run("AudienceRefError when audience entries reference the instance", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		q.seedAudienceEntries("inst-1", []db.ListAudienceEntriesByInstanceRow{
			{ID: "ae-1", AudienceID: "aud-1", PluginInstanceID: "inst-1", AudienceName: "ops-audience"},
		})
		m := newTestLifecycle(q, store)
		err := m.Delete(ctx, "plugin-1", "inst-1")
		var audRef AudienceRefError
		if !errors.As(err, &audRef) {
			t.Errorf("err = %v, want AudienceRefError", err)
		}
		if len(audRef.Names) != 1 || audRef.Names[0] != "ops-audience" {
			t.Errorf("AudienceRefError.Names = %v, want [ops-audience]", audRef.Names)
		}
	})

	t.Run("AudienceRefError deduplicates audience names", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		// Two entries with the same audience name → deduplicated to one name.
		q.seedAudienceEntries("inst-1", []db.ListAudienceEntriesByInstanceRow{
			{ID: "ae-1", AudienceID: "aud-1", PluginInstanceID: "inst-1", AudienceName: "ops-audience"},
			{ID: "ae-2", AudienceID: "aud-1", PluginInstanceID: "inst-1", AudienceName: "ops-audience"},
		})
		m := newTestLifecycle(q, store)
		err := m.Delete(ctx, "plugin-1", "inst-1")
		var audRef AudienceRefError
		if !errors.As(err, &audRef) {
			t.Errorf("err = %v, want AudienceRefError", err)
		}
		if len(audRef.Names) != 1 {
			t.Errorf("AudienceRefError.Names = %v, want exactly 1 unique name", audRef.Names)
		}
	})

	t.Run("InflightError with Op=inflightOpDelete when calls in progress", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		m := newTestLifecycle(q, store,
			withInflight(&fakeInflightCounter{counts: map[string]int{"prod": 5}}),
		)
		err := m.Delete(ctx, "plugin-1", "inst-1")
		var inflightErr InflightError
		if !errors.As(err, &inflightErr) {
			t.Errorf("err = %v, want InflightError", err)
		}
		if inflightErr.Op != inflightOpDelete {
			t.Errorf("InflightError.Op = %v, want inflightOpDelete", inflightErr.Op)
		}
		if inflightErr.Count != 5 {
			t.Errorf("InflightError.Count = %d, want 5", inflightErr.Count)
		}
	})

	t.Run("happy path: subprocess stopped, row deleted, audit event emitted", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		seedStorePlugin(t, store, "plugin-1", "p", nil)
		seedStoreInstance(t, store, "inst-1", "plugin-1", "prod")

		pm := &fakeProcessManager{}
		m := newTestLifecycle(q, store, withProcMgr(pm))
		err := m.Delete(ctx, "plugin-1", "inst-1")
		if err != nil {
			t.Fatalf("Delete: unexpected error: %v", err)
		}

		// Subprocess must have been stopped.
		pm.mu.Lock()
		stopped := pm.stoppedIDs
		pm.mu.Unlock()
		if len(stopped) != 1 || stopped[0] != "inst-1" {
			t.Errorf("Stop called with %v, want [inst-1]", stopped)
		}

		// Row must be removed from the real store.
		if storeHasInstance(t, store, "inst-1") {
			t.Error("instance should be deleted from store after Delete")
		}

		// Audit event must be emitted.
		found := false
		for _, ev := range q.auditEvents {
			if ev.EventType == auditInstanceDeleted {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected plugin_instance_deleted audit event")
		}
	})

	t.Run("succeeds with nil processManager (no panic)", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		seedStorePlugin(t, store, "plugin-1", "p", nil)
		seedStoreInstance(t, store, "inst-1", "plugin-1", "prod")

		m := newTestLifecycle(q, store) // nil procMgr
		if err := m.Delete(ctx, "plugin-1", "inst-1"); err != nil {
			t.Fatalf("Delete: unexpected error: %v", err)
		}
		if storeHasInstance(t, store, "inst-1") {
			t.Error("instance should be deleted even with nil processManager")
		}
	})

	t.Run("succeeds even when subprocess Stop returns error", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		seedStorePlugin(t, store, "plugin-1", "p", nil)
		seedStoreInstance(t, store, "inst-1", "plugin-1", "prod")

		pm := &fakeProcessManager{stopErr: errors.New("subprocess wedged")}
		m := newTestLifecycle(q, store, withProcMgr(pm))
		if err := m.Delete(ctx, "plugin-1", "inst-1"); err != nil {
			t.Fatalf("Delete: unexpected error: %v", err)
		}
		if storeHasInstance(t, store, "inst-1") {
			t.Error("instance should be deleted from store despite Stop failure")
		}
	})

	t.Run("releases tool-namespace reservation with instance name on successful delete", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "unreg-inst", PluginID: "plugin-1", InstanceName: "unreg-target", HealthState: "healthy"})
		seedStorePlugin(t, store, "plugin-1", "p", nil)
		seedStoreInstance(t, store, "unreg-inst", "plugin-1", "unreg-target")

		unreg := &fakeUnreg{}
		m := newTestLifecycle(q, store, withUnreg(unreg))
		if err := m.Delete(ctx, "plugin-1", "unreg-inst"); err != nil {
			t.Fatalf("Delete: unexpected error: %v", err)
		}
		// The unregistrar must be called with the instance NAME (not the ID).
		// This is the regression guard against a key-mismatch: if the call were
		// keyed by instance ID instead of name, the arbiter would silently no-op.
		if len(unreg.names) != 1 || unreg.names[0] != "unreg-target" {
			t.Errorf("UnregisterInstance called with %v, want [unreg-target]", unreg.names)
		}
	})
}

// ─── Uninstall ───────────────────────────────────────────────────────────────

func TestInstanceLifecycle_Uninstall(t *testing.T) {
	ctx := context.Background()

	t.Run("ErrStoreUnavailable when store is nil", func(t *testing.T) {
		q := newFakePluginQuerier()
		m := newTestLifecycle(q, nil) // no store
		err := m.Uninstall(ctx, "plugin-1")
		if !errors.Is(err, ErrStoreUnavailable) {
			t.Errorf("err = %v, want ErrStoreUnavailable", err)
		}
	})

	t.Run("ErrPluginNotFound when plugin missing", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		m := newTestLifecycle(q, store)
		err := m.Uninstall(ctx, "nonexistent")
		if !errors.Is(err, ErrPluginNotFound) {
			t.Errorf("err = %v, want ErrPluginNotFound", err)
		}
	})

	t.Run("InstancesRemainError when instances still exist", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "my-plugin", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-a", PluginID: "plugin-1", InstanceName: "slack-prod", HealthState: "healthy"})
		q.seed(db.PluginInstance{ID: "inst-b", PluginID: "plugin-1", InstanceName: "slack-staging", HealthState: "healthy"})
		m := newTestLifecycle(q, store)
		err := m.Uninstall(ctx, "plugin-1")
		var remainErr InstancesRemainError
		if !errors.As(err, &remainErr) {
			t.Errorf("err = %v, want InstancesRemainError", err)
		}
		if len(remainErr.Names) != 2 {
			t.Errorf("InstancesRemainError.Names = %v, want 2 names", remainErr.Names)
		}
	})

	t.Run("happy path with binary dir: plugin deleted, dir removed", func(t *testing.T) {
		store := newPluginTestStore(t)
		pluginsDir := t.TempDir()
		binaryDir := filepath.Join(pluginsDir, "installed", "my-plugin")
		if err := os.MkdirAll(binaryDir, 0o755); err != nil {
			t.Fatalf("create binary dir: %v", err)
		}
		binaryPath := filepath.Join(binaryDir, "my-plugin")
		if err := os.WriteFile(binaryPath, []byte("fake binary"), 0o755); err != nil {
			t.Fatalf("write binary: %v", err)
		}
		bp := binaryPath
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-2", Name: "my-plugin", ManifestSnapshot: instanceConfigManifestNoSchema, BinaryPath: &bp})
		seedStorePlugin(t, store, "plugin-2", "my-plugin", &bp)

		pm := &fakeProcessManager{}
		m := newTestLifecycle(q, store, withProcMgr(pm), withPluginsDir(pluginsDir))
		if err := m.Uninstall(ctx, "plugin-2"); err != nil {
			t.Fatalf("Uninstall: unexpected error: %v", err)
		}

		if storeHasPlugin(t, store, "plugin-2") {
			t.Error("plugin should be deleted from store after Uninstall")
		}
		if _, err := os.Stat(binaryDir); !os.IsNotExist(err) {
			t.Errorf("expected binary dir to be removed, stat err: %v", err)
		}

		found := false
		for _, ev := range q.auditEvents {
			if ev.EventType == auditPluginUninstalled {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected plugin_uninstalled audit event")
		}
	})

	t.Run("binary path outside pluginsDir: FS skipped, plugin still deleted", func(t *testing.T) {
		store := newPluginTestStore(t)
		pluginsDir := t.TempDir()
		outsidePath := filepath.Join("/tmp", "evil", "binary")
		bp := outsidePath
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-evil", Name: "evil", ManifestSnapshot: instanceConfigManifestNoSchema, BinaryPath: &bp})
		seedStorePlugin(t, store, "plugin-evil", "evil", &bp)

		m := newTestLifecycle(q, store, withPluginsDir(pluginsDir))
		if err := m.Uninstall(ctx, "plugin-evil"); err != nil {
			t.Fatalf("Uninstall: unexpected error: %v", err)
		}
		if storeHasPlugin(t, store, "plugin-evil") {
			t.Error("plugin should be deleted from store even when containment check fails")
		}
	})

	t.Run("nil binary path: no FS op, plugin still deleted", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-2", Name: "no-binary", ManifestSnapshot: instanceConfigManifestNoSchema, BinaryPath: nil})
		seedStorePlugin(t, store, "plugin-2", "no-binary", nil)

		m := newTestLifecycle(q, store, withPluginsDir(t.TempDir()))
		if err := m.Uninstall(ctx, "plugin-2"); err != nil {
			t.Fatalf("Uninstall: unexpected error: %v", err)
		}
		if storeHasPlugin(t, store, "plugin-2") {
			t.Error("plugin should be deleted even when binary_path is nil")
		}
	})

	t.Run("nil processManager: succeeds without panic", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-3", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		seedStorePlugin(t, store, "plugin-3", "p", nil)

		m := newTestLifecycle(q, store) // nil procMgr
		if err := m.Uninstall(ctx, "plugin-3"); err != nil {
			t.Fatalf("Uninstall: unexpected error: %v", err)
		}
	})

	t.Run("InflightError Op field distinguishes Deactivate vs Delete", func(t *testing.T) {
		// The InflightError returned by Deactivate uses inflightOpDeactivate,
		// while Delete uses inflightOpDelete. This difference drives the handler's
		// error message selection.
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		counter := &fakeInflightCounter{counts: map[string]int{"prod": 1}}

		m := newTestLifecycle(q, nil, withInflight(counter))
		_, deactivateErr := m.Deactivate(ctx, "plugin-1", "inst-1")
		var deactivateInflight InflightError
		if !errors.As(deactivateErr, &deactivateInflight) {
			t.Fatalf("Deactivate: expected InflightError, got %v", deactivateErr)
		}
		if deactivateInflight.Op != inflightOpDeactivate {
			t.Errorf("Deactivate InflightError.Op = %v, want inflightOpDeactivate", deactivateInflight.Op)
		}

		store := newPluginTestStore(t)
		m2 := newTestLifecycle(q, store, withInflight(counter))
		deleteErr := m2.Delete(ctx, "plugin-1", "inst-1")
		var deleteInflight InflightError
		if !errors.As(deleteErr, &deleteInflight) {
			t.Fatalf("Delete: expected InflightError, got %v", deleteErr)
		}
		if deleteInflight.Op != inflightOpDelete {
			t.Errorf("Delete InflightError.Op = %v, want inflightOpDelete", deleteInflight.Op)
		}
	})

	t.Run("releases tool-namespace reservations per instance name on uninstall", func(t *testing.T) {
		// Uninstall requires all instances to be deleted first (InstancesRemainError
		// guards non-empty lists). When it succeeds the instance list is empty and
		// unreg is called zero times — correct behaviour. This test verifies: no
		// panic when Unreg is wired, and zero calls when the plugin has no instances.
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-9", Name: "no-insts", ManifestSnapshot: instanceConfigManifestNoSchema})
		seedStorePlugin(t, store, "plugin-9", "no-insts", nil)

		unreg := &fakeUnreg{}
		m := newTestLifecycle(q, store, withUnreg(unreg), withPluginsDir(t.TempDir()))
		if err := m.Uninstall(ctx, "plugin-9"); err != nil {
			t.Fatalf("Uninstall: unexpected error: %v", err)
		}
		// Instances are pre-deleted before Uninstall; unreg is called 0 times.
		if len(unreg.names) != 0 {
			t.Errorf("UnregisterInstance called with %v, want no calls (all instances pre-deleted)", unreg.names)
		}
	})
}
