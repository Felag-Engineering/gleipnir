package container

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// newFakeDockerSocket starts an HTTP server listening on a Unix socket in a
// temp directory and returns the socket path. This stands in for a real
// container-runtime socket so DockerRuntime's request plumbing (URL paths,
// JSON encoding, response decoding) can be exercised without one — real-daemon
// (Podman/Docker) coverage is a separate, later CI-gated issue.
func newFakeDockerSocket(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "fake.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on fake socket: %v", err)
	}

	srv := httptest.NewUnstartedServer(mux)
	srv.Listener.Close()
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)

	return socketPath
}

func TestNewDockerRuntime_LazyDial(t *testing.T) {
	// Constructing a client over a Unix socket that does not exist yet must
	// not itself fail — the SDK only prepares the HTTP transport; a real
	// call is what would surface a connection error.
	rt, err := NewDockerRuntime("/nonexistent/does-not-exist.sock")
	if err != nil {
		t.Fatalf("NewDockerRuntime() error = %v, want nil (dial is lazy)", err)
	}
	if err := rt.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestDockerRuntime_CreateStartInspectListByLabel(t *testing.T) {
	const containerID = "deadbeef1234"

	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.51")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1.51/containers/create", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"Id": containerID})
	})
	mux.HandleFunc("/v1.51/containers/"+containerID+"/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1.51/containers/"+containerID+"/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"Id":   containerID,
			"Name": "/plugin-abc123",
			"State": map[string]any{
				"Status": "running",
			},
			"Config": map[string]any{
				"Image":  "img@sha256:deadbeef",
				"Labels": map[string]string{"gleipnir.instance_id": "abc123"},
			},
		})
	})
	mux.HandleFunc("/v1.51/containers/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"Id":      containerID,
				"Names":   []string{"/plugin-abc123"},
				"Image":   "img@sha256:deadbeef",
				"Labels":  map[string]string{"gleipnir.instance_id": "abc123"},
				"State":   "running",
				"Created": 1700000000,
			},
		})
	})
	socketPath := newFakeDockerSocket(t, mux)
	rt, err := NewDockerRuntime(socketPath)
	if err != nil {
		t.Fatalf("NewDockerRuntime() error = %v", err)
	}
	defer rt.Close()

	ctx := t.Context()
	id, err := rt.Create(ctx, validCreateOptions())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if id != ContainerID(containerID) {
		t.Errorf("Create() id = %q, want %q", id, containerID)
	}

	if err := rt.Start(ctx, id); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	info, err := rt.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if info.Name != "plugin-abc123" || info.State != ContainerStateRunning {
		t.Errorf("Inspect() = %+v, want name=plugin-abc123 state=running", info)
	}

	list, err := rt.ListByLabel(ctx, "gleipnir.instance_id", "abc123")
	if err != nil {
		t.Fatalf("ListByLabel() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != ContainerID(containerID) {
		t.Fatalf("ListByLabel() = %+v, want exactly container %q", list, containerID)
	}
}

func TestDockerRuntime_StopRemoveStatsLogsAndNetworks(t *testing.T) {
	const containerID = "deadbeef1234"
	const networkID = "net5678"

	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.51")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1.51/containers/"+containerID+"/stop", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1.51/containers/"+containerID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1.51/containers/"+containerID+"/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"cpu_stats":    map[string]any{"cpu_usage": map[string]any{"total_usage": 200}, "system_cpu_usage": 2000, "online_cpus": 2},
			"precpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": 100}, "system_cpu_usage": 1000},
			"memory_stats": map[string]any{"usage": 1024, "limit": 4096},
		})
	})
	mux.HandleFunc("/v1.51/containers/"+containerID+"/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("log line\n"))
	})
	mux.HandleFunc("/v1.51/networks/create", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"Id": networkID})
	})
	mux.HandleFunc("/v1.51/networks/"+networkID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1.51/networks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"Id": networkID, "Name": "gleipnir-plugin-abc123", "Internal": true, "Labels": map[string]string{"k": "v"}},
		})
	})

	socketPath := newFakeDockerSocket(t, mux)
	rt, err := NewDockerRuntime(socketPath)
	if err != nil {
		t.Fatalf("NewDockerRuntime() error = %v", err)
	}
	defer rt.Close()

	ctx := t.Context()

	if err := rt.Stop(ctx, containerID, 0); err != nil {
		t.Errorf("Stop() error = %v", err)
	}
	if err := rt.Remove(ctx, containerID, true); err != nil {
		t.Errorf("Remove() error = %v", err)
	}

	stats, err := rt.Stats(ctx, containerID)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.CPUPercent != 20 || stats.MemoryUsageBytes != 1024 || stats.MemoryLimitBytes != 4096 {
		t.Errorf("Stats() = %+v, want CPUPercent=20 MemoryUsageBytes=1024 MemoryLimitBytes=4096", stats)
	}

	rc, err := rt.Logs(ctx, containerID, LogOptions{Tail: "10"})
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	rc.Close()

	netID, err := rt.CreateNetwork(ctx, NetworkOptions{Name: "gleipnir-plugin-abc123", Internal: true})
	if err != nil {
		t.Fatalf("CreateNetwork() error = %v", err)
	}
	if netID != NetworkID(networkID) {
		t.Errorf("CreateNetwork() id = %q, want %q", netID, networkID)
	}

	nets, err := rt.ListNetworksByLabel(ctx, "k", "v")
	if err != nil {
		t.Fatalf("ListNetworksByLabel() error = %v", err)
	}
	if len(nets) != 1 || nets[0].ID != NetworkID(networkID) {
		t.Fatalf("ListNetworksByLabel() = %+v, want exactly network %q", nets, networkID)
	}

	if err := rt.RemoveNetwork(ctx, netID); err != nil {
		t.Errorf("RemoveNetwork() error = %v", err)
	}
}

func TestDockerRuntime_CreateNetworkRejectsExternalNetwork(t *testing.T) {
	mux := http.NewServeMux()
	socketPath := newFakeDockerSocket(t, mux)

	rt, err := NewDockerRuntime(socketPath)
	if err != nil {
		t.Fatalf("NewDockerRuntime() error = %v", err)
	}
	defer rt.Close()

	_, err = rt.CreateNetwork(t.Context(), NetworkOptions{Name: "n", Internal: false})
	if _, ok := err.(*ConstraintViolationError); !ok {
		t.Errorf("CreateNetwork() error type = %T, want *ConstraintViolationError", err)
	}
}

func TestDockerRuntime_CreateRejectsHostileOptionsBeforeTheSocket(t *testing.T) {
	// The mux has no registered handlers at all: if Create reached the
	// socket for a hostile request, the test server would 404 and the test
	// would still (incidentally) pass. Assert on the error type instead of
	// relying on that — ValidateCreate must short-circuit before dialing.
	mux := http.NewServeMux()
	socketPath := newFakeDockerSocket(t, mux)

	rt, err := NewDockerRuntime(socketPath)
	if err != nil {
		t.Fatalf("NewDockerRuntime() error = %v", err)
	}
	defer rt.Close()

	opts := validCreateOptions()
	opts.Privileged = true

	if _, err := rt.Create(t.Context(), opts); err == nil {
		t.Fatal("Create() with Privileged=true = nil error, want *ConstraintViolationError")
	} else if _, ok := err.(*ConstraintViolationError); !ok {
		t.Errorf("Create() error type = %T, want *ConstraintViolationError", err)
	}
}
