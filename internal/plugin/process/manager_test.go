//go:build unix

package process_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
	"github.com/felag-engineering/gleipnir/internal/plugin/tools"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
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

func (q *fakeQuerier) InsertPluginAuditEvent(_ context.Context, _ db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return db.PluginAuditEvent{}, nil
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

// TestManager_ConcurrentStart_SingleSubprocess verifies that when two goroutines
// call Start for the same instance ID concurrently, exactly one subprocess is
// spawned. Before the fix a double-spawn race allowed both callers to pass the
// guard check before either had written to m.instances, resulting in two
// subprocesses being started. The sentinel-under-lock approach closes this
// window: the second caller observes the in-progress spawn and returns an error.
//
// This test is designed to be run with -race to catch the data race on the
// spawn counter. The starter intentionally sleeps to widen the race window
// so it is reliably reproducible even without -race.
func TestManager_ConcurrentStart_SingleSubprocess(t *testing.T) {
	reg := identity.New()

	var spawnCount atomic.Int32

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := process.NewManager(process.ManagerConfig{
		Querier:        &fakeQuerier{},
		IdentityIssuer: reg,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			// Sleep long enough to ensure the second goroutine reaches the guard
			// check before the first goroutine inserts into m.instances. Without
			// the sentinel fix both goroutines would pass the guard and both would
			// increment spawnCount.
			time.Sleep(50 * time.Millisecond)
			spawnCount.Add(1)
			fc := fixtureConfig(t, "serve-and-block", reg, nil)
			fc.InstanceID = cfg.InstanceID
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{ID: "p-concurrent", Status: "active"}
	instance := db.PluginInstance{
		ID:           "i-concurrent",
		PluginID:     "p-concurrent",
		InstanceName: "concurrent-inst",
		HealthState:  "healthy",
	}

	var (
		wg    sync.WaitGroup
		errCh = make(chan error, 2)
	)

	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- mgr.Start(ctx, plugin, instance, os.Args[0])
		}()
	}
	wg.Wait()
	close(errCh)

	var successes, failures int
	for err := range errCh {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}

	t.Cleanup(func() { mgr.Stop(ctx, instance.ID) }) //nolint:errcheck

	if successes != 1 {
		t.Errorf("concurrent Start successes = %d, want exactly 1", successes)
	}
	if failures != 1 {
		t.Errorf("concurrent Start failures = %d, want exactly 1", failures)
	}
	if n := spawnCount.Load(); n != 1 {
		t.Errorf("subprocess spawn count = %d, want 1 (double-spawn race detected)", n)
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
	// Check liveness BEFORE releasing: a force-cancel would have cancelled this
	// ctx while the call was still in-flight, but we release well within the grace
	// window so the drain completes naturally and the ctx is still live here.
	// (After release() the ctx is cancelled by the release itself — issue #498 —
	// so this distinction is only observable before release, not after.)
	if wrappedCtx.Err() != nil {
		t.Errorf("held call ctx was force-cancelled before release; drain should complete naturally")
	}
	release()

	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadInstance: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ReloadInstance did not return")
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

// TestReloadInstance_StartFails_CleanUpToRecoverableState verifies the issue
// #587 recoverability guarantee: when the post-reload Start fails to spawn the
// new subprocess, ReloadInstance must NOT leave a zombie (absent from the map
// but with a bumped generation still registered against no subprocess). Instead
// it cleans up to a consistent, recoverable end-state:
//   - error returned,
//   - instance absent from the manager map,
//   - instance unregistered from the generation controller (so a later restart
//     re-registers fresh at generation 1, not the orphaned bumped generation),
//   - health = unhealthy.
func TestReloadInstance_StartFails_CleanUpToRecoverableState(t *testing.T) {
	reg := identity.New()
	ctrl := generation.New()
	q := &auditCapturingQuerier{
		fakeQuerier: fakeQuerier{
			plugins: []db.Plugin{{ID: "p1", Status: "active"}},
			instances: map[string][]db.PluginInstance{
				"p1": {{ID: "i-reload-fail", PluginID: "p1", InstanceName: "inst-fail", HealthState: "healthy"}},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The starter succeeds on the first (initial) Start and fails on the second
	// (post-reload restart), exercising the spawn-failure cleanup path.
	var startCount atomic.Int32
	mgr := process.NewManager(process.ManagerConfig{
		Querier:              q,
		IdentityIssuer:       reg,
		GenerationController: ctrl,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			if startCount.Add(1) == 1 {
				fc := fixtureConfig(t, "serve-and-block", reg, nil)
				fc.InstanceID = cfg.InstanceID
				return process.Start(ctx, fc)
			}
			// Spawn fails on the reload restart. Returning a LaunchError mirrors a
			// real handshake failure and drives handleLaunchFailure (health=unhealthy
			// with "subprocess_handshake_failed:").
			return nil, &process.LaunchError{Cause: errors.New("simulated reload spawn failure")}
		},
	})

	plugin := db.Plugin{ID: "p1", Status: "active"}
	instance := db.PluginInstance{ID: "i-reload-fail", PluginID: "p1", InstanceName: "inst-fail", HealthState: "healthy"}

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("initial Start: %v", err)
	}

	// Sanity: before reload the generation is 1.
	if gen := ctrl.RegisterInstance(instance.ID); gen != 1 {
		t.Fatalf("pre-reload generation = %d, want 1", gen)
	}

	err := mgr.ReloadInstance(ctx, plugin, instance, os.Args[0], 5*time.Second)
	if err == nil {
		mgr.Stop(ctx, instance.ID) //nolint:errcheck
		t.Fatal("expected error from ReloadInstance when restart spawn fails, got nil")
	}

	// End-state 1: the instance must be absent from the manager map.
	if inst := mgr.Lookup(instance.ID); inst != nil {
		t.Errorf("instance still present in manager map after failed reload (zombie); want absent")
	}

	// End-state 2: the instance must be unregistered from the generation
	// controller. The observable signal is that RegisterInstance re-creates a
	// fresh entry at generation 1 — if the zombie entry had survived, the bumped
	// generation (2) would be returned instead.
	if gen := ctrl.RegisterInstance(instance.ID); gen != 1 {
		t.Errorf("generation after failed reload = %d; want 1 (instance should have been unregistered, not left at the bumped generation)", gen)
	}

	// End-state 3: health must have been driven to unhealthy with the
	// handshake-failure detail (handleLaunchFailure path).
	var sawUnhealthy bool
	for _, detail := range q.healthDetails {
		if strings.HasPrefix(detail, "subprocess_handshake_failed:") {
			sawUnhealthy = true
			break
		}
	}
	if !sawUnhealthy {
		t.Errorf("no subprocess_handshake_failed health detail recorded; got: %v", q.healthDetails)
	}
}

// TestReloadInstance_StartFails_Recoverable verifies the end-state from a failed
// reload is actually recoverable: a fresh Start after the failed reload succeeds
// and the instance returns to a live, registered state. This is the operator's
// "re-activate" recovery path.
func TestReloadInstance_StartFails_Recoverable(t *testing.T) {
	reg := identity.New()
	ctrl := generation.New()
	q := &fakeQuerier{
		plugins: []db.Plugin{{ID: "p1", Status: "active"}},
		instances: map[string][]db.PluginInstance{
			"p1": {{ID: "i-recover", PluginID: "p1", InstanceName: "inst-recover", HealthState: "healthy"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var startCount atomic.Int32
	mgr := process.NewManager(process.ManagerConfig{
		Querier:              q,
		IdentityIssuer:       reg,
		GenerationController: ctrl,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			// Fail only the second call (the reload restart). Calls 1 and 3 succeed.
			if startCount.Add(1) == 2 {
				return nil, &process.LaunchError{Cause: errors.New("simulated reload spawn failure")}
			}
			fc := fixtureConfig(t, "serve-and-block", reg, nil)
			fc.InstanceID = cfg.InstanceID
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{ID: "p1", Status: "active"}
	instance := db.PluginInstance{ID: "i-recover", PluginID: "p1", InstanceName: "inst-recover", HealthState: "healthy"}

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("initial Start: %v", err)
	}
	if err := mgr.ReloadInstance(ctx, plugin, instance, os.Args[0], 5*time.Second); err == nil {
		t.Fatal("expected error from failed reload, got nil")
	}

	// Recovery: a fresh Start must succeed and leave a live, registered instance.
	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("recovery Start after failed reload: %v", err)
	}
	defer mgr.Stop(ctx, instance.ID) //nolint:errcheck

	if inst := mgr.Lookup(instance.ID); inst == nil {
		t.Errorf("instance not present in manager map after recovery Start; want live")
	}
}

// TestReloadInstance_StartFails_NoLeakedRefcount runs the failed-reload path with
// the race detector and an in-flight Acquire to confirm cleanup does not leave a
// leaked refcount or deadlock a blocked Acquire caller. Run the package with
// -race to exercise the concurrency guarantee.
func TestReloadInstance_StartFails_NoLeakedRefcount(t *testing.T) {
	reg := identity.New()
	ctrl := generation.New()
	q := &fakeQuerier{
		plugins: []db.Plugin{{ID: "p1", Status: "active"}},
		instances: map[string][]db.PluginInstance{
			"p1": {{ID: "i-reload-race", PluginID: "p1", InstanceName: "inst-race", HealthState: "healthy"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var startCount atomic.Int32
	mgr := process.NewManager(process.ManagerConfig{
		Querier:              q,
		IdentityIssuer:       reg,
		GenerationController: ctrl,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			if startCount.Add(1) == 1 {
				fc := fixtureConfig(t, "serve-and-block", reg, nil)
				fc.InstanceID = cfg.InstanceID
				return process.Start(ctx, fc)
			}
			return nil, &process.LaunchError{Cause: errors.New("simulated reload spawn failure")}
		},
	})

	plugin := db.Plugin{ID: "p1", Status: "active"}
	instance := db.PluginInstance{ID: "i-reload-race", PluginID: "p1", InstanceName: "inst-race", HealthState: "healthy"}

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("initial Start: %v", err)
	}

	// Hold an in-flight refcount across the reload. BeginDrain force-cancels it
	// after the (very short) grace window; the held ctx must observe cancellation.
	wrappedCtx, release, _, err := ctrl.Acquire(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- mgr.ReloadInstance(ctx, plugin, instance, os.Args[0], 30*time.Millisecond)
	}()

	// The held call's ctx must be force-cancelled (drain grace is short).
	select {
	case <-wrappedCtx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("held call ctx was not force-cancelled within 10s")
	}
	release() // simulate the handler observing ctx.Done()

	select {
	case err := <-reloadDone:
		if err == nil {
			t.Fatal("expected error from failed reload, got nil")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ReloadInstance did not return after failed restart")
	}

	// After cleanup, a fresh Acquire on the unregistered instance must return an
	// error (not block, not panic) — proving the controller entry was removed.
	if _, _, _, acqErr := ctrl.Acquire(ctx, instance.ID); acqErr == nil {
		t.Errorf("Acquire succeeded after failed-reload cleanup; want error (instance should be unregistered)")
	}
}

// ── auditCapturingQuerier ─────────────────────────────────────────────────────

// auditCapturingQuerier extends fakeQuerier with audit-event capture so tests
// can assert on the plugin_crashed event written by handleLaunchFailure.
type auditCapturingQuerier struct {
	fakeQuerier
	mu     sync.Mutex
	events []db.InsertPluginAuditEventParams
	// healthDetails captures the detail string from UpdatePluginInstanceHealth.
	// The DB column is *string; we store the dereferenced value (empty string
	// when nil) for simpler test assertions.
	healthDetails []string
}

func (q *auditCapturingQuerier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	q.mu.Lock()
	q.events = append(q.events, arg)
	q.mu.Unlock()
	return db.PluginAuditEvent{}, nil
}

func (q *auditCapturingQuerier) UpdatePluginInstanceHealth(_ context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error) {
	detail := ""
	if arg.HealthDetail != nil {
		detail = *arg.HealthDetail
	}
	q.mu.Lock()
	q.healthDetails = append(q.healthDetails, detail)
	q.mu.Unlock()
	return 1, nil
}

func (q *auditCapturingQuerier) auditEvents() []db.InsertPluginAuditEventParams {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]db.InsertPluginAuditEventParams, len(q.events))
	copy(out, q.events)
	return out
}

// ── Launch-failure tests ──────────────────────────────────────────────────────

// TestManager_LaunchFailure_EmitsAuditEventAndHealthDetail verifies the three
// assertions from the task spec when a subprocess fails during launch:
//  1. The error returned from Manager.Start wraps a process.LaunchError.
//  2. A plugin_crashed audit event is written with plugin_id/instance_id/error
//     in the payload and severity "high".
//  3. The instance health detail set on the DB row starts with
//     "subprocess_handshake_failed:" — not a raw gRPC error string.
//
// We use the real "exit-no-handshake" fixture which exits before the go-plugin
// handshake completes.
func TestManager_LaunchFailure_EmitsAuditEventAndHealthDetail(t *testing.T) {
	reg := identity.New()
	q := &auditCapturingQuerier{
		fakeQuerier: fakeQuerier{
			plugins: []db.Plugin{{ID: "p-launch-fail", Status: "active"}},
			instances: map[string][]db.PluginInstance{
				"p-launch-fail": {{
					ID:           "i-launch-fail",
					PluginID:     "p-launch-fail",
					InstanceName: "launch-fail-inst",
					HealthState:  "healthy",
					Version:      1,
				}},
			},
		},
	}

	mgr := process.NewManager(process.ManagerConfig{
		Querier:        q,
		IdentityIssuer: reg,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			fc := fixtureConfig(t, "exit-no-handshake", reg, nil)
			fc.InstanceID = cfg.InstanceID
			fc.PluginID = cfg.PluginID
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{ID: "p-launch-fail", Status: "active"}
	instance := db.PluginInstance{ID: "i-launch-fail", PluginID: "p-launch-fail", InstanceName: "launch-fail-inst", HealthState: "healthy"}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := mgr.Start(ctx, plugin, instance, os.Args[0])
	if err == nil {
		t.Fatal("expected error from Manager.Start when handshake fails, got nil")
	}

	// 1. Error chain must include *process.LaunchError.
	var le *process.LaunchError
	if !errors.As(err, &le) {
		t.Errorf("error chain does not contain *process.LaunchError; got: %v", err)
	}

	// 2. plugin_crashed audit event must be written with required fields.
	events := q.auditEvents()
	var crashed *db.InsertPluginAuditEventParams
	for i := range events {
		if events[i].EventType == "plugin_crashed" {
			crashed = &events[i]
			break
		}
	}
	if crashed == nil {
		t.Fatalf("no plugin_crashed audit event found; events: %+v", events)
	}
	if crashed.Severity != "high" {
		t.Errorf("plugin_crashed severity = %q, want %q", crashed.Severity, "high")
	}
	if crashed.PluginInstanceID == nil || *crashed.PluginInstanceID != instance.ID {
		t.Errorf("plugin_crashed PluginInstanceID = %v, want %q", crashed.PluginInstanceID, instance.ID)
	}
	if !strings.Contains(crashed.PayloadJson, `"plugin_id"`) {
		t.Errorf("plugin_crashed payload missing plugin_id field: %s", crashed.PayloadJson)
	}
	if !strings.Contains(crashed.PayloadJson, `"instance_id"`) {
		t.Errorf("plugin_crashed payload missing instance_id field: %s", crashed.PayloadJson)
	}
	if !strings.Contains(crashed.PayloadJson, `"error"`) {
		t.Errorf("plugin_crashed payload missing error field: %s", crashed.PayloadJson)
	}

	// 3. Health detail recorded in the DB must be actionable (not a raw gRPC string).
	var sawHandshakeFailed bool
	for _, detail := range q.healthDetails {
		if strings.HasPrefix(detail, "subprocess_handshake_failed:") {
			sawHandshakeFailed = true
			break
		}
	}
	if !sawHandshakeFailed {
		t.Errorf("health detail did not start with 'subprocess_handshake_failed:'; recorded details: %v", q.healthDetails)
	}
}

// ── Tool registration tests ───────────────────────────────────────────────────

// fakeToolRegistrar records RegisterInstanceTools and UnregisterInstance calls
// for assertion in unit tests. Thread-safe.
type fakeToolRegistrar struct {
	mu              sync.Mutex
	registerCalls   []registerCall
	unregisterCalls []string
	registerErr     error // if set, RegisterInstanceTools returns this
}

type registerCall struct {
	instanceID   string
	instanceName string
	toolNames    []string
	generation   int64
}

func (f *fakeToolRegistrar) RegisterInstanceTools(_ context.Context, instanceID, instanceName string, toolNames []string, generation int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registerCalls = append(f.registerCalls, registerCall{instanceID, instanceName, toolNames, generation})
	return f.registerErr
}

func (f *fakeToolRegistrar) UnregisterInstance(_ context.Context, instanceName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unregisterCalls = append(f.unregisterCalls, instanceName)
}

// manifestWithTools returns a minimal manifest YAML string declaring the given
// tool names. Includes schema_version, name, and version so Unmarshal produces
// a well-formed Manifest.
func manifestWithTools(toolNames ...string) string {
	var sb strings.Builder
	sb.WriteString("schema_version: \"1\"\nname: test-plugin\nversion: \"1.0.0\"\ntools:\n")
	for _, t := range toolNames {
		sb.WriteString("  - name: " + t + "\n")
	}
	return sb.String()
}

// TestManager_Start_RegistersTools verifies that Manager.Start calls
// RegisterInstanceTools with the correct tool names, instance ID, instance
// name, and generation (1 on cold start) extracted from the manifest snapshot.
func TestManager_Start_RegistersTools(t *testing.T) {
	reg := identity.New()
	registrar := &fakeToolRegistrar{}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := process.NewManager(process.ManagerConfig{
		Querier:        &fakeQuerier{},
		IdentityIssuer: reg,
		ToolRegistrar:  registrar,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			fc := fixtureConfig(t, "serve-and-block", reg, nil)
			// Copy InstanceID and InstanceName so the stored instance reflects
			// the real identifiers; RegisterInstanceTools reads InstanceName via
			// instance.InstanceName from the db row (not from proc.Config), so
			// this does not affect what Manager passes to RegisterInstanceTools,
			// but it ensures Stop's unregistration call uses the right name.
			fc.InstanceID = cfg.InstanceID
			fc.InstanceName = cfg.InstanceName
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{
		ID:               "p-tools",
		Status:           "active",
		ManifestSnapshot: manifestWithTools("send", "read"),
	}
	instance := db.PluginInstance{
		ID:           "i-tools",
		PluginID:     "p-tools",
		InstanceName: "my-plugin",
		HealthState:  "healthy",
	}

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { mgr.Stop(ctx, instance.ID) }) //nolint:errcheck

	registrar.mu.Lock()
	calls := registrar.registerCalls
	registrar.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("RegisterInstanceTools called %d times, want 1", len(calls))
	}
	call := calls[0]
	if call.instanceID != instance.ID {
		t.Errorf("instanceID = %q, want %q", call.instanceID, instance.ID)
	}
	if call.instanceName != instance.InstanceName {
		t.Errorf("instanceName = %q, want %q", call.instanceName, instance.InstanceName)
	}
	if len(call.toolNames) != 2 || call.toolNames[0] != "send" || call.toolNames[1] != "read" {
		t.Errorf("toolNames = %v, want [send read]", call.toolNames)
	}
	if call.generation != 1 {
		t.Errorf("generation = %d, want 1 (cold start)", call.generation)
	}
}

// TestManager_Stop_UnregistersTools verifies that Manager.Stop calls
// UnregisterInstance with the correct instance name.
func TestManager_Stop_UnregistersTools(t *testing.T) {
	reg := identity.New()
	registrar := &fakeToolRegistrar{}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := process.NewManager(process.ManagerConfig{
		Querier:        &fakeQuerier{},
		IdentityIssuer: reg,
		ToolRegistrar:  registrar,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			fc := fixtureConfig(t, "serve-and-block", reg, nil)
			// Copy both InstanceID and InstanceName from the manager-supplied cfg
			// so inst.cfg.InstanceName reflects the real instance name when Stop
			// reads it for tool unregistration.
			fc.InstanceID = cfg.InstanceID
			fc.InstanceName = cfg.InstanceName
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{
		ID:               "p-stop",
		Status:           "active",
		ManifestSnapshot: manifestWithTools("tool-x"),
	}
	instance := db.PluginInstance{
		ID:           "i-stop",
		PluginID:     "p-stop",
		InstanceName: "stop-plugin",
		HealthState:  "healthy",
	}

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := mgr.Stop(ctx, instance.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	registrar.mu.Lock()
	unregCalls := registrar.unregisterCalls
	registrar.mu.Unlock()

	if len(unregCalls) == 0 {
		t.Fatal("UnregisterInstance was not called after Stop")
	}
	// Assert at least one call with the correct instance name.
	found := false
	for _, name := range unregCalls {
		if name == instance.InstanceName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("UnregisterInstance not called with %q; calls: %v", instance.InstanceName, unregCalls)
	}
}

// TestManager_Start_ToolConflict_DrivesUnhealthy verifies that when a plugin
// instance's manifest declares a tool name that conflicts with an existing
// reservation in the arbiter, the instance is driven to unhealthy and a
// plugin_tool_namespace_conflict audit event is written.
//
// This is the acceptance-criteria integration test from issue #400: it uses a
// real testutil.NewTestStore + tools.Registrar + pre-populated arbiter.
func TestManager_Start_ToolConflict_DrivesUnhealthy(t *testing.T) {
	reg := identity.New()
	store := testutil.NewTestStore(t)

	// Seed a plugin and instance row in the real DB so the state machine can
	// update health_state and insert audit events.
	ctx := context.Background()
	now := "2024-01-01T00:00:00Z"
	if _, err := store.Queries().CreatePlugin(ctx, db.CreatePluginParams{
		ID:               "p-conflict",
		Name:             "conflict-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: "{}",
		TrustedPubkey:    "pubkey",
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := store.Queries().CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID:                "i-conflict",
		PluginID:          "p-conflict",
		InstanceName:      "inst-a",
		ConfigJson:        "{}",
		HandshakeVersions: "{}",
		HealthState:       string(model.PluginHealthStateHealthy),
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}

	// Pre-populate the arbiter with a reservation for "inst-a.send" owned by
	// an MCP source so the plugin's start will conflict.
	arbiter := toolregistry.New()
	mcpSrc := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "inst-a"}
	if err := arbiter.Reserve(toolregistry.DotName("inst-a", "send"), mcpSrc); err != nil {
		t.Fatalf("pre-reserve: %v", err)
	}

	toolRegistrar := tools.New(arbiter, store.Queries(), nil)

	// Use the dbQuerierAdapter so Manager reads from the real test store while
	// the tools.Registrar writes health state and audit events into it.
	realQ := &dbQuerierAdapter{q: store.Queries()}

	mgr := process.NewManager(process.ManagerConfig{
		Querier:        realQ,
		IdentityIssuer: reg,
		ToolRegistrar:  toolRegistrar,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			fc := fixtureConfig(t, "serve-and-block", reg, nil)
			fc.InstanceID = cfg.InstanceID
			return process.Start(ctx, fc)
		},
	})

	plugin := db.Plugin{
		ID:               "p-conflict",
		Status:           "active",
		ManifestSnapshot: manifestWithTools("send"),
	}
	instance := db.PluginInstance{
		ID:           "i-conflict",
		PluginID:     "p-conflict",
		InstanceName: "inst-a",
		HealthState:  string(model.PluginHealthStateHealthy),
	}

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Start returns an error because RegisterInstanceTools encounters a conflict.
	err := mgr.Start(startCtx, plugin, instance, os.Args[0])
	if err == nil {
		// Unexpected success — defensively stop the spawned subprocess before failing.
		mgr.Stop(startCtx, instance.ID) //nolint:errcheck
		t.Fatal("expected error from Manager.Start on tool conflict, got nil")
	}

	// Stop is now a defensive no-op: Start's tool-registration rollback (#587)
	// already killed the subprocess and removed it from the map on the conflict
	// path. We still call it so the test stays correct if that rollback ever
	// changes.
	mgr.Stop(startCtx, instance.ID) //nolint:errcheck

	// Assert health_state in DB is unhealthy.
	row, fetchErr := store.Queries().GetPluginInstanceByID(ctx, "i-conflict")
	if fetchErr != nil {
		t.Fatalf("GetPluginInstanceByID: %v", fetchErr)
	}
	if row.HealthState != string(model.PluginHealthStateUnhealthy) {
		t.Errorf("health_state = %q after conflict, want %q", row.HealthState, model.PluginHealthStateUnhealthy)
	}

	// Assert a plugin_tool_namespace_conflict audit event exists.
	iid := "i-conflict"
	events, listErr := store.Queries().ListPluginAuditEventsByInstance(ctx, db.ListPluginAuditEventsByInstanceParams{
		PluginInstanceID: &iid,
		Offset:           0,
		Limit:            20,
	})
	if listErr != nil {
		t.Fatalf("ListPluginAuditEventsByInstance: %v", listErr)
	}
	found := false
	for _, e := range events {
		if e.EventType == "plugin_tool_namespace_conflict" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no plugin_tool_namespace_conflict audit event found; events: %+v", events)
	}
}

// dbQuerierAdapter adapts *db.Queries to satisfy the process.Manager's querier
// interface (a subset of db.Queries). Used in the conflict integration test so
// the Manager reads from the real test store while the tools.Registrar also
// writes health state and audit events into the same store.
type dbQuerierAdapter struct {
	q *db.Queries
}

func (a *dbQuerierAdapter) ListPluginsByStatus(ctx context.Context, status string) ([]db.Plugin, error) {
	return a.q.ListPluginsByStatus(ctx, status)
}

func (a *dbQuerierAdapter) ListPluginInstancesByPlugin(ctx context.Context, pluginID string) ([]db.PluginInstance, error) {
	return a.q.ListPluginInstancesByPlugin(ctx, pluginID)
}

func (a *dbQuerierAdapter) GetPluginByID(ctx context.Context, id string) (db.Plugin, error) {
	return a.q.GetPluginByID(ctx, id)
}

func (a *dbQuerierAdapter) GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error) {
	return a.q.GetPluginInstanceByID(ctx, id)
}

func (a *dbQuerierAdapter) UpdatePluginInstanceHealth(ctx context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error) {
	return a.q.UpdatePluginInstanceHealth(ctx, arg)
}

func (a *dbQuerierAdapter) InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return a.q.InsertPluginAuditEvent(ctx, arg)
}

// ── Gap B tests (#576) ───────────────────────────────────────────────────────

// healthCapturingQuerier extends fakeQuerier with instance tracking: it holds
// a map of instance rows by ID so GetPluginInstanceByID returns the right row
// (needed for the Gap B healthy-transition gate that re-fetches the instance),
// and it records UpdatePluginInstanceHealth calls for assertion.
type healthCapturingQuerier struct {
	fakeQuerier
	mu            sync.Mutex
	instancesByID map[string]db.PluginInstance
	healthCalls   []db.UpdatePluginInstanceHealthParams
}

func newHealthCapturingQuerier() *healthCapturingQuerier {
	return &healthCapturingQuerier{
		instancesByID: make(map[string]db.PluginInstance),
	}
}

func (q *healthCapturingQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if inst, ok := q.instancesByID[id]; ok {
		return inst, nil
	}
	// Fallback to fakeQuerier's flat instances map.
	for _, instances := range q.fakeQuerier.instances {
		for _, inst := range instances {
			if inst.ID == id {
				return inst, nil
			}
		}
	}
	return db.PluginInstance{}, errors.New("not found")
}

func (q *healthCapturingQuerier) UpdatePluginInstanceHealth(_ context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error) {
	q.mu.Lock()
	q.healthCalls = append(q.healthCalls, arg)
	q.mu.Unlock()
	return 1, nil
}

// manifestWithChannelService returns a minimal manifest YAML declaring a
// ChannelService but no TriggerService and no tools.
func manifestWithChannelService() string {
	return `schema_version: "1"
name: channel-plugin
version: "1.0.0"
services:
  channel: v1
`
}

// manifestWithTriggerService returns a minimal manifest YAML declaring a
// TriggerService — used to verify the Gap B gate excludes trigger plugins.
func manifestWithTriggerService() string {
	return `schema_version: "1"
name: trigger-plugin
version: "1.0.0"
services:
  trigger: v1
`
}

// stubSuccessStarter returns a fake starter that succeeds without spawning a
// real subprocess. The returned *process.Instance is nil, which is sufficient
// for Manager.Start unit tests that don't call inst.Stop().
//
// The TestProcessStarter field type is processStarter; the Manager stores the
// result in m.instances. Because *Instance methods are not called in Gap B
// tests (no Stop, no Pid), a nil *Instance is safe here.
//
// NOTE: process.Instance has no exported constructor; for tests that DO call
// Stop we use the real fixture approach (see TestManager_IdempotentStart).
// Gap B tests only assert on health writes, not on subprocess lifecycle.
func stubSuccessStarter(t *testing.T, reg *identity.Registry) func(context.Context, process.Config) (*process.Instance, error) {
	t.Helper()
	return func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
		// Use a real minimal instance so m.instances[id] is non-nil and Stop
		// can be called at cleanup time without panic. We use the fixture binary
		// approach from other tests in this file.
		fc := fixtureConfig(t, "serve-and-block", reg, nil)
		fc.InstanceID = cfg.InstanceID
		return process.Start(ctx, fc)
	}
}

// TestManager_Start_ChannelOnly_AdvancesToHealthy verifies Gap B (#576):
// a channel-only instance (no TriggerService) that is unhealthy with empty
// readiness detail is marked healthy after a successful spawn.
func TestManager_Start_ChannelOnly_AdvancesToHealthy(t *testing.T) {
	reg := identity.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	q := newHealthCapturingQuerier()
	emptyDetail := ""
	inst := db.PluginInstance{
		ID:           "i-channel",
		PluginID:     "p-channel",
		InstanceName: "channel-inst",
		HealthState:  string(model.PluginHealthStateUnhealthy),
		HealthDetail: &emptyDetail, // empty = config + creds present
		Version:      1,
	}
	q.instancesByID[inst.ID] = inst

	mgr := process.NewManager(process.ManagerConfig{
		Querier:            q,
		IdentityIssuer:     reg,
		TestProcessStarter: stubSuccessStarter(t, reg),
	})

	plugin := db.Plugin{
		ID:               "p-channel",
		Status:           "active",
		ManifestSnapshot: manifestWithChannelService(),
	}

	if err := mgr.Start(ctx, plugin, inst, os.Args[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { mgr.Stop(ctx, inst.ID) }) //nolint:errcheck

	// The Gap B transition is synchronous inside Start; assert directly.
	q.mu.Lock()
	calls := q.healthCalls
	q.mu.Unlock()

	var sawHealthy bool
	for _, c := range calls {
		if c.HealthState == string(model.PluginHealthStateHealthy) {
			sawHealthy = true
			break
		}
	}
	if !sawHealthy {
		t.Errorf("expected UpdatePluginInstanceHealth(healthy) after channel-only spawn, got calls: %+v", calls)
	}
}

// TestManager_Start_ChannelOnly_ConfigMissing_NoHealthyTransition verifies that
// a channel-only instance with a non-empty readiness detail (e.g. config_missing)
// does NOT get the Gap B healthy transition — it must stay unhealthy until the
// operator finishes configuration.
func TestManager_Start_ChannelOnly_ConfigMissing_NoHealthyTransition(t *testing.T) {
	reg := identity.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	q := newHealthCapturingQuerier()
	configMissing := "config_missing"
	inst := db.PluginInstance{
		ID:           "i-channel-cfg",
		PluginID:     "p-channel-cfg",
		InstanceName: "channel-cfg-inst",
		HealthState:  string(model.PluginHealthStateUnhealthy),
		HealthDetail: &configMissing,
		Version:      1,
	}
	q.instancesByID[inst.ID] = inst

	mgr := process.NewManager(process.ManagerConfig{
		Querier:            q,
		IdentityIssuer:     reg,
		TestProcessStarter: stubSuccessStarter(t, reg),
	})

	plugin := db.Plugin{
		ID:               "p-channel-cfg",
		Status:           "active",
		ManifestSnapshot: manifestWithChannelService(),
	}

	if err := mgr.Start(ctx, plugin, inst, os.Args[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { mgr.Stop(ctx, inst.ID) }) //nolint:errcheck

	q.mu.Lock()
	calls := q.healthCalls
	q.mu.Unlock()

	for _, c := range calls {
		if c.HealthState == string(model.PluginHealthStateHealthy) {
			t.Errorf("unexpected healthy transition for config_missing instance; calls: %+v", calls)
		}
	}
}

// TestManager_Start_TriggerPlugin_NoHealthyTransition verifies that a trigger
// plugin (Services.Trigger != "") is excluded from the Gap B healthy transition —
// trigger/supervisor.go owns that path when the TriggerService stream connects.
func TestManager_Start_TriggerPlugin_NoHealthyTransition(t *testing.T) {
	reg := identity.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	q := newHealthCapturingQuerier()
	emptyDetail := ""
	inst := db.PluginInstance{
		ID:           "i-trigger",
		PluginID:     "p-trigger",
		InstanceName: "trigger-inst",
		HealthState:  string(model.PluginHealthStateUnhealthy),
		HealthDetail: &emptyDetail,
		Version:      1,
	}
	q.instancesByID[inst.ID] = inst

	mgr := process.NewManager(process.ManagerConfig{
		Querier:            q,
		IdentityIssuer:     reg,
		TestProcessStarter: stubSuccessStarter(t, reg),
	})

	plugin := db.Plugin{
		ID:               "p-trigger",
		Status:           "active",
		ManifestSnapshot: manifestWithTriggerService(),
	}

	if err := mgr.Start(ctx, plugin, inst, os.Args[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { mgr.Stop(ctx, inst.ID) }) //nolint:errcheck

	q.mu.Lock()
	calls := q.healthCalls
	q.mu.Unlock()

	for _, c := range calls {
		if c.HealthState == string(model.PluginHealthStateHealthy) {
			t.Errorf("trigger plugin should NOT get Gap B healthy transition; got: %+v", calls)
		}
	}
}

// TestManager_Start_ToolOnly_AdvancesToHealthy verifies that a tool-only plugin
// (no TriggerService, has tools) also receives the Gap B healthy transition.
// The manifest is parsed before the ToolRegistrar block, so the transition runs
// even for tool-only plugins. A non-nil ToolRegistrar is provided to mirror
// production wiring.
func TestManager_Start_ToolOnly_AdvancesToHealthy(t *testing.T) {
	reg := identity.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	q := newHealthCapturingQuerier()
	emptyDetail := ""
	inst := db.PluginInstance{
		ID:           "i-tool",
		PluginID:     "p-tool",
		InstanceName: "tool-inst",
		HealthState:  string(model.PluginHealthStateUnhealthy),
		HealthDetail: &emptyDetail,
		Version:      1,
	}
	q.instancesByID[inst.ID] = inst

	registrar := &fakeToolRegistrar{}
	mgr := process.NewManager(process.ManagerConfig{
		Querier:            q,
		IdentityIssuer:     reg,
		ToolRegistrar:      registrar,
		TestProcessStarter: stubSuccessStarter(t, reg),
	})

	plugin := db.Plugin{
		ID:               "p-tool",
		Status:           "active",
		ManifestSnapshot: manifestWithTools("send", "read"),
	}

	if err := mgr.Start(ctx, plugin, inst, os.Args[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { mgr.Stop(ctx, inst.ID) }) //nolint:errcheck

	q.mu.Lock()
	calls := q.healthCalls
	q.mu.Unlock()

	var sawHealthy bool
	for _, c := range calls {
		if c.HealthState == string(model.PluginHealthStateHealthy) {
			sawHealthy = true
			break
		}
	}
	if !sawHealthy {
		t.Errorf("expected UpdatePluginInstanceHealth(healthy) after tool-only spawn, got calls: %+v", calls)
	}
}
