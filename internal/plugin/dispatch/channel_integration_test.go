//go:build unix

package dispatch_test

import (
	"context"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
)

// TestDispatcher_Notify_ProductionWiring exercises the full production wire:
//
//	real subprocess started via process.Manager.Start
//	→ Manager.LookupByName resolves the instance name
//	→ Client.Conn() provides the live gRPC connection
//	→ channelv1.NewChannelServiceClient(conn) wraps it
//	→ Notify RPC reaches the fixture's ChannelService
//	→ fixture responds Ok=true
//
// This is the integration assertion required by issue #317: the production
// ConnFactory (backed by Manager.LookupByName + Client.Conn()) routes a Notify
// call through to a real subprocess. The Connect closure is inline here because
// we are testing the dispatcher path, not the managerConnFactory helper (which
// has its own unit test in conn_factory_test.go).
func TestDispatcher_Notify_ProductionWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("fixture subprocess: skipped in short mode")
	}

	// Stand up an in-memory SQLite store, mirroring the existing test-utility
	// pattern from channel_test.go.
	s := testutil.NewTestStore(t)

	// Seed the DB with a plugin, an instance, an audience, and a Notify-enabled
	// audience entry targeting the real instance we will start below.
	const instanceName = "dispatch-integration-inst"
	const instanceID = "dispatch-integ-i1"
	pluginID := insertPlugin(t, s, "dispatch-integ-p1", "dispatch-integ-plugin")
	insertPluginInstance(t, s, instanceID, pluginID, instanceName)
	audID := insertAudience(t, s, "dispatch-integ-aud1", "dispatch-integ-audience", 1 /* disable in-app fallback */)
	insertAudienceEntry(t, s, "dispatch-integ-ae1", audID, instanceID, 0, true, false)

	// Start a real subprocess via process.Manager.Start. The TestProcessStarter
	// wraps the real process.Start with the re-exec env injection so the
	// subprocess runs our dispatchFixtureChannel (registered in TestMain above)
	// instead of the test suite.
	//
	// Using the real process.Start path (not the stub) is mandatory: only it
	// populates inst.Client().Conn() with a live gRPC connection. The stub
	// processStarter used by TestManager_ServerInterceptors_PropagateToConfig
	// returns a synthetic Instance with no live client.
	reg := identity.New()
	mgr := process.NewManager(process.ManagerConfig{
		Querier:        &dispatchIntegFakeQuerier{},
		IdentityIssuer: reg,
		TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
			cfg.BinaryPath = os.Args[0]
			cfg.Launch = func(ctx context.Context, binaryPath string, host hostwire.HostServer, opts hostwire.Options) (*hostwire.Client, func(), error) {
				opts.Env = append(opts.Env, "GLEIPNIR_TEST_FIXTURE=dispatch-serve-via-sdk")
				return hostwire.Launch(ctx, binaryPath, host, opts)
			}
			return process.Start(ctx, cfg)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	plugin := db.Plugin{ID: pluginID, Status: "active"}
	instance := db.PluginInstance{
		ID:           instanceID,
		PluginID:     pluginID,
		InstanceName: instanceName,
		HealthState:  "healthy",
	}

	if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
		t.Fatalf("Manager.Start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := mgr.StopAll(stopCtx); err != nil {
			t.Logf("StopAll: %v", err)
		}
	}()

	// Build the production ConnFactory as an inline closure over the manager.
	// This duplicates the trivial body of managerConnFactory.Connect; the helper
	// struct is unit-tested separately in conn_factory_test.go. Here we test the
	// dispatcher path, so an inline closure keeps the focus on the dispatcher.
	connFactory := func(name string) (*grpc.ClientConn, error) {
		inst := mgr.LookupByName(name)
		if inst == nil {
			t.Errorf("LookupByName(%q) returned nil — instance not registered", name)
			return nil, dispatch.ErrInstanceNotRunning
		}
		conn := inst.Client().Conn()
		if conn == nil {
			t.Errorf("inst.Client().Conn() is nil for %q — real process.Start path required", name)
			return nil, dispatch.ErrInstanceNotRunning
		}
		if conn.GetState() == connectivity.Shutdown {
			t.Errorf("conn state is Shutdown for %q", name)
		}
		return conn, nil
	}

	dispatcher := dispatch.NewDispatcher(dispatch.DispatcherConfig{
		Queries: s.Queries(),
		Connect: connFactory,
	})
	// After this change connCache retains the dialed connection; Close() is
	// required so goleak.VerifyTestMain does not flag the retained gRPC goroutine.
	// Use t.Cleanup (not defer) so it runs even if a later t.Fatalf short-circuits.
	t.Cleanup(func() { _ = dispatcher.Close() })

	rc := dispatch.RouteContext{
		RunID:    "run-dispatch-integ",
		PolicyID: "policy-dispatch-integ",
		ToolName: "test.notify",
	}

	notifyCtx, notifyCancel := context.WithTimeout(ctx, 15*time.Second)
	defer notifyCancel()

	if err := dispatcher.Notify(notifyCtx, audID, rc, "test.event", "{}"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
}

// dispatchIntegFakeQuerier satisfies process.Manager's querier interface with
// empty in-memory data. We drive the manager via Start()/StopAll() directly in
// the test; StartAllActive is never called so these methods are no-ops.
type dispatchIntegFakeQuerier struct{}

func (q *dispatchIntegFakeQuerier) ListPluginsByStatus(_ context.Context, _ string) ([]db.Plugin, error) {
	return nil, nil
}
func (q *dispatchIntegFakeQuerier) ListPluginInstancesByPlugin(_ context.Context, _ string) ([]db.PluginInstance, error) {
	return nil, nil
}
func (q *dispatchIntegFakeQuerier) GetPluginByID(_ context.Context, _ string) (db.Plugin, error) {
	return db.Plugin{}, nil
}
func (q *dispatchIntegFakeQuerier) GetPluginInstanceByID(_ context.Context, _ string) (db.PluginInstance, error) {
	return db.PluginInstance{}, nil
}
func (q *dispatchIntegFakeQuerier) UpdatePluginInstanceHealth(_ context.Context, _ db.UpdatePluginInstanceHealthParams) (int64, error) {
	return 1, nil
}
func (q *dispatchIntegFakeQuerier) InsertPluginAuditEvent(_ context.Context, _ db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return db.PluginAuditEvent{}, nil
}
