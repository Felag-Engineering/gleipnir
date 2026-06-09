//go:build unix

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
)

// ntfyBinaryPath holds the path to the ntfy binary compiled by TestMain.
var ntfyBinaryPath string

// TestMain builds the ntfy binary once for all integration tests. If the
// Go toolchain is not on PATH, all integration tests are skipped.
func TestMain(m *testing.M) {
	// Skip early if go is not available — the integration test requires building.
	if _, err := exec.LookPath("go"); err != nil {
		// Cannot skip from TestMain; set a flag instead and skip per-test.
		os.Exit(m.Run())
	}

	dir, err := os.MkdirTemp("", "ntfy-integration-*")
	if err != nil {
		panic("create temp dir: " + err.Error())
	}
	defer os.RemoveAll(dir)

	binaryPath := filepath.Join(dir, "ntfy")
	// Build the ntfy binary from the module root of this package.
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir, _ = os.Getwd()
	if out, err := build.CombinedOutput(); err != nil {
		// Print the build output to help diagnose failures and exit.
		os.Stderr.Write(out)
		os.Exit(1)
	}

	ntfyBinaryPath = binaryPath
	os.Exit(m.Run())
}

// skipIfNoBinary skips the test if the ntfy binary was not built (go not on PATH).
func skipIfNoBinary(t *testing.T) {
	t.Helper()
	if ntfyBinaryPath == "" {
		t.Skip("skipping: go toolchain not on PATH or ntfy binary not built")
	}
}

// ntfyHostServer is a hostwire.HostServer that serves GetInstanceConfig and
// GetCredentials, returning the ntfy server URL and an empty credential set.
type ntfyHostServer struct {
	hostv1.UnimplementedHostServiceServer
	instanceConfigJSON string
}

func (h *ntfyHostServer) Register(s *grpc.Server) {
	hostv1.RegisterHostServiceServer(s, h)
}

func (h *ntfyHostServer) GetInstanceConfig(_ context.Context, _ *hostv1.GetInstanceConfigRequest) (*hostv1.GetInstanceConfigResponse, error) {
	return &hostv1.GetInstanceConfigResponse{ConfigJson: h.instanceConfigJSON}, nil
}

func (h *ntfyHostServer) GetCredentials(_ context.Context, _ *hostv1.GetCredentialsRequest) (*hostv1.GetCredentialsResponse, error) {
	// No API key — ntfy supports unauthenticated topics in our test setup.
	return &hostv1.GetCredentialsResponse{CredentialsJson: `{}`}, nil
}

// TestNtfy_EndToEnd is a full end-to-end integration test. It:
//  1. Starts a fake ntfy HTTP server (httptest.Server).
//  2. Spawns the real ntfy binary via hostwire.Launch with a HostServer that
//     returns the fake server URL in GetInstanceConfig.
//  3. Dispatches ChannelService.Notify via the hostwire client.
//  4. Asserts the fake ntfy server received exactly one POST to the expected
//     topic path.
func TestNtfy_EndToEnd(t *testing.T) {
	skipIfNoBinary(t)

	// 1. Fake ntfy HTTP backend — records the first POST.
	var postCount atomic.Int32
	var gotPath string
	var gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		postCount.Add(1)
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	// 2. Spawn the ntfy binary via hostwire.Launch.
	instanceCfg := map[string]string{
		"server_url":    backend.URL,
		"default_topic": "test",
	}
	cfgJSON, _ := json.Marshal(instanceCfg)

	host := &ntfyHostServer{instanceConfigJSON: string(cfgJSON)}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, teardown, err := hostwire.Launch(ctx, ntfyBinaryPath, host, hostwire.Options{
		StartupTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("hostwire.Launch: %v", err)
	}
	defer teardown()

	// 3. Dispatch Notify via the hostwire client.
	notifyCtx, notifyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer notifyCancel()

	resp, err := client.Channel.Notify(notifyCtx, &channelv1.NotifyRequest{
		PayloadJson: `{"body":"integration test message"}`,
	})
	if err != nil {
		t.Fatalf("Channel.Notify: %v", err)
	}
	if !resp.GetOk() {
		errMsg := ""
		if resp.GetError() != nil {
			errMsg = resp.GetError().GetMessage()
		}
		t.Fatalf("Notify: want Ok=true, got Ok=false; error=%q", errMsg)
	}

	// 4. Assert the fake ntfy server received exactly one POST to /test.
	if n := postCount.Load(); n != 1 {
		t.Errorf("fake ntfy server: want 1 POST, got %d", n)
	}
	if gotPath != "/test" {
		t.Errorf("POST path: want /test, got %q", gotPath)
	}
	if gotBody != "integration test message" {
		t.Errorf("POST body: want %q, got %q", "integration test message", gotBody)
	}
}
