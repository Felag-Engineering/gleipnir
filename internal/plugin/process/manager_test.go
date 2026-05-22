//go:build unix

package process_test

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
)

// ── fakeQuerier ──────────────────────────────────────────────────────────────

// fakeQuerier is an in-memory implementation of the querier interface required
// by Manager. No real DB connection is needed for unit tests.
type fakeQuerier struct {
	plugins   []db.Plugin
	instances map[string][]db.PluginInstance // keyed by plugin_id
}

func (q *fakeQuerier) ListPluginsByStatus(_ context.Context, status string) ([]db.Plugin, error) {
	var out []db.Plugin
	for _, p := range q.plugins {
		if p.Status == status {
			out = append(out, p)
		}
	}
	return out, nil
}

func (q *fakeQuerier) ListPluginInstancesByPlugin(_ context.Context, pluginID string) ([]db.PluginInstance, error) {
	return q.instances[pluginID], nil
}

func (q *fakeQuerier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	for _, p := range q.plugins {
		if p.ID == id {
			return p, nil
		}
	}
	return db.Plugin{}, errors.New("not found")
}

func (q *fakeQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	for _, instances := range q.instances {
		for _, inst := range instances {
			if inst.ID == id {
				return inst, nil
			}
		}
	}
	return db.PluginInstance{}, errors.New("not found")
}

func (q *fakeQuerier) UpdatePluginInstanceHealth(_ context.Context, _ db.UpdatePluginInstanceHealthParams) (int64, error) {
	return 1, nil
}

// ── Manager unit tests (blocked states, skip logic, StopAll) ─────────────────

// TestManager_SkipNonActivePlugin verifies that Manager.Start refuses to spawn
// a subprocess when the plugin's status is not "active".
func TestManager_SkipNonActivePlugin(t *testing.T) {
	reg := identity.New()
	mgr := process.NewManager(process.ManagerConfig{
		Querier:        &fakeQuerier{},
		IdentityIssuer: reg,
	})

	plugin := db.Plugin{ID: "p1", Status: "inactive"}
	instance := db.PluginInstance{ID: "i1", PluginID: "p1", InstanceName: "inst1", HealthState: "healthy"}

	if err := mgr.Start(context.Background(), plugin, instance, "/bin/true"); err == nil {
		t.Fatal("expected error for non-active plugin, got nil")
	}
}

// TestManager_SkipBlockedHealthState verifies that Manager.Start returns nil
// (no subprocess started) for every health state in the blocked set.
func TestManager_SkipBlockedHealthState(t *testing.T) {
	blockedStates := []model.PluginHealthState{
		model.PluginHealthStateSignatureInvalid,
		model.PluginHealthStateVerificationError,
		model.PluginHealthStatePendingKeyApproval,
		model.PluginHealthStatePendingConfigMigration,
		model.PluginHealthStatePendingManifestApproval,
		model.PluginHealthStateCrashed,
	}

	for _, state := range blockedStates {
		t.Run(string(state), func(t *testing.T) {
			reg := identity.New()
			var started bool
			mgr := process.NewManager(process.ManagerConfig{
				Querier:        &fakeQuerier{},
				IdentityIssuer: reg,
				TestProcessStarter: func(_ context.Context, _ process.Config) (*process.Instance, error) {
					started = true
					return nil, errors.New("should not be called")
				},
			})

			plugin := db.Plugin{ID: "p1", Status: "active"}
			instance := db.PluginInstance{
				ID:           "i1",
				PluginID:     "p1",
				InstanceName: "inst1",
				HealthState:  string(state),
			}

			if err := mgr.Start(context.Background(), plugin, instance, "/bin/true"); err != nil {
				t.Fatalf("unexpected error for blocked state %s: %v", state, err)
			}
			if started {
				t.Errorf("processStarter was called for blocked health state %s", state)
			}
		})
	}
}

// TestManager_IdempotentStart verifies that starting the same instance twice
// returns an error on the second call without calling the starter again.
func TestManager_IdempotentStart(t *testing.T) {
	reg := identity.New()
	var callCount int

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := process.NewManager(process.ManagerConfig{
		Querier:        &fakeQuerier{},
		IdentityIssuer: reg,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			callCount++
			fc := fixtureConfig(t, "serve-and-block", reg, nil)
			fc.InstanceID = cfg.InstanceID
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{ID: "p1", Status: "active"}
	instance := db.PluginInstance{ID: "i1", PluginID: "p1", InstanceName: "i1", HealthState: "healthy"}

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer mgr.Stop(ctx, "i1") //nolint:errcheck

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err == nil {
		t.Fatal("expected error for duplicate Start, got nil")
	}
	if callCount != 1 {
		t.Errorf("processStarter called %d times, want 1", callCount)
	}
}

// TestManager_StopAll verifies that StopAll terminates all running instances
// and Lookup returns nil for each afterward.
func TestManager_StopAll(t *testing.T) {
	reg := identity.New()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := process.NewManager(process.ManagerConfig{
		Querier:        &fakeQuerier{},
		IdentityIssuer: reg,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			fc := fixtureConfig(t, "serve-and-block", reg, nil)
			fc.InstanceID = cfg.InstanceID
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{ID: "p1", Status: "active"}
	instanceIDs := []string{"i1", "i2"}
	for _, id := range instanceIDs {
		inst := db.PluginInstance{ID: id, PluginID: "p1", InstanceName: id, HealthState: "healthy"}
		if err := mgr.Start(ctx, plugin, inst, os.Args[0]); err != nil {
			t.Fatalf("Start %s: %v", id, err)
		}
	}

	if err := mgr.StopAll(ctx); err != nil {
		t.Fatalf("StopAll: %v", err)
	}

	for _, id := range instanceIDs {
		if mgr.Lookup(id) != nil {
			t.Errorf("Lookup(%s): expected nil after StopAll", id)
		}
	}
}

// TestManager_StopMissingID verifies that stopping a non-existent instance ID
// returns nil (idempotent).
func TestManager_StopMissingID(t *testing.T) {
	reg := identity.New()
	mgr := process.NewManager(process.ManagerConfig{
		Querier:        &fakeQuerier{},
		IdentityIssuer: reg,
	})

	if err := mgr.Stop(context.Background(), "nonexistent"); err != nil {
		t.Errorf("Stop for nonexistent instance: want nil, got %v", err)
	}
}

// TestManager_StartAllActive verifies two cases:
//  1. BinaryPath == nil → log-and-skip; processStarter is never called.
//  2. BinaryPath is set → processStarter is invoked once with the correct path.
func TestManager_StartAllActive(t *testing.T) {
	binaryPath := "/x/y/binary"

	cases := []struct {
		name          string
		binaryPath    *string
		wantStarted   bool
		wantStartPath string
	}{
		{
			name:        "nil binary_path skips spawn",
			binaryPath:  nil,
			wantStarted: false,
		},
		{
			name:          "populated binary_path spawns instance",
			binaryPath:    &binaryPath,
			wantStarted:   true,
			wantStartPath: binaryPath,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQuerier{
				plugins: []db.Plugin{
					{ID: "p1", Status: "active", BinaryPath: tc.binaryPath},
				},
				instances: map[string][]db.PluginInstance{
					"p1": {{ID: "i1", PluginID: "p1", InstanceName: "i1", HealthState: "healthy"}},
				},
			}
			reg := identity.New()

			var startedPath string
			var startCalled bool
			mgr := process.NewManager(process.ManagerConfig{
				Querier:        q,
				IdentityIssuer: reg,
				TestProcessStarter: func(_ context.Context, cfg process.Config) (*process.Instance, error) {
					startCalled = true
					startedPath = cfg.BinaryPath
					// Return an error so we don't need a real subprocess.
					return nil, errors.New("stub: no subprocess needed")
				},
			})

			if err := mgr.StartAllActive(context.Background()); err != nil {
				t.Fatalf("StartAllActive: %v", err)
			}

			if startCalled != tc.wantStarted {
				t.Errorf("processStarter called = %v, want %v", startCalled, tc.wantStarted)
			}
			if tc.wantStarted && startedPath != tc.wantStartPath {
				t.Errorf("BinaryPath passed to starter = %q, want %q", startedPath, tc.wantStartPath)
			}
		})
	}
}

// TestManager_StartByPluginID_SpawnsOnlyTargetPlugin verifies that StartByPluginID
// spawns only the instances belonging to the given plugin, not instances of other
// plugins.
func TestManager_StartByPluginID_SpawnsOnlyTargetPlugin(t *testing.T) {
	binaryPath := "/x/y/binary"
	q := &fakeQuerier{
		plugins: []db.Plugin{
			{ID: "p1", Status: "active", BinaryPath: &binaryPath},
			{ID: "p2", Status: "active", BinaryPath: &binaryPath},
		},
		instances: map[string][]db.PluginInstance{
			"p1": {{ID: "i1", PluginID: "p1", InstanceName: "inst-1", HealthState: "healthy"}},
			"p2": {{ID: "i2", PluginID: "p2", InstanceName: "inst-2", HealthState: "healthy"}},
		},
	}
	reg := identity.New()

	var startedIDs []string
	mgr := process.NewManager(process.ManagerConfig{
		Querier:        q,
		IdentityIssuer: reg,
		TestProcessStarter: func(_ context.Context, cfg process.Config) (*process.Instance, error) {
			startedIDs = append(startedIDs, cfg.InstanceID)
			return nil, errors.New("stub: no subprocess needed")
		},
	})

	if err := mgr.StartByPluginID(context.Background(), "p1"); err != nil {
		t.Fatalf("StartByPluginID: %v", err)
	}

	if len(startedIDs) != 1 || startedIDs[0] != "i1" {
		t.Errorf("started instance IDs = %v, want [i1]", startedIDs)
	}
}

// TestManager_StartByPluginID_HonorsBlockedHealthStates verifies that
// StartByPluginID does not invoke the starter for instances in blocked states.
func TestManager_StartByPluginID_HonorsBlockedHealthStates(t *testing.T) {
	binaryPath := "/x/y/binary"
	q := &fakeQuerier{
		plugins: []db.Plugin{
			{ID: "p1", Status: "active", BinaryPath: &binaryPath},
		},
		instances: map[string][]db.PluginInstance{
			"p1": {
				{ID: "i-healthy", PluginID: "p1", InstanceName: "healthy-inst", HealthState: "healthy"},
				{ID: "i-crashed", PluginID: "p1", InstanceName: "crashed-inst", HealthState: "crashed"},
			},
		},
	}
	reg := identity.New()

	var startedIDs []string
	mgr := process.NewManager(process.ManagerConfig{
		Querier:        q,
		IdentityIssuer: reg,
		TestProcessStarter: func(_ context.Context, cfg process.Config) (*process.Instance, error) {
			startedIDs = append(startedIDs, cfg.InstanceID)
			return nil, errors.New("stub: no subprocess needed")
		},
	})

	if err := mgr.StartByPluginID(context.Background(), "p1"); err != nil {
		t.Fatalf("StartByPluginID: %v", err)
	}

	// Only the healthy instance should have triggered a start attempt.
	if len(startedIDs) != 1 || startedIDs[0] != "i-healthy" {
		t.Errorf("started instance IDs = %v, want [i-healthy]", startedIDs)
	}
}

// TestManager_StartByPluginID_NilBinaryPath verifies that StartByPluginID logs
// and returns nil (not an error) when the plugin row has no binary_path set.
func TestManager_StartByPluginID_NilBinaryPath(t *testing.T) {
	q := &fakeQuerier{
		plugins: []db.Plugin{
			{ID: "p1", Status: "active", BinaryPath: nil},
		},
		instances: map[string][]db.PluginInstance{
			"p1": {{ID: "i1", PluginID: "p1", InstanceName: "inst-1", HealthState: "healthy"}},
		},
	}
	reg := identity.New()

	var startCalled bool
	mgr := process.NewManager(process.ManagerConfig{
		Querier:        q,
		IdentityIssuer: reg,
		TestProcessStarter: func(_ context.Context, _ process.Config) (*process.Instance, error) {
			startCalled = true
			return nil, errors.New("should not be called")
		},
	})

	if err := mgr.StartByPluginID(context.Background(), "p1"); err != nil {
		t.Fatalf("StartByPluginID: unexpected error: %v", err)
	}
	if startCalled {
		t.Error("processStarter was called for nil binary_path")
	}
}

// TestManager_ServerInterceptors_PropagateToConfig verifies that interceptors
// set on ManagerConfig.ServerInterceptors are passed through to the process.Config
// that the TestProcessStarter receives. This is the unit check for the wiring
// that production hostsvc activation depends on.
func TestManager_ServerInterceptors_PropagateToConfig(t *testing.T) {
	reg := identity.New()

	// Build a counting interceptor to use as a fixture.
	var interceptorCalls atomic.Int32
	fixtureInterceptor := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		interceptorCalls.Add(1)
		return handler(ctx, req)
	}

	var capturedInterceptors []grpc.UnaryServerInterceptor
	mgr := process.NewManager(process.ManagerConfig{
		Querier:            &fakeQuerier{},
		IdentityIssuer:     reg,
		ServerInterceptors: []grpc.UnaryServerInterceptor{fixtureInterceptor},
		TestProcessStarter: func(_ context.Context, cfg process.Config) (*process.Instance, error) {
			capturedInterceptors = cfg.ServerInterceptors
			// Return an error to avoid needing a real subprocess.
			return nil, errors.New("stub: no subprocess needed for this test")
		},
	})

	plugin := db.Plugin{ID: "p1", Status: "active"}
	instance := db.PluginInstance{ID: "i1", PluginID: "p1", InstanceName: "i1", HealthState: "healthy"}

	// The start will fail (stub returns error), but the interceptors must have
	// been captured before the error path.
	_ = mgr.Start(context.Background(), plugin, instance, "/bin/true")

	if len(capturedInterceptors) != 1 {
		t.Fatalf("interceptor count in Config = %d, want 1", len(capturedInterceptors))
	}
	// Invoke the captured interceptor to verify it is our fixture function.
	_, _ = capturedInterceptors[0](context.Background(), nil, nil, func(_ context.Context, _ any) (any, error) { return nil, nil })
	if interceptorCalls.Load() != 1 {
		t.Errorf("captured interceptor call count = %d, want 1", interceptorCalls.Load())
	}
}

// ── ReloadInstance tests ──────────────────────────────────────────────────────

// fakeInstance is returned by the fake processStarter below. It satisfies the
// *process.Instance shape by being a real Instance from process.Start — but
// for these tests we use a TestProcessStarter that returns a minimal stub.
//
// Because process.Instance has no exported constructor we drive ReloadInstance
// through a TestProcessStarter that records calls and returns an *Instance
// obtained from a real (but trivially short) Start so the Manager can call
// Stop() on it without panicking.
//
// To avoid real subprocess spawning (which requires the UNIX re-exec fixture),
// we use the same os.Args[0] + GLEIPNIR_TEST_FIXTURE pattern the other tests use.

// TestReloadInstance_DrainsAndRestarts verifies the happy-path reload:
// an in-flight Host RPC refcount is released within the grace period,
// BeginDrain returns drained=true, the subprocess starter is called twice
// (once for initial Start, once for the post-reload Start), and the controller
// advances to generation 2.
func TestReloadInstance_DrainsAndRestarts(t *testing.T) {
	reg := identity.New()
	ctrl := generation.New()

	var startCount atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := process.NewManager(process.ManagerConfig{
		Querier:              &fakeQuerier{},
		IdentityIssuer:       reg,
		GenerationController: ctrl,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			startCount.Add(1)
			fc := fixtureConfig(t, "serve-and-block", reg, nil)
			fc.InstanceID = cfg.InstanceID
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{ID: "p1", Status: "active"}
	instance := db.PluginInstance{ID: "i-reload-1", PluginID: "p1", InstanceName: "inst1", HealthState: "healthy"}

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop(ctx, instance.ID) //nolint:errcheck

	// Simulate an in-flight Host RPC by acquiring a refcount slot directly.
	wrappedCtx, release, _, err := ctrl.Acquire(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- mgr.ReloadInstance(ctx, plugin, instance, os.Args[0], 5*time.Second)
	}()

	// Release the slot before the grace deadline → drain should complete naturally.
	time.Sleep(20 * time.Millisecond)
	release()

	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadInstance: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ReloadInstance did not return")
	}

	// The held call's context must NOT have been force-cancelled.
	if wrappedCtx.Err() != nil {
		t.Errorf("held call ctx was cancelled; drain must have completed naturally")
	}

	// processStarter should have been called twice: initial Start + post-reload Start.
	if n := startCount.Load(); n != 2 {
		t.Errorf("processStarter called %d times, want 2", n)
	}

	// Generation must have advanced to 2.
	gen := ctrl.RegisterInstance(instance.ID)
	if gen != 2 {
		t.Errorf("generation after reload = %d, want 2", gen)
	}
}

// TestReloadInstance_ForceCancelsExceedingGrace verifies the force-cancel path:
// when the held refcount is not released within the grace period, BeginDrain
// force-cancels the call, ReloadInstance still completes, and the generation
// advances to 2.
func TestReloadInstance_ForceCancelsExceedingGrace(t *testing.T) {
	reg := identity.New()
	ctrl := generation.New()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := process.NewManager(process.ManagerConfig{
		Querier:              &fakeQuerier{},
		IdentityIssuer:       reg,
		GenerationController: ctrl,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			fc := fixtureConfig(t, "serve-and-block", reg, nil)
			fc.InstanceID = cfg.InstanceID
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{ID: "p1", Status: "active"}
	instance := db.PluginInstance{ID: "i-reload-2", PluginID: "p1", InstanceName: "inst2", HealthState: "healthy"}

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop(ctx, instance.ID) //nolint:errcheck

	// Acquire a slot and never release it within the grace window.
	wrappedCtx, release, _, err := ctrl.Acquire(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release() // idempotent guard

	reloadDone := make(chan error, 1)
	go func() {
		// Very short grace so the force-cancel path is exercised quickly.
		reloadDone <- mgr.ReloadInstance(ctx, plugin, instance, os.Args[0], 30*time.Millisecond)
	}()

	// Wait for the held call's ctx to be force-cancelled.
	select {
	case <-wrappedCtx.Done():
		// Expected.
	case <-time.After(10 * time.Second):
		t.Fatal("held call ctx was not force-cancelled within 10s")
	}

	// Release after force-cancel (simulates the handler observing ctx.Done).
	release()

	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadInstance returned error after force-cancel: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ReloadInstance did not return after force-cancel")
	}

	// Generation must have advanced to 2 regardless of force-cancel.
	gen := ctrl.RegisterInstance(instance.ID)
	if gen != 2 {
		t.Errorf("generation after reload = %d, want 2", gen)
	}
}

// TestReloadInstance_WithoutControllerReturnsError verifies that ReloadInstance
// returns an error when ManagerConfig.GenerationController is nil.
func TestReloadInstance_WithoutControllerReturnsError(t *testing.T) {
	reg := identity.New()
	mgr := process.NewManager(process.ManagerConfig{
		Querier:        &fakeQuerier{},
		IdentityIssuer: reg,
		// No GenerationController set.
	})

	plugin := db.Plugin{ID: "p1", Status: "active"}
	instance := db.PluginInstance{ID: "i-reload-3", PluginID: "p1", InstanceName: "inst3", HealthState: "healthy"}

	err := mgr.ReloadInstance(context.Background(), plugin, instance, "/bin/true", time.Second)
	if err == nil {
		t.Fatal("expected error when GenerationController is nil, got nil")
	}
}
