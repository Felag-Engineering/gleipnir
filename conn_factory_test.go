//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc/connectivity"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

// TestMain intercepts the test binary when run as a plugin fixture subprocess.
// When GLEIPNIR_TEST_FIXTURE=serve-via-sdk the test binary serves the plugin
// protocol instead of running the test suite — the standard Go re-exec pattern
// for tests that spawn subprocesses.
func TestMain(m *testing.M) {
	if os.Getenv("GLEIPNIR_TEST_FIXTURE") == "serve-via-sdk" {
		serve.Serve(
			serve.WithChannelService(func(_ hostv1.HostServiceClient) channelv1.ChannelServiceServer {
				return &connFactoryFixtureChannel{}
			}),
		)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// connFactoryFixtureChannel returns Ok=true on every Notify call.
type connFactoryFixtureChannel struct {
	channelv1.UnimplementedChannelServiceServer
}

func (s *connFactoryFixtureChannel) Notify(_ context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
	return &channelv1.NotifyResponse{Ok: true}, nil
}

// TestManagerConnFactory covers the three observable states of managerConnFactory.
func TestManagerConnFactory(t *testing.T) {
	t.Run("nil_manager_returns_unavailable", func(t *testing.T) {
		f := &managerConnFactory{}
		_, err := f.Connect("any-instance")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, dispatch.ErrManagerUnavailable) {
			t.Errorf("want dispatch.ErrManagerUnavailable, got %v", err)
		}
	})

	t.Run("missing_instance_returns_not_running", func(t *testing.T) {
		reg := identity.New()
		mgr := process.NewManager(process.ManagerConfig{
			Querier:        &connFactoryFakeQuerier{},
			IdentityIssuer: reg,
		})

		f := &managerConnFactory{}
		f.setManager(mgr)

		_, err := f.Connect("nonexistent-instance")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, dispatch.ErrInstanceNotRunning) {
			t.Errorf("want dispatch.ErrInstanceNotRunning, got %v", err)
		}
	})

	t.Run("running_instance_returns_conn", func(t *testing.T) {
		if testing.Short() {
			t.Skip("fixture subprocess: skipped in short mode")
		}

		reg := identity.New()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		plugin := db.Plugin{ID: "p1", Status: "active"}
		instance := db.PluginInstance{
			ID:           "factory-test-instance",
			PluginID:     "p1",
			InstanceName: "factory-test",
			HealthState:  "healthy",
		}

		// Use a TestProcessStarter that calls the real process.Start but with the
		// GLEIPNIR_TEST_FIXTURE env var injected via a Launch wrapper. This causes
		// the re-exec'd subprocess to run serve.Serve (our fixture above) rather
		// than the test suite — identical to the fixtureConfig pattern in
		// internal/plugin/process/process_test.go.
		//
		// The real process.Start path (not the stub) must be taken so that
		// inst.Client().Conn() is backed by a live gRPC connection. Without it,
		// managerConnFactory.Connect would return a nil conn from the accessor.
		mgr := process.NewManager(process.ManagerConfig{
			Querier:        &connFactoryFakeQuerier{},
			IdentityIssuer: reg,
			TestProcessStarter: func(ctx context.Context, cfg process.Config) (*process.Instance, error) {
				cfg.BinaryPath = os.Args[0]
				cfg.Launch = func(ctx context.Context, binaryPath string, host hostwire.HostServer, opts hostwire.Options) (*hostwire.Client, func(), error) {
					opts.Env = append(opts.Env, "GLEIPNIR_TEST_FIXTURE=serve-via-sdk")
					return hostwire.Launch(ctx, binaryPath, host, opts)
				}
				return process.Start(ctx, cfg)
			},
		})

		if err := mgr.Start(ctx, plugin, instance, os.Args[0]); err != nil {
			t.Fatalf("Manager.Start: %v", err)
		}
		defer mgr.StopAll(ctx) //nolint:errcheck

		f := &managerConnFactory{}
		f.setManager(mgr)

		conn, err := f.Connect("factory-test")
		if err != nil {
			t.Fatalf("Connect(%q): %v", "factory-test", err)
		}
		if conn == nil {
			t.Fatal("Connect returned nil conn")
		}
		if state := conn.GetState(); state == connectivity.Shutdown {
			t.Errorf("conn state is Shutdown, want live connection")
		}
	})
}

// connFactoryFakeQuerier satisfies process.Manager's querier interface with
// empty in-memory data. Manager unit tests that use TestProcessStarter never
// invoke StartAllActive, so these methods are no-ops.
type connFactoryFakeQuerier struct{}

func (q *connFactoryFakeQuerier) ListPluginsByStatus(_ context.Context, _ string) ([]db.Plugin, error) {
	return nil, nil
}
func (q *connFactoryFakeQuerier) ListPluginInstancesByPlugin(_ context.Context, _ string) ([]db.PluginInstance, error) {
	return nil, nil
}
func (q *connFactoryFakeQuerier) GetPluginByID(_ context.Context, _ string) (db.Plugin, error) {
	return db.Plugin{}, errors.New("not found")
}
func (q *connFactoryFakeQuerier) GetPluginInstanceByID(_ context.Context, _ string) (db.PluginInstance, error) {
	return db.PluginInstance{}, errors.New("not found")
}
func (q *connFactoryFakeQuerier) UpdatePluginInstanceHealth(_ context.Context, _ db.UpdatePluginInstanceHealthParams) (int64, error) {
	return 1, nil
}
func (q *connFactoryFakeQuerier) InsertPluginAuditEvent(_ context.Context, _ db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return db.PluginAuditEvent{}, nil
}
